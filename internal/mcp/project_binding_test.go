package mcp

import (
	"context"
	"encoding/json"
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
