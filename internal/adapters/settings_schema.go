package adapters

import (
	"encoding/json"
	"strings"
	"unicode"
)

// buildSettingsSchemaBytes produces the schema bytes the platform UI
// expects for a node's `_settings` port (and edge `schema` overrides
// for configurable target fields). The platform's
// `UpdateWithDefinitions` overlays definitions from `$defs`; without
// that shape, the UI shows "Object is empty" and downstream chain
// simulators can't recover the typed shape of configurable fields.
//
// Inputs:
//   - settings: the actual values being written (used to infer shape
//     when the caller did not pass an explicit schema for a field)
//   - userSchema: the caller's optional per-field schema hints. Each
//     top-level key in this map is treated as a candidate
//     configurable definition. The value can be either:
//       (a) a single field definition like {type, properties, ...}
//           — in which case we wrap it into a $defs entry
//       (b) the platform-native shape {$defs: {...}, $ref: "..."}
//           — passed through verbatim
//
// Output: schema bytes in platform-native form, or nil when there is
// nothing meaningful to write.
func buildSettingsSchemaBytes(settings, userSchema map[string]interface{}) ([]byte, error) {
	// Caller already passed the canonical form — pass through.
	if _, ok := userSchema["$defs"]; ok {
		return marshalOptional(userSchema)
	}

	defs := map[string]interface{}{}

	for key, rawValue := range settings {
		// Only object-valued settings turn into configurable defs.
		// Scalars/arrays stay implicit on the Settings type — the
		// component's Status.Ports schema already declares them.
		if _, isObject := rawValue.(map[string]interface{}); !isObject {
			continue
		}

		defName := titleize(key)
		def := map[string]interface{}{
			"configurable": true,
			"path":         "$." + key,
			"title":        defName,
			"type":         "object",
		}

		// Require an explicit caller-supplied schema for this field.
		// Inference from value shape was removed (SDK ≥ v0.10.7) because
		// it taught models to skip schema declarations, which then
		// produced silent gaps in downstream edge validation. If the
		// caller omits the schema for a configurable field, we leave
		// the def empty here and the strict pre-flight in build_flow /
		// edit_flow rejects the call upstream with a clear error.
		if userFieldSchema, ok := userSchema[key].(map[string]interface{}); ok {
			mergeSchemaHints(def, userFieldSchema)
		}

		// Only emit when we actually have a useful properties bag —
		// an empty object adds no information and confuses overlays.
		if props, ok := def["properties"].(map[string]interface{}); !ok || len(props) == 0 {
			continue
		}
		defs[defName] = def
	}

	// Also accept per-field schemas the caller passed for keys that
	// don't appear in settings (e.g., declaring shape ahead of value).
	for key, raw := range userSchema {
		if _, alreadyEmitted := defs[titleize(key)]; alreadyEmitted {
			continue
		}
		fieldSchema, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		defName := titleize(key)
		def := map[string]interface{}{
			"configurable": true,
			"path":         "$." + key,
			"title":        defName,
			"type":         "object",
		}
		mergeSchemaHints(def, fieldSchema)
		if props, ok := def["properties"].(map[string]interface{}); !ok || len(props) == 0 {
			continue
		}
		defs[defName] = def
	}

	if len(defs) == 0 {
		return nil, nil
	}

	out := map[string]interface{}{"$defs": defs}
	return json.Marshal(out)
}

// buildEdgeSchemaBytes is the edge-side counterpart: edge schemas
// declare shapes for the target port's configurable fields, and the
// platform expects them in the same $defs-overlay form.
func buildEdgeSchemaBytes(configuration, userSchema map[string]interface{}) ([]byte, error) {
	return buildSettingsSchemaBytes(configuration, userSchema)
}

// mergeSchemaHints copies type/properties/required from src into dst
// without overwriting the configurable/path/title fields dst already
// carries.
func mergeSchemaHints(dst, src map[string]interface{}) {
	for _, k := range []string{"type", "properties", "required", "additionalProperties", "items"} {
		if v, ok := src[k]; ok {
			dst[k] = v
		}
	}
}

// titleize matches the platform's $defs naming convention: first
// rune uppercase, all following runes lowercase. This is what the
// swaggest/jsonschema-go reflector produces from Go struct field
// names (e.g. OutputData → "Outputdata", Inputdata → "Inputdata"),
// and the platform's UpdateWithDefinitions matches definitions by
// exact name first. Producing "OutputData" instead of "Outputdata"
// silently bypasses the overlay.
func titleize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	for i := 1; i < len(r); i++ {
		r[i] = unicode.ToLower(r[i])
	}
	return string(r)
}

// ensures the import isn't dropped when only used in tests
var _ = strings.HasPrefix
