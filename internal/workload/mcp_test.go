package workload

import (
	"encoding/json"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

func envValue(env []corev1.EnvVar, name string) (string, bool) {
	for _, e := range env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func TestMCPEnvEmptyListAddsNothing(t *testing.T) {
	if got := mcpEnv(nil); got != nil {
		t.Fatalf("mcpEnv(nil) = %v, want nil", got)
	}
	spec := podFor(t, testSession("u1"))
	for _, c := range spec.Containers {
		if c.Name != "agent" {
			continue
		}
		if _, ok := envValue(c.Env, "TINY_MCP_CONFIG"); ok {
			t.Fatal("agent container has TINY_MCP_CONFIG with no mcpServers")
		}
		return
	}
	t.Fatal("no agent container")
}

func TestMCPEnvHTTPAndStdio(t *testing.T) {
	servers := []agentsv1.MCPServer{
		{Name: "bp", URL: "http://director:8080/mcp/run/abc"},
		{Name: "pw", Command: "npx", Args: []string{"@playwright/mcp", "--headless"}},
	}
	env := mcpEnv(servers)

	cfg, ok := envValue(env, "TINY_MCP_CONFIG")
	if !ok {
		t.Fatal("TINY_MCP_CONFIG missing")
	}
	var got, want any
	if err := json.Unmarshal([]byte(cfg), &got); err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"mcpServers":{"bp":{"type":"http","url":"http://director:8080/mcp/run/abc"},"pw":{"command":"npx","args":["@playwright/mcp","--headless"]}}}`
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TINY_MCP_CONFIG = %s", cfg)
	}

	args, _ := envValue(env, "TINY_CODEX_MCP_ARGS")
	wantArgs := `-c mcp_servers.bp.url="http://director:8080/mcp/run/abc" -c mcp_servers.pw.command="npx" -c mcp_servers.pw.args=["@playwright/mcp","--headless"]`
	if args != wantArgs {
		t.Fatalf("TINY_CODEX_MCP_ARGS = %s\nwant %s", args, wantArgs)
	}

	s := testSession("u2")
	s.Spec.MCPServers = servers
	for _, c := range podFor(t, s).Containers {
		if c.Name == "agent" {
			if _, ok := envValue(c.Env, "TINY_MCP_CONFIG"); !ok {
				t.Fatal("agent container missing TINY_MCP_CONFIG")
			}
		}
	}
}
