package mcp

import (
	"strings"
	"testing"
)

func schemaWith(names ...string) map[string]interface{} {
	props := map[string]interface{}{}
	for _, n := range names {
		props[n] = map[string]interface{}{"type": "string"}
	}
	return map[string]interface{}{"type": "object", "properties": props}
}

// The failure this closes: build_flow took a `position` on every node and
// discarded it while the guide instructed callers to send one. Accepting a
// field and dropping it answers "done" to something that did not happen.
func TestUnknownArgumentIsRefused(t *testing.T) {
	err := checkArguments("build_flow", schemaWith("nodes", "edges", "project"), map[string]interface{}{
		"nodes":  []interface{}{},
		"layout": "grid",
	})
	if err == nil {
		t.Fatal("an undeclared argument was accepted")
	}
	if !strings.Contains(err.Error(), `"layout"`) {
		t.Errorf("the error must name what was refused: %v", err)
	}
	// The caller is a model reading this error; it needs the valid names to
	// fix the call rather than guess again.
	for _, want := range []string{"nodes", "edges", "project"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should list %q as accepted: %v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "Nothing was done") {
		t.Errorf("the error should say the call had no effect: %v", err)
	}
}

// A typo is the same mistake as an unsupported field, and the fix is one
// character — worth naming rather than making someone re-read a schema.
func TestTypoGetsASuggestion(t *testing.T) {
	for sent, want := range map[string]string{
		"node_di": "node_id",
		"nodeid":  "node_id",
		"nodeId":  "node_id",
	} {
		err := checkArguments("edit_flow", schemaWith("node_id", "action"), map[string]interface{}{sent: "x"})
		if err == nil {
			t.Fatalf("%q was accepted", sent)
		}
		if !strings.Contains(err.Error(), "did you mean "+`"`+want+`"`) {
			t.Errorf("%q should suggest %q, got: %v", sent, want, err)
		}
	}
}

// A name that is not close to anything gets no guess. Suggesting the nearest
// string regardless of distance sends a caller down a wrong path with
// confidence.
func TestDistantNameGetsNoSuggestion(t *testing.T) {
	err := checkArguments("edit_flow", schemaWith("node_id", "action"), map[string]interface{}{"widgets": "x"})
	if err == nil {
		t.Fatal("an undeclared argument was accepted")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("no suggestion should be offered for a distant name: %v", err)
	}
}

func TestDeclaredArgumentsPass(t *testing.T) {
	err := checkArguments("edit_flow", schemaWith("node_id", "action", "project", "flow"), map[string]interface{}{
		"node_id": "n1",
		"action":  "delete_node",
		"project": "p",
		"flow":    "f",
	})
	if err != nil {
		t.Fatalf("valid arguments were refused: %v", err)
	}
}

// The host injects project and flow into the schema it advertises. Checking
// against the tool's own schema instead would refuse a caller for sending
// exactly what it was told to send.
func TestHostInjectedParametersAreAccepted(t *testing.T) {
	schema := schemaWith("node_id")
	injectProjectFlowParams("edit_flow", schema)

	err := checkArguments("edit_flow", schema, map[string]interface{}{"node_id": "n1", "project": "p", "flow": "f"})
	if err != nil {
		t.Fatalf("host-injected parameters were refused: %v", err)
	}
}

// A schema with no properties key does not describe fields at all, so there is
// no contract to enforce.
func TestSchemaWithoutPropertiesAcceptsAnything(t *testing.T) {
	if err := checkArguments("some_tool", map[string]interface{}{"type": "object"}, map[string]interface{}{"anything": 1}); err != nil {
		t.Fatalf("a schema that declares no fields should accept anything: %v", err)
	}
}

// An EMPTY properties map is a contract: this tool takes nothing. A typo
// against it is as wrong as any other undeclared field, and treating it as
// "anything goes" is how a mistyped argument to a zero-argument tool passes
// silently.
func TestToolThatTakesNoArgumentsRefusesThem(t *testing.T) {
	err := checkArguments("list_modules", map[string]interface{}{
		"type": "object", "properties": map[string]interface{}{},
	}, map[string]interface{}{"filter": "common"})
	if err == nil {
		t.Fatal("an argument to a zero-argument tool was accepted")
	}
	if !strings.Contains(err.Error(), "takes no arguments") || !strings.Contains(err.Error(), `"filter"`) {
		t.Errorf("the error should say the tool takes nothing and name what was sent: %v", err)
	}
}

func TestNoArgumentsIsFine(t *testing.T) {
	if err := checkArguments("list_projects", schemaWith("project"), map[string]interface{}{}); err != nil {
		t.Fatalf("an empty call was refused: %v", err)
	}
}
