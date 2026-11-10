package v1alpha1

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

// TestMCPServersRejectsUnsafeValues runs the real CRD through an API server:
// the schema and CEL rules are what keep a server entry from smuggling
// whitespace or quotes into the agent's env.
func TestMCPServersRejectsUnsafeValues(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS not set; run via make test")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = env.Stop() })

	scheme := runtime.NewScheme()
	if err := AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		t.Fatal(err)
	}

	create := func(name string, servers ...MCPServer) error {
		return c.Create(context.Background(), &Session{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       SessionSpec{MCPServers: servers},
		})
	}

	if err := create("ok", MCPServer{Name: "bp", URL: "http://director:8080/mcp/run/abc"},
		MCPServer{Name: "pw", Command: "npx", Args: []string{"@playwright/mcp"}}); err != nil {
		t.Fatalf("valid servers rejected: %v", err)
	}

	bad := map[string][]MCPServer{
		"arg-with-space":       {{Name: "pw", Command: "npx", Args: []string{"a b"}}},
		"bad-name":             {{Name: "Bad Name", URL: "http://x/y"}},
		"reserved-name":        {{Name: "tiny", URL: "http://x/y"}},
		"both":                 {{Name: "pw", URL: "http://x/y", Command: "npx"}},
		"neither":              {{Name: "pw"}},
		"args-without-command": {{Name: "pw", URL: "http://x/y", Args: []string{"a"}}},
		"url-with-quote":       {{Name: "pw", URL: `http://x/"y`}},
		"duplicate-names":      {{Name: "pw", URL: "http://x/y"}, {Name: "pw", URL: "http://x/z"}},
	}
	for name, servers := range bad {
		if err := create(name, servers...); err == nil {
			t.Errorf("%s: want validation error, got none", name)
		}
	}

	tolerate := func(name string, ts ...corev1.Toleration) error {
		return c.Create(context.Background(), &Session{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       SessionSpec{Tolerations: ts},
		})
	}
	if err := tolerate("keyed", corev1.Toleration{Key: "pool", Operator: corev1.TolerationOpEqual, Value: "runs", Effect: corev1.TaintEffectNoSchedule}); err != nil {
		t.Fatalf("keyed toleration rejected: %v", err)
	}
	if err := tolerate("wildcard", corev1.Toleration{Operator: corev1.TolerationOpExists}); err == nil {
		t.Error("wildcard toleration: want validation error, got none")
	}

	withRuntime := func(name, class string) error {
		return c.Create(context.Background(), &Session{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       SessionSpec{RuntimeClassName: class},
		})
	}
	if err := withRuntime("gvisor", "gvisor"); err != nil {
		t.Fatalf("valid runtime class rejected: %v", err)
	}
	for _, bad := range []string{"GVisor", "has space", "-leading", "trailing-", "a;b"} {
		if err := withRuntime("rc-"+fmt.Sprint(len(bad))+"-"+fmt.Sprintf("%x", bad)[:6], bad); err == nil {
			t.Errorf("runtime class %q: want validation error, got none", bad)
		}
	}
}
