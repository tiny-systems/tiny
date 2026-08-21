package replay

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tiny-systems/module/pkg/redact"
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

// emit is the span a runner writes when a component puts a value on a source
// port — `port` set, no `from`/`to`. It carries the payload as the producer
// meant it, before any edge merged anything, and it is what a replay must send.
func emit(spanID, parent, port, payload string) sdktools.TraceSpanInfo {
	s := sdktools.TraceSpanInfo{SpanID: id(spanID), ParentSpanID: id(parent), Port: port}
	if payload != "" {
		s.Events = []sdktools.TraceEventInfo{{Name: "data", Data: map[string]string{"payload": payload}}}
	}
	return s
}

func span(spanID, parent, from, to, payload string) sdktools.TraceSpanInfo {
	s := sdktools.TraceSpanInfo{SpanID: id(spanID), ParentSpanID: id(parent), From: from, To: to}
	if payload != "" {
		s.Events = []sdktools.TraceEventInfo{{Name: "data", Data: map[string]string{"payload": payload}}}
	}
	return s
}

type fakeReader struct {
	rounds [][]sdktools.TraceSpanInfo
	// A trigger-fired replay lands in a trace of its own, so the fake has to be
	// able to answer for one.
	newRunID string
	newRun   []sdktools.TraceSpanInfo
}

func (f *fakeReader) ReadTraces(context.Context, string, string, time.Duration, int, int) ([]sdktools.TraceSummary, error) {
	if f.newRunID == "" {
		return nil, nil
	}
	return []sdktools.TraceSummary{{ID: f.newRunID, Start: time.Now().Add(time.Second).UnixMicro()}}, nil
}

func (f *fakeReader) ReadTraceDetail(_ context.Context, _, traceID string) ([]sdktools.TraceSpanInfo, error) {
	if traceID == f.newRunID && f.newRunID != "" {
		return f.newRun, nil
	}
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

	signalledNode, signalledPort, signalledData string
}

func (p *fakePublisher) Replay(_ context.Context, from, node, port, edge string, data []byte) error {
	p.from, p.node, p.port, p.edge, p.data = from, node, port, edge, string(data)
	return p.err
}

func (p *fakePublisher) SendSignal(_ context.Context, _, node, port string, data []byte, _ string) error {
	p.signalledNode, p.signalledPort, p.signalledData = node, port, string(data)
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
		span("1", "", "", "a:in", `{"x":1}`),        // no upstream to stand in for
		emit("1a", "1", "a:out", `{"x":1}`),         // what a:out emitted
		span("2", "1a", "a:out", "b:in", `{"x":1}`), // replayable
		emit("2a", "2", "b:out", ""),                // emitted nothing
		span("3", "2a", "b:out", "c:in", `{"x":1}`), // upstream payload unknown
	})
	if len(hops) != 1 || hops[0].To != "b:in" {
		t.Fatalf("hops = %+v", hops)
	}
}

// ---------- running ----------

func TestReplayPublishesAsTheRecordedUpstreamPort(t *testing.T) {
	recorded := []sdktools.TraceSpanInfo{
		emit("s0", "", "sig:out", `{"raw":"a,b"}`),
		span("aa", "s0", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		emit("r0", "aa", "dec:response", `{"count":2}`),
		span("bb", "r0", "dec:response", "dbg:in", `{"count":2}`),
	}
	after := append([]sdktools.TraceSpanInfo{}, recorded...)
	after = append(after,
		span("cc", "aa", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		emit("r1", "cc", "dec:response", `{"count":2}`),
		span("dd", "r1", "dec:response", "dbg:in", `{"count":2}`))

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
		emit("s0", "", "sig:out", `{"raw":"a,b"}`),
		span("aa", "s0", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		emit("r0", "aa", "dec:response", `{"count":2}`),
		span("bb", "r0", "dec:response", "dbg:in", `{"count":2}`),
	}
	// A real replay produces a new span for the port it was delivered to, then
	// the hops that followed. Leaving the target's own span out made the diff
	// report it as missing — which was the test lying, not the diff.
	after := append(append([]sdktools.TraceSpanInfo{}, recorded...),
		span("cc", "aa", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		emit("r1", "cc", "dec:response", `{"count":9}`),
		span("dd", "r1", "dec:response", "dbg:in", `{"count":9}`)) // different value, same shape

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
		emit("s0", "", "sig:out", `{"raw":"a,b"}`),
		span("aa", "s0", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		emit("r0", "aa", "dec:response", `{"count":2,"rows":[{"name":"x"}]}`),
		span("bb", "r0", "dec:response", "dbg:in", `{"count":2,"rows":[{"name":"x"}]}`),
	}
	after := append(append([]sdktools.TraceSpanInfo{}, recorded...),
		span("cc", "aa", "sig:out", "dec:request", `{"encoded":"a,b"}`),
		emit("r1", "cc", "dec:response", `{"count":2}`),
		span("dd", "r1", "dec:response", "dbg:in", `{"count":2}`)) // rows gone

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
	recorded := []sdktools.TraceSpanInfo{
		emit("s0", "", "sig:out", `{"raw":"x"}`),
		span("aa", "s0", "sig:out", "dec:request", `{}`),
	}
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

// Hops must come back in the order the run took them, which means walking the
// span tree. Span ids come from UUIDs, so sorting them orders by nothing —
// which is how "the first hop" once turned out to be the last one.
func TestHopsFollowTheRunNotTheIDs(t *testing.T) {
	// Ids chosen so string order is the reverse of causal order.
	spans := []sdktools.TraceSpanInfo{
		emit("s0", "", "sig:out", `{}`),
		span("cc", "bb", "b:out", "c:in", `{}`),
		span("aa", "s0", "sig:out", "a:in", `{}`),
		emit("a1", "aa", "a:out", `{}`),
		emit("b1", "bb", "b:out", `{}`),
		span("bb", "a1", "a:out", "b:in", `{}`),
	}
	hops := Hops(spans)
	if len(hops) != 3 {
		t.Fatalf("%d hops", len(hops))
	}
	order := []string{hops[0].To, hops[1].To, hops[2].To}
	want := []string{"a:in", "b:in", "c:in"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want the causal order %v", order, want)
		}
	}
}

// A whole-run replay is the entry hop replayed: everything below it re-runs,
// and the diff already scopes to descendants. So the default hop has to be the
// run's entry, not an arbitrary one.
func TestTheDefaultHopIsTheRunsEntry(t *testing.T) {
	spans := []sdktools.TraceSpanInfo{
		emit("s0", "", "sig:out", `{}`),
		span("cc", "bb", "b:out", "c:in", `{}`),
		span("aa", "s0", "sig:out", "a:in", `{}`),
		emit("a1", "aa", "a:out", `{}`),
		emit("b1", "bb", "b:out", `{}`),
		span("bb", "a1", "a:out", "b:in", `{}`),
	}
	hop, err := Find(Hops(spans), "")
	if err != nil {
		t.Fatal(err)
	}
	if hop.To != "a:in" {
		t.Fatalf("default hop delivers to %s, want the entry a:in", hop.To)
	}
}

// A trace whose parentage is broken must still offer its hops rather than
// silently returning a shorter list.
func TestHopsWithBrokenParentageAreStillOffered(t *testing.T) {
	spans := []sdktools.TraceSpanInfo{
		emit("s0", "", "sig:out", `{}`),
		span("aa", "s0", "sig:out", "a:in", `{}`),
		emit("x1", "", "x:out", `{}`),
		span("zz", "missing", "x:out", "y:in", `{}`), // parent not in the trace
	}
	if got := len(Hops(spans)); got != 2 {
		t.Fatalf("%d hops, want both", got)
	}
}

// The credential in a recorded payload is masked, so replaying that payload
// authenticates with the mask. Where the hop comes off a trigger there is
// somewhere to get a real one: fire the trigger and let it rebuild its context
// from live settings, which is how a normal run gets a key at all.
func TestARedactedTriggerPayloadFiresTheTriggerInstead(t *testing.T) {
	recorded := []sdktools.TraceSpanInfo{
		emit("s0", "", "sig:out", `{"apiKey":"`+redact.Value+`","question":"why"}`),
		span("aa", "s0", "sig:out", "llm:request", `{"apiKey":"`+redact.Value+`"}`),
	}
	// The fired trigger produces its own run, not a branch of the recording.
	newRun := []sdktools.TraceSpanInfo{
		emit("n0", "", "sig:out", `{"apiKey":"real","question":"why"}`),
		span("n1", "n0", "sig:out", "llm:request", `{"apiKey":"real"}`),
	}

	pub := &fakePublisher{}
	r := runner(&fakeReader{rounds: [][]sdktools.TraceSpanInfo{recorded, recorded}, newRunID: "aa11bb22cc33dd44ee55ff6600778899", newRun: newRun}, pub)
	res := r.Run(context.Background(), "18cdc2ac6079041a5c82809f66fcd83c", Hops(recorded)[0])

	if res.Err != nil {
		t.Fatalf("replay failed: %v", res.Err)
	}
	if pub.signalledNode != "sig" || pub.signalledPort != "_control" {
		t.Fatalf("fired %s:%s, want the trigger's control port", pub.signalledNode, pub.signalledPort)
	}
	if pub.signalledData != `{"send":true}` {
		t.Errorf("payload = %s", pub.signalledData)
	}
	if pub.data != "" {
		t.Error("it also re-sent the masked payload as an edge message")
	}
}

// Mid-chain there is nothing to rebuild a credential from. Refusing and naming
// the entry beats running and reporting the whole flow as broken, which is
// what happened the first time.
func TestARedactedMidChainPayloadIsRefusedWithADirection(t *testing.T) {
	recorded := []sdktools.TraceSpanInfo{
		emit("s0", "", "sig:out", `{"apiKey":"x"}`),
		span("aa", "s0", "sig:out", "js:request", `{}`),
		emit("j0", "aa", "js:response", `{"apiKey":"`+redact.Value+`"}`),
		span("bb", "j0", "js:response", "llm:request", `{"apiKey":"`+redact.Value+`"}`),
	}
	r := runner(&fakeReader{rounds: [][]sdktools.TraceSpanInfo{recorded, recorded}}, &fakePublisher{})

	hop, err := Find(Hops(recorded), "llm:request")
	if err != nil {
		t.Fatal(err)
	}
	res := r.Run(context.Background(), "18cdc2ac6079041a5c82809f66fcd83c", hop)
	if res.Err == nil {
		t.Fatal("a masked mid-chain credential was replayed anyway")
	}
	if !strings.Contains(res.Err.Error(), "js:request") {
		t.Errorf("the refusal does not say where to replay from instead: %v", res.Err)
	}
}
