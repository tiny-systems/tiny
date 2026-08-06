package adapters

import (
	"strings"
	"testing"
)

// TestMatchTraceIDPrefix pins the truncated-trace-id resolution rules.
// The bug this guards: get_traces returns full 32-char ids, a caller
// shortens one for display (id[:16]) and pastes the short form back into
// get_trace_detail — the collector's exact-match lookup then NotFounds a
// trace that is right there in the list. A unique prefix must resolve;
// an ambiguous or unknown one must say what went wrong.
func TestMatchTraceIDPrefix(t *testing.T) {
	ids := []string{
		"18c90de02098e8606221bb31edf8307c",
		"18c90de0209399004543bdfb0e237b5a",
		"18c90dd4dca274d76774d405003c44b7",
	}

	t.Run("unique 16-char prefix resolves", func(t *testing.T) {
		got, err := matchTraceIDPrefix(ids, "18c90de02098e860")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "18c90de02098e8606221bb31edf8307c" {
			t.Fatalf("resolved to %s", got)
		}
	})

	t.Run("ambiguous prefix errors with candidates", func(t *testing.T) {
		_, err := matchTraceIDPrefix(ids, "18c90de020")
		if err == nil {
			t.Fatal("expected ambiguity error")
		}
		if !strings.Contains(err.Error(), "ambiguous") {
			t.Fatalf("error should say ambiguous: %v", err)
		}
		if !strings.Contains(err.Error(), "18c90de02098e8606221bb31edf8307c") {
			t.Fatalf("error should list candidates: %v", err)
		}
	})

	t.Run("unknown prefix errors as truncated", func(t *testing.T) {
		_, err := matchTraceIDPrefix(ids, "deadbeef")
		if err == nil {
			t.Fatal("expected not-found error")
		}
		if !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("error should explain truncation: %v", err)
		}
	})
}
