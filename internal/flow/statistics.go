package flow

import (
	"context"
	"time"

	sdktools "github.com/tiny-systems/module/pkg/tools"
	"github.com/tiny-systems/module/pkg/utils"
	platform "github.com/tiny-systems/platform-go"
	"google.golang.org/grpc"
)

// traceSource reads traces from the cluster's otel-collector. Satisfied by
// *adapters.TraceReader; kept as an interface so the editor's Statistics
// service stays decoupled from the port-forwarding adapter and is testable.
type traceSource interface {
	ReadTraces(ctx context.Context, projectName, flowName string, lookback time.Duration, offset, limit int) ([]sdktools.TraceSummary, error)
	ReadTraceSpans(ctx context.Context, projectName, traceID string) ([]utils.Span, error)
}

const (
	// tracesLookback is the window the editor's trace list scans. Local dev with
	// dev-grade collector retention — a day is plenty and cheap.
	tracesLookback = 24 * time.Hour
	// tracesLimit caps a single trace-list page.
	tracesLimit = 100
)

// statisticsService serves the editor's Executions/Traces tab from the
// otel-collector through a trace reader — the same source the MCP get_traces
// tools use, so users and agents see identical data.
type statisticsService struct {
	platform.UnimplementedStatisticsServiceServer
	trace traceSource
}

// GetTraces lists recent traces for the Executions tab. Nil trace source (no
// reader wired) returns an empty list rather than erroring the panel.
func (s statisticsService) GetTraces(ctx context.Context, req *platform.StatisticsGetTracesRequest) (*platform.StatisticsGetTracesResponse, error) {
	if s.trace == nil {
		return &platform.StatisticsGetTracesResponse{}, nil
	}
	offset := int(req.Offset)
	summaries, err := s.trace.ReadTraces(ctx, req.ProjectName, req.FlowName, tracesLookback, offset, tracesLimit)
	if err != nil {
		return nil, err
	}
	traces := make([]*platform.TraceStat, 0, len(summaries))
	for _, t := range summaries {
		traces = append(traces, &platform.TraceStat{
			ID:       t.ID,
			Spans:    int64(t.Spans),
			Errors:   int64(t.Errors),
			Data:     int64(t.Data),
			Duration: t.Duration,
			Start:    t.Start,
			End:      t.End,
		})
	}
	return &platform.StatisticsGetTracesResponse{
		Traces: traces,
		Total:  int64(offset + len(traces)),
		Offset: int64(offset),
	}, nil
}

// GetTraceByID returns the span waterfall for one trace.
func (s statisticsService) GetTraceByID(ctx context.Context, req *platform.StatisticsGetTraceByIDRequest) (*platform.StatisticsGetTraceByIDResponse, error) {
	resp := &platform.StatisticsGetTraceByIDResponse{TraceID: req.TraceID}
	if s.trace == nil {
		return resp, nil
	}
	spans, err := s.trace.ReadTraceSpans(ctx, req.ProjectName, req.TraceID)
	if err != nil {
		return nil, err
	}
	resp.Spans = make([]*platform.TraceSpan, 0, len(spans))
	for _, sp := range spans {
		resp.Spans = append(resp.Spans, spanToTraceSpan(sp))
	}
	return resp, nil
}

// loadTraceStats reads one trace's spans and extracts the per-port/per-edge
// statistics the canvas overlays as latency, error markers, and execution
// order. Mirrors the platform's loadTraceData (flow/inspect-node.go): the SDK's
// ExtractTraceStatistics does the work, identical to the hosted product.
//
// Called ONCE per GetFlowStream subscription — req.TraceID is fixed for the
// stream's life (the editor re-subscribes when the selection changes), so the
// shared otel forwarder is touched a single time here, never on the per-event
// render path. Reading it per render through that one forwarder is exactly what
// wedged the canvas before, which is why the overlay was removed until now.
//
// Returns nil (no overlay) when no trace is selected, no reader is wired, the
// read fails, or the trace has no spans — all degrade to the plain graph.
func (s *Service) loadTraceStats(ctx context.Context, projectName, traceID string) *utils.TraceStatistics {
	if traceID == "" || s.trace == nil {
		return nil
	}
	spans, err := s.trace.ReadTraceSpans(ctx, projectName, traceID)
	if err != nil || len(spans) == 0 {
		return nil
	}
	stat, _ := utils.ExtractTraceStatistics(&utils.TraceData{TraceID: traceID, Spans: spans})
	return stat
}

// GetStream is the live telemetry channel. It holds the stream open and sends
// nothing.
//
// It briefly polled the otel-collector on a 2s ticker so the editor's trace
// list would refresh itself. That shipped in v0.4.12 and caused intermittent
// hangs: the collector is reached through ONE shared port-forwarder, which the
// flow-render path also uses, so a slow read there stalls every trace-dependent
// request at once — the editor simply stops responding, then recovers.
//
// Reverted to sending nothing until liveness can be done without periodic load
// on that shared forwarder (piggyback the flow stream, or watch rather than
// poll). Holding the stream open rather than closing it keeps the editor
// showing "live" instead of a connection error; the trace list falls back to
// its manual refresh.
func (s statisticsService) GetStream(_ *platform.StatisticsStreamRequest, stream grpc.ServerStreamingServer[platform.StatisticsStreamResponse]) error {
	<-stream.Context().Done()
	return nil
}

// spanToTraceSpan maps an SDK span to the editor's TraceSpan. ParentSpanID and
// status aren't first-class on utils.Span — they ride in attributes — so we
// lift them out while still passing every attribute through for the detail view.
func spanToTraceSpan(s utils.Span) *platform.TraceSpan {
	attrs := make([]*platform.TraceSpanAttribute, 0, len(s.Attributes))
	var parent, statusStr string
	for _, a := range s.Attributes {
		switch a.Key {
		case "parent_span_id":
			parent = a.Value
		case "status":
			statusStr = a.Value
		}
		attrs = append(attrs, &platform.TraceSpanAttribute{Key: a.Key, Value: a.Value})
	}
	events := make([]*platform.TraceSpanEvent, 0, len(s.Events))
	for _, e := range s.Events {
		evAttrs := make([]*platform.TraceSpanAttribute, 0, len(e.Attributes))
		for _, a := range e.Attributes {
			evAttrs = append(evAttrs, &platform.TraceSpanAttribute{Key: a.Key, Value: a.Value})
		}
		events = append(events, &platform.TraceSpanEvent{Name: e.Name, Attributes: evAttrs})
	}
	return &platform.TraceSpan{
		SpanID:            s.SpanID,
		ParentSpanID:      parent,
		Name:              s.Name,
		StartTimeUnixNano: s.StartTimeUnixNano,
		EndTimeUnixNano:   s.EndTimeUnixNano,
		Attributes:        attrs,
		Events:            events,
		Status:            statusStr,
	}
}
