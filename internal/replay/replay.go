package replay

import (
	"context"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/tiny-systems/module/pkg/redact"
	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// Reader reads the recorded run, and the runs that follow a replay.
//
// Two methods because a replay lands in one of two places. An edge message
// parented to a recorded hop stays in that trace, as a branch. A fired trigger
// cannot: the signal's Send emits from its own goroutine, so everything after
// the control hop begins a trace of its own and no parent survives that
// boundary. Looking only inside the recorded trace found nothing, and reported
// a working flow as entirely broken.
type Reader interface {
	ReadTraceDetail(ctx context.Context, projectName, traceID string) ([]sdktools.TraceSpanInfo, error)
	ReadTraces(ctx context.Context, projectName, flowName string, lookback time.Duration, offset, limit int) ([]sdktools.TraceSummary, error)
}

// Publisher re-sends a recorded message as though an edge carried it, or fires
// a trigger the way a button press does.
type Publisher interface {
	Replay(ctx context.Context, fromNodePort, toNodeID, toPort, edgeID string, data []byte) error
	SendSignal(ctx context.Context, projectName, nodeID, portName string, data []byte, traceID string) error
}

// Hop is one recorded edge delivery — a message that went from somewhere to
// somewhere, with the payload it carried.
type Hop struct {
	SpanID  string
	From    string // "<node>:<port>"
	To      string // "<node>:<port>"
	EdgeID  string
	Payload string
}

func (h Hop) String() string { return h.From + " → " + h.To }

// Runner re-drives a hop and reports what changed.
type Runner struct {
	Project   string
	Reader    Reader
	Publisher Publisher

	// Settle is how long the replayed branch must produce no new spans before
	// it counts as finished.
	Settle time.Duration

	// Timeout bounds the wait for the branch to appear at all. Configurable so
	// a test does not sit through the production default.
	Timeout time.Duration

	Sleep func(time.Duration)
}

// Result is one replay.
type Result struct {
	TraceID string
	Hop     Hop
	Changes []Change
	// Compared is how many ports the diff actually judged. Without it,
	// "unchanged" is unfalsifiable: a replay that reached nothing and a replay
	// that reached everything and matched read identically.
	Compared int
	Err      error
	Duration time.Duration
}

func (r Result) Unchanged() bool { return r.Err == nil && len(r.Changes) == 0 }

// Change is one difference between the recorded run and the replay.
type Change struct {
	Port string
	Kind string // "missing", "new", "shape"
	Was  string
	Now  string
}

func (c Change) String() string {
	switch c.Kind {
	case "missing":
		return fmt.Sprintf("%s no longer receives anything (it did before)", c.Port)
	case "new":
		return fmt.Sprintf("%s receives something now (it did not before): %s", c.Port, c.Now)
	default:
		return fmt.Sprintf("%s changed shape\n        was %s\n        now %s", c.Port, c.Was, c.Now)
	}
}

// Hops lists the edge deliveries in a recorded trace, in the order the run
// took them — the choices available to replay from.
//
// Ordered by walking the span tree, not by sorting ids. Span ids come from
// UUIDs, so sorting them ordered the hops by nothing at all: "the first hop"
// was whichever id happened to sort first, which is how a replay of hop 8 of
// one run ended up re-driving a different hop entirely.
func Hops(spans []sdktools.TraceSpanInfo) []Hop {
	emitted := emittedByPort(spans)
	replayable := map[string]Hop{}
	for _, s := range spans {
		if s.From == "" || s.To == "" {
			continue // a source-port span or a trigger: nothing upstream to stand in for
		}
		// The payload to replay is what the UPSTREAM EMITTED, not what arrived
		// after the edge merged it. A trace records both: the source-port span
		// holds the emitted value, this span holds the delivered one. Sending
		// the delivered value back down the same edge makes the edge's
		// expressions evaluate against an already-merged shape — they resolve
		// to null, and every derived field, credentials included, arrives
		// empty.
		payload, ok := emitted[s.From]
		if !ok {
			continue
		}
		replayable[s.SpanID] = Hop{SpanID: s.SpanID, From: s.From, To: s.To, EdgeID: s.EdgeID, Payload: payload}
	}
	if len(replayable) == 0 {
		return nil
	}

	children := map[string][]sdktools.TraceSpanInfo{}
	present := make(map[string]bool, len(spans))
	for _, s := range spans {
		present[s.SpanID] = true
	}
	for _, s := range spans {
		children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
	}

	// Roots are spans whose parent is not in this trace — the run's entry
	// points. A trace normally has one; a re-driven branch adds none, since its
	// parent is the hop it re-drove.
	var roots []sdktools.TraceSpanInfo
	for _, s := range spans {
		if s.ParentSpanID == "" || !present[s.ParentSpanID] {
			roots = append(roots, s)
		}
	}
	sort.SliceStable(roots, func(i, j int) bool { return roots[i].SpanID < roots[j].SpanID })

	var out []Hop
	seen := map[string]bool{}
	var walk func(s sdktools.TraceSpanInfo, depth int)
	walk = func(s sdktools.TraceSpanInfo, depth int) {
		if depth > 128 || seen[s.SpanID] {
			return
		}
		seen[s.SpanID] = true
		if hop, ok := replayable[s.SpanID]; ok {
			out = append(out, hop)
		}
		kids := children[s.SpanID]
		sort.SliceStable(kids, func(i, j int) bool { return kids[i].SpanID < kids[j].SpanID })
		for _, k := range kids {
			walk(k, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	// Anything the walk could not reach — a trace with broken parentage — still
	// belongs in the list, at the end, rather than disappearing from it.
	for _, s := range spans {
		if hop, ok := replayable[s.SpanID]; ok && !seen[s.SpanID] {
			out = append(out, hop)
		}
	}
	return out
}

// Find picks a hop by target port, matching on a suffix so a caller can say
// "budget-guard-52465:request" and not carry the flow id.
//
// Named rather than numbered because an index is only stable for one reading of
// one trace: run anything in between and hop 8 is a different hop. A port name
// means the same thing tomorrow.
func Find(hops []Hop, ref string) (Hop, error) {
	if ref == "" {
		if len(hops) == 0 {
			return Hop{}, fmt.Errorf("no replayable hop in this run")
		}
		return hops[0], nil
	}
	var matches []Hop
	for _, h := range hops {
		if h.To == ref || strings.HasSuffix(h.To, ref) || strings.HasSuffix(h.To, "."+ref) {
			matches = append(matches, h)
		}
	}
	switch len(matches) {
	case 0:
		return Hop{}, fmt.Errorf("no hop delivered to %q in this run", ref)
	case 1:
		return matches[0], nil
	}
	// Several deliveries to the same port is normal — a loop. The earliest is
	// the useful default, and saying so beats picking one silently.
	return matches[0], nil
}

// Run re-drives one hop of a recorded trace and diffs what followed.
//
// The replay is parented to the recorded hop, so it lands in the same trace as
// a sub-branch rather than as an unrelated run — which is what lets a canvas
// draw it beside the original and a diff tell the two apart.
func (r *Runner) Run(ctx context.Context, traceID string, hop Hop) Result {
	started := time.Now()
	settle := r.Settle
	if settle <= 0 {
		settle = 3 * time.Second
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	before, err := r.Reader.ReadTraceDetail(ctx, r.Project, traceID)
	if err != nil {
		return Result{TraceID: traceID, Hop: hop, Err: err, Duration: time.Since(started)}
	}
	seen := spanIDs(before)

	toNode, toPort, ok := splitPort(hop.To)
	if !ok {
		return Result{TraceID: traceID, Hop: hop, Err: fmt.Errorf("recorded target %q is not <node>:<port>", hop.To), Duration: time.Since(started)}
	}

	branchCtx, err := childOf(ctx, traceID, hop.SpanID)
	if err != nil {
		return Result{TraceID: traceID, Hop: hop, Err: err, Duration: time.Since(started)}
	}

	// A recorded payload carrying a redaction marker cannot be replayed as
	// data: the credential in it is masked, and sending the mask produces an
	// authentication failure rather than a comparison. Where the hop comes
	// straight off a trigger, the trigger can be fired instead — it rebuilds
	// its own context from live settings, key included, which is how a normal
	// run gets one. Anywhere else there is nothing to reconstruct from, and
	// saying so beats a diff that reports the whole flow as broken.
	redacted := strings.Contains(hop.Payload, redact.Value)
	firedTrigger := false
	firedAt := time.Now()
	switch {
	case redacted && isTrigger(before, hop.From):
		node, _, _ := splitPort(hop.From)
		firedAt = time.Now()
		if err := r.Publisher.SendSignal(branchCtx, r.Project, node, controlPort, []byte(`{"send":true}`), traceID); err != nil {
			return Result{TraceID: traceID, Hop: hop, Err: fmt.Errorf("fire trigger %s: %w", node, err), Duration: time.Since(started)}
		}
		firedTrigger = true
	case redacted:
		return Result{
			TraceID: traceID, Hop: hop, Duration: time.Since(started),
			Err: fmt.Errorf("this hop carries a redacted credential and does not come from a trigger, so there is nothing to rebuild it from — replay from the run's entry instead (--at %s)", entryPort(before)),
		}
	default:
		if err := r.Publisher.Replay(branchCtx, hop.From, toNode, toPort, hop.EdgeID, []byte(hop.Payload)); err != nil {
			return Result{TraceID: traceID, Hop: hop, Err: err, Duration: time.Since(started)}
		}
	}

	var branch []sdktools.TraceSpanInfo
	if firedTrigger {
		branch = r.awaitNewRun(ctx, firedAt, settle, sleep)
	} else {
		branch = r.awaitBranch(ctx, traceID, seen, settle, sleep)
	}
	if len(branch) == 0 {
		return Result{
			TraceID: traceID, Hop: hop, Duration: time.Since(started),
			Err: fmt.Errorf("nothing ran: the message published but no new span appeared. The target node may be gone, or its module may not be running"),
		}
	}

	replayed := index(branch)
	return Result{
		TraceID:  traceID,
		Hop:      hop,
		Changes:  Diff(before, replayed, hop),
		Compared: len(portsBelow(before, hop)),
		Duration: time.Since(started),
	}
}

// Diff compares what the recording saw against what the replay saw.
//
// Scoped to the hops that descended from the replayed one in the recording.
// Everything upstream did not run again and never should have, so reporting it
// as missing buries the answer — the first version did exactly that and
// produced five lines of noise around nothing.
func Diff(recordedSpans []sdktools.TraceSpanInfo, replayed map[string][]string, hop Hop) []Change {
	recorded := index(recordedSpans)
	expected := portsBelow(recordedSpans, hop)

	var changes []Change

	for port, now := range replayed {
		was, existed := recorded[port]
		if !existed {
			changes = append(changes, Change{Port: port, Kind: "new", Now: Shape(now[0])})
			continue
		}
		if s1, s2 := Shape(was[0]), Shape(now[0]); s1 != s2 {
			changes = append(changes, Change{Port: port, Kind: "shape", Was: s1, Now: s2})
		}
	}

	for port := range expected {
		if _, ran := replayed[port]; ran {
			continue
		}
		if len(recorded[port]) == 0 {
			continue
		}
		changes = append(changes, Change{Port: port, Kind: "missing", Was: Shape(recorded[port][0])})
	}

	sort.Slice(changes, func(i, j int) bool { return changes[i].Port < changes[j].Port })
	return changes
}

// portsBelow returns the ports the recording reached at or below the replayed
// hop, walking the span tree by parentage.
//
// This is what the parent span id is for. Without it the only available guess
// was "every port the recording touched", which reports a whole run as missing
// whenever a replay starts halfway down.
func portsBelow(spans []sdktools.TraceSpanInfo, hop Hop) map[string]bool {
	children := map[string][]sdktools.TraceSpanInfo{}
	for _, s := range spans {
		children[s.ParentSpanID] = append(children[s.ParentSpanID], s)
	}

	ports := map[string]bool{}
	if hop.To != "" {
		ports[hop.To] = true
	}

	var walk func(spanID string, depth int)
	walk = func(spanID string, depth int) {
		if depth > 64 {
			return // a cycle in recorded parentage must not hang a diff
		}
		for _, child := range children[spanID] {
			target := child.To
			if target == "" {
				target = child.Port
			}
			if target != "" {
				ports[target] = true
			}
			walk(child.SpanID, depth+1)
		}
	}
	walk(hop.SpanID, 0)
	return ports
}

func (r *Runner) awaitBranch(ctx context.Context, traceID string, before map[string]bool, settle time.Duration, sleep func(time.Duration)) []sdktools.TraceSpanInfo {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var (
		branch     []sdktools.TraceSpanInfo
		lastCount  int
		stableFrom time.Time
	)
	for time.Now().Before(deadline) {
		current, err := r.Reader.ReadTraceDetail(ctx, r.Project, traceID)
		if err == nil {
			branch = branch[:0]
			for _, s := range current {
				if !before[s.SpanID] {
					branch = append(branch, s)
				}
			}
			if len(branch) != lastCount {
				lastCount = len(branch)
				stableFrom = time.Now()
			} else if len(branch) > 0 && !stableFrom.IsZero() && time.Since(stableFrom) >= settle {
				return branch
			}
		}
		sleep(500 * time.Millisecond)
	}
	return branch
}

const controlPort = "_control"

// isTrigger reports whether a source port belongs to a node nothing sent to in
// this run — a signal, a cron, a ticker. Such a node builds its own context
// from its settings, so firing it produces a live credential where a recording
// only has a mask.
func isTrigger(spans []sdktools.TraceSpanInfo, sourcePort string) bool {
	node, _, ok := splitPort(sourcePort)
	if !ok {
		return false
	}
	for _, s := range spans {
		if s.To == "" {
			continue
		}
		if target, _, ok := splitPort(s.To); ok && target == node && !strings.HasPrefix(portOf(s.To), "_") {
			return false // something delivered to one of its business ports
		}
	}
	return true
}

func portOf(ref string) string {
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		return ref[i+1:]
	}
	return ""
}

// entryPort names the run's first delivery, for an error message that tells the
// reader where to replay from instead.
func entryPort(spans []sdktools.TraceSpanInfo) string {
	hops := Hops(spans)
	if len(hops) == 0 {
		return "<the run has no replayable entry>"
	}
	return hops[0].To
}

// emittedByPort collects what each source port emitted — the payload a
// downstream edge was handed before it merged anything.
//
// These are the spans the runner writes for an output: they carry `port` rather
// than `from`/`to`, and they are the only record of a value as its producer
// meant it.
func emittedByPort(spans []sdktools.TraceSpanInfo) map[string]string {
	out := map[string]string{}
	for _, s := range spans {
		if s.Port == "" || s.To != "" {
			continue
		}
		if payload, ok := dataPayload(s); ok {
			if _, already := out[s.Port]; !already {
				out[s.Port] = payload
			}
		}
	}
	return out
}

// awaitNewRun collects the run a fired trigger started — a trace of its own,
// since the signal's Send crosses a goroutine and carries no parent across it.
func (r *Runner) awaitNewRun(ctx context.Context, firedAt time.Time, settle time.Duration, sleep func(time.Duration)) []sdktools.TraceSpanInfo {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	cutoff := firedAt.Add(-time.Second).UnixMicro()

	var (
		spans      []sdktools.TraceSpanInfo
		lastCount  int
		stableFrom time.Time
	)
	for time.Now().Before(deadline) {
		summaries, err := r.Reader.ReadTraces(ctx, r.Project, "", time.Since(firedAt)+2*time.Second, 0, 25)
		if err == nil {
			current := r.detailsSince(ctx, summaries, cutoff)
			if len(current) != lastCount {
				lastCount = len(current)
				stableFrom = time.Now()
				spans = current
			} else if len(current) > 0 && !stableFrom.IsZero() && time.Since(stableFrom) >= settle {
				return current
			}
		}
		sleep(500 * time.Millisecond)
	}
	return spans
}

func (r *Runner) detailsSince(ctx context.Context, summaries []sdktools.TraceSummary, cutoff int64) []sdktools.TraceSpanInfo {
	var out []sdktools.TraceSpanInfo
	for _, s := range summaries {
		if s.Start < cutoff {
			continue
		}
		detail, err := r.Reader.ReadTraceDetail(ctx, r.Project, s.ID)
		if err != nil {
			continue
		}
		out = append(out, detail...)
	}
	return out
}

// index maps each port to the payloads that reached it.
func index(spans []sdktools.TraceSpanInfo) map[string][]string {
	out := map[string][]string{}
	for _, s := range spans {
		target := s.To
		if target == "" {
			target = s.Port
		}
		if target == "" {
			continue
		}
		if payload, ok := dataPayload(s); ok {
			out[target] = append(out[target], payload)
		}
	}
	return out
}

func dataPayload(s sdktools.TraceSpanInfo) (string, bool) {
	for _, e := range s.Events {
		if e.Name != "data" {
			continue
		}
		if p, ok := e.Data["payload"]; ok {
			return p, true
		}
	}
	return "", false
}

func spanIDs(spans []sdktools.TraceSpanInfo) map[string]bool {
	out := make(map[string]bool, len(spans))
	for _, s := range spans {
		out[s.SpanID] = true
	}
	return out
}

func splitPort(ref string) (node, port string, ok bool) {
	i := strings.LastIndex(ref, ":")
	if i <= 0 || i == len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
}

// childOf builds a context whose span parent is the recorded hop, so the
// replay is recorded as a branch of the run it re-drove.
func childOf(ctx context.Context, traceID, spanID string) (context.Context, error) {
	tid, err := oteltrace.TraceIDFromHex(traceID)
	if err != nil {
		return nil, fmt.Errorf("recorded trace id %q: %w", traceID, err)
	}
	raw, err := hex.DecodeString(spanID)
	if err != nil || len(raw) != 8 {
		return nil, fmt.Errorf("recorded span id %q is not an 8-byte hex id", spanID)
	}
	var sid oteltrace.SpanID
	copy(sid[:], raw)

	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
	if !sc.IsValid() {
		return nil, fmt.Errorf("recorded trace/span ids do not form a valid parent")
	}

	// The propagator is what puts the parent on the wire. OTel's global default
	// is a no-op, so without this the context is built correctly, injected into
	// nothing, and the module starts a trace of its own — the replay runs, and
	// looks to the caller like nothing ran at all.
	if _, ok := otel.GetTextMapPropagator().(propagation.TraceContext); !ok {
		otel.SetTextMapPropagator(propagation.TraceContext{})
	}

	return oteltrace.ContextWithSpanContext(ctx, sc), nil
}
