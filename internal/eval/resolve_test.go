package eval

import (
	"context"
	"strings"
	"testing"
)

type stubNodes struct {
	names []string
	err   error
}

func (s stubNodes) ListNodeNames(context.Context, string) ([]string, error) {
	return s.names, s.err
}

const (
	fullA = "7a5d6b30.tinysystems-common-module-v0.signal-ce365"
	fullB = "bb470df1.tinysystems-common-module-v0.signal-f2b7b"
	fullC = "bb470df1.tinysystems-llm-module-v0.llm-router-6d1b1"
)

func runnerWithNodes(names ...string) *Runner {
	return &Runner{Project: "maksym", Nodes: stubNodes{names: names}}
}

// The reason suffix addressing exists: re-importing a project mints new
// flow-id prefixes, so an eval that hardcodes a full name breaks on every
// import. The suffix is the part that survives.
func TestResolveNodeBySuffix(t *testing.T) {
	r := runnerWithNodes(fullA, fullB, fullC)

	got, err := r.resolveNode(context.Background(), "signal-f2b7b")
	if err != nil {
		t.Fatalf("resolveNode: %v", err)
	}
	if got != fullB {
		t.Errorf("resolved to %q, want %q", got, fullB)
	}
}

// A full name must keep working untouched — every eval written so far uses one.
func TestResolveNodeLeavesAFullNameAlone(t *testing.T) {
	r := runnerWithNodes(fullA, fullB)

	got, err := r.resolveNode(context.Background(), fullA)
	if err != nil {
		t.Fatalf("resolveNode: %v", err)
	}
	if got != fullA {
		t.Errorf("rewrote a full name to %q", got)
	}
}

// A full name for a node that is NOT in the project is still passed through:
// the eval may address a node in another project deliberately, and failing
// here would be this function inventing a rule nobody asked for.
func TestResolveNodeAcceptsAFullNameItCannotSee(t *testing.T) {
	r := runnerWithNodes(fullA)

	got, err := r.resolveNode(context.Background(), fullB)
	if err != nil {
		t.Fatalf("resolveNode rejected an unseen full name: %v", err)
	}
	if got != fullB {
		t.Errorf("got %q", got)
	}
}

// Ambiguity must stop the run and say what it was torn between. Picking one
// silently would make an eval pass against whichever node happened to sort
// first, which is worse than not running.
func TestResolveNodeRefusesAnAmbiguousSuffix(t *testing.T) {
	r := runnerWithNodes(
		"aaaa1111.tinysystems-common-module-v0.signal-dup",
		"bbbb2222.tinysystems-common-module-v0.signal-dup",
	)

	_, err := r.resolveNode(context.Background(), "signal-dup")
	if err == nil {
		t.Fatal("an ambiguous suffix resolved instead of failing")
	}
	for _, want := range []string{"aaaa1111", "bbbb2222"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not name candidate %s: %v", want, err)
		}
	}
}

// A suffix matching nothing names the project, because the usual cause is
// pointing an eval at the wrong one.
func TestResolveNodeReportsNoMatch(t *testing.T) {
	r := runnerWithNodes(fullA)

	_, err := r.resolveNode(context.Background(), "signal-nope")
	if err == nil {
		t.Fatal("a nonexistent suffix resolved")
	}
	if !strings.Contains(err.Error(), "signal-nope") || !strings.Contains(err.Error(), "maksym") {
		t.Errorf("error should name the suffix and the project: %v", err)
	}
}

// Without a lister a suffix cannot be resolved, and the message has to say so
// rather than surfacing as the publisher's "separator not found".
func TestResolveNodeWithoutAListerExplainsItself(t *testing.T) {
	r := &Runner{Project: "maksym"}

	_, err := r.resolveNode(context.Background(), "signal-f2b7b")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "full name") {
		t.Errorf("error should point at using a full name: %v", err)
	}
}

func TestResolveNodeRejectsEmpty(t *testing.T) {
	r := runnerWithNodes(fullA)
	if _, err := r.resolveNode(context.Background(), "  "); err == nil {
		t.Error("empty trigger node was accepted")
	}
}

// A suffix must match on a name boundary. "router-6d1b1" is not a suffix of
// "llm-router-6d1b1" for this purpose — matching mid-token would make the
// ambiguity check meaningless.
func TestResolveNodeMatchesOnABoundary(t *testing.T) {
	r := runnerWithNodes(fullC)

	if _, err := r.resolveNode(context.Background(), "router-6d1b1"); err == nil {
		t.Error("a mid-token match was accepted")
	}
	got, err := r.resolveNode(context.Background(), "llm-router-6d1b1")
	if err != nil {
		t.Fatalf("the real component suffix failed: %v", err)
	}
	if got != fullC {
		t.Errorf("got %q", got)
	}
}
