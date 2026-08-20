package adapters

import (
	"strings"
	"testing"

	"github.com/tiny-systems/module/pkg/utils"
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

// The runtime masks a payload before it reaches a span — but a collector holds
// spans written by whatever module version produced them, including versions
// released before that existed. This is the surface an agent reads through
// get_trace_detail, so it must not hand over a key whatever is in storage.
func TestSpanPayloadsAreMaskedOnTheWayOut(t *testing.T) {
	span := utils.Span{
		Attributes: []utils.SpanAttribute{{Key: "to", Value: "flow.mod.n1:request"}},
		Events: []utils.SpanEvent{{
			Name: "data",
			Attributes: []utils.SpanAttribute{
				{Key: "payload", Value: `{"apiKey":"sk-ant-api03-` + strings.Repeat("A", 80) + `","alert":"disk full"}`},
			},
		}},
	}

	got := spanToInfo(span)
	if len(got.Events) != 1 {
		t.Fatalf("%d events", len(got.Events))
	}
	payload := got.Events[0].Data["payload"]
	if strings.Contains(payload, "sk-ant-api03-AAAA") {
		t.Fatal("a credential in stored span data reached the caller")
	}
	if !strings.Contains(payload, "disk full") {
		t.Errorf("the rest of the payload was lost: %s", payload)
	}
}
