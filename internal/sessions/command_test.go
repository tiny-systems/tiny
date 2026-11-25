package sessions

import (
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	clientfake "sigs.k8s.io/controller-runtime/pkg/client/fake"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/kube"
)

func commandTestStore(t *testing.T) (*Store, client.Client) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, agentsv1.AddToScheme, appsv1.AddToScheme, nodev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	// The cluster offers gVisor, as a sandboxed cluster would.
	gvisor := &nodev1.RuntimeClass{ObjectMeta: metav1.ObjectMeta{Name: "gvisor"}, Handler: "runsc"}
	fc := clientfake.NewClientBuilder().WithScheme(scheme).WithObjects(gvisor).Build()
	return &Store{Kube: &kube.Client{Client: fc, Namespace: "agents"}}, fc
}

// `tiny run` creates a command session: agent "command", the argv as
// given, and a pod with one container that runs it, no sidecar and no
// service-account token.
func TestCreateCommandSession(t *testing.T) {
	store, fc := commandTestStore(t)
	argv := []string{"python3", "-c", "print('hi there')"}
	se, err := store.Create(t.Context(), CreateOpts{
		Name: "run-1", Image: "python:3.13-slim", Repo: "https://github.com/acme/app",
		RuntimeClass: "gvisor", Command: argv,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if se.Spec.Agent != "command" || !slices.Equal(se.Spec.Command, argv) {
		t.Fatalf("spec agent=%q command=%q", se.Spec.Agent, se.Spec.Command)
	}
	var dep appsv1.Deployment
	if err := fc.Get(t.Context(), client.ObjectKey{Namespace: "agents", Name: "run-1-agent"}, &dep); err != nil {
		t.Fatalf("deployment: %v", err)
	}
	pod := dep.Spec.Template.Spec
	if len(pod.Containers) != 1 {
		t.Fatalf("containers = %d, want just the command's", len(pod.Containers))
	}
	if !slices.Equal(pod.Containers[0].Args, argv) {
		t.Fatalf("args = %q, want %q", pod.Containers[0].Args, argv)
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken {
		t.Fatal("a command session must not get a service-account token")
	}
	if pod.RuntimeClassName == nil || *pod.RuntimeClassName != "gvisor" {
		t.Fatalf("runtimeClassName = %v, want gvisor", pod.RuntimeClassName)
	}
	envs := map[string]string{}
	for _, e := range pod.Containers[0].Env {
		envs[e.Name] = e.Value
	}
	if envs["TINY_AGENT"] != "command" || envs["TINY_REPO"] != "https://github.com/acme/app" {
		t.Fatalf("env TINY_AGENT=%q TINY_REPO=%q", envs["TINY_AGENT"], envs["TINY_REPO"])
	}
}

// A command session runs no agent, so asking for one alongside a command
// is a mistake to report, not to resolve silently.
func TestCreateCommandRefusesAnAgent(t *testing.T) {
	store, fc := commandTestStore(t)
	_, err := store.Create(t.Context(), CreateOpts{Name: "x", Agent: "claude", Command: []string{"ls"}})
	if err == nil || !strings.Contains(err.Error(), "runs no agent") {
		t.Fatalf("want a refusal, got %v", err)
	}
	var list agentsv1.SessionList
	if err := fc.List(t.Context(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatal("a refused create left a session behind")
	}
}

func TestParseExitCode(t *testing.T) {
	for raw, want := range map[string]int{"0\n": 0, " 3 ": 3, "128": 128} {
		got, done, err := parseExitCode(raw)
		if err != nil || !done || got != want {
			t.Fatalf("parseExitCode(%q) = %d, %v, %v", raw, got, done, err)
		}
	}
	if _, _, err := parseExitCode("garbage"); err == nil {
		t.Fatal("garbage must not parse as an exit code")
	}
}

// The fleet screen shows a finished command as ✓ exited 0 or ✗ exited N,
// and a running one as running.
func TestApplyCommandResult(t *testing.T) {
	r := Row{Phase: "Running", Command: true}
	applyCommandResult(&r, cachedResult{done: false})
	if r.Phase != "Running" || r.Title != "running its command" || r.Exit != nil {
		t.Fatalf("unfinished: %+v", r)
	}

	r = Row{Phase: "Running", Command: true}
	applyCommandResult(&r, cachedResult{done: true, code: 0})
	if r.Glyph() != "✓" || r.Title != "exited 0" || r.Exit == nil || *r.Exit != 0 {
		t.Fatalf("exit 0: glyph %s %+v", r.Glyph(), r)
	}

	r = Row{Phase: "Running", Command: true}
	applyCommandResult(&r, cachedResult{done: true, code: 3})
	if r.Glyph() != "✗" || r.Title != "exited 3" || *r.Exit != 3 {
		t.Fatalf("exit 3: glyph %s %+v", r.Glyph(), r)
	}
}

// A finished result is final; an unfinished one is asked again only after
// recheckAfter, so a fleet refresh doesn't exec into every pod each time.
func TestCommandCacheFreshness(t *testing.T) {
	var c commandCache
	now := time.Now()
	if _, fresh := c.lookup("p", now); fresh {
		t.Fatal("an empty cache has nothing fresh")
	}
	c.store("p", cachedResult{done: false, checked: now})
	if _, fresh := c.lookup("p", now.Add(recheckAfter/2)); !fresh {
		t.Fatal("a recent unfinished result should be reused")
	}
	if _, fresh := c.lookup("p", now.Add(recheckAfter+time.Second)); fresh {
		t.Fatal("a stale unfinished result should be rechecked")
	}
	c.store("p", cachedResult{done: true, code: 1, checked: now})
	if r, fresh := c.lookup("p", now.Add(time.Hour)); !fresh || r.code != 1 {
		t.Fatal("a finished result never needs rechecking")
	}
}

func TestJoinMarksCommandSessions(t *testing.T) {
	now := time.Now()
	cmd := session("run-1", "", time.Minute, now)
	cmd.Spec.Agent, cmd.Spec.Command = "command", []string{"ls"}
	agent := session("fix", "", time.Minute, now)
	snap := join([]agentsv1.Session{cmd, agent}, nil, map[string]*corev1.Pod{}, now)
	got := map[string]bool{}
	for _, r := range snap.Rows {
		got[r.Name] = r.Command
	}
	if !got["run-1"] || got["fix"] {
		t.Fatalf("command flags = %v", got)
	}
}
