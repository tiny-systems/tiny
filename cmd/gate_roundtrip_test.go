package cmd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/tiny-systems/tiny/internal/adapters"
)

// A human-in-the-loop gate, end to end against a real cluster.
//
// The `ask` component was removed from common-module in favour of `chat`, which
// left five flows naming a component their module no longer served. Chat
// absorbed the contract — an `ask` target port that queues a question, an
// `answer` source carrying {questionId, values, context} — so migrating was a
// rewire. This asserts the rewire actually gates: firing the trigger parks a
// question on the chat node, and nothing downstream runs until it is answered.
//
// Parking is the whole point and also why this cannot be an ordinary eval: an
// eval waits for a flow to settle, and a gate deliberately never settles. The
// assertion is that it stops, not that it finishes.
//
// Needs a provisioned cluster, so it is opt-in:
//
//	TINY_E2E_CONTEXT=minikube go test ./cmd -run TestGateParksUntilAnswered -v
//
// chatQueueKey is where the chat component persists pending questions:
// queueStateKey ("queue") behind module.State's "_state/" prefix.
const (
	chatQueueKey   = "_state/queue"
	chatKindField  = "_kind"
	chatQIDField   = "_qid"
	chatKindAnswer = "answer"
)

func TestGateParksUntilAnswered(t *testing.T) {
	if os.Getenv("TINY_E2E_CONTEXT") == "" {
		t.Skip("set TINY_E2E_CONTEXT to run the gate test against a cluster")
	}

	flagContext = os.Getenv("TINY_E2E_CONTEXT")
	flagNamespace = envOr("TINY_E2E_NS", "tinysystems")
	project := envOr("TINY_GATE_PROJECT", "maksym")
	flow := envOr("TINY_GATE_FLOW", "ask-gate")

	bundle, cleanup, err := buildKubeBundle(project)
	if err != nil {
		t.Fatalf("connect to cluster: %v", err)
	}
	defer cleanup()

	sender, ok := bundle.SignalSender.(*adapters.SignalSender)
	if !ok || sender == nil {
		t.Fatal("cluster connection cannot publish — is tinysystems-nats reachable?")
	}

	trigger, chat := gateNodes(t, flow)
	t.Logf("trigger %s, gate %s", short(trigger), short(chat))

	before := pendingCount(t, chat)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := sender.SendSignal(ctx, project, trigger, "_control", []byte(`{"send":true}`), ""); err != nil {
		t.Fatalf("fire trigger: %v", err)
	}

	// The question has to arrive, and then it has to STAY — a gate that emits
	// and moves on is not a gate. Poll for arrival, then hold and re-check.
	deadline := time.Now().Add(45 * time.Second)
	var got int
	for time.Now().Before(deadline) {
		got = pendingCount(t, chat)
		if got > before {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if got <= before {
		t.Fatalf("no question parked on the gate: pending was %d, still %d — the flow did not reach chat:ask", before, got)
	}

	time.Sleep(8 * time.Second)
	held := pendingCount(t, chat)
	if held < got {
		t.Errorf("question left the queue without an answer: %d -> %d", got, held)
	}
	t.Logf("gate holds %d pending question(s), waiting on a human", held)

	// The other half of a gate: an answer has to release it. A card that parks
	// forever is a broken gate too, just a safer one.
	head := headQuestionID(t, chat)
	if head == "" {
		t.Fatal("queue is non-empty but has no head id to answer")
	}
	answer := map[string]any{
		chatKindField: chatKindAnswer,
		chatQIDField:  head,
		// The form's button field. A pressed button is what marks a submission
		// an answer rather than a re-render, and the answer must name the head
		// of the queue or a replayed card could consume the next question.
		"approve": true,
	}
	payload, err := json.Marshal(answer)
	if err != nil {
		t.Fatalf("marshal answer: %v", err)
	}
	if err := sender.SendSignal(ctx, project, chat, "_control", payload, ""); err != nil {
		t.Fatalf("answer the question: %v", err)
	}

	drained := false
	for deadline := time.Now().Add(45 * time.Second); time.Now().Before(deadline); {
		if pendingCount(t, chat) < held {
			drained = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !drained {
		t.Fatalf("answering %s did not release the gate: still %d pending", head, held)
	}
	t.Logf("answered %s — gate released, queue down to %d", head, pendingCount(t, chat))
}

// headQuestionID returns the id of the question at the front of the queue.
// Answers must name it: the card carries the head's id so a replayed card for
// an already-answered question cannot consume the one behind it.
func headQuestionID(t *testing.T, node string) string {
	t.Helper()
	out := kubectlJSON(t, "get", "tinynode", "-n", flagNamespace, node, "-o", "json")
	status, _ := out["status"].(map[string]any)
	md, _ := status["metadata"].(map[string]any)
	raw, _ := md[chatQueueKey].(string)
	if raw == "" {
		return ""
	}
	data := []byte(raw)
	if dec, err := base64.StdEncoding.DecodeString(raw); err == nil {
		data = dec
	}
	var queue []map[string]any
	if json.Unmarshal(data, &queue) != nil || len(queue) == 0 {
		return ""
	}
	id, _ := queue[0]["id"].(string)
	return id
}

func short(fqn string) string {
	for i := len(fqn) - 1; i >= 0; i-- {
		if fqn[i] == '.' {
			return fqn[i+1:]
		}
	}
	return fqn
}

// gateNodes finds the flow's trigger and its chat gate by reading the cluster,
// so the test does not hard-code node suffixes that change on every rebuild.
func gateNodes(t *testing.T, flow string) (trigger, chat string) {
	t.Helper()
	out := kubectlJSON(t, "get", "tinynodes", "-n", flagNamespace,
		"-l", "tinysystems.io/flow-name="+flow, "-o", "json")

	items, _ := out["items"].([]any)
	for _, it := range items {
		n, _ := it.(map[string]any)
		spec, _ := n["spec"].(map[string]any)
		meta, _ := n["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		switch spec["component"] {
		case "signal":
			trigger = name
		case "chat":
			chat = name
		}
	}
	if trigger == "" || chat == "" {
		t.Fatalf("flow %s: need a signal and a chat node, found trigger=%q chat=%q", flow, trigger, chat)
	}
	return trigger, chat
}

// pendingCount reads how many questions the gate is holding.
//
// The key is "_state/queue" — chat's queueStateKey, written through
// module.State, which prefixes "_state/". Worth naming exactly: the first
// version of this test guessed at "pending"/"questions", found nothing, and
// reported a working migration as broken. A metadata key is part of a
// component's contract; read it from the component, never from memory.
func pendingCount(t *testing.T, node string) int {
	t.Helper()
	out := kubectlJSON(t, "get", "tinynode", "-n", flagNamespace, node, "-o", "json")
	status, _ := out["status"].(map[string]any)
	md, _ := status["metadata"].(map[string]any)

	raw, ok := md[chatQueueKey].(string)
	if !ok || raw == "" {
		return 0
	}
	data := []byte(raw)
	if dec, err := base64.StdEncoding.DecodeString(raw); err == nil {
		data = dec
	}
	var queue []any
	if err := json.Unmarshal(data, &queue); err != nil {
		t.Fatalf("%s: %s is not a JSON list: %v", short(node), chatQueueKey, err)
	}
	return len(queue)
}

func kubectlJSON(t *testing.T, args ...string) map[string]any {
	t.Helper()
	full := append([]string{"--context", flagContext}, args...)
	out, err := execCapture("kubectl", full...)
	if err != nil {
		t.Fatalf("kubectl %v: %v", args, err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("kubectl %v: %v (%s)", args, err, truncate(string(out)))
	}
	return m
}

func truncate(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

func execCapture(bin string, args ...string) ([]byte, error) {
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", bin, err)
	}
	return out, nil
}
