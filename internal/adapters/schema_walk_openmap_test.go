package adapters

import "testing"

// A port whose payload keys are decided at runtime (a form's fields, a
// document's columns) can only advertise example properties. Walking such a
// map must accept unlisted keys, or an edge reading a legitimate value is
// rejected at build time.
func TestWalkPathAcceptsUnlistedKeysOnOpenMap(t *testing.T) {
	// Shape produced by a Go map[string]interface{} field that was seeded with
	// example entries: both properties AND additionalProperties.
	root := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"values": map[string]interface{}{
				"type":                 "object",
				"additionalProperties": map[string]interface{}{},
				"properties": map[string]interface{}{
					"approve": map[string]interface{}{"type": "boolean"},
					"deny":    map[string]interface{}{"type": "boolean"},
				},
			},
		},
	}

	for _, path := range []string{"$.values.approve", "$.values.note", "$.values.anythingElse"} {
		if _, ok := walkPath(path, root, root); !ok {
			t.Errorf("walkPath(%q) rejected a key on an open map", path)
		}
	}
}

// A closed object must still reject unknown fields — the open-map allowance
// cannot become a blanket accept, or genuine typos stop being caught.
func TestWalkPathStillRejectsUnknownFieldOnClosedObject(t *testing.T) {
	root := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"status": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"code": map[string]interface{}{"type": "number"},
				},
			},
		},
	}
	if _, ok := walkPath("$.status.coed", root, root); ok {
		t.Error("walkPath accepted a typo on a closed object")
	}
	if _, ok := walkPath("$.status.code", root, root); !ok {
		t.Error("walkPath rejected a valid field on a closed object")
	}
}

// additionalProperties:false is the explicit way to close an object.
func TestWalkPathRespectsAdditionalPropertiesFalse(t *testing.T) {
	root := map[string]interface{}{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]interface{}{
			"known": map[string]interface{}{"type": "string"},
		},
	}
	if _, ok := walkPath("$.unknown", root, root); ok {
		t.Error("walkPath accepted an unknown field despite additionalProperties:false")
	}
}
