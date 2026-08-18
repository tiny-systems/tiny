package adapters

import (
	"context"
	"strings"
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func sharedNode(name, flow, project string) *v1alpha1.TinyNode {
	return &v1alpha1.TinyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pruneNamespace,
			Labels: map[string]string{
				v1alpha1.FlowNameLabel:    flow,
				v1alpha1.ProjectNameLabel: project,
			},
		},
	}
}

func testFlow(name, project string) *v1alpha1.TinyFlow {
	return &v1alpha1.TinyFlow{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pruneNamespace,
			Labels:    map[string]string{v1alpha1.ProjectNameLabel: project},
		},
	}
}

func annotationOf(t *testing.T, e *NodeEditor, nodeID string) (string, bool) {
	t.Helper()
	node := &v1alpha1.TinyNode{}
	if err := e.kube.Client.Get(context.Background(), types.NamespacedName{Namespace: pruneNamespace, Name: nodeID}, node); err != nil {
		t.Fatalf("get node: %v", err)
	}
	v, ok := node.Annotations[v1alpha1.SharedWithFlowsAnnotation]
	return v, ok
}

func TestShareNode_WritesCommaJoinedFlows(t *testing.T) {
	kc := pruneTestClient(t,
		sharedNode("kv-abc12", "watch", "proj"),
		testFlow("watch", "proj"),
		testFlow("setup", "proj"),
		testFlow("alerts", "proj"),
	)
	e := NewNodeEditor(kc)

	got, err := e.shareNode(context.Background(), "proj", "watch", "kv-abc12", []string{"setup", "alerts"})
	if err != nil {
		t.Fatalf("shareNode: %v", err)
	}
	if len(got) != 2 || got[0] != "alerts" || got[1] != "setup" {
		t.Fatalf("resolved = %v, want [alerts setup]", got)
	}

	// Readers split on "," and match each segment exactly — a space after the
	// separator would make the following flow name never match.
	value, ok := annotationOf(t, e, "kv-abc12")
	if !ok {
		t.Fatal("annotation not written")
	}
	if value != "alerts,setup" {
		t.Fatalf("annotation = %q, want %q", value, "alerts,setup")
	}
	if strings.Contains(value, " ") {
		t.Fatalf("annotation %q contains a space; readers match segments exactly", value)
	}
}

// The node's home flow already sees it. Writing it into the shared list would
// be harmless but misleading, and it would come back in the reported set.
func TestShareNode_DropsOwnFlow(t *testing.T) {
	kc := pruneTestClient(t,
		sharedNode("kv-abc12", "watch", "proj"),
		testFlow("watch", "proj"),
		testFlow("setup", "proj"),
	)
	e := NewNodeEditor(kc)

	got, err := e.shareNode(context.Background(), "proj", "watch", "kv-abc12", []string{"watch", "setup", "setup"})
	if err != nil {
		t.Fatalf("shareNode: %v", err)
	}
	if len(got) != 1 || got[0] != "setup" {
		t.Fatalf("resolved = %v, want [setup]", got)
	}
	if value, _ := annotationOf(t, e, "kv-abc12"); value != "setup" {
		t.Fatalf("annotation = %q, want %q", value, "setup")
	}
}

// An annotation naming a flow that does not exist is invisible: nothing errors,
// the node simply never appears where it was meant to.
func TestShareNode_RejectsUnknownFlow(t *testing.T) {
	kc := pruneTestClient(t,
		sharedNode("kv-abc12", "watch", "proj"),
		testFlow("watch", "proj"),
	)
	e := NewNodeEditor(kc)

	_, err := e.shareNode(context.Background(), "proj", "watch", "kv-abc12", []string{"typo-flow"})
	if err == nil {
		t.Fatal("expected an error for a flow that does not exist")
	}
	if !strings.Contains(err.Error(), "typo-flow") {
		t.Fatalf("error %q should name the offending flow", err)
	}
	if _, ok := annotationOf(t, e, "kv-abc12"); ok {
		t.Fatal("annotation was written despite the error")
	}
}

func TestShareNode_EmptyListUnshares(t *testing.T) {
	node := sharedNode("kv-abc12", "watch", "proj")
	node.Annotations = map[string]string{v1alpha1.SharedWithFlowsAnnotation: "setup"}
	kc := pruneTestClient(t, node, testFlow("watch", "proj"), testFlow("setup", "proj"))
	e := NewNodeEditor(kc)

	got, err := e.shareNode(context.Background(), "proj", "watch", "kv-abc12", nil)
	if err != nil {
		t.Fatalf("shareNode: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("resolved = %v, want empty", got)
	}
	if value, ok := annotationOf(t, e, "kv-abc12"); ok {
		t.Fatalf("annotation still present as %q; un-sharing must remove it", value)
	}
}
