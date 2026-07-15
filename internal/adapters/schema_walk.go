package adapters

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// This file implements a minimal schema-aware validator for edge
// configurations. It does NOT reproduce the full semantic evaluator used
// by the hosted platform — it only catches the single most common
// failure mode: an {{expression}} that references a JSONPath which
// does not exist in the source port's schema.
//
// Scope:
//
//   - Walks edge configuration maps looking for "{{...}}" expressions.
//   - Extracts pure JSONPath references of the form $.a.b.c (no
//     operators, no functions, no array indexing) from each.
//   - For each path, walks the source port's JSON schema along the
//     property chain, resolving $ref and stopping at configurable
//     fields (those are intentionally user-shaped).
//   - Returns a list of unresolved paths plus the set of field names
//     available at the first point of divergence, so the caller can
//     build a useful hint back to the LLM.
//
// What it does NOT do:
//
//   - It does not simulate runtime data (no recursive graph walk).
//   - It does not evaluate expressions with operators, functions, or
//     ternaries — those are skipped.
//   - It does not validate configurable-field contents even when their
//     shape is known from upstream settings — that's a platform-level
//     concern that requires walking the whole flow graph.
//
// The tradeoff: this catches typos, wrong port assumptions, and
// "I guessed a field that isn't there" errors — without requiring
// full simulation infrastructure.

// schemaWalkResult reports the outcome of validating a single edge
// configuration against the source port's schema.
type schemaWalkResult struct {
	// Unresolved is the list of user-written paths that did not resolve.
	Unresolved []pathIssue
	// AvailableFields is the set of field names at the root of the
	// source port's schema (to use in error hints when paths don't
	// resolve at the very first step).
	AvailableFields []string
}

type pathIssue struct {
	// Path is the original JSONPath that failed (e.g. "$.decoded.user").
	Path string
	// FailedAt is the segment where resolution stopped (e.g. "decoded").
	FailedAt string
	// Available is the set of field names that WERE present at the
	// failure point. If non-empty, the LLM can pick one of these.
	Available []string
	// Reason is a short human-readable explanation.
	Reason string
}

// validateEdgeExpressions walks the given edge configuration and checks
// every pure JSONPath expression against the source port's schema. The
// sourceSchema should be the raw JSON schema bytes from
// TinyNode.Status.Ports[<port>].Schema.
func validateEdgeExpressions(config map[string]interface{}, sourceSchema []byte) (schemaWalkResult, error) {
	var result schemaWalkResult
	if len(sourceSchema) == 0 {
		// No schema available (port not yet reconciled). Can't validate,
		// accept as-is rather than blocking the edge.
		return result, nil
	}

	var rootDoc map[string]interface{}
	if err := json.Unmarshal(sourceSchema, &rootDoc); err != nil {
		return result, fmt.Errorf("parse source schema: %w", err)
	}

	// Resolve the root type and remember top-level field names for hints.
	rootType := resolveSchemaRef(rootDoc, rootDoc)
	result.AvailableFields = listPropertyNames(rootType)

	// Walk the config and collect every pure-JSONPath expression.
	paths := make(map[string]struct{})
	collectPathsFromValue(config, paths)

	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)

	for _, path := range sorted {
		issue, ok := walkPath(path, rootDoc, rootType)
		if !ok {
			result.Unresolved = append(result.Unresolved, issue)
		}
	}

	return result, nil
}

// ---- expression extraction ----

// pureJSONPathExpr matches a {{...}} where the inside is a JSONPath
// starting with $. Rejects expressions that contain operators, function
// calls, arithmetic, or interpolation — those are out of scope for this
// validator.
var pureJSONPathExpr = regexp.MustCompile(`^\{\{\s*(\$(?:\.[A-Za-z_][A-Za-z0-9_]*)+)\s*\}\}$`)

// innerJSONPathRef matches a $.path.to.field sequence anywhere inside a
// mustache. Used when scanning an expression with operators to extract
// each base-path reference for a best-effort check.
var innerJSONPathRef = regexp.MustCompile(`\$(?:\.[A-Za-z_][A-Za-z0-9_]*)+`)

// collectPathsFromValue recursively walks a decoded edge configuration
// and adds every pure JSONPath reference it finds to paths.
func collectPathsFromValue(v interface{}, paths map[string]struct{}) {
	switch val := v.(type) {
	case string:
		for _, path := range extractJSONPathsFromString(val) {
			paths[path] = struct{}{}
		}
	case map[string]interface{}:
		for _, child := range val {
			collectPathsFromValue(child, paths)
		}
	case []interface{}:
		for _, child := range val {
			collectPathsFromValue(child, paths)
		}
	}
}

// extractJSONPathsFromString returns every JSONPath reference found in s.
// For pure "{{$.a.b}}" expressions, returns a single path. For mixed
// expressions like "Hello {{$.name}}!", returns just the path. For
// expressions with operators like "{{$.count + 1}}", returns the base
// paths ("$.count") as best-effort.
func extractJSONPathsFromString(s string) []string {
	// Short-circuit the pure case for clarity.
	if m := pureJSONPathExpr.FindStringSubmatch(s); m != nil {
		return []string{m[1]}
	}

	if !strings.Contains(s, "{{") {
		return nil
	}

	// For any other case, scan inside all mustaches and extract any
	// $-rooted references. This is a best-effort static lookup.
	var out []string
	start := 0
	for {
		open := strings.Index(s[start:], "{{")
		if open < 0 {
			return out
		}
		open += start
		close := strings.Index(s[open:], "}}")
		if close < 0 {
			return out
		}
		close += open
		inner := s[open+2 : close]
		for _, match := range innerJSONPathRef.FindAllString(inner, -1) {
			out = append(out, match)
		}
		start = close + 2
	}
}

// ---- schema walking ----

// walkPath checks whether the JSONPath `path` resolves in `rootType`,
// following $ref links through rootDoc. On failure it returns a
// pathIssue explaining where and why.
//
// If the root is a wildcard type (no properties, no scalar type, no
// configurable overlay yet) — as happens for untyped `any` Go fields
// like the ticker's Context — the path is accepted without walking.
// We simply don't have enough schema information to reject anything,
// and false positives are worse than silent accept.
func walkPath(path string, rootDoc map[string]interface{}, rootType map[string]interface{}) (pathIssue, bool) {
	if isWildcardType(rootType) {
		return pathIssue{}, true
	}

	segments := strings.Split(strings.TrimPrefix(path, "$."), ".")
	current := rootType

	for i, seg := range segments {
		if isWildcardType(current) {
			return pathIssue{}, true
		}

		props := propertiesOf(current)
		next, ok := props[seg]
		if !ok {
			available := sortedKeys(props)
			return pathIssue{
				Path:      path,
				FailedAt:  seg,
				Available: available,
				Reason:    fmt.Sprintf("field %q not found after %s", seg, strings.Join(segments[:i], ".")),
			}, false
		}

		current = resolveSchemaRef(rootDoc, toMap(next))
	}
	return pathIssue{}, true
}

// isWildcardType reports whether a schema node is effectively "any" —
// the user can write whatever path they want through it and we have no
// grounds to reject. True in three cases:
//   - explicit configurable:true overlay
//   - map-like schema with additionalProperties but no explicit properties
//   - bare definition with no type, no properties, no array/scalar marker
//     (this is what `type Context any` produces through the SDK schema
//     generator)
func isWildcardType(node map[string]interface{}) bool {
	if node == nil {
		return true
	}
	if v, ok := node["configurable"].(bool); ok && v {
		return true
	}
	_, hasProps := node["properties"]
	_, hasAdditional := node["additionalProperties"]
	typ, _ := node["type"].(string)

	if !hasProps && hasAdditional {
		return true
	}
	if !hasProps && typ == "" {
		// No type, no properties, no additionalProperties -> bare any
		return true
	}
	return false
}

// propertiesOf returns the "properties" map of a schema node, or nil.
// Handles additionalProperties as a wildcard that admits anything by
// returning an empty map (walkPath will fall through to configurable
// detection or fail).
func propertiesOf(node map[string]interface{}) map[string]interface{} {
	if node == nil {
		return nil
	}
	if props, ok := node["properties"].(map[string]interface{}); ok {
		return props
	}
	// A map-like schema with additionalProperties but no explicit
	// properties: treat every key as valid by returning a non-nil empty
	// map; callers will fall through to the "not found" branch unless
	// we explicitly accept below.
	if _, ok := node["additionalProperties"]; ok {
		// Can't know keys statically; fake acceptance by returning nil
		// and letting the caller treat it as configurable.
		return nil
	}
	return nil
}

// isConfigurable is retained as an alias for isWildcardType for
// historical clarity — the two concepts are equivalent for our
// validation purposes.
func isConfigurable(node map[string]interface{}) bool {
	return isWildcardType(node)
}

// listPropertyNames returns the list of top-level property names of a
// schema node, sorted. Used for error hints.
func listPropertyNames(node map[string]interface{}) []string {
	return sortedKeys(propertiesOf(node))
}

// resolveSchemaRef returns the effective node after following any $ref.
// $refs of the form "#/$defs/Name" are resolved against the rootDoc's
// $defs section. Non-ref inputs are returned unchanged.
func resolveSchemaRef(rootDoc, node map[string]interface{}) map[string]interface{} {
	if node == nil {
		return nil
	}
	ref, ok := node["$ref"].(string)
	if !ok || !strings.HasPrefix(ref, "#/$defs/") {
		return node
	}
	name := strings.TrimPrefix(ref, "#/$defs/")
	defs, ok := rootDoc["$defs"].(map[string]interface{})
	if !ok {
		return node
	}
	target, ok := defs[name].(map[string]interface{})
	if !ok {
		return node
	}

	// The definition itself may carry overrides from the reference
	// site (configurable flags, property orderings, etc.). Merge the
	// reference's own keys on top of the resolved definition but only
	// for scalar attributes (not properties), so references that mark
	// a field as readonly/configurable take precedence.
	merged := make(map[string]interface{}, len(target)+len(node))
	for k, v := range target {
		merged[k] = v
	}
	for k, v := range node {
		if k == "$ref" {
			continue
		}
		if _, ok := merged[k]; !ok {
			merged[k] = v
		} else if k != "properties" && k != "$defs" {
			// Reference-site overrides win for scalar attributes.
			merged[k] = v
		}
	}
	return merged
}

// toMap safely converts an interface{} to a map[string]interface{},
// returning nil on mismatch.
func toMap(v interface{}) map[string]interface{} {
	m, _ := v.(map[string]interface{})
	return m
}

// sortedKeys returns the keys of m in sorted order.
func sortedKeys(m map[string]interface{}) []string {
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

// formatIssues renders a list of pathIssue values into a single hint
// string suitable for returning to the LLM. Groups by failure point
// so the caller sees available alternatives.
func formatIssues(issues []pathIssue, rootFields []string) string {
	if len(issues) == 0 {
		return ""
	}

	var b strings.Builder
	for i, iss := range issues {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "expression %s failed at segment %q", iss.Path, iss.FailedAt)
		if len(iss.Available) > 0 {
			fmt.Fprintf(&b, " (available: %s)", strings.Join(iss.Available, ", "))
		}
	}
	if len(rootFields) > 0 && len(issues) > 1 {
		fmt.Fprintf(&b, ". Top-level fields on source port: %s", strings.Join(rootFields, ", "))
	}
	return b.String()
}
