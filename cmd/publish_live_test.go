package cmd

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/tiny-systems/tiny/internal/kube"
)

// TestBuildSolutionExportKeepsExpressions builds the real export for a real
// project and asserts that credential-carrying EDGE expressions survive
// redaction. A synthetic unit test already covers the rule; this covers the
// actual publish path against actual node data, which is where it shipped a
// broken solution: {{$.context.apiKey}} became a literal marker and every
// provider call in the installed copy failed with invalid x-api-key.
//
// Env-gated: needs a cluster.
//
//	TINY_LIVE_PROJECT=sre-agent TINY_LIVE_CONTEXT=minikube go test ./cmd -run Expressions -v
func TestBuildSolutionExportKeepsExpressions(t *testing.T) {
	project := os.Getenv("TINY_LIVE_PROJECT")
	if project == "" {
		t.Skip("set TINY_LIVE_PROJECT (and optionally TINY_LIVE_CONTEXT) to run")
	}
	k, err := kube.NewClient(kube.Options{Context: os.Getenv("TINY_LIVE_CONTEXT"), Namespace: "tinysystems"})
	if err != nil {
		t.Fatalf("kube client: %v", err)
	}

	export, err := buildSolutionExport(context.Background(), k, project, "", "", nil)
	if err != nil {
		t.Fatalf("build export: %v", err)
	}

	raw, err := json.Marshal(export)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)

	if strings.Contains(body, "sk-ant-") || strings.Contains(body, "sk-live-") {
		t.Error("a literal provider key survived into the export")
	}

	// Every credential-shaped edge value in this project is an expression;
	// none of them may have been rewritten.
	var checked int
	for _, elem := range export.Elements {
		data, _ := elem["data"].(map[string]interface{})
		if data == nil {
			continue
		}
		for _, cfg := range collectConfigs(data) {
			for key, val := range cfg {
				s, ok := val.(string)
				if !ok || !strings.Contains(strings.ToLower(key), "apikey") {
					continue
				}
				checked++
				if s != "" && !strings.Contains(s, "{{") {
					t.Errorf("edge %v key %q rewritten to literal %q — wiring severed", elem["id"], key, s)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatalf("project %q exported no credential-carrying edge configs — wrong project to assert on", project)
	}
	t.Logf("checked %d credential-carrying values", checked)
}

// collectConfigs returns every configuration object on an element: the edge's
// own, plus one per node handle.
func collectConfigs(data map[string]interface{}) []map[string]interface{} {
	var out []map[string]interface{}
	add := func(v interface{}) {
		switch c := v.(type) {
		case map[string]interface{}:
			out = append(out, c)
		case json.RawMessage:
			var m map[string]interface{}
			if json.Unmarshal(c, &m) == nil {
				out = append(out, m)
			}
		}
	}
	add(data["configuration"])
	handles, _ := data["handles"].([]interface{})
	for _, h := range handles {
		if handle, ok := h.(map[string]interface{}); ok {
			add(handle["configuration"])
		}
	}
	return out
}
