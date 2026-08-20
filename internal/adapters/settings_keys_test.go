package adapters

import (
	"testing"

	"github.com/tiny-systems/module/api/v1alpha1"
)

func nodeWithSettingsSchema(props ...string) *v1alpha1.TinyNode {
	schema := `{"$ref":"#/$defs/Settings","$defs":{"Settings":{"type":"object","properties":{`
	for i, p := range props {
		if i > 0 {
			schema += ","
		}
		schema += `"` + p + `":{"type":"string"}`
	}
	schema += `}}}}`

	n := &v1alpha1.TinyNode{}
	n.Status.Ports = []v1alpha1.TinyNodePortStatus{
		{Name: v1alpha1.SettingsPort, Schema: []byte(schema)},
	}
	return n
}

// The one that cost real time: a 108-character apiKey sitting in a node's
// settings, dropped by json.Unmarshal on every reconcile, while the flow it was
// meant to authenticate failed with "api key missing".
func TestUnknownKeyIsReported(t *testing.T) {
	n := nodeWithSettingsSchema("provider", "model", "maxTokens")
	unknown := unknownSettingKeys(n, map[string]interface{}{
		"model":  "claude-sonnet-5",
		"apiKey": "sk-not-a-real-key",
	})
	if len(unknown) != 1 || unknown[0] != "apiKey" {
		t.Fatalf("unknown = %v, want [apiKey]", unknown)
	}
}

func TestDeclaredKeysAreNotReported(t *testing.T) {
	n := nodeWithSettingsSchema("provider", "model")
	if unknown := unknownSettingKeys(n, map[string]interface{}{"provider": "anthropic", "model": "x"}); unknown != nil {
		t.Fatalf("unknown = %v, want none", unknown)
	}
}

// A node that has not reconciled publishes no schema. Absence of evidence must
// not read as every key being wrong — that would flag a whole project the
// moment its modules restart.
func TestNoSchemaMeansNoJudgement(t *testing.T) {
	n := &v1alpha1.TinyNode{}
	if unknown := unknownSettingKeys(n, map[string]interface{}{"anything": 1}); unknown != nil {
		t.Fatalf("unknown = %v, want none while the schema is unknown", unknown)
	}
}

func TestEmptySettingsAreFine(t *testing.T) {
	n := nodeWithSettingsSchema("provider")
	if unknown := unknownSettingKeys(n, nil); unknown != nil {
		t.Fatalf("unknown = %v", unknown)
	}
}

// Several unknown keys come back sorted, so the same project reports the same
// issue text every time rather than reshuffling with map order.
func TestUnknownKeysAreSorted(t *testing.T) {
	n := nodeWithSettingsSchema("model")
	unknown := unknownSettingKeys(n, map[string]interface{}{
		"zebra": 1, "apiKey": 2, "model": 3,
	})
	if len(unknown) != 2 || unknown[0] != "apiKey" || unknown[1] != "zebra" {
		t.Fatalf("unknown = %v, want sorted [apiKey zebra]", unknown)
	}
}

func TestSchemaWithoutARefStillResolves(t *testing.T) {
	n := &v1alpha1.TinyNode{}
	n.Status.Ports = []v1alpha1.TinyNodePortStatus{
		{Name: v1alpha1.SettingsPort, Schema: []byte(`{"type":"object","properties":{"model":{"type":"string"}}}`)},
	}
	if unknown := unknownSettingKeys(n, map[string]interface{}{"model": "x", "apiKey": "y"}); len(unknown) != 1 || unknown[0] != "apiKey" {
		t.Fatalf("unknown = %v", unknown)
	}
}
