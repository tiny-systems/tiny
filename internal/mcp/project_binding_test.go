package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// projectSpy records the project a tool actually ran against.
type projectSpy struct{ seen string }

func (p *projectSpy) Name() string        { return "spy" }
func (p *projectSpy) Description() string { return "records the execution project" }
func (p *projectSpy) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}
func (p *projectSpy) Execute(_ context.Context, execCtx sdktools.ExecutionContext, _ map[string]interface{}) sdktools.ToolResult {
	p.seen = execCtx.ProjectName
	return sdktools.ToolResult{Success: true}
}

// TestSessionProjectWins: a tiny session is bound to one project and the
// editor only serves that one. A caller naming a different project used to
// win, so a model that invented a name installed into a project nothing was
// serving — healthy nodes, and a dashboard reporting 0 widgets, 0 flows.
func TestSessionProjectWins(t *testing.T) {
	spy := &projectSpy{}
	registry := sdktools.NewRegistry()
	registry.Register(spy)

	s := &Server{registry: registry, execCtx: sdktools.ExecutionContext{ProjectName: "my-agent"}}

	raw, _ := json.Marshal(toolCallParams{
		Name:      "spy",
		Arguments: map[string]interface{}{"project": "invented-by-the-model"},
	})
	if res := s.callTool(context.Background(), raw); res.IsError {
		t.Fatalf("call failed: %+v", res)
	}
	if spy.seen != "my-agent" {
		t.Errorf("ran against %q, want the session project %q", spy.seen, "my-agent")
	}
}

// With no session project (hosted-style multi-project use), the caller's
// value is still what selects the project.
func TestCallerProjectUsedWhenSessionUnbound(t *testing.T) {
	spy := &projectSpy{}
	registry := sdktools.NewRegistry()
	registry.Register(spy)

	s := &Server{registry: registry, execCtx: sdktools.ExecutionContext{}}

	raw, _ := json.Marshal(toolCallParams{
		Name:      "spy",
		Arguments: map[string]interface{}{"project": "chosen-by-caller"},
	})
	if res := s.callTool(context.Background(), raw); res.IsError {
		t.Fatalf("call failed: %+v", res)
	}
	if spy.seen != "chosen-by-caller" {
		t.Errorf("ran against %q, want %q", spy.seen, "chosen-by-caller")
	}
}

// The override is the design; the silence was the bug. A caller that names a
// different project must be told which one actually answered, or it can build
// a flow somewhere its author never intended — which is exactly what happened
// to two agents evaluating this server.
func TestProjectSubstitutionIsReported(t *testing.T) {
	result := noteProjectSubstitution(
		sdktools.ToolResult{Success: true, Output: map[string]interface{}{"project": "bound-one"}},
		"asked-for", "bound-one")

	out, ok := result.Output.(map[string]interface{})
	if !ok {
		t.Fatalf("output shape changed: %T", result.Output)
	}
	note, _ := out["project_note"].(string)
	if note == "" {
		t.Fatal("no note: the substitution is still silent")
	}
	for _, want := range []string{"asked-for", "bound-one", "tiny -p"} {
		if !strings.Contains(note, want) {
			t.Errorf("note should mention %q, got: %s", want, note)
		}
	}
	if out["project"] != "bound-one" {
		t.Fatal("the original answer must survive the annotation")
	}
}

// A failed call needs the note too — "project not found" is baffling when the
// project you asked about was never the one being searched.
func TestProjectSubstitutionIsReportedOnFailure(t *testing.T) {
	result := noteProjectSubstitution(
		sdktools.ToolResult{Success: false, Error: "flow not found"}, "asked-for", "bound-one")
	if !strings.Contains(result.Error, "asked-for") || !strings.Contains(result.Error, "bound-one") {
		t.Fatalf("error lost the substitution note: %s", result.Error)
	}
}

// Not every tool returns a map. The note must survive that rather than being
// dropped on the floor.
func TestProjectSubstitutionSurvivesNonMapOutput(t *testing.T) {
	result := noteProjectSubstitution(
		sdktools.ToolResult{Success: true, Output: []string{"a", "b"}}, "asked-for", "bound-one")
	out, ok := result.Output.(map[string]interface{})
	if !ok || out["project_note"] == nil {
		t.Fatalf("note dropped for non-map output: %#v", result.Output)
	}
	if out["result"] == nil {
		t.Fatal("original output was discarded")
	}
}
