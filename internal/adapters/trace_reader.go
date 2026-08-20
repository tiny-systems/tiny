package adapters

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	m "github.com/tiny-systems/module/module"
	"github.com/tiny-systems/module/pkg/redact"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"github.com/tiny-systems/module/pkg/utils"

	"github.com/tiny-systems/tiny/internal/kube"
)

const (
	// fullTraceIDLen is a complete trace ID: 16 bytes, hex-encoded.
	fullTraceIDLen = 32
	// tracePrefixLookback bounds the trace-list scan used to resolve a
	// truncated trace ID — same window the editor's trace list shows.
	tracePrefixLookback = 24 * time.Hour
)

// TraceReaderOptions configures how the reader reaches the otel-collector.
type TraceReaderOptions struct {
	KubeClient  *kube.Client
	ServiceName string // e.g. "tinysystems-otel-collector"
	ServicePort int    // e.g. 2345
}

// TraceReader implements sdktools.TraceReader by port-forwarding to the
// in-cluster otel-collector and querying its gRPC statistics service.
//
// The SDK already provides the full client (utils.TraceService) — this
// adapter just feeds it a PortForwarder and converts the SDK's
// trace-shaped responses to the tool-facing types.
type TraceReader struct {
	svc         *utils.TraceService
	portForward *kube.PortForwarder
	namespace   string
}

func NewTraceReader(opts TraceReaderOptions) (*TraceReader, error) {
	if opts.KubeClient == nil {
		return nil, fmt.Errorf("kube client required")
	}

	pf := kube.NewPortForwarder(opts.KubeClient)
	svc := utils.NewTraceService(utils.TraceServiceConfig{
		Client:      pf,
		OtelService: opts.ServiceName,
		OtelPort:    opts.ServicePort,
	})

	return &TraceReader{
		svc:         svc,
		portForward: pf,
		namespace:   opts.KubeClient.Namespace,
	}, nil
}

// Close releases port-forward connections.
func (r *TraceReader) Close() {
	if r.svc != nil {
		_ = r.svc.Close()
	}
	if r.portForward != nil {
		r.portForward.Close()
	}
}

// ReadTraces returns traces for the given project/flow within the lookback window.
func (r *TraceReader) ReadTraces(ctx context.Context, projectName, flowName string, lookback time.Duration, offset, limit int) ([]sdktools.TraceSummary, error) {
	end := time.Now()
	start := end.Add(-lookback)

	resp, err := r.svc.GetTraces(ctx, r.namespace, projectName, flowName, start, end, int64(offset))
	if err != nil {
		return nil, fmt.Errorf("read traces: %w", err)
	}
	if resp == nil {
		return nil, nil
	}

	out := make([]sdktools.TraceSummary, 0, len(resp.Traces))
	for _, t := range resp.Traces {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, sdktools.TraceSummary{
			ID:       t.ID,
			Spans:    int(t.Spans),
			Errors:   int(t.Errors),
			Data:     int(t.Data),
			Duration: t.Duration,
			Start:    t.Start,
			End:      t.End,
		})
	}
	return out, nil
}

// resolveTraceID normalizes a trace ID and, when it is shorter than a full
// 32-char ID, resolves it against the recent trace list — git-style unique
// prefix. The collector's by-ID lookup is an exact map hit, so a truncated
// ID NotFounds even when the trace is sitting right there in the list.
// That's a real failure mode, observed live: agents and UIs shorten IDs for
// display (`id[:16]` and the like), the short form gets pasted back into
// get_trace_detail / scenarios(trace_id), and the bare "trace not found"
// gives no hint the ID was merely truncated.
func (r *TraceReader) resolveTraceID(ctx context.Context, projectName, traceID string) (string, error) {
	traceID = strings.ToLower(strings.TrimSpace(traceID))
	if traceID == "" || len(traceID) >= fullTraceIDLen {
		return traceID, nil
	}
	end := time.Now()
	resp, err := r.svc.GetTraces(ctx, r.namespace, projectName, "", end.Add(-tracePrefixLookback), end, 0)
	if err != nil {
		return "", fmt.Errorf("resolve trace id %s: %w", traceID, err)
	}
	var ids []string
	if resp != nil {
		for _, t := range resp.Traces {
			ids = append(ids, t.ID)
		}
	}
	return matchTraceIDPrefix(ids, traceID)
}

// matchTraceIDPrefix picks the single ID matching prefix. Pure so the
// resolution rules unit-test without a live collector.
func matchTraceIDPrefix(ids []string, prefix string) (string, error) {
	var matches []string
	for _, id := range ids {
		if strings.HasPrefix(id, prefix) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("trace %s not found — it looks truncated (full ids are %d chars) and no recent trace matches this prefix; pass the id exactly as get_traces returns it", prefix, fullTraceIDLen)
	default:
		sort.Strings(matches)
		show := matches
		if len(show) > 3 {
			show = show[:3]
		}
		return "", fmt.Errorf("trace id %s is ambiguous — %d recent traces share that prefix (%s); pass the full %d-char id", prefix, len(matches), strings.Join(show, ", "), fullTraceIDLen)
	}
}

// ReadTraceDetail returns the full span list for a specific trace.
func (r *TraceReader) ReadTraceDetail(ctx context.Context, projectName, traceID string) ([]sdktools.TraceSpanInfo, error) {
	traceID, err := r.resolveTraceID(ctx, projectName, traceID)
	if err != nil {
		return nil, err
	}
	trace, err := r.svc.GetTraceByID(ctx, r.namespace, projectName, traceID)
	if err != nil {
		return nil, fmt.Errorf("get trace %s: %w", traceID, err)
	}
	if trace == nil {
		return nil, nil
	}

	out := make([]sdktools.TraceSpanInfo, 0, len(trace.Spans))
	for _, s := range trace.Spans {
		out = append(out, spanToInfo(s))
	}
	return out, nil
}

// ReadTraceSpans returns the trace's raw spans (with absolute timing and
// attributes intact), for callers that render a waterfall rather than the
// tool-facing summary. The editor's Statistics service uses this so span
// start/end times survive the mapping.
func (r *TraceReader) ReadTraceSpans(ctx context.Context, projectName, traceID string) ([]utils.Span, error) {
	traceID, err := r.resolveTraceID(ctx, projectName, traceID)
	if err != nil {
		return nil, err
	}
	trace, err := r.svc.GetTraceByID(ctx, r.namespace, projectName, traceID)
	if err != nil {
		return nil, fmt.Errorf("get trace %s: %w", traceID, err)
	}
	if trace == nil {
		return nil, nil
	}
	return trace.Spans, nil
}

// spanToInfo converts an SDK utils.Span to the tool-facing TraceSpanInfo.
// Some fields (from/to/port) live in span attributes rather than as
// first-class fields; we pull them out here.
func spanToInfo(s utils.Span) sdktools.TraceSpanInfo {
	durationMs := float64(s.EndTimeUnixNano-s.StartTimeUnixNano) / 1_000_000

	var from, to, port string
	var usage map[string]float64
	for _, attr := range s.Attributes {
		switch attr.Key {
		case "from":
			from = attr.Value
		case "to":
			to = attr.Value
		case "port":
			port = attr.Value
		default:
			// Metered work: the unit is whatever the component named, and is
			// carried through untouched. A unit this reader has never seen
			// still totals correctly.
			unit, amount, ok := usageAttr(attr)
			if !ok {
				continue
			}
			if usage == nil {
				usage = map[string]float64{}
			}
			usage[unit] += amount
		}
	}

	events := make([]sdktools.TraceEventInfo, 0, len(s.Events))
	for _, e := range s.Events {
		data := make(map[string]string, len(e.Attributes))
		for _, a := range e.Attributes {
			// Masked again on the way out, not only on the way in. The
			// runtime redacts a payload before it reaches a span, but a
			// collector holds spans written by whatever module version
			// produced them — including ones released before that existed.
			// This is the surface an agent reads, so it is the one that must
			// not hand over a key regardless of what is in storage.
			value, _ := redact.TextByShape(a.Value)
			data[a.Key] = value
		}
		events = append(events, sdktools.TraceEventInfo{
			Name: e.Name,
			Data: data,
		})
	}

	return sdktools.TraceSpanInfo{
		SpanID:     s.SpanID,
		Name:       s.Name,
		From:       from,
		To:         to,
		Port:       port,
		DurationMs: durationMs,
		Events:     events,
		Usage:      usage,
	}
}

// usageAttr recognises a metered unit and reads its amount.
//
// The value arrives as a string because every span attribute does; a unit whose
// amount cannot be parsed is skipped rather than counted as zero, since a wrong
// total is worse than a missing one.
func usageAttr(attr utils.SpanAttribute) (unit string, amount float64, ok bool) {
	if !strings.HasPrefix(attr.Key, m.UsageAttrPrefix) {
		return "", 0, false
	}
	unit = strings.TrimPrefix(attr.Key, m.UsageAttrPrefix)
	if unit == "" {
		return "", 0, false
	}
	amount, err := strconv.ParseFloat(attr.Value, 64)
	if err != nil {
		return "", 0, false
	}
	return unit, amount, true
}

var _ sdktools.TraceReader = (*TraceReader)(nil)
