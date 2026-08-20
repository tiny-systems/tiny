package eval

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/tiny-systems/module/pkg/evals"
	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// Sender fires the trigger. The context carries the trace the run will be
// recorded under, which is how a result is matched to the eval that caused it
// rather than to whatever else the cluster was doing.
type Sender interface {
	SendSignal(ctx context.Context, projectName, nodeID, portName string, data []byte, traceID string) error
}

// Reader reads back what happened.
//
// Both halves are needed because a run is not one trace. A trigger's own hop
// is recorded under the trace the eval minted, but a component that emits from
// its own goroutine — the signal's Send, every long-running component —
// starts a fresh trace for what follows. Correlating on the minted id alone
// would judge the first hop and call the rest missing, which is exactly the
// wrong answer: it reports a working flow as broken.
type Reader interface {
	ReadTraceDetail(ctx context.Context, projectName, traceID string) ([]sdktools.TraceSpanInfo, error)
	ReadTraces(ctx context.Context, projectName, flowName string, lookback time.Duration, offset, limit int) ([]sdktools.TraceSummary, error)
}

// Result is one eval's outcome.
type Result struct {
	Spec     evals.Spec
	TraceID  string
	Failures []evals.Failure
	// Err is set when the eval could not be run at all — the trigger did not
	// publish, no trace ever appeared. Distinct from Failures, because "the
	// claim is false" and "the check never ran" call for different reactions.
	Err      error
	Duration time.Duration
	// Observed is what the run produced. Kept on the result because a caller
	// with no expectations is asking exactly this — what happened — and that
	// is the material an eval gets written from.
	Observed evals.Observed
}

func (r Result) Passed() bool { return r.Err == nil && len(r.Failures) == 0 }

// Runner executes evals against a live project.
type Runner struct {
	Project string
	Sender  Sender
	Reader  Reader

	// Settle is how long the run must produce no new spans before it counts as
	// finished. A flow that pauses on a slow API call has gaps; too short and
	// the eval judges half a run.
	Settle time.Duration

	// Now and Sleep exist so tests do not wait in real time.
	Sleep func(time.Duration)
}

// Run fires one eval and checks it.
func (r *Runner) Run(ctx context.Context, spec evals.Spec) Result {
	started := time.Now()
	settle := r.Settle
	if settle <= 0 {
		settle = 3 * time.Second
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}

	traceCtx, traceID, err := withFreshTrace(ctx)
	if err != nil {
		return Result{Spec: spec, Err: err, Duration: time.Since(started)}
	}

	payload, err := json.Marshal(spec.Trigger.Data)
	if err != nil {
		return Result{Spec: spec, TraceID: traceID, Err: fmt.Errorf("encode trigger data: %w", err), Duration: time.Since(started)}
	}

	node, err := r.resolveNode(ctx, spec.Trigger.Node)
	if err != nil {
		return Result{Spec: spec, TraceID: traceID, Err: err, Duration: time.Since(started)}
	}

	if err := r.Sender.SendSignal(traceCtx, r.Project, node, spec.Trigger.Port, payload, traceID); err != nil {
		return Result{Spec: spec, TraceID: traceID, Err: fmt.Errorf("fire %s:%s: %w", node, spec.Trigger.Port, err), Duration: time.Since(started)}
	}

	spans, err := r.awaitRun(ctx, spec, traceID, started, spec.Timeout.Or(60*time.Second), settle, sleep)
	if err != nil {
		return Result{Spec: spec, TraceID: traceID, Err: err, Duration: time.Since(started)}
	}

	got := observe(spans)
	return Result{
		Spec:     spec,
		TraceID:  traceID,
		Failures: evals.Check(spec, got),
		Observed: got,
		Duration: time.Since(started),
	}
}

// resolveNode is a hook for suffix resolution; the trigger address is used
// as-is today, since the sender addresses nodes by their full name.
func (r *Runner) resolveNode(_ context.Context, node string) (string, error) {
	if strings.TrimSpace(node) == "" {
		return "", fmt.Errorf("trigger node is empty")
	}
	return node, nil
}

// awaitRun collects everything the trigger set off and waits for it to settle.
//
// It gathers the minted trace plus every trace the project started after the
// trigger, because a run crosses trace boundaries the moment a component emits
// from its own goroutine. Waiting for "no new spans for a while" rather than a
// fixed sleep is what makes this usable on a flow whose duration depends on an
// API call: a fast run is judged as soon as it is done, a slow one is not
// judged early.
func (r *Runner) awaitRun(ctx context.Context, spec evals.Spec, traceID string, firedAt time.Time, timeout, settle time.Duration, sleep func(time.Duration)) ([]sdktools.TraceSpanInfo, error) {
	deadline := time.Now().Add(timeout)
	poll := 500 * time.Millisecond

	var (
		spans      []sdktools.TraceSpanInfo
		lastCount  int
		stableFrom time.Time
	)

	for time.Now().Before(deadline) {
		current := r.collect(ctx, spec, traceID, firedAt)
		if len(current) > 0 {
			spans = current
			if len(current) != lastCount {
				lastCount = len(current)
				stableFrom = time.Now()
			} else if !stableFrom.IsZero() && time.Since(stableFrom) >= settle {
				return spans, nil
			}
		}
		sleep(poll)
	}

	if len(spans) == 0 {
		return nil, fmt.Errorf("nothing ran within %s — the trigger published but no trace appeared. Check the node id, and that its module is running", timeout)
	}
	// Timed out with spans present: judge what there is rather than reporting
	// nothing. A flow that legitimately runs longer than the timeout should
	// fail on its assertions, not on a stopwatch.
	return spans, nil
}

// collect merges the minted trace with the traces the trigger set off.
//
// Scoped by the eval's flow when it names one, which is how two evals running
// against different flows stay out of each other's results.
func (r *Runner) collect(ctx context.Context, spec evals.Spec, traceID string, firedAt time.Time) []sdktools.TraceSpanInfo {
	seen := map[string]bool{}
	var spans []sdktools.TraceSpanInfo

	if detail, err := r.Reader.ReadTraceDetail(ctx, r.Project, traceID); err == nil && len(detail) > 0 {
		seen[traceID] = true
		spans = append(spans, detail...)
	}

	lookback := time.Since(firedAt) + time.Second
	summaries, err := r.Reader.ReadTraces(ctx, r.Project, spec.Flow, lookback, 0, 50)
	if err != nil {
		return spans
	}
	// A microsecond before the trigger is still this run: the collector stamps
	// a trace when its first span starts, which can precede the publish
	// returning.
	cutoff := firedAt.Add(-time.Second).UnixMicro()
	for _, s := range summaries {
		if seen[s.ID] || s.Start < cutoff {
			continue
		}
		detail, err := r.Reader.ReadTraceDetail(ctx, r.Project, s.ID)
		if err != nil {
			continue
		}
		seen[s.ID] = true
		spans = append(spans, detail...)
	}
	return spans
}

// observe flattens a trace into what the assertions read.
func observe(spans []sdktools.TraceSpanInfo) evals.Observed {
	got := evals.Observed{Payloads: map[string][]string{}}

	for _, s := range spans {
		target := s.To
		if target == "" {
			target = s.Port
		}
		for _, e := range s.Events {
			switch e.Name {
			case "data":
				if payload, ok := e.Data["payload"]; ok && target != "" {
					got.Payloads[target] = append(got.Payloads[target], payload)
				}
			case "error", "exception":
				got.Errors++
			}
		}
		for unit, amount := range s.Usage {
			if got.Usage == nil {
				got.Usage = map[string]float64{}
			}
			got.Usage[unit] += amount
		}
	}
	return got
}

// withFreshTrace mints a trace id and puts it on the context so the run the
// trigger starts is recorded under it. The parent span is never exported —
// only its identity travels, and the module pods produce the spans.
func withFreshTrace(ctx context.Context) (context.Context, string, error) {
	var traceID oteltrace.TraceID
	var spanID oteltrace.SpanID
	if _, err := rand.Read(traceID[:]); err != nil {
		return nil, "", err
	}
	if _, err := rand.Read(spanID[:]); err != nil {
		return nil, "", err
	}

	sc := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: oteltrace.FlagsSampled,
		Remote:     true,
	})
	if !sc.IsValid() {
		return nil, "", fmt.Errorf("generated an invalid trace context")
	}

	// The propagator is what puts the id on the wire. tiny does not otherwise
	// need one, so set it here rather than depending on a global someone else
	// configured.
	if _, ok := otel.GetTextMapPropagator().(propagation.TraceContext); !ok {
		otel.SetTextMapPropagator(propagation.TraceContext{})
	}

	return oteltrace.ContextWithSpanContext(ctx, sc), traceID.String(), nil
}
