package mcp

import (
	"fmt"
	"sort"
	"strings"
)

// A field the server does not understand used to be accepted and dropped.
//
// That is the same failure as every other silent one here: the caller is told
// it succeeded, and finds out later — if ever — that the thing it asked for
// did not happen. build_flow took a position on every node and discarded it
// while the guide instructed callers to send one, so flows were laid out by an
// agent, silently stacked in a column, and nobody learned why until someone
// opened the canvas.
//
// A typo behaves the same way. `nodeId` instead of `node_id` is not a smaller
// mistake than an unsupported field; it is the same mistake, and answering
// "done" to it is worse than refusing.
//
// So an argument the advertised schema does not name is refused, and the
// refusal says what was accepted — the caller is a model reading the error,
// and the list is what lets it fix the call on the next attempt rather than
// guessing.

// checkArguments reports an error naming any argument the schema does not
// declare. The schema passed in must be the one the server ADVERTISED, after
// the host's own parameters are injected, or a caller is refused for sending
// exactly what it was told to send.
func checkArguments(toolName string, schema map[string]interface{}, args map[string]interface{}) error {
	if len(args) == 0 {
		return nil
	}
	properties, ok := schema["properties"].(map[string]interface{})
	if !ok {
		// No properties key at all: the schema does not describe an object's
		// fields, so there is no contract to hold a caller to. An EMPTY
		// properties map is different — that says "no arguments", which is a
		// contract, and a typo against it deserves the same answer as any
		// other undeclared field.
		return nil
	}

	var unknown []string
	for name := range args {
		if _, declared := properties[name]; declared || hostParameter(name) {
			continue
		}
		unknown = append(unknown, name)
	}
	if len(unknown) == 0 {
		return nil
	}
	sort.Strings(unknown)

	if len(properties) == 0 {
		return fmt.Errorf("%s takes no arguments, but %s %s given. Nothing was done",
			toolName, quoteList(unknown), plural(len(unknown)))
	}

	accepted := make([]string, 0, len(properties))
	for name := range properties {
		accepted = append(accepted, name)
	}
	sort.Strings(accepted)

	var b strings.Builder
	fmt.Fprintf(&b, "%s does not take %s", toolName, quoteList(unknown))
	if len(unknown) == 1 {
		if near := closestName(unknown[0], accepted); near != "" {
			fmt.Fprintf(&b, " — did you mean %q?", near)
		}
	}
	fmt.Fprintf(&b, ". It accepts: %s. Nothing was done; resend with the right names.", strings.Join(accepted, ", "))
	return fmt.Errorf("%s", b.String())
}

// hostParameter reports whether a name is one the host supplies rather than
// the tool. A session's project and flow are described on every tool that
// needs them and are habitually sent to those that do not; refusing them would
// punish a caller for the ambient context it was told to pass, which is not
// the mistake this is here to catch.
func hostParameter(name string) bool {
	return name == "project" || name == "flow"
}

func quoteList(names []string) string {
	quoted := make([]string, 0, len(names))
	for _, n := range names {
		quoted = append(quoted, fmt.Sprintf("%q", n))
	}
	return strings.Join(quoted, ", ")
}

// closestName finds the accepted name within one small edit of what was sent,
// which covers the mistakes that actually happen: a case slip, a missing
// underscore, a plural. Anything further away is not worth guessing at.
func closestName(sent string, accepted []string) string {
	normalise := func(s string) string {
		return strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, "_", ""), "-", ""))
	}
	target := normalise(sent)
	for _, name := range accepted {
		if normalise(name) == target {
			return name
		}
	}
	for _, name := range accepted {
		n := normalise(name)
		// Transposition counts as one mistake: node_di for node_id is a
		// finger slip, and plain edit distance scores it the same as a word
		// nobody typed.
		if isTransposition(n, target) || editDistanceWithin(n, target, 1) {
			return name
		}
	}
	return ""
}

// editDistanceWithin reports whether two strings are within max edits, without
// computing the full distance.
func editDistanceWithin(a, b string, max int) bool {
	if a == b {
		return true
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	if len(b)-len(a) > max {
		return false
	}

	edits := 0
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] == b[j] {
			i++
			j++
			continue
		}
		edits++
		if edits > max {
			return false
		}
		if len(a) == len(b) {
			i++
			j++
			continue
		}
		j++ // b is longer: treat as an insertion
	}
	return edits+(len(b)-j)+(len(a)-i) <= max
}

// isTransposition reports whether two strings differ only by one adjacent
// swap.
func isTransposition(a, b string) bool {
	if len(a) != len(b) || a == b {
		return false
	}
	diff := []int{}
	for i := range a {
		if a[i] != b[i] {
			diff = append(diff, i)
			if len(diff) > 2 {
				return false
			}
		}
	}
	return len(diff) == 2 && diff[1] == diff[0]+1 && a[diff[0]] == b[diff[1]] && a[diff[1]] == b[diff[0]]
}

func plural(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}
