package flow

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/utils"
	"github.com/tiny-systems/tiny/internal/kube"
)

// The canvas is a third serialiser of the same graph.
//
// build_flow writes it one way and read_project reads it another — that pair
// is covered elsewhere. The editor is a third: it saves a whole graph at once
// and reads it back through the flow stream, and the two halves are separate
// code again. Every drift between them has landed the same way, as work a
// person did on the canvas that quietly did not survive: an edge mapping
// erased on save, a shared node's configuration dropped when another layer
// was saved, a position that moved back.
//
// This saves through the real save path and reads back through the same
// serialisers the stream uses — ApiNodeToMap for a node, buildEdge for an
// edge — requiring what was drawn to come back.

// canvasNode is the element shape the editor actually sends: a position, and
// any settings carried on the _settings HANDLE rather than as a bare field.
// The distinction matters — a test that invents its own shape passes or fails
// on something the editor never sends, which is not a test of the save path.
func canvasNode(id string, x, y float64, settings map[string]interface{}) graphElement {
	el := nodeEl(id)
	el.Position = &graphPos{X: x, Y: y}
	if settings != nil {
		el.Data["handles"] = []interface{}{
			map[string]interface{}{
				"id":            v1alpha1.SettingsPort,
				"configuration": settings,
			},
		}
	}
	return el
}

// readBack renders the saved graph the way the flow stream renders it for the
// canvas, so the assertions run against the serialiser the editor actually
// receives rather than against the CRs directly.
func readBack(t *testing.T, kc *kube.Client, flowName string) (map[string]map[string]interface{}, []map[string]interface{}) {
	t.Helper()
	ctx := context.Background()

	list := &v1alpha1.TinyNodeList{}
	if err := kc.Client.List(ctx, list); err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	nodesMap := make(map[string]v1alpha1.TinyNode, len(list.Items))
	for _, n := range list.Items {
		nodesMap[n.Name] = n
	}

	statusPortSchemaMap, portConfigMap, edgeConfigMap, portSchemaMap, _, err := utils.GetFlowMaps(nodesMap)
	if err != nil {
		t.Fatalf("build flow maps: %v", err)
	}
	portExampleMap := utils.GetPortExampleMap(nodesMap)

	nodes := map[string]map[string]interface{}{}
	var edges []map[string]interface{}
	for _, node := range list.Items {
		if node.Labels[v1alpha1.FlowNameLabel] != flowName {
			continue
		}
		nodes[node.Name] = utils.ApiNodeToMap(node, map[string]interface{}{}, false)

		for _, edge := range node.Spec.Edges {
			edgeMap, err := buildEdge(ctx, node, edge, flowName, false, nodesMap,
				statusPortSchemaMap, portConfigMap, edgeConfigMap, portSchemaMap,
				portExampleMap, nil, nil)
			if err != nil {
				t.Fatalf("render edge %s: %v", edge.ID, err)
			}
			edges = append(edges, edgeMap)
		}
	}
	return nodes, edges
}

func TestCanvasRoundTrip_WhatWasDrawnComesBack(t *testing.T) {
	kc := newFakeKube(t, newTestFlow("main", "aaaabbbb-0000-1111-2222-333344445555"))

	src := "aaaabbbb.tinysystems-common-module-v0.signal-0001"
	dst := "aaaabbbb.tinysystems-common-module-v0.display-0002"
	mapping := map[string]interface{}{"text": "{{$.context.summary}}"}

	saveGraph(t, kc, "main",
		canvasNode(src, 40, 300, map[string]interface{}{"schedule": "*/5 * * * *"}),
		canvasNode(dst, 620, 300, nil),
		edgeEl(src, "out", dst, "in", mapping),
	)

	nodes, edges := readBack(t, kc, "main")

	if len(nodes) != 2 {
		t.Fatalf("read back %d nodes, saved 2", len(nodes))
	}
	if len(edges) != 1 {
		t.Fatalf("read back %d edges, saved 1", len(edges))
	}

	// Position — the thing a person spent time arranging.
	for id, want := range map[string][2]float64{src: {40, 300}, dst: {620, 300}} {
		pos, ok := nodes[id]["position"].(map[string]interface{})
		if !ok {
			t.Fatalf("node %s came back with no position", id)
		}
		if float64(utils.GetInt(pos["x"])) != want[0] || float64(utils.GetInt(pos["y"])) != want[1] {
			t.Errorf("node %s position = %v, want x=%v y=%v", id, pos, want[0], want[1])
		}
	}

	// The mapping on the edge: erased on save once, which silently broke every
	// flow that was edited on the canvas.
	data, _ := edges[0]["data"].(map[string]interface{})
	cfg := configOf(t, data["configuration"])
	if cfg["text"] != "{{$.context.summary}}" {
		t.Errorf("edge configuration = %v, want the mapping that was drawn", cfg)
	}
}

// Saving one layer must not disturb another's work. A shared node belongs to
// its own flow; a save of the layer it is shared INTO used to be able to
// rewrite it.
func TestCanvasRoundTrip_SavingOneLayerLeavesTheOtherIntact(t *testing.T) {
	kc := newFakeKube(t,
		newTestFlow("main", "aaaabbbb-0000-1111-2222-333344445555"),
		newTestFlow("other", "ccccdddd-0000-1111-2222-333344445555"),
	)

	owned := "aaaabbbb.tinysystems-common-module-v0.signal-0001"
	target := "aaaabbbb.tinysystems-common-module-v0.display-0002"
	saveGraph(t, kc, "main",
		canvasNode(owned, 40, 300, nil),
		canvasNode(target, 620, 300, nil),
		edgeEl(owned, "out", target, "in", map[string]interface{}{"text": "first layer"}),
	)

	// A second layer saves its own, unrelated graph.
	otherA := "ccccdddd.tinysystems-common-module-v0.signal-0003"
	otherB := "ccccdddd.tinysystems-common-module-v0.display-0004"
	saveGraph(t, kc, "other",
		canvasNode(otherA, 80, 80, nil),
		canvasNode(otherB, 400, 80, nil),
		edgeEl(otherA, "out", otherB, "in", map[string]interface{}{"text": "second layer"}),
	)

	// The first layer must be exactly as it was left.
	nodes, edges := readBack(t, kc, "main")
	if len(nodes) != 2 || len(edges) != 1 {
		t.Fatalf("the first layer lost work: %d nodes, %d edges", len(nodes), len(edges))
	}
	data, _ := edges[0]["data"].(map[string]interface{})
	if cfg := configOf(t, data["configuration"]); cfg["text"] != "first layer" {
		t.Errorf("the first layer's mapping = %v after another layer was saved", cfg)
	}

	pos, _ := nodes[owned]["position"].(map[string]interface{})
	if utils.GetInt(pos["x"]) != 40 {
		t.Errorf("the first layer's layout moved: %v", pos)
	}
}

// A node saved with settings keeps them; the editor sends settings and the
// graph is the only record of what a person configured.
func TestCanvasRoundTrip_SettingsSurvive(t *testing.T) {
	kc := newFakeKube(t, newTestFlow("main", "aaaabbbb-0000-1111-2222-333344445555"))
	id := "aaaabbbb.tinysystems-common-module-v0.signal-0001"

	saveGraph(t, kc, "main", canvasNode(id, 10, 10, map[string]interface{}{
		"schedule": "*/5 * * * *",
		"context":  map[string]interface{}{"namespace": "tinysystems"},
	}))

	node := getNode(t, kc, id)
	var found bool
	for _, pc := range node.Spec.Ports {
		if pc.Port == v1alpha1.SettingsPort && pc.From == "" && len(pc.Configuration) > 0 {
			found = true
			if !containsAll(string(pc.Configuration), "*/5 * * * *", "tinysystems") {
				t.Errorf("settings came back incomplete: %s", pc.Configuration)
			}
		}
	}
	if !found {
		t.Fatal("a node saved with settings has none")
	}
}

// configOf reads an edge's configuration whatever concrete shape the
// serialiser used for it.
func configOf(t *testing.T, v interface{}) map[string]interface{} {
	t.Helper()
	switch cfg := v.(type) {
	case map[string]interface{}:
		return cfg
	case []byte:
		var out map[string]interface{}
		if err := json.Unmarshal(cfg, &out); err != nil {
			t.Fatalf("edge configuration is not JSON: %s", cfg)
		}
		return out
	case json.RawMessage:
		var out map[string]interface{}
		if err := json.Unmarshal(cfg, &out); err != nil {
			t.Fatalf("edge configuration is not JSON: %s", cfg)
		}
		return out
	}
	t.Fatalf("edge configuration has unexpected type %T", v)
	return nil
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}
