package sessions

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	nodev1 "k8s.io/api/node/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/workload"
)

// sandboxStore is a namespace on a cluster that offers gVisor.
func sandboxStore(t *testing.T) (*Store, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme, rbacv1.AddToScheme, nodev1.AddToScheme, agentsv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	gvisor := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"}
	c := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(gvisor).Build()
	return &Store{Kube: &kube.Client{Client: c, Namespace: "agents"}}, c
}

func podRuntimeClass(t *testing.T, c client.Client, session string) string {
	t.Helper()
	var dep appsv1.Deployment
	if err := c.Get(t.Context(), client.ObjectKey{Namespace: "agents", Name: session + "-agent"}, &dep); err != nil {
		t.Fatalf("deployment: %v", err)
	}
	if rc := dep.Spec.Template.Spec.RuntimeClassName; rc != nil {
		return *rc
	}
	return ""
}

// With the sandbox on, a session that names no runtime runs under gVisor,
// and the spec says so; `none` opts one session out.
func TestSandboxDefaultsNewSessions(t *testing.T) {
	store, c := sandboxStore(t)
	ctx := t.Context()
	if err := store.SandboxOn(ctx, ""); err != nil {
		t.Fatalf("sandbox on: %v", err)
	}
	if got, _ := store.SandboxState(ctx); got != "gvisor" {
		t.Fatalf("state = %q, want gvisor", got)
	}

	se, err := store.Create(ctx, CreateOpts{Name: "boxed", Command: []string{"echo"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if se.Spec.RuntimeClassName != "gvisor" || podRuntimeClass(t, c, "boxed") != "gvisor" {
		t.Fatalf("spec %q pod %q, want gvisor", se.Spec.RuntimeClassName, podRuntimeClass(t, c, "boxed"))
	}

	se, err = store.Create(ctx, CreateOpts{Name: "bare", Command: []string{"echo"}, RuntimeClass: workload.NoRuntimeClass})
	if err != nil {
		t.Fatalf("create opted out: %v", err)
	}
	if se.Spec.RuntimeClassName != "" || podRuntimeClass(t, c, "bare") != "" {
		t.Fatalf("opted-out session runs under %q", se.Spec.RuntimeClassName)
	}

	if err := store.SandboxOff(ctx); err != nil {
		t.Fatalf("sandbox off: %v", err)
	}
	if _, err := store.Create(ctx, CreateOpts{Name: "after", Command: []string{"echo"}}); err != nil {
		t.Fatalf("create after off: %v", err)
	}
	if got := podRuntimeClass(t, c, "after"); got != "" {
		t.Fatalf("after sandbox off the pod runs under %q", got)
	}
}

// A class the cluster lacks would break every new session, so it is
// refused and the setting stays as it was.
func TestSandboxOnRefusesAMissingClass(t *testing.T) {
	store, _ := sandboxStore(t)
	ctx := t.Context()
	err := store.SandboxOn(ctx, "kata")
	if err == nil || !strings.Contains(err.Error(), `"kata" does not exist`) {
		t.Fatalf("err = %v, want a missing-class refusal", err)
	}
	if got, _ := store.SandboxState(ctx); got != "" {
		t.Fatalf("state = %q after a refusal", got)
	}
	if err := store.SandboxOn(ctx, workload.NoRuntimeClass); err == nil {
		t.Fatal(`"none" accepted as a sandbox`)
	}
}

// A Session created some other way (kubectl, an action, another client)
// still gets the sandbox when its workload is built; an opted-out one
// stays out.
func TestSandboxReachesSessionsCreatedElsewhere(t *testing.T) {
	store, c := sandboxStore(t)
	ctx := t.Context()
	if err := store.SandboxOn(ctx, ""); err != nil {
		t.Fatal(err)
	}
	raw := &agentsv1.Session{ObjectMeta: metav1.ObjectMeta{Namespace: "agents", Name: "raw", UID: "u1"},
		Spec: agentsv1.SessionSpec{Agent: "command", Command: []string{"echo"}}}
	if err := c.Create(ctx, raw); err != nil {
		t.Fatal(err)
	}
	if err := workload.Ensure(ctx, c, raw); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if got := podRuntimeClass(t, c, "raw"); got != "gvisor" {
		t.Fatalf("raw session's pod runs under %q, want gvisor", got)
	}

	out, err := store.Create(ctx, CreateOpts{Name: "out", Command: []string{"echo"}, RuntimeClass: workload.NoRuntimeClass})
	if err != nil {
		t.Fatal(err)
	}
	if out.Annotations[workload.NoSandboxAnnotation] != "true" {
		t.Fatal("opt-out not recorded on the session")
	}
	if got := podRuntimeClass(t, c, "out"); got != "" {
		t.Fatalf("opted-out session's pod runs under %q", got)
	}
}
