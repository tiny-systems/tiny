package adapters

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/kube"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

// What you write is what you read.
//
// The write path (NodeEditor, used by build_flow and edit_flow) and the read
// path (ProjectReader, used by read_project) are separate implementations of
// the same graph, and nothing made them agree. Every drift between them has
// been the same experience for a caller: it sets something, reads back to
// check its own work, and cannot tell whether the system dropped the value or
// merely fails to report it.
//
// Three shipped bugs lived in exactly that gap — node positions were written
// and never returned, edge configurations came back as {} while traces proved
// they were live, and a flow's title came back as its resource name so a
// display name looked discarded. Each was found by an agent trying to verify
// its own work, and each was invisible to tests that only exercised one side.
//
// This asserts the round trip instead: build a graph through the real write
// path, read it through the real read path, and require what comes back to
// match what went in. A future field that is writable and not readable fails
// here rather than in front of whoever is using the product.

const rtNamespace = "tinysystems"

// reconcile stands in for the operator: it publishes the ports a node's
// component would declare. The write path consults published ports when
// validating an edge, so without this the test would exercise a graph in a
// state no live flow is ever configured in.
func reconcile(t *testing.T, kc *kube.Client, nodeID string, ports ...v1alpha1.TinyNodePortStatus) {
	t.Helper()
	node := &v1alpha1.TinyNode{}
	key := types.NamespacedName{Namespace: rtNamespace, Name: nodeID}
	if err := kc.Client.Get(context.Background(), key, node); err != nil {
		t.Fatalf("get node %s: %v", nodeID, err)
	}
	node.Status.Ports = ports
	node.Status.ObservedGeneration = node.Generation
	if err := kc.Client.Status().Update(context.Background(), node); err != nil {
		// The fake client may not split status; fall back to a plain update.
		if err := kc.Client.Update(context.Background(), node); err != nil {
			t.Fatalf("publish ports for %s: %v", nodeID, err)
		}
	}
}

func port(name string, source bool) v1alpha1.TinyNodePortStatus {
	return v1alpha1.TinyNodePortStatus{Name: name, Source: source}
}

func TestGraphRoundTrip_WrittenValuesComeBack(t *testing.T) {
	flow := &v1alpha1.TinyFlow{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "watch",
			Namespace:   rtNamespace,
			UID:         "9f1c2b3a-4d5e-6f70-8192-a3b4c5d6e7f8",
			Labels:      map[string]string{v1alpha1.ProjectNameLabel: "proj"},
			Annotations: map[string]string{v1alpha1.FlowDescriptionAnnotation: "Pod watch"},
		},
	}
	kc := pruneTestClient(t, flow)
	editor := NewNodeEditor(kc)
	reader := NewProjectReader(kc)
	ctx := context.Background()

	// --- write ------------------------------------------------------------
	src, err := editor.AddNode(ctx, "proj", "watch", "cron", "tinysystems/common-module-v0", nil)
	if err != nil {
		t.Fatalf("add source node: %v", err)
	}
	dst, err := editor.AddNode(ctx, "proj", "watch", "display", "tinysystems/common-module-v0", nil)
	if err != nil {
		t.Fatalf("add target node: %v", err)
	}

	reconcile(t, kc, src.NodeID, port("out", true), port(v1alpha1.SettingsPort, false))
	reconcile(t, kc, dst.NodeID, port("in", false), port(v1alpha1.SettingsPort, false))

	if err := editor.RepositionNode(ctx, "proj", "watch", src.NodeID, 40, 300); err != nil {
		t.Fatalf("position source: %v", err)
	}
	if err := editor.RepositionNode(ctx, "proj", "watch", dst.NodeID, 620, 300); err != nil {
		t.Fatalf("position target: %v", err)
	}

	settings := map[string]interface{}{"schedule": "*/5 * * * *"}
	if _, err := editor.ConfigureNodeSettings(ctx, "proj", "watch", src.NodeID, settings, nil); err != nil {
		t.Fatalf("configure settings: %v", err)
	}

	edge, err := editor.AddEdge(ctx, "proj", "watch", src.NodeID, "out", dst.NodeID, "in")
	if err != nil {
		t.Fatalf("add edge: %v", err)
	}
	config := map[string]interface{}{"text": "{{$.context.summary}}"}
	if _, err := editor.ConfigureEdge(ctx, "proj", "watch", edge.EdgeID, config, nil, ""); err != nil {
		t.Fatalf("configure edge: %v", err)
	}

	// --- read -------------------------------------------------------------
	got, err := reader.ReadProjectElements(ctx, "proj")
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	nodes := map[string]map[string]interface{}{}
	var edges []map[string]interface{}
	for _, el := range got.Elements {
		switch el["type"] {
		case "tinyNode":
			nodes[el["id"].(string)] = el
		case "tinyEdge":
			edges = append(edges, el)
		}
	}

	if len(nodes) != 2 {
		t.Fatalf("read back %d nodes, wrote 2", len(nodes))
	}
	if len(edges) != 1 {
		t.Fatalf("read back %d edges, wrote 1", len(edges))
	}

	// Position: written through the same call build_flow makes.
	for id, want := range map[string][2]int{src.NodeID: {40, 300}, dst.NodeID: {620, 300}} {
		pos, ok := nodes[id]["position"].(map[string]interface{})
		if !ok {
			t.Fatalf("node %s came back with no position, but one was set", id)
		}
		if pos["x"] != want[0] || pos["y"] != want[1] {
			t.Errorf("node %s position = %v, want x=%d y=%d", id, pos, want[0], want[1])
		}
	}

	// The flow's name as a person gave it, not its resource name.
	if title := nodes[src.NodeID]["flow_title"]; title != "Pod watch" {
		t.Errorf("flow_title = %v, want the display name", title)
	}

	// Settings.
	data := nodes[src.NodeID]["data"].(map[string]interface{})
	readSettings, _ := data["settings"].(map[string]interface{})
	if readSettings["schedule"] != "*/5 * * * *" {
		t.Errorf("settings = %v, want the schedule that was written", readSettings)
	}

	// Component identity.
	if data["component"] != "cron" {
		t.Errorf("component = %v, want cron", data["component"])
	}

	// The edge, both its endpoints and the mapping it carries.
	e := edges[0]
	if e["source"] != src.NodeID || e["target"] != dst.NodeID {
		t.Errorf("edge endpoints = %v -> %v", e["source"], e["target"])
	}
	if e["sourceHandle"] != "out" || e["targetHandle"] != "in" {
		t.Errorf("edge handles = %v -> %v", e["sourceHandle"], e["targetHandle"])
	}
	edgeData := e["data"].(map[string]interface{})
	readConfig, _ := edgeData["configuration"].(map[string]interface{})
	if readConfig["text"] != "{{$.context.summary}}" {
		t.Errorf("edge configuration = %v, want the mapping that was written", readConfig)
	}
}

// A node nobody placed must report no position, rather than an origin that
// reads as a deliberate placement at 0,0.
func TestGraphRoundTrip_UnplacedNodeHasNoPosition(t *testing.T) {
	node := &v1alpha1.TinyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "n1",
			Namespace: rtNamespace,
			Labels: map[string]string{
				v1alpha1.ProjectNameLabel: "proj",
				v1alpha1.FlowNameLabel:    "watch",
			},
		},
	}
	kc := pruneTestClient(t, node)

	got, err := NewProjectReader(kc).ReadProjectElements(context.Background(), "proj")
	if err != nil {
		t.Fatalf("read project: %v", err)
	}
	for _, el := range got.Elements {
		if el["type"] != "tinyNode" {
			continue
		}
		if el["position"] != nil {
			t.Fatalf("position = %v, want none for a node never placed", el["position"])
		}
	}
}

// Two sources feeding one target port keep their own mappings. The
// configuration lives on the target keyed by source, so reading it by port
// alone would hand both edges whichever mapping happened to be stored first.
func TestGraphRoundTrip_EdgesToOnePortKeepTheirOwnMapping(t *testing.T) {
	target := &v1alpha1.TinyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "target",
			Namespace: rtNamespace,
			Labels: map[string]string{
				v1alpha1.ProjectNameLabel: "proj",
				v1alpha1.FlowNameLabel:    "watch",
			},
		},
		Spec: v1alpha1.TinyNodeSpec{
			Ports: []v1alpha1.TinyNodePortConfig{
				{Port: "in", From: "a:out", Configuration: json.RawMessage(`{"text":"from a"}`)},
				{Port: "in", From: "b:out", Configuration: json.RawMessage(`{"text":"from b"}`)},
			},
		},
	}
	mk := func(name string) *v1alpha1.TinyNode {
		return &v1alpha1.TinyNode{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: rtNamespace,
				Labels: map[string]string{
					v1alpha1.ProjectNameLabel: "proj",
					v1alpha1.FlowNameLabel:    "watch",
				},
			},
			Spec: v1alpha1.TinyNodeSpec{
				Edges: []v1alpha1.TinyNodeEdge{{ID: name + "_out-target_in", Port: "out", To: "target:in"}},
			},
		}
	}
	kc := pruneTestClient(t, target, mk("a"), mk("b"))

	got, err := NewProjectReader(kc).ReadProjectElements(context.Background(), "proj")
	if err != nil {
		t.Fatalf("read project: %v", err)
	}

	seen := map[string]string{}
	for _, el := range got.Elements {
		if el["type"] != "tinyEdge" {
			continue
		}
		cfg, _ := el["data"].(map[string]interface{})["configuration"].(map[string]interface{})
		text, _ := cfg["text"].(string)
		seen[el["source"].(string)] = text
	}
	if seen["a"] != "from a" || seen["b"] != "from b" {
		t.Fatalf("mappings crossed or were lost: %v", seen)
	}
}
