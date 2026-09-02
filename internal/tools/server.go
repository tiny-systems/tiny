/*
Package tools is the MCP toolbox an agent reaches the cluster through: the gate an unattended agent reaches
for when it hits a decision it must not make alone.

Two doors into the same object:

  - The good path: the agent calls the ask_human MCP tool. A Question appears
    in the cluster and the call BLOCKS until a human writes an answer into its
    status — the answer is the tool result, and the agent continues.
  - The safety net: the agent ignored the tool and simply asked in its shell,
    or hit a permission prompt. A Claude Code Notification hook (or any
    runner) POSTs /attention, and the same Question appears — answered not by
    unblocking a tool call but by whoever attaches to the session.

Nothing here knows what a "session runner" is. kelos, a bare pod, anything
able to reach this server over the cluster network gets the same gate.
*/
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	tinyv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

// Reasons a question exists. Tool questions block an agent mid-call; a
// notification question records that the agent is visibly waiting on a human
// through some other channel (its own prompt, a permission dialog).
const (
	ReasonTool         = "tool"
	ReasonNotification = "notification"
)

// SessionLabel lets screens select a session's questions without parsing spec.
const SessionLabel = "tinysystems.io/session"

// Server holds what the tools need: a cluster client and the namespace the
// Questions live in.
type Server struct {
	Client    client.Client
	Namespace string
	// PollInterval is how often a blocked ask checks for its answer. A watch
	// would be tidier; a poll on one named object is simpler and the latency
	// is human-scale anyway. Zero means 2s.
	PollInterval time.Duration
}

// AskInput is the ask_human argument shape shown to the model.
type AskInput struct {
	Question string   `json:"question" jsonschema:"What you need the human to decide, with enough context to answer without reading your transcript."`
	Options  []string `json:"options,omitempty" jsonschema:"Choices to offer as one-press buttons. Omit for a free-text answer."`
}

// AskOutput carries the human's decision back to the model.
type AskOutput struct {
	Answer string `json:"answer"`
}

// AwaitInput resumes waiting on a question an earlier call created.
type AwaitInput struct {
	QuestionID string `json:"questionId" jsonschema:"The id from the interrupted ask_human call's error message."`
}

// MCP builds the tool server.
func (s *Server) MCP() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "tiny-mcp", Version: "v0.1.0"}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "ask_human",
		Description: "Ask the human operator a question and WAIT for their answer. Use it before any action that is " +
			"hard to undo, when the task is ambiguous, or when you need information only they have. Give options when " +
			"the answer is a choice; omit them for free text. The call blocks until they answer — minutes or hours. " +
			"If it returns an interruption naming a questionId, call await_answer with that id instead of asking again.",
	}, s.ask)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "await_answer",
		Description: "Resume waiting for the answer to a question ask_human already created.",
	}, s.await)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "session_list",
		Description: "List the coding-agent sessions running in this cluster: name, phase, pod.",
	}, s.sessionList)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_title",
		Description: "Set this session's title — a short present-tense line saying what you are working on right now. " +
			"It is shown next to your session on the operator's fleet screen. Update it whenever the nature of your " +
			"work changes.",
	}, s.setTitle)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "session_create",
		Description: "Start another coding-agent session with a task of its own. The human operator is asked to " +
			"allow it first — the call blocks until they decide.",
	}, s.sessionCreate)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "enable_store",
		Description: "Enable the namespace's shared artifact store (minio) when `mc` has no `store` alias. Goes " +
			"through the approval gate — instant when namespace policy pre-consents. Returns the mc command " +
			"that wires this session up.",
	}, s.enableStore)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "expose_port",
		Description: "Make a port your process is listening on reachable inside the cluster as a Service. The human " +
			"operator is asked to allow it first — the call blocks until they decide. Returns the URL.",
	}, s.exposePort)
	return srv
}

// Handler serves MCP on /mcp, the hook safety net on /attention, and liveness
// on /healthz — one port, one Service.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/mcp", withCallerIP(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.MCP() }, nil)))
	mux.Handle("/attention", withCallerIP(http.HandlerFunc(s.handleAttention)))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	return mux
}

type ctxKey int

const callerIPKey ctxKey = 0

// withCallerIP stashes the HTTP peer address for sessionFor. The MCP layer
// hides the request; the address rides the context instead.
func withCallerIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), callerIPKey, host)))
	})
}

func (s *Server) ask(ctx context.Context, _ *mcp.CallToolRequest, in AskInput) (*mcp.CallToolResult, AskOutput, error) {
	if strings.TrimSpace(in.Question) == "" {
		return nil, AskOutput{}, fmt.Errorf("question is required")
	}
	q, err := s.createQuestion(ctx, tinyv1.QuestionSpec{
		Text:    in.Question,
		Options: in.Options,
		Session: s.sessionFor(ctx),
		Reason:  ReasonTool,
	})
	if err != nil {
		return nil, AskOutput{}, err
	}
	return s.waitFor(ctx, q.Name)
}

func (s *Server) await(ctx context.Context, _ *mcp.CallToolRequest, in AwaitInput) (*mcp.CallToolResult, AskOutput, error) {
	if in.QuestionID == "" {
		return nil, AskOutput{}, fmt.Errorf("questionId is required")
	}
	return s.waitFor(ctx, in.QuestionID)
}

// attentionRequest is what a hook posts. Everything is optional but the
// message — a Notification hook rarely knows more than "the agent wants you".
type attentionRequest struct {
	Message string `json:"message"`
	Session string `json:"session,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Self-reported cgroup sample, reason "usage" only.
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

// handleAttention records that a session is visibly waiting on a human. It
// answers immediately (hooks must not hang the agent) and dedupes: one open
// notification per session, its text refreshed, rather than a card per hook
// firing.
func (s *Server) handleAttention(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var in attentionRequest
	// Hooks post small JSON; anything bigger is a bug or an attack.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&in); err != nil {
		http.Error(w, "bad body: "+err.Error(), http.StatusBadRequest)
		return
	}
	// usage and limit are status refreshes, not questions.
	if in.Reason == "usage" || in.Reason == "limit" {
		s.recordStatus(r.Context(), in)
		ack(w, "recorded")
		return
	}

	// A finished turn is phase information, not a question. The message —
	// the tail of what the agent just said — becomes the session's live
	// activity line, refreshed every turn: the fleet screen's always-actual
	// WHAT column.
	if in.Reason == "stop" {
		if ref := s.sessionFor(r.Context()); ref.Name != "" && strings.TrimSpace(in.Message) != "" {
			se := &tinyv1.Session{}
			if err := s.Client.Get(r.Context(), client.ObjectKey{Namespace: s.Namespace, Name: ref.Name}, se); err == nil {
				se.Status.Activity = strings.TrimSpace(in.Message)
				_ = s.Client.Status().Update(r.Context(), se)
			}
		}
		ack(w, "recorded")
		return
	}
	if strings.TrimSpace(in.Message) == "" {
		in.Message = "The agent is waiting for your input."
	}

	ctx := r.Context()
	session := s.sessionFor(ctx)
	if session.Name == "" {
		session = s.sessionForIP(ctx, remoteIP(r))
	}
	// The body's session may only CONFIRM the derived identity, never replace
	// it — accepting a caller-chosen name would let any pod plant attention
	// cards under another session's row.
	if in.Session != "" && session.Name != "" && in.Session != session.Name {
		http.Error(w, fmt.Sprintf("session %q does not match the caller's identity %q", in.Session, session.Name), http.StatusForbidden)
		return
	}
	if session.Name == "" && in.Session != "" {
		// No identity derivable at all (rare: central mode, hostNetwork). The
		// hint is used but marked as such, so a screen can render it softer.
		session.Name = in.Session
		session.Kind = "unverified"
	}

	// Reuse the open notification for this session if one exists.
	if session.Name != "" {
		list := &tinyv1.QuestionList{}
		if err := s.Client.List(ctx, list, client.InNamespace(s.Namespace), client.MatchingLabels{SessionLabel: session.Name}); err == nil {
			for i := range list.Items {
				q := &list.Items[i]
				if q.Spec.Reason == ReasonNotification && !q.Answered() {
					q.Spec.Text = in.Message
					if err := s.Client.Update(ctx, q); err == nil {
						writeJSON(w, map[string]string{"questionId": q.Name, "deduped": ackTrue})
						return
					}
				}
			}
		}
	}

	q, err := s.createQuestion(ctx, tinyv1.QuestionSpec{
		Text:    in.Message,
		Session: session,
		Reason:  ReasonNotification,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"questionId": q.Name})
}

// AnnounceRunning marks the sidecar's own session Running and records its
// pod — the phase writers the deleted manager left orphaned. Called once at
// serve start; no-op outside a session pod.
func (s *Server) AnnounceRunning(ctx context.Context) {
	ref := s.sessionFor(ctx)
	if ref.Name == "" {
		return
	}
	se := &tinyv1.Session{}
	if err := s.Client.Get(ctx, s.key(ref.Name), se); err != nil {
		return
	}
	se.Status.Phase = tinyv1.SessionRunning
	se.Status.Pod = ref.Pod
	se.Status.Message = ""
	_ = s.Client.Status().Update(ctx, se)
}

// recordStatus mutates the caller's session status for the self-reported
// signals: cgroup usage, the pane-scraped usage-limit banner (a paused
// session should LOOK paused on the fleet), and the agent's exit.
func (s *Server) recordStatus(ctx context.Context, in attentionRequest) {
	ref := s.sessionFor(ctx)
	if ref.Name == "" {
		return
	}
	se := &tinyv1.Session{}
	if err := s.Client.Get(ctx, s.key(ref.Name), se); err != nil {
		return
	}
	if ref.Pod != "" {
		se.Status.Pod = ref.Pod
	}
	switch in.Reason {
	case "usage":
		if in.CPU == "" && in.Memory == "" {
			return
		}
		se.Status.Usage = &tinyv1.SessionUsage{CPU: in.CPU, Memory: in.Memory}
	case "exited":
		if in.Message == "0" {
			se.Status.Phase = tinyv1.SessionDone
			se.Status.Message = ""
		} else {
			se.Status.Phase = tinyv1.SessionFailed
			se.Status.Message = "agent exited with code " + in.Message
		}
	case "limit":
		switch {
		case in.Message != "":
			se.Status.Activity = "⏸ " + in.Message
		case strings.HasPrefix(se.Status.Activity, "⏸ "):
			se.Status.Activity = ""
		default:
			return
		}
	}
	_ = s.Client.Status().Update(ctx, se)
}

func (s *Server) key(name string) client.ObjectKey {
	return client.ObjectKey{Namespace: s.Namespace, Name: name}
}

func (s *Server) createQuestion(ctx context.Context, spec tinyv1.QuestionSpec) (*tinyv1.Question, error) {
	q := &tinyv1.Question{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "q-",
			Namespace:    s.Namespace,
		},
		Spec: spec,
	}
	if spec.Session.Name != "" {
		q.Labels = map[string]string{SessionLabel: spec.Session.Name}
	}
	if err := s.Client.Create(ctx, q); err != nil {
		return nil, fmt.Errorf("create question: %w", err)
	}
	return q, nil
}

// pollQuestion blocks until done(q) says the wait is over, the context
// ends, or the question is gone. An interruption is reported WITH the
// question id so the model resumes with await_answer instead of asking
// again — asking again would put a duplicate card in front of the human.
func (s *Server) pollQuestion(ctx context.Context, name string, done func(*tinyv1.Question) (bool, error)) error {
	interval := s.PollInterval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	interrupted := fmt.Errorf("interrupted while waiting — call await_answer with questionId %q to keep waiting", name)

	for {
		q := &tinyv1.Question{}
		err := s.Client.Get(ctx, types.NamespacedName{Namespace: s.Namespace, Name: name}, q)
		switch {
		case err != nil && ctx.Err() != nil:
			return interrupted
		case err != nil:
			return fmt.Errorf("question %s is gone: %w", name, err)
		default:
			if ok, derr := done(q); derr != nil || ok {
				return derr
			}
		}

		select {
		case <-ctx.Done():
			return interrupted
		case <-t.C:
		}
	}
}

// waitForResult waits until the question is answered AND its action carried
// out, returning the action's result. A deny is an error naming the human's
// words.
func (s *Server) waitForResult(ctx context.Context, name string) (string, error) {
	var result string
	err := s.pollQuestion(ctx, name, func(q *tinyv1.Question) (bool, error) {
		switch {
		case q.Answered() && !tinyv1.AllowsAction(q.Status.Answer):
			return false, fmt.Errorf("denied by the human operator: %s", q.Status.Answer)
		case q.Answered() && q.Status.ActionDone:
			result = q.Status.Result
			return true, nil
		}
		return false, nil
	})
	return result, err
}

// waitFor blocks until the named question carries an answer.
func (s *Server) waitFor(ctx context.Context, name string) (*mcp.CallToolResult, AskOutput, error) {
	var answer string
	err := s.pollQuestion(ctx, name, func(q *tinyv1.Question) (bool, error) {
		if q.Answered() {
			answer = q.Status.Answer
			return true, nil
		}
		return false, nil
	})
	if err != nil {
		return nil, AskOutput{}, err
	}
	return nil, AskOutput{Answer: answer}, nil
}

// sessionFor works out which session asked, with zero configuration: the
// caller's pod is found by its IP, and the pod's labels say who owns it. Best
// effort by design — an unattributed question still reaches the human, it
// just renders without a session row to sit under.
func (s *Server) sessionFor(ctx context.Context) tinyv1.SessionRef {
	// Sidecar mode: the pod says who it is through the downward API —
	// unforgeable, no lookup. Central mode falls back to caller IP.
	if name := os.Getenv("TINY_SESSION_NAME"); name != "" {
		return tinyv1.SessionRef{Name: name, Kind: "Session", Pod: os.Getenv("POD_NAME")}
	}
	ip, _ := ctx.Value(callerIPKey).(string)
	return s.sessionForIP(ctx, ip)
}

func (s *Server) sessionForIP(ctx context.Context, ip string) tinyv1.SessionRef {
	if ip == "" || s.Client == nil {
		return tinyv1.SessionRef{}
	}
	pods := &corev1.PodList{}
	if err := s.Client.List(ctx, pods, client.InNamespace(s.Namespace)); err != nil {
		return tinyv1.SessionRef{}
	}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.PodIP != ip {
			continue
		}
		ref := tinyv1.SessionRef{Pod: p.Name, Kind: "Pod", Name: p.Name}
		if v := p.Labels[SessionLabel]; v != "" {
			ref.Name = v
			ref.Kind = "Session"
		}
		return ref
	}
	return tinyv1.SessionRef{}
}

func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ackTrue is the wire's little word for "yes, handled".
const ackTrue = "true"

// ack answers a hook post that produced no question.
func ack(w http.ResponseWriter, what string) {
	writeJSON(w, map[string]string{what: ackTrue})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are gone; the wire is what it is. Log so a truncated
		// response is at least diagnosable.
		log.Printf("writeJSON: %v", err)
	}
}
