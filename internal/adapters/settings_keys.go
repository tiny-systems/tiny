package adapters

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
)

// Settings keys nothing will ever read.
//
// json.Unmarshal drops a key with no matching struct field without a word, so a
// setting typed into a node that the component does not declare simply does
// nothing — forever, silently. The runtime does notice: the runner logs
// "orphaned keys will be silently dropped" on every reconcile. That log lives
// in the module's pod, where nobody building a flow will ever look.
//
// This was found the expensive way. Three nodes in a live project carried a
// 108-character `apiKey` in their settings; the component has no such field,
// takes its key from the request port instead, and had been logging the
// complaint every fifteen seconds for weeks. The credential sat in the resource
// doing nothing, and the flow it was meant to authenticate failed with "api key
// missing".
//
// The node's own status carries the settings schema, so the check needs nothing
// but the node.

// unknownSettingKeys returns the stored settings keys the component does not
// declare. Key names only — a value here may be a credential, and naming it is
// enough to fix it.
func unknownSettingKeys(n *v1alpha1.TinyNode, settings map[string]interface{}) []string {
	if len(settings) == 0 {
		return nil
	}
	declared := declaredSettingFields(n)
	if declared == nil {
		// No schema published yet: the node has not reconciled, and absence of
		// evidence would otherwise read as every key being wrong.
		return nil
	}

	var unknown []string
	for key := range settings {
		if _, ok := declared[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// declaredSettingFields reads the property names off the node's published
// _settings schema. Returns nil when there is no schema to compare against.
func declaredSettingFields(n *v1alpha1.TinyNode) map[string]struct{} {
	for _, p := range n.Status.Ports {
		if p.Name != v1alpha1.SettingsPort || len(p.Schema) == 0 {
			continue
		}
		var schema map[string]interface{}
		if err := json.Unmarshal(p.Schema, &schema); err != nil {
			return nil
		}
		props := schemaProperties(schema)
		if len(props) == 0 {
			return nil
		}
		return props
	}
	return nil
}

// schemaProperties resolves the root of a generated schema and returns its
// property names. The SDK emits `{"$ref": "#/$defs/Settings", "$defs": {...}}`,
// so the properties live one hop away.
func schemaProperties(schema map[string]interface{}) map[string]struct{} {
	root := schema
	if ref, ok := schema["$ref"].(string); ok {
		defs, _ := schema["$defs"].(map[string]interface{})
		name := strings.TrimPrefix(ref, "#/$defs/")
		if target, ok := defs[name].(map[string]interface{}); ok {
			root = target
		}
	}
	props, ok := root["properties"].(map[string]interface{})
	if !ok {
		return nil
	}
	out := make(map[string]struct{}, len(props))
	for name := range props {
		out[name] = struct{}{}
	}
	return out
}
