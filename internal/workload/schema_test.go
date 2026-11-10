package workload

// The API server drops spec fields its CRD doesn't know, without an error.
// A newer CLI against an older install must refuse instead of starting a
// session that silently lacks what was asked for: a missing
// runtimeClassName means "no sandbox" while the user believes otherwise.

import (
	"context"
	"slices"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

func TestDroppedFieldsNamesWhatTheServerDidNotKeep(t *testing.T) {
	want := agentsv1.SessionSpec{Task: "t", RuntimeClassName: "gvisor", NodeSelector: map[string]string{"pool": "a"}}
	got := agentsv1.SessionSpec{Task: "t"}
	if d := droppedFields(want, got); !slices.Equal(d, []string{"nodeSelector", "runtimeClassName"}) {
		t.Fatalf("dropped = %v", d)
	}
	if d := droppedFields(want, want); len(d) != 0 {
		t.Fatalf("identical specs reported dropped %v", d)
	}
	// Unset fields are not "dropped": omitempty keeps them out of both.
	if d := droppedFields(agentsv1.SessionSpec{Task: "t"}, agentsv1.SessionSpec{Task: "t"}); len(d) != 0 {
		t.Fatalf("empty fields reported dropped %v", d)
	}
}

// pruningClient behaves like an API server whose CRD predates
// runtimeClassName: Create stores the object without it.
func pruningClient(t *testing.T) client.Client {
	t.Helper()
	return interceptor.NewClient(clientfake.NewClientBuilder().WithScheme(workspaceScheme(t)).Build(), interceptor.Funcs{
		Create: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.CreateOption) error {
			if s, ok := obj.(*agentsv1.Session); ok {
				s.Spec.RuntimeClassName = ""
			}
			return c.Create(ctx, obj, opts...)
		},
	})
}

func TestPrunedSessionIsRefusedAndRemoved(t *testing.T) {
	c := pruningClient(t)
	s := &agentsv1.Session{ObjectMeta: metav1.ObjectMeta{Name: "gv", Namespace: "ns"},
		Spec: agentsv1.SessionSpec{Task: "t", RuntimeClassName: "gvisor"}}
	want := *s.Spec.DeepCopy()
	if err := c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	err := RefuseIfPruned(context.Background(), c, want, s)
	if err == nil || !strings.Contains(err.Error(), "runtimeClassName") || !strings.Contains(err.Error(), "tiny init") {
		t.Fatalf("want a refusal naming the field and the fix, got %v", err)
	}
	gerr := c.Get(context.Background(), client.ObjectKey{Namespace: "ns", Name: "gv"}, &agentsv1.Session{})
	if !apierrors.IsNotFound(gerr) {
		t.Fatalf("the half-made session must be deleted before any workload exists; get = %v", gerr)
	}
}

func TestKeptFieldsPassTheGuard(t *testing.T) {
	c := clientfake.NewClientBuilder().WithScheme(workspaceScheme(t)).Build()
	s := &agentsv1.Session{ObjectMeta: metav1.ObjectMeta{Name: "ok", Namespace: "ns"},
		Spec: agentsv1.SessionSpec{Task: "t", RuntimeClassName: "gvisor"}}
	want := *s.Spec.DeepCopy()
	if err := c.Create(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if err := RefuseIfPruned(context.Background(), c, want, s); err != nil {
		t.Fatalf("nothing was pruned, got %v", err)
	}
}
