package v1alpha1

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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
		"arg-with-space":  {{Name: "pw", Command: "npx", Args: []string{"a b"}}},
		"bad-name":        {{Name: "Bad Name", URL: "http://x/y"}},
		"reserved-name":   {{Name: "tiny", URL: "http://x/y"}},
		"both":            {{Name: "pw", URL: "http://x/y", Command: "npx"}},
		"neither":         {{Name: "pw"}},
		"url-with-quote":  {{Name: "pw", URL: `http://x/"y`}},
		"duplicate-names": {{Name: "pw", URL: "http://x/y"}, {Name: "pw", URL: "http://x/z"}},
	}
	for name, servers := range bad {
		if err := create(name, servers...); err == nil {
			t.Errorf("%s: want validation error, got none", name)
		}
	}
}
