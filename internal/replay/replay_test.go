package replay

import (
	"context"
	"strings"
	"testing"
	"time"

	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// id pads a readable label into a valid 16-hex span id: the replay builds a
// real OTel parent from it, so a fixture id has to be one.
func id(label string) string {
	if label == "" {
		return ""
	}
	return label + strings.Repeat("0", 16-len(label))
}

func span(spanID, parent, from, to, payload string) sdktools.TraceSpanInfo {
	s := sdktools.TraceSpanInfo{SpanID: id(spanID), ParentSpanID: id(parent), From: from, To: to}
	if payload != "" {
		s.Events = []sdktools.TraceEventInfo{{Name: "data", Data: map[string]string{"payload": payload}}}
	}
	return s
}

type fakeReader struct{ rounds [][]sdktools.TraceSpanInfo }

func (f *fakeReader) ReadTraceDetail(context.Context, string, string) ([]sdktools.TraceSpanInfo, error) {
	if len(f.rounds) == 0 {
		return nil, nil
	}
	r := f.rounds[0]
	if len(f.rounds) > 1 {
		f.rounds = f.rounds[1:]
	}
	return r, nil
}

type fakePublisher struct {
	from, node, port, edge string
	data                   string
	err                    error
}

func (p *fakePublisher) Replay(_ context.Context, from, node, port, edge string, data []byte) error {
	p.from, p.node, p.port, p.edge, p.data = from, node, port, edge, string(data)
	return p.err
}

func runner(reader Reader, pub Publisher) *Runner {
	return &Runner{Project: "proj", Reader: reader, Publisher: pub, Settle: time.Millisecond, Timeout: 50 * time.Millisecond, Sleep: func(time.Duration) {}}
}

// ---------- shapes ----------

// A model rewords its answer between calls. Comparing values would make every
// replay of an LLM flow a false alarm; comparing shape asks the question that
// matters — is the same kind of thing still coming out.
func TestRewordedTextIsNotAChange(t *testing.T) {
	a := Shape(`{"answer":"the api pod is crashlooping","count":2}`)
	b := Shape(`{"answer":"pod api-1 has restarted 7 times","count":5}`)
	if a != b {
		t.Fatalf("shapes differ for the same structure:\n %s\n %s", a, b)
	}
}

func TestAFieldChangingTypeIsAChange(t *testing.T) {
	if Shape(`{"restarts":7}`) == Shape(`{"restarts":"7"}`) {
		t.Fatal("a number becoming a string read as unchanged — that is exactly what a module release breaks")
	}
}

func TestAMissingFieldIsAChange(t *testing.T) {
	if Shape(`{"answer":"x","usage":{"in":1}}`) == Shape(`{"answer":"x"}`) {
		t.Fatal("a dropped field read as unchanged")
	}
}

// A list growing is not a structural change; a list whose elements changed
// shape is.
func TestListLengthIsNotShapeButElementsAre(t *testing.T) {
	if Shape(`{"pods":[{"name":"a"}]}`) != Shape(`{"pods":[{"name":"a"},{"name":"b"}]}`) {
		t.Error("a longer list read as a change")
	}
	if Shape(`{"pods":[{"name":"a"}]}`) == Shape(`{"pods":[{"title":"a"}]}`) {
		t.Error("renamed element field read as unchanged")
	}
}

func TestNonJSONPayloadHasAShapeToo(t *testing.T) {
	if Shape("plain text") != "text" {
		t.Fatalf("shape = %q", Shape("plain text"))
	}
}

// ---------- choosing a hop ----------

// Only edge deliveries can be replayed: a source-port span has no upstream to
// stand in for, and a span with no payload has nothing to send.
func TestHopsAreEdgeDeliveriesWithPayloads(t *testing.T) {
	hops := Hops([]sdktools.TraceSpanInfo{
		span("1", "", "", "a:in", `{"x":1}`),       // no upstream
		span("2", "1", "a:out", "b:in", `{"x":1}`), // replayable
		span("3", "2", "b:out", "c:in", ""),        // no payload
		{SpanID: "4", Port: "b:out", Events: nil},  // source port span
	})
	if len(hops) != 1 || hops[0].To != "b:in" {
		t.Fatalf("hops = %+v", hops)
	}
}

// ---------- running ----------

func TestReplayPublishesAsTheRecordedUpstreamPort(t *testing.T) {
	recorded := []sdktools.TraceSpanInfo{
		span("aa", "", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		span("bb", "aa", "dec:response", "dbg:in", `{"count":2}`),
	}
	after := append([]sdktools.TraceSpanInfo{}, recorded...)
	after = append(after,
		span("cc", "aa", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		span("dd", "cc", "dec:response", "dbg:in", `{"count":2}`))

	pub := &fakePublisher{}
	r := runner(&fakeReader{rounds: [][]sdktools.TraceSpanInfo{recorded, after}}, pub)

	res := r.Run(context.Background(), "18cdc2ac6079041a5c82809f66fcd83c", Hop{
		SpanID: id("aa"), From: "sig:out", To: "dec:request", EdgeID: "e1", Payload: `{"encoded":"a,b"}`,
	})
	if res.Err != nil {
		t.Fatalf("replay failed: %v", res.Err)
	}
	// The whole point: it arrives as an edge message from the recorded
	// upstream port, not as an external signal, so the runner re-evaluates the
	// edge and refills derived fields from live context.
	if pub.from != "sig:out" || pub.node != "dec" || pub.port != "request" {
		t.Fatalf("published as %s -> %s:%s", pub.from, pub.node, pub.port)
	}
	if pub.edge != "e1" {
		t.Errorf("edge id = %q — the canvas needs it to highlight the wire", pub.edge)
	}
	if pub.data != `{"encoded":"a,b"}` {
		t.Errorf("payload = %s", pub.data)
	}
}

func TestUnchangedReplayReportsNothing(t *testing.T) {
	recorded := []sdktools.TraceSpanInfo{
		span("aa", "", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		span("bb", "aa", "dec:response", "dbg:in", `{"count":2}`),
	}
	// A real replay produces a new span for the port it was delivered to, then
	// the hops that followed. Leaving the target's own span out made the diff
	// report it as missing — which was the test lying, not the diff.
	after := append(append([]sdktools.TraceSpanInfo{}, recorded...),
		span("cc", "aa", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		span("dd", "cc", "dec:response", "dbg:in", `{"count":9}`)) // different value, same shape

	r := runner(&fakeReader{rounds: [][]sdktools.TraceSpanInfo{recorded, after}}, &fakePublisher{})
	res := r.Run(context.Background(), "18cdc2ac6079041a5c82809f66fcd83c", Hop{
		SpanID: id("aa"), From: "sig:out", To: "dec:request", Payload: `{}`,
	})
	if !res.Unchanged() {
		t.Fatalf("reported changes for a run that only changed values: %v", res.Changes)
	}
}

func TestAChangedShapeIsReported(t *testing.T) {
	recorded := []sdktools.TraceSpanInfo{
		span("aa", "", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		span("bb", "aa", "dec:response", "dbg:in", `{"count":2,"rows":[{"name":"x"}]}`),
	}
	after := append(append([]sdktools.TraceSpanInfo{}, recorded...),
		span("cc", "aa", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		span("dd", "cc", "dec:response", "dbg:in", `{"count":2}`)) // rows gone

	r := runner(&fakeReader{rounds: [][]sdktools.TraceSpanInfo{recorded, after}}, &fakePublisher{})
	res := r.Run(context.Background(), "18cdc2ac6079041a5c82809f66fcd83c", Hop{
		SpanID: id("aa"), From: "sig:out", To: "dec:request", Payload: `{}`,
	})
	if len(res.Changes) == 0 {
		t.Fatal("a dropped field went unreported")
	}
	found := false
	for _, c := range res.Changes {
		if c.Port == "dbg:in" && c.Kind == "shape" {
			found = true
			if !strings.Contains(c.Was, "rows") || strings.Contains(c.Now, "rows") {
				t.Errorf("the change does not say what was lost: %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("changes = %+v", res.Changes)
	}
}

// A replay that published but set nothing running is a different failure from
// a replay that ran and behaved differently, and must not be reported as one.
func TestNothingRunningIsAnErrorNotAChange(t *testing.T) {
	recorded := []sdktools.TraceSpanInfo{span("aa", "", "sig:out", "dec:request", `{}`)}
	r := runner(&fakeReader{rounds: [][]sdktools.TraceSpanInfo{recorded, recorded}}, &fakePublisher{})

	res := r.Run(context.Background(), "18cdc2ac6079041a5c82809f66fcd83c", Hop{
		SpanID: id("aa"), From: "sig:out", To: "dec:request", Payload: `{}`,
	})
	if res.Err == nil {
		t.Fatal("expected an error when nothing ran")
	}
	if len(res.Changes) != 0 {
		t.Errorf("changes = %+v", res.Changes)
	}
}

// The replay must be a branch of the run it re-drove, not an unrelated run —
// that is what lets a canvas draw them together and a diff tell them apart.
func TestAnInvalidRecordedIDIsRefused(t *testing.T) {
	r := runner(&fakeReader{rounds: [][]sdktools.TraceSpanInfo{{}}}, &fakePublisher{})
	res := r.Run(context.Background(), "not-a-trace-id", Hop{SpanID: id("aa"), From: "a:out", To: "b:in"})
	if res.Err == nil || !strings.Contains(res.Err.Error(), "trace id") {
		t.Fatalf("err = %v", res.Err)
	}
}
