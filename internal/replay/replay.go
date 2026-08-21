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

	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// Reader reads the recorded run.
type Reader interface {
	ReadTraceDetail(ctx context.Context, projectName, traceID string) ([]sdktools.TraceSpanInfo, error)
}

// Publisher re-sends a recorded message as though an edge carried it.
type Publisher interface {
	Replay(ctx context.Context, fromNodePort, toNodeID, toPort, edgeID string, data []byte) error
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
	TraceID  string
	Hop      Hop
	Changes  []Change
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

// Hops lists the edge deliveries in a recorded trace, oldest first — the
// choices available to replay from.
func Hops(spans []sdktools.TraceSpanInfo) []Hop {
	var hops []Hop
	for _, s := range spans {
		if s.From == "" || s.To == "" {
			continue // a source-port span or a trigger: nothing upstream to stand in for
		}
		payload, ok := dataPayload(s)
		if !ok {
			continue
		}
		hops = append(hops, Hop{SpanID: s.SpanID, From: s.From, To: s.To, EdgeID: s.EdgeID, Payload: payload})
	}
	sort.SliceStable(hops, func(i, j int) bool { return hops[i].SpanID < hops[j].SpanID })
	return hops
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

	if err := r.Publisher.Replay(branchCtx, hop.From, toNode, toPort, hop.EdgeID, []byte(hop.Payload)); err != nil {
		return Result{TraceID: traceID, Hop: hop, Err: err, Duration: time.Since(started)}
	}

	branch := r.awaitBranch(ctx, traceID, seen, settle, sleep)
	if len(branch) == 0 {
		return Result{
			TraceID: traceID, Hop: hop, Duration: time.Since(started),
			Err: fmt.Errorf("nothing ran: the message published but no new span appeared. The target node may be gone, or its module may not be running"),
		}
	}

	return Result{
		TraceID:  traceID,
		Hop:      hop,
		Changes:  Diff(before, index(branch), hop),
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
