package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// The whole chain, driven the way an agent drives it.
//
// Every unit test here checks one side of something. The failures that reached
// people were the ones that only appear when the sides meet: a position
// accepted and dropped, an edge configuration written and read back empty, a
// flow built green that produced no trace, a widget pinned that the dashboard
// never showed. Each was found by an agent using the product, and none of them
// could have failed a unit test.
//
// This drives the same tools an agent calls, against a real cluster, and
// checks the result each time — build, read back, pin a widget, fire it, and
// confirm a trace exists. It deliberately does NOT use a model: the value is
// in exercising the chain, and a model in the loop costs money per run and
// fails for reasons that are not the product's.
//
// Env-gated, because it needs a cluster and it writes to it:
//
//	TINY_E2E_CONTEXT=minikube go test ./cmd -run E2E -v
//
// It creates its own throwaway project and removes it afterwards.

func TestE2EBuildAndRunAFlow(t *testing.T) {
	kctx := os.Getenv("TINY_E2E_CONTEXT")
	if kctx == "" {
		t.Skip("set TINY_E2E_CONTEXT to run the end-to-end flow test")
	}

	flagContext = kctx
	flagNamespace = envOr("TINY_E2E_NS", "tinysystems")
	project := fmt.Sprintf("e2e-%d", time.Now().UnixNano()%1e6)

	bundle, cleanup, err := buildKubeBundle(project)
	if err != nil {
		t.Fatalf("connect to cluster: %v", err)
	}
	defer cleanup()

	registry := buildRegistry()
	ctx := context.Background()

	execCtx := sdktools.ExecutionContext(bundle)
	execCtx.ProjectName = project

	call := func(t *testing.T, tool string, args map[string]interface{}) map[string]interface{} {
		t.Helper()
		res := registry.Execute(ctx, execCtx, tool, args)
		if !res.Success {
			t.Fatalf("%s failed: %s", tool, res.Error)
		}
		out, _ := res.Output.(map[string]interface{})
		return out
	}

	defer func() {
		// Leave nothing behind, whatever happened above.
		execCtx.FlowName = "main"
		registry.Execute(ctx, execCtx, "delete_flow", map[string]interface{}{"flow": "main", "project": project})
	}()

	// --- build ------------------------------------------------------------
	execCtx.FlowName = "main"
	call(t, "create_flow", map[string]interface{}{"name": "main", "project": project})

	built := call(t, "build_flow", map[string]interface{}{
		"project": project,
		"flow":    "main",
		"nodes": []interface{}{
			map[string]interface{}{
				"alias": "trigger", "component": "signal", "module": "tinysystems/common-module-v0",
				"position": map[string]interface{}{"x": 40, "y": 300},
			},
			map[string]interface{}{
				"alias": "panel", "component": "display", "module": "tinysystems/common-module-v0",
				"position": map[string]interface{}{"x": 620, "y": 300},
			},
		},
		"edges": []interface{}{
			map[string]interface{}{
				"from": "trigger:out", "to": "panel:in",
				"configuration": map[string]interface{}{"text": "e2e says hello"},
			},
		},
	})
	if errs, ok := built["errors"].([]interface{}); ok && len(errs) > 0 {
		t.Fatalf("build_flow reported errors: %v", errs)
	}

	// --- read back: what was written must come back ------------------------
	read := call(t, "read_project", map[string]interface{}{"project": project})
	var nodeIDs []string
	var positioned, configuredEdges int
	for _, el := range asElements(read["elements"]) {
		switch el["type"] {
		case "tinyNode":
			id, _ := el["id"].(string)
			nodeIDs = append(nodeIDs, id)
			if pos, ok := el["position"].(map[string]interface{}); ok && pos["x"] != nil {
				positioned++
			}
		case "tinyEdge":
			data, _ := el["data"].(map[string]interface{})
			cfg, _ := data["configuration"].(map[string]interface{})
			if text, _ := cfg["text"].(string); text == "e2e says hello" {
				configuredEdges++
			}
		}
	}
	if len(nodeIDs) != 2 {
		t.Fatalf("read back %d nodes, built 2", len(nodeIDs))
	}
	if positioned != 2 {
		t.Errorf("%d of 2 nodes came back with the position that was set", positioned)
	}
	if configuredEdges != 1 {
		t.Errorf("the edge's mapping did not survive the round trip")
	}

	// --- the dashboard surface --------------------------------------------
	var panelID string
	for _, id := range nodeIDs {
		if strings.Contains(id, "display") {
			panelID = id
		}
	}
	if panelID == "" {
		t.Fatal("could not identify the display node")
	}
	call(t, "set_node_dashboard", map[string]interface{}{
		"project": project, "node_id": panelID, "enabled": true,
	})

	dash := call(t, "get_dashboard", map[string]interface{}{"project": project})
	if !strings.Contains(marshal(dash), panelID) {
		t.Errorf("a node pinned to the dashboard does not appear on it: %s", marshal(dash))
	}

	// --- fire it and confirm it ran ---------------------------------------
	var triggerID string
	for _, id := range nodeIDs {
		if strings.Contains(id, "signal") {
			triggerID = id
		}
	}
	if triggerID == "" {
		t.Fatal("could not identify the signal node")
	}

	// A node needs a moment after creation before it will accept a signal;
	// the tools wait internally, and this retries rather than assuming.
	var fired bool
	for attempt := 0; attempt < 5 && !fired; attempt++ {
		res := registry.Execute(ctx, execCtx, "send_signal", map[string]interface{}{
			"project": project, "node_id": triggerID, "port": "_control",
			"data": map[string]interface{}{"send": true, "context": map[string]interface{}{}},
		})
		fired = res.Success
		if !fired {
			time.Sleep(3 * time.Second)
		}
	}
	if !fired {
		t.Fatal("the trigger never accepted a signal")
	}

	// A trace is the only proof the graph actually ran, as opposed to being
	// stored correctly — which is the distinction every green-but-blank flow
	// turned on.
	deadline := time.Now().Add(45 * time.Second)
	for {
		traces := call(t, "get_traces", map[string]interface{}{"project": project, "time_range": "1h"})
		// A multi-span trace is the chain. Single-span traces are reconcile
		// and settings traffic, which a flow produces whether or not it ever
		// ran — asserting on their presence would pass for a graph that is
		// stored and dead, which is the exact failure this test exists for.
		for _, tr := range viaJSON(traces["traces"]) {
			if spans, _ := tr["spans"].(float64); spans >= 2 {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the flow was built and signalled but no chain ever ran — it is stored, not running: %s", marshal(traces))
		}
		time.Sleep(3 * time.Second)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func marshal(v interface{}) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(b)
}

// asElements accepts either shape a tool may hand back for a list of graph
// elements. A tool that returns its own typed slice does not satisfy an
// assertion to []interface{}, and a silent nil there would make this test
// report an empty project for a graph that built perfectly — which it did,
// once, and cost a debugging round.
func asElements(v interface{}) []map[string]interface{} {
	switch list := v.(type) {
	case []map[string]interface{}:
		return list
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(list))
		for _, raw := range list {
			if el, ok := raw.(map[string]interface{}); ok {
				out = append(out, el)
			}
		}
		return out
	}
	return nil
}

// viaJSON re-reads a tool's output through JSON so a test does not have to
// know which concrete type a tool happened to return. Tools hand back typed
// slices in some places and generic maps in others, and a type assertion that
// guesses wrong yields nil silently — which reads exactly like "nothing
// happened" and sent this test chasing a bug that was not there.
func viaJSON(v interface{}) []map[string]interface{} {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []map[string]interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}
