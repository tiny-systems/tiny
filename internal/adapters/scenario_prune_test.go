package adapters

import (
	"context"
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/tiny-systems/tiny/internal/kube"
)

const pruneNamespace = "tinysystems"

func pruneTestClient(t *testing.T, objs ...client.Object) *kube.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("register v1alpha1: %v", err)
	}
	return &kube.Client{
		Client:    fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build(),
		Namespace: pruneNamespace,
	}
}

func scenarioNode(name, project string) *v1alpha1.TinyNode {
	return &v1alpha1.TinyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pruneNamespace,
			Labels:    map[string]string{v1alpha1.ProjectNameLabel: project},
		},
	}
}

func scenarioWith(name, project string, ports ...string) *v1alpha1.TinyScenario {
	s := &v1alpha1.TinyScenario{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: pruneNamespace,
			Labels:    map[string]string{v1alpha1.ProjectNameLabel: project},
		},
	}
	for _, p := range ports {
		s.Spec.Ports = append(s.Spec.Ports, v1alpha1.ScenarioPortData{Port: p, Data: []byte(`{"x":1}`)})
	}
	return s
}

func portsOf(t *testing.T, kc *kube.Client, name string) []string {
	t.Helper()
	s := &v1alpha1.TinyScenario{}
	if err := kc.Client.Get(context.Background(), types.NamespacedName{Namespace: pruneNamespace, Name: name}, s); err != nil {
		t.Fatalf("get scenario %s: %v", name, err)
	}
	out := make([]string, 0, len(s.Spec.Ports))
	for _, p := range s.Spec.Ports {
		out = append(out, p.Port)
	}
	return out
}

func TestPruneOrphanScenarioPorts_DropsOnlyDeadNodes(t *testing.T) {
	kc := pruneTestClient(t,
		scenarioNode("alive-abc12", "proj"),
		scenarioWith("auto-scaffold", "proj", "alive-abc12:input", "deleted-xyz99:input", "alive-abc12:other"),
	)

	removed, err := pruneOrphanScenarioPorts(context.Background(), kc, "proj")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}

	got := portsOf(t, kc, "auto-scaffold")
	want := []string{"alive-abc12:input", "alive-abc12:other"}
	if len(got) != len(want) {
		t.Fatalf("ports = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ports = %v, want %v", got, want)
		}
	}
}

// A sweep must not reach across projects: two projects can hold nodes with
// unrelated names, and pruning by the wrong node set would delete live samples.
func TestPruneOrphanScenarioPorts_IsScopedToOneProject(t *testing.T) {
	kc := pruneTestClient(t,
		scenarioNode("mine-abc12", "mine"),
		scenarioNode("theirs-def34", "theirs"),
		scenarioWith("theirs-scenario", "theirs", "theirs-def34:input"),
	)

	removed, err := pruneOrphanScenarioPorts(context.Background(), kc, "mine")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0 — the other project's scenario is not ours to touch", removed)
	}
	if got := portsOf(t, kc, "theirs-scenario"); len(got) != 1 {
		t.Fatalf("other project's ports = %v, want them untouched", got)
	}
}

// Nothing to do must not write: an Update on every delete would churn
// resourceVersions and wake every watcher for no reason.
func TestPruneOrphanScenarioPorts_NoWriteWhenClean(t *testing.T) {
	kc := pruneTestClient(t,
		scenarioNode("alive-abc12", "proj"),
		scenarioWith("clean", "proj", "alive-abc12:input"),
	)

	before := &v1alpha1.TinyScenario{}
	if err := kc.Client.Get(context.Background(), types.NamespacedName{Namespace: pruneNamespace, Name: "clean"}, before); err != nil {
		t.Fatalf("get: %v", err)
	}

	removed, err := pruneOrphanScenarioPorts(context.Background(), kc, "proj")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}

	after := &v1alpha1.TinyScenario{}
	if err := kc.Client.Get(context.Background(), types.NamespacedName{Namespace: pruneNamespace, Name: "clean"}, after); err != nil {
		t.Fatalf("get: %v", err)
	}
	if before.ResourceVersion != after.ResourceVersion {
		t.Fatalf("scenario was rewritten (%s -> %s) with nothing to prune", before.ResourceVersion, after.ResourceVersion)
	}
}

// An empty project name means "no project scope known" — sweeping every
// scenario in the namespace on that basis would be destructive.
func TestPruneOrphanScenarioPorts_EmptyProjectDoesNothing(t *testing.T) {
	kc := pruneTestClient(t, scenarioWith("orphaned", "proj", "deleted-xyz99:input"))

	removed, err := pruneOrphanScenarioPorts(context.Background(), kc, "")
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d, want 0", removed)
	}
	if got := portsOf(t, kc, "orphaned"); len(got) != 1 {
		t.Fatalf("ports = %v, want untouched", got)
	}
}
