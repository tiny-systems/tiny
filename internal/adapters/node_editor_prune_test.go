package adapters

import (
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
)

// node builds a TinyNode with the given incoming Ports (From/Port pairs) and
// outgoing Edges (ID/To pairs) for the prune tests.
func node(ports [][2]string, edges [][2]string) *v1alpha1.TinyNode {
	n := &v1alpha1.TinyNode{}
	for _, p := range ports {
		n.Spec.Ports = append(n.Spec.Ports, v1alpha1.TinyNodePortConfig{From: p[0], Port: p[1]})
	}
	for _, e := range edges {
		n.Spec.Edges = append(n.Spec.Edges, v1alpha1.TinyNodeEdge{ID: e[0], To: e[1]})
	}
	return n
}

func TestRemoveRefs_PrunesBothHalves(t *testing.T) {
	// Mirrors the live bug: a target node keeps a Ports[].From pointing at a
	// deleted source, and a source node keeps an Edges[].To pointing at it.
	n := node(
		[][2]string{{"gone:out", "response"}, {"kept:out", "response"}},
		[][2]string{{"e1", "gone:in"}, {"e2", "kept:in"}},
	)
	if changed := removeRefs(n, "gone:"); !changed {
		t.Fatal("expected changed=true")
	}
	if len(n.Spec.Ports) != 1 || n.Spec.Ports[0].From != "kept:out" {
		t.Fatalf("ports not pruned correctly: %+v", n.Spec.Ports)
	}
	if len(n.Spec.Edges) != 1 || n.Spec.Edges[0].To != "kept:in" {
		t.Fatalf("edges not pruned correctly: %+v", n.Spec.Edges)
	}
}

func TestRemoveRefs_NoMatchIsNoOp(t *testing.T) {
	n := node([][2]string{{"kept:out", "response"}}, [][2]string{{"e1", "kept:in"}})
	if changed := removeRefs(n, "gone:"); changed {
		t.Fatal("expected changed=false when nothing references the deleted node")
	}
	if len(n.Spec.Ports) != 1 || len(n.Spec.Edges) != 1 {
		t.Fatal("no-op prune must not drop anything")
	}
}

func TestRemoveRefs_PrefixBoundary(t *testing.T) {
	// "node1:" must not match "node1extra:out" — the colon guards against a
	// node id that is a prefix of another node's id.
	n := node([][2]string{{"node1extra:out", "response"}}, [][2]string{{"e1", "node1extra:in"}})
	if changed := removeRefs(n, "node1:"); changed {
		t.Fatal("prefix boundary violated: node1: must not match node1extra:")
	}
	if len(n.Spec.Ports) != 1 || len(n.Spec.Edges) != 1 {
		t.Fatal("boundary case dropped a non-matching ref")
	}
}

func TestRemoveEdgeByID(t *testing.T) {
	n := node(nil, [][2]string{{"e1", "a:in"}, {"e2", "b:in"}})
	if changed := removeEdgeByID(n, "e1"); !changed {
		t.Fatal("expected changed=true")
	}
	if len(n.Spec.Edges) != 1 || n.Spec.Edges[0].ID != "e2" {
		t.Fatalf("wrong edge removed: %+v", n.Spec.Edges)
	}
	if removeEdgeByID(n, "missing") {
		t.Fatal("expected changed=false for unknown edge id")
	}
}

func TestRemovePortFrom(t *testing.T) {
	// Two edges from the same source but into different target ports: only the
	// one matching BOTH From and Port must go.
	n := node([][2]string{{"src:out", "response"}, {"src:out", "other"}}, nil)
	if changed := removePortFrom(n, "src:out", "response"); !changed {
		t.Fatal("expected changed=true")
	}
	if len(n.Spec.Ports) != 1 || n.Spec.Ports[0].Port != "other" {
		t.Fatalf("wrong port entry removed: %+v", n.Spec.Ports)
	}
	if removePortFrom(n, "src:out", "response") {
		t.Fatal("expected changed=false when the entry is already gone")
	}
}
