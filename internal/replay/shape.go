// Package replay re-drives a recorded run and reports what changed.
//
// The question it answers is the one that has cost the most time: a module
// released, does this flow still behave? Traces already hold every hop's
// payload, and the runner will accept a message that names its upstream port —
// so a recorded hop can be sent again and the rest of the run happens for real.
//
// What it does NOT do is reproduce a run byte for byte. Two things make that
// impossible and pretending otherwise would produce a diff nobody trusts: a
// model rewords its answer between calls, and a credential is redacted in the
// recording on purpose. So the comparison is structural — which ports were
// reached, and what shape the data had — which is the same tolerance the evals
// carry, for the same reason.
package replay

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Shape describes a payload's structure with its values removed: field names
// and types, recursively.
//
// Comparing shapes rather than values is what makes a replay diff readable. A
// changed answer from a model is noise; a field that stopped arriving, or
// arrived as a string where it used to be a number, is the thing a module
// release breaks and nothing currently catches.
func Shape(payload string) string {
	var v any
	if err := json.Unmarshal([]byte(payload), &v); err != nil {
		return "text"
	}
	return shapeOf(v, 0)
}

const maxShapeDepth = 6

func shapeOf(v any, depth int) string {
	if depth > maxShapeDepth {
		return "…"
	}
	switch t := v.(type) {
	case nil:
		// null carries no type. Treated as absent rather than as its own shape,
		// so a field that is sometimes null does not read as a change.
		return "null"
	case bool:
		return "bool"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		if len(t) == 0 {
			return "[]"
		}
		// One element stands for the array: a list that grew from three items
		// to four is not a structural change, and reporting it as one would
		// bury the changes that matter.
		return "[" + shapeOf(t[0], depth+1) + "]"
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+":"+shapeOf(t[k], depth+1))
		}
		return "{" + strings.Join(parts, ",") + "}"
	}
	return fmt.Sprintf("%T", v)
}
