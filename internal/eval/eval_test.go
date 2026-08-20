package eval

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tiny-systems/module/pkg/evals"
	sdktools "github.com/tiny-systems/module/pkg/tools"
)

func spanTo(target string, payloads ...string) sdktools.TraceSpanInfo {
	s := sdktools.TraceSpanInfo{To: target}
	for _, p := range payloads {
		s.Events = append(s.Events, sdktools.TraceEventInfo{Name: "data", Data: map[string]string{"payload": p}})
	}
	return s
}

// ---------- the runner ----------

type fakeSender struct {
	node, port string
	data       []byte
	err        error
	traceSeen  string
}

func (f *fakeSender) SendSignal(ctx context.Context, _, nodeID, portName string, data []byte, traceID string) error {
	f.node, f.port, f.data, f.traceSeen = nodeID, portName, data, traceID
	return f.err
}

type fakeReader struct {
	rounds [][]sdktools.TraceSpanInfo
	calls  int
	// extra stands in for a second trace the run spilled into — what happens
	// the moment a component emits from its own goroutine.
	extra   []sdktools.TraceSpanInfo
	extraID string
}

func (f *fakeReader) ReadTraces(context.Context, string, string, time.Duration, int, int) ([]sdktools.TraceSummary, error) {
	if f.extraID == "" {
		return nil, nil
	}
	return []sdktools.TraceSummary{{ID: f.extraID, Start: time.Now().Add(time.Second).UnixMicro()}}, nil
}

func (f *fakeReader) ReadTraceDetail(_ context.Context, _, traceID string) ([]sdktools.TraceSpanInfo, error) {
	if traceID == f.extraID && f.extraID != "" {
		return f.extra, nil
	}
	if f.calls < len(f.rounds) {
		r := f.rounds[f.calls]
		f.calls++
		return r, nil
	}
	if len(f.rounds) == 0 {
		return nil, nil
	}
	return f.rounds[len(f.rounds)-1], nil
}

func TestRunFiresTheTriggerAndChecksTheRun(t *testing.T) {
	sender := &fakeSender{}
	reader := &fakeReader{rounds: [][]sdktools.TraceSpanInfo{
		{spanTo("d:in", `{"count":2}`)},
	}}
	r := &Runner{Project: "proj", Sender: sender, Reader: reader, Settle: time.Millisecond, Sleep: func(time.Duration) {}}

	result := r.Run(context.Background(), evals.Spec{
		Name:    "counts two",
		Trigger: evals.Trigger{Node: "signal-1", Port: "_control", Data: map[string]interface{}{"send": true}},
		Expect:  evals.Expect{Arrives: []evals.Arrival{{At: "d:in", Path: "$.count", Equals: 2}}},
	})

	if !result.Passed() {
		t.Fatalf("failed: %v %v", result.Err, result.Failures)
	}
	if sender.node != "signal-1" || sender.port != "_control" {
		t.Errorf("fired at %s:%s", sender.node, sender.port)
	}
	if string(sender.data) != `{"send":true}` {
		t.Errorf("payload = %s", sender.data)
	}
	if result.TraceID == "" || len(result.TraceID) != 32 {
		t.Errorf("trace id = %q, want a 32-char hex id the run can be found by", result.TraceID)
	}
}

// A trigger that never publishes is a different problem from a claim that is
// false, and must not be reported as one.
func TestATriggerThatCannotFireIsAnErrorNotAFailure(t *testing.T) {
	r := &Runner{Project: "p", Sender: &fakeSender{err: context.DeadlineExceeded}, Reader: &fakeReader{}, Sleep: func(time.Duration) {}}
	result := r.Run(context.Background(), evals.Spec{Name: "x", Trigger: evals.Trigger{Node: "n"}})
	if result.Err == nil {
		t.Fatal("a failed publish was reported as an assertion failure")
	}
	if len(result.Failures) != 0 {
		t.Errorf("failures = %v", result.Failures)
	}
}

// Nothing ran at all: say so, and say what to check. This is the most common
// first-run mistake — a node id that does not exist.
func TestNoTraceSaysWhatToCheck(t *testing.T) {
	r := &Runner{
		Project: "p", Sender: &fakeSender{}, Reader: &fakeReader{},
		Settle: time.Millisecond, Sleep: func(time.Duration) {},
	}
	result := r.Run(context.Background(), evals.Spec{
		Name: "x", Trigger: evals.Trigger{Node: "n"}, Timeout: evals.Duration(5 * time.Millisecond),
	})
	if result.Err == nil || !strings.Contains(result.Err.Error(), "node id") {
		t.Fatalf("err = %v", result.Err)
	}
}

// The run is judged once it stops growing, not after a fixed sleep — a flow
// waiting on a slow API must not be judged half-finished.
func TestRunWaitsForTheTraceToStopGrowing(t *testing.T) {
	reader := &fakeReader{rounds: [][]sdktools.TraceSpanInfo{
		{spanTo("a:in", `{}`)},
		{spanTo("a:in", `{}`), spanTo("d:in", `{"count":2}`)},
	}}
	r := &Runner{Project: "p", Sender: &fakeSender{}, Reader: reader, Settle: time.Millisecond, Sleep: func(time.Duration) {}}

	result := r.Run(context.Background(), evals.Spec{
		Name:    "waits",
		Trigger: evals.Trigger{Node: "n"},
		Expect:  evals.Expect{Arrives: []evals.Arrival{{At: "d:in", Path: "$.count", Equals: 2}}},
	})
	if !result.Passed() {
		t.Fatalf("judged before the run finished: %v %v", result.Err, result.Failures)
	}
}

// The run crosses trace boundaries as soon as a component emits from its own
// goroutine — the signal's Send does exactly that. Judging only the minted
// trace would report a working flow as broken, which is the worst failure an
// eval can have.
func TestRunCollectsTheTracesTheRunSpilledInto(t *testing.T) {
	reader := &fakeReader{
		rounds:  [][]sdktools.TraceSpanInfo{{spanTo("signal-1:_control", `{"send":true}`)}},
		extraID: "b2f95f855507166bbece50b1b1ba1eda",
		extra:   []sdktools.TraceSpanInfo{spanTo("d:in", `{"count":2}`)},
	}
	r := &Runner{Project: "p", Sender: &fakeSender{}, Reader: reader, Settle: time.Millisecond, Sleep: func(time.Duration) {}}

	result := r.Run(context.Background(), evals.Spec{
		Name:    "spills",
		Trigger: evals.Trigger{Node: "signal-1"},
		Expect:  evals.Expect{Arrives: []evals.Arrival{{At: "d:in", Path: "$.count", Equals: 2}}},
	})
	if !result.Passed() {
		t.Fatalf("the second trace was not collected: %v %v", result.Err, result.Failures)
	}
}

func TestObserveCountsErrorsAndUsage(t *testing.T) {
	spans := []sdktools.TraceSpanInfo{
		{To: "a:in", Events: []sdktools.TraceEventInfo{{Name: "error", Data: map[string]string{"exception.message": "boom"}}}},
		{To: "b:in", Usage: map[string]float64{"llm_calls": 1, "llm_input_tokens": 100}},
		{To: "c:in", Usage: map[string]float64{"llm_calls": 1}},
	}
	got := observe(spans)
	if got.Errors != 1 {
		t.Errorf("errors = %d", got.Errors)
	}
	if got.Usage["llm_calls"] != 2 || got.Usage["llm_input_tokens"] != 100 {
		t.Errorf("usage = %v, want summed across spans", got.Usage)
	}
}
