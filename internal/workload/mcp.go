package workload

import (
	"encoding/json"
	"strings"

	corev1 "k8s.io/api/core/v1"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

// mcpEnv renders spec.mcpServers into the two env vars the agent entrypoint
// consumes: claude's --mcp-config JSON and codex's -c overrides. The CRD
// patterns exclude whitespace, quotes and backslashes, so values go in
// without escaping. Nothing is added for an empty list.
func mcpEnv(servers []agentsv1.MCPServer) []corev1.EnvVar {
	if len(servers) == 0 {
		return nil
	}
	claude := map[string]map[string]any{}
	var codex []string
	for _, m := range servers {
		p := "mcp_servers." + m.Name
		if m.URL != "" {
			claude[m.Name] = map[string]any{"type": "http", "url": m.URL}
			codex = append(codex, "-c "+p+`.url="`+m.URL+`"`)
			continue
		}
		entry := map[string]any{"command": m.Command}
		codex = append(codex, "-c "+p+`.command="`+m.Command+`"`)
		if len(m.Args) > 0 {
			entry["args"] = m.Args
			codex = append(codex, "-c "+p+`.args=["`+strings.Join(m.Args, `","`)+`"]`)
		}
		claude[m.Name] = entry
	}
	cfg, _ := json.Marshal(map[string]any{"mcpServers": claude})
	return []corev1.EnvVar{
		{Name: "TINY_MCP_CONFIG", Value: string(cfg)},
		{Name: "TINY_CODEX_MCP_ARGS", Value: strings.Join(codex, " ")},
	}
}
