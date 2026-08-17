package flow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
	platform "github.com/tiny-systems/platform-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tiny-systems/tiny/internal/kube"
)

// Flows are transparent LAYERS of one project: a node belongs to one flow but
// can be shared into others, and an edge may cross layers. The edge is stored
// on its SOURCE node, the edge configuration on its TARGET node — so wiring
// across layers writes to a node the saved flow does not own. Both entries
// carry the FlowID of the flow that CREATED them, and saving flow X must
// replace exactly the entries tagged X and preserve everything else.
//
// These tests drive saveFlow (SaveFlow minus the kube-client lookup, which
// needs a live rest.Config) against a fake controller-runtime client.

const (
	xNamespace = "test-ns"
	xProject   = "demo"

	// Two layers of one project. flowPrefix takes the first 8 hex chars of
	// the TinyFlow UID, so the prefixes below are what SaveFlow stamps as
	// FlowID.
	flowA   = "flow-a"
	flowB   = "flow-b"
	uidA    = "aaaaaaaa-1111-2222-3333-444444444444"
	uidB    = "bbbbbbbb-1111-2222-3333-444444444444"
	prefixA = "aaaaaaaa"
	prefixB = "bbbbbbbb"

	// a1 is owned by flow A. b1 is owned by flow B and shared into A.
	nodeA1 = "aaaaaaaa.common.dummy-a1"
	nodeB1 = "bbbbbbbb.common.dummy-b1"
	// A third-layer node b1 is already wired to, so flow B's own entries
	// reference something plausible.
	nodeB2 = "bbbbbbbb.common.dummy-b2"

	xModule    = "common"
	xComponent = "dummy"

	outPort = "out"
	inPort  = "in"
)

// ---- fixtures ----

// newFakeKube returns a kube.Client backed by the controller-runtime fake
// client with the TinySystems scheme registered.
func newFakeKube(t *testing.T, objs ...client.Object) *kube.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("register v1alpha1: %v", err)
	}
	return &kube.Client{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(),
		Namespace: xNamespace,
	}
}

// newTestFlow builds a TinyFlow whose UID gives the flow its ID prefix.
func newTestFlow(name, uid string) *v1alpha1.TinyFlow {
	return &v1alpha1.TinyFlow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: xNamespace,
			UID:       types.UID(uid),
			Labels:    map[string]string{v1alpha1.ProjectNameLabel: xProject},
		},
	}
}

// newTestNode builds a TinyNode owned by flowName. sharedWith, when non-empty,
// is the shared-with-flows annotation that makes it visible on another layer's
// canvas.
func newTestNode(name, flowName, sharedWith string) *v1alpha1.TinyNode {
	n := &v1alpha1.TinyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: xNamespace,
			Labels: map[string]string{
				v1alpha1.FlowNameLabel:    flowName,
				v1alpha1.ProjectNameLabel: xProject,
			},
			Annotations: map[string]string{},
		},
		Spec: v1alpha1.TinyNodeSpec{Module: xModule, Component: xComponent},
	}
	if sharedWith != "" {
		n.Annotations[v1alpha1.SharedWithFlowsAnnotation] = sharedWith
	}
	return n
}

// ---- graph payload builders (the editor's whole-graph save) ----

func nodeEl(id string) graphElement {
	return graphElement{
		ID:       id,
		Type:     "tinyNode",
		Position: &graphPos{X: 10, Y: 20},
		Data: map[string]interface{}{
			"module":    xModule,
			"component": xComponent,
		},
	}
}

func edgeEl(src, srcHandle, tgt, tgtHandle string, cfg map[string]interface{}) graphElement {
	return graphElement{
		ID:           src + "-" + tgt,
		Type:         "tinyEdge",
		Source:       src,
		SourceHandle: srcHandle,
		Target:       tgt,
		TargetHandle: tgtHandle,
		Data:         map[string]interface{}{"configuration": cfg},
	}
}

// saveGraph runs a whole-graph save of flowName with the given elements.
func saveGraph(t *testing.T, kc *kube.Client, flowName string, els ...graphElement) {
	t.Helper()
	graph, err := json.Marshal(graphPayload{Elements: els})
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	if _, err := saveFlow(context.Background(), kc, &platform.SaveFlowRequest{
		FlowName:    flowName,
		ProjectName: xProject,
		Graph:       graph,
	}); err != nil {
		t.Fatalf("saveFlow(%s): %v", flowName, err)
	}
}

// ---- assertions ----

func getNode(t *testing.T, kc *kube.Client, name string) *v1alpha1.TinyNode {
	t.Helper()
	n := &v1alpha1.TinyNode{}
	if err := kc.Client.Get(context.Background(), types.NamespacedName{Namespace: xNamespace, Name: name}, n); err != nil {
		t.Fatalf("get node %s: %v", name, err)
	}
	return n
}

// findEdge locates an edge by its semantic identity: source port + "node:port"
// target.
func findEdge(n *v1alpha1.TinyNode, port, to string) (v1alpha1.TinyNodeEdge, bool) {
	for _, e := range n.Spec.Edges {
		if e.Port == port && e.To == to {
			return e, true
		}
	}
	return v1alpha1.TinyNodeEdge{}, false
}

// findPortConfig locates a port config by the pair the SDK runner matches on:
// (From, Port). From == "" is the node's own settings.
func findPortConfig(n *v1alpha1.TinyNode, port, from string) (v1alpha1.TinyNodePortConfig, bool) {
	for _, pc := range n.Spec.Ports {
		if pc.Port == port && pc.From == from {
			return pc, true
		}
	}
	return v1alpha1.TinyNodePortConfig{}, false
}

// ---- tests ----

// A node shared in from another layer is a legitimate edge TARGET. The edge
// itself lands on the owned source; the edge configuration has to land on the
// foreign target, or the mapping is lost. Before the cross-layer fix the whole
// edge was skipped because the target was not a node this flow owns.
func TestSaveFlowPersistsEdgeIntoNodeOwnedByAnotherLayer(t *testing.T) {
	kc := newFakeKube(t,
		newTestFlow(flowA, uidA),
		newTestFlow(flowB, uidB),
		newTestNode(nodeA1, flowA, ""),
		newTestNode(nodeB1, flowB, flowA),
	)

	saveGraph(t, kc, flowA,
		nodeEl(nodeA1),
		nodeEl(nodeB1), // shared in — drawn on A's canvas, owned by B
		edgeEl(nodeA1, outPort, nodeB1, inPort, map[string]interface{}{"greeting": "{{ $.msg }}"}),
	)

	a1 := getNode(t, kc, nodeA1)
	edge, ok := findEdge(a1, outPort, nodeB1+":"+inPort)
	if !ok {
		t.Fatalf("edge %s:%s -> %s:%s not stored on the source node, edges = %+v", nodeA1, outPort, nodeB1, inPort, a1.Spec.Edges)
	}
	if edge.FlowID != prefixA {
		t.Fatalf("edge FlowID = %q, want %q (the flow that created it)", edge.FlowID, prefixA)
	}

	b1 := getNode(t, kc, nodeB1)
	pc, ok := findPortConfig(b1, inPort, nodeA1+":"+outPort)
	if !ok {
		t.Fatalf("edge config not stored on the foreign target node, ports = %+v", b1.Spec.Ports)
	}
	if pc.FlowID != prefixA {
		t.Fatalf("edge config FlowID = %q, want %q", pc.FlowID, prefixA)
	}
	if string(pc.Configuration) != `{"greeting":"{{ $.msg }}"}` {
		t.Fatalf("edge config = %q, want the payload's configuration", pc.Configuration)
	}
}

// The reverse direction: the shared node is the SOURCE, so this flow's edge
// has to be written onto a node it does not own, and the configuration onto
// the node it does.
func TestSaveFlowPersistsEdgeOutOfNodeOwnedByAnotherLayer(t *testing.T) {
	kc := newFakeKube(t,
		newTestFlow(flowA, uidA),
		newTestFlow(flowB, uidB),
		newTestNode(nodeA1, flowA, ""),
		newTestNode(nodeB1, flowB, flowA),
	)

	saveGraph(t, kc, flowA,
		nodeEl(nodeA1),
		nodeEl(nodeB1),
		edgeEl(nodeB1, outPort, nodeA1, inPort, map[string]interface{}{"greeting": "{{ $.msg }}"}),
	)

	b1 := getNode(t, kc, nodeB1)
	edge, ok := findEdge(b1, outPort, nodeA1+":"+inPort)
	if !ok {
		t.Fatalf("edge not stored on the foreign source node, edges = %+v", b1.Spec.Edges)
	}
	if edge.FlowID != prefixA {
		t.Fatalf("edge FlowID = %q, want %q (the flow that created it, not the flow that owns the node)", edge.FlowID, prefixA)
	}

	a1 := getNode(t, kc, nodeA1)
	pc, ok := findPortConfig(a1, inPort, nodeB1+":"+outPort)
	if !ok {
		t.Fatalf("edge config not stored on the target node, ports = %+v", a1.Spec.Ports)
	}
	if pc.FlowID != prefixA {
		t.Fatalf("edge config FlowID = %q, want %q", pc.FlowID, prefixA)
	}
}

// A node's spec carries what EVERY layer wired onto it. Saving flow A must
// leave flow B's entries verbatim — both on the shared node A writes to and on
// A's own node that B previously wired into.
func TestSaveFlowPreservesAnotherLayersEdgesAndConfigs(t *testing.T) {
	b1 := newTestNode(nodeB1, flowB, flowA)
	b1.Spec.Edges = []v1alpha1.TinyNodeEdge{{
		ID: "b-edge", Port: outPort, To: nodeB2 + ":" + inPort, FlowID: prefixB,
	}}
	b1.Spec.Ports = []v1alpha1.TinyNodePortConfig{{
		Port: inPort, From: nodeB2 + ":" + outPort,
		Configuration: []byte(`{"kept":"by-b"}`), FlowID: prefixB,
	}}

	// B also wired into A's node: the edge lives on b1 (above is a different
	// one), the config on a1 — tagged B, and A's save must not eat it.
	a1 := newTestNode(nodeA1, flowA, "")
	a1.Spec.Ports = []v1alpha1.TinyNodePortConfig{{
		Port: inPort, From: nodeB2 + ":" + outPort,
		Configuration: []byte(`{"kept":"by-b-on-a"}`), FlowID: prefixB,
	}}
	a1.Spec.Edges = []v1alpha1.TinyNodeEdge{{
		ID: "b-edge-on-a", Port: outPort, To: nodeB2 + ":" + inPort, FlowID: prefixB,
	}}

	kc := newFakeKube(t,
		newTestFlow(flowA, uidA),
		newTestFlow(flowB, uidB),
		a1, b1,
		newTestNode(nodeB2, flowB, ""),
	)

	// A save that actually rewrites both nodes.
	saveGraph(t, kc, flowA,
		nodeEl(nodeA1),
		nodeEl(nodeB1),
		edgeEl(nodeA1, outPort, nodeB1, inPort, map[string]interface{}{"greeting": "hi"}),
	)

	gotB1 := getNode(t, kc, nodeB1)
	if e, ok := findEdge(gotB1, outPort, nodeB2+":"+inPort); !ok || e.FlowID != prefixB || e.ID != "b-edge" {
		t.Fatalf("flow B's edge on its own node was lost or altered: edges = %+v", gotB1.Spec.Edges)
	}
	if pc, ok := findPortConfig(gotB1, inPort, nodeB2+":"+outPort); !ok || pc.FlowID != prefixB || string(pc.Configuration) != `{"kept":"by-b"}` {
		t.Fatalf("flow B's edge config on its own node was lost or altered: ports = %+v", gotB1.Spec.Ports)
	}

	gotA1 := getNode(t, kc, nodeA1)
	if e, ok := findEdge(gotA1, outPort, nodeB2+":"+inPort); !ok || e.FlowID != prefixB || e.ID != "b-edge-on-a" {
		t.Fatalf("flow B's edge on flow A's node was lost or altered: edges = %+v", gotA1.Spec.Edges)
	}
	if pc, ok := findPortConfig(gotA1, inPort, nodeB2+":"+outPort); !ok || pc.FlowID != prefixB || string(pc.Configuration) != `{"kept":"by-b-on-a"}` {
		t.Fatalf("flow B's edge config on flow A's node was lost or altered: ports = %+v", gotA1.Spec.Ports)
	}
}

// Deleting a cross-layer edge on A's canvas must remove A's entry from the
// shared node and nothing else — the layer that is saved owns exactly its own
// slice.
func TestSaveFlowRemovesOnlyThisLayersEntryFromSharedNode(t *testing.T) {
	// b1 already holds an edge A created (b1 -> a1) plus one of B's own.
	b1 := newTestNode(nodeB1, flowB, flowA)
	b1.Spec.Edges = []v1alpha1.TinyNodeEdge{
		{ID: "a-edge", Port: outPort, To: nodeA1 + ":" + inPort, FlowID: prefixA},
		{ID: "b-edge", Port: outPort, To: nodeB2 + ":" + inPort, FlowID: prefixB},
	}
	b1.Spec.Ports = []v1alpha1.TinyNodePortConfig{
		{Port: inPort, From: nodeA1 + ":" + outPort, Configuration: []byte(`{"by":"a"}`), FlowID: prefixA},
		{Port: inPort, From: nodeB2 + ":" + outPort, Configuration: []byte(`{"by":"b"}`), FlowID: prefixB},
	}

	// The mirror of A's edge: its configuration sits on A's own node.
	a1 := newTestNode(nodeA1, flowA, "")
	a1.Spec.Ports = []v1alpha1.TinyNodePortConfig{
		{Port: inPort, From: nodeB1 + ":" + outPort, Configuration: []byte(`{"by":"a"}`), FlowID: prefixA},
	}

	kc := newFakeKube(t,
		newTestFlow(flowA, uidA),
		newTestFlow(flowB, uidB),
		a1, b1,
		newTestNode(nodeB2, flowB, ""),
	)

	// The user deleted both cross-layer edges: the payload has the nodes but
	// no edges at all.
	saveGraph(t, kc, flowA, nodeEl(nodeA1), nodeEl(nodeB1))

	gotB1 := getNode(t, kc, nodeB1)
	if _, ok := findEdge(gotB1, outPort, nodeA1+":"+inPort); ok {
		t.Fatalf("flow A's edge survived a save of flow A that dropped it: edges = %+v", gotB1.Spec.Edges)
	}
	if _, ok := findPortConfig(gotB1, inPort, nodeA1+":"+outPort); ok {
		t.Fatalf("flow A's edge config survived a save of flow A that dropped it: ports = %+v", gotB1.Spec.Ports)
	}
	if e, ok := findEdge(gotB1, outPort, nodeB2+":"+inPort); !ok || e.FlowID != prefixB {
		t.Fatalf("flow B's edge was removed by a save of flow A: edges = %+v", gotB1.Spec.Edges)
	}
	if pc, ok := findPortConfig(gotB1, inPort, nodeB2+":"+outPort); !ok || pc.FlowID != prefixB {
		t.Fatalf("flow B's edge config was removed by a save of flow A: ports = %+v", gotB1.Spec.Ports)
	}

	gotA1 := getNode(t, kc, nodeA1)
	if _, ok := findPortConfig(gotA1, inPort, nodeB1+":"+outPort); ok {
		t.Fatalf("the removed edge's config lingers on flow A's own node: ports = %+v", gotA1.Spec.Ports)
	}
}

// A port config with From == "" is the node's OWN settings, not an edge
// mapping — it belongs to the node whatever FlowID it carries and whatever
// layer is being saved. (Worst case here: it is tagged with the saving flow.)
func TestSaveFlowKeepsSharedNodesOwnSettings(t *testing.T) {
	b1 := newTestNode(nodeB1, flowB, flowA)
	b1.Spec.Ports = []v1alpha1.TinyNodePortConfig{{
		Port:          v1alpha1.SettingsPort,
		From:          "",
		Configuration: []byte(`{"schedule":"@every 1m"}`),
		FlowID:        prefixA,
	}}

	kc := newFakeKube(t,
		newTestFlow(flowA, uidA),
		newTestFlow(flowB, uidB),
		newTestNode(nodeA1, flowA, ""),
		b1,
	)

	// An edge into b1 forces the shared node to be rewritten, so the settings
	// entry is genuinely exposed to the rewrite.
	saveGraph(t, kc, flowA,
		nodeEl(nodeA1),
		nodeEl(nodeB1),
		edgeEl(nodeA1, outPort, nodeB1, inPort, map[string]interface{}{"greeting": "hi"}),
	)

	gotB1 := getNode(t, kc, nodeB1)
	pc, ok := findPortConfig(gotB1, v1alpha1.SettingsPort, "")
	if !ok {
		t.Fatalf("the shared node's own settings were removed by another flow's save: ports = %+v", gotB1.Spec.Ports)
	}
	if string(pc.Configuration) != `{"schedule":"@every 1m"}` {
		t.Fatalf("settings configuration = %q, want it untouched", pc.Configuration)
	}
}

// A foreign flow's save may write this flow's wiring onto a shared node, but
// it must never claim it: no relabelling to the saving flow, no deletion, and
// the shared-with annotation stays.
func TestSaveFlowNeverRelabelsOrDeletesSharedNode(t *testing.T) {
	kc := newFakeKube(t,
		newTestFlow(flowA, uidA),
		newTestFlow(flowB, uidB),
		newTestNode(nodeA1, flowA, ""),
		newTestNode(nodeB1, flowB, flowA),
	)

	saveGraph(t, kc, flowA,
		nodeEl(nodeA1),
		nodeEl(nodeB1),
		edgeEl(nodeA1, outPort, nodeB1, inPort, map[string]interface{}{"greeting": "hi"}),
	)

	gotB1 := getNode(t, kc, nodeB1) // fatals if the node was deleted
	if got := gotB1.Labels[v1alpha1.FlowNameLabel]; got != flowB {
		t.Fatalf("shared node's flow-name label = %q, want %q — flow A claimed a node it does not own", got, flowB)
	}
	if got := gotB1.Annotations[v1alpha1.SharedWithFlowsAnnotation]; got != flowA {
		t.Fatalf("shared-with-flows annotation = %q, want %q", got, flowA)
	}

	// And a save that no longer draws the shared node at all still must not
	// delete it.
	saveGraph(t, kc, flowA, nodeEl(nodeA1))

	stillThere := getNode(t, kc, nodeB1)
	if got := stillThere.Labels[v1alpha1.FlowNameLabel]; got != flowB {
		t.Fatalf("shared node's flow-name label = %q after being removed from A's canvas, want %q", got, flowB)
	}
}
