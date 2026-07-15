package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// Scenario-aware validation fallback.
//
// When the source port schema has no structural information (bare `any`
// types produced by things like ticker.out or the output of a json_decode
// node), the schema walker treats every path as a wildcard and accepts
// anything. That's the right default — false positives would be worse
// than silent accept — but it loses us any chance of catching obvious
// mistakes.
//
// This file adds a second pass: if the schema walker couldn't find
// concrete structure, look up the active scenario for the current
// flow and see if it has sample data for the source port. If it does,
// use the JSON structure of the sample as an informal schema and
// validate the caller's JSONPath expressions against it. Fields the
// sample has are accepted; fields it doesn't are rejected with a hint
// listing what was actually there.

// scenarioLookup finds the best available scenario for the given project
// and reads the sample data for the requested port.
//
// "Best" is simple in v0.1.0: the first scenario found for the project.
// Multi-scenario disambiguation can land later.
type scenarioLookup struct {
	kube *kube.Client
}

// findPortSample returns the sample data bytes for the given node+port
// from the first scenario in the project, or nil if nothing matches.
func (s *scenarioLookup) findPortSample(ctx context.Context, projectName, nodeID, portName string) []byte {
	if s == nil || s.kube == nil || projectName == "" {
		return nil
	}

	list := &v1alpha1.TinyScenarioList{}
	err := s.kube.Client.List(ctx, list,
		client.InNamespace(s.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName},
	)
	if err != nil || len(list.Items) == 0 {
		return nil
	}

	// Port keys in a scenario are of the form "<node-id>:<port-name>".
	wanted := nodeID + ":" + portName

	for i := range list.Items {
		for _, p := range list.Items[i].Spec.Ports {
			if p.Port == wanted && len(p.Data) > 0 {
				return p.Data
			}
		}
	}
	return nil
}

// validateAgainstSample checks every JSONPath expression in the edge
// config against a JSON value (the scenario sample). Expressions whose
// path fully resolves to a field in the sample are accepted; others
// are returned as issues with a hint listing the sample's actual fields.
//
// This is a looser check than schema walking: it only knows about what
// happens to be in the sample, not what could legitimately be there.
// It's a fallback, not a replacement for schema-based validation.
func validateAgainstSample(config map[string]interface{}, sampleBytes []byte) (schemaWalkResult, error) {
	var result schemaWalkResult
	if len(sampleBytes) == 0 {
		return result, nil
	}

	var sample interface{}
	if err := json.Unmarshal(sampleBytes, &sample); err != nil {
		return result, fmt.Errorf("parse scenario sample: %w", err)
	}

	if m, ok := sample.(map[string]interface{}); ok {
		result.AvailableFields = sortedKeys(m)
	}

	paths := make(map[string]struct{})
	collectPathsFromValue(config, paths)

	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	for _, path := range sorted {
		if issue, ok := walkSamplePath(path, sample); !ok {
			result.Unresolved = append(result.Unresolved, issue)
		}
	}
	return result, nil
}

// walkSamplePath descends into a concrete JSON value by the segments of
// a JSONPath expression.
func walkSamplePath(path string, sample interface{}) (pathIssue, bool) {
	if sample == nil {
		return pathIssue{}, true
	}
	segments := strings.Split(strings.TrimPrefix(path, "$."), ".")
	current := sample

	for i, seg := range segments {
		// Reached a leaf earlier than expected — can't continue.
		if current == nil {
			return pathIssue{
				Path:     path,
				FailedAt: seg,
				Reason:   "sample value is null",
			}, false
		}

		obj, ok := current.(map[string]interface{})
		if !ok {
			return pathIssue{
				Path:     path,
				FailedAt: seg,
				Reason:   fmt.Sprintf("sample segment %q is not an object", segments[i-1]),
			}, false
		}

		next, exists := obj[seg]
		if !exists {
			return pathIssue{
				Path:      path,
				FailedAt:  seg,
				Available: sortedStringKeys(obj),
				Reason:    fmt.Sprintf("field %q not in scenario sample", seg),
			}, false
		}
		current = next
	}
	return pathIssue{}, true
}

// sortedStringKeys returns the sorted keys of a generic map.
func sortedStringKeys(m map[string]interface{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
