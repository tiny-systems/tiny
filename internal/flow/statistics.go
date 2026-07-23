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

// GetStream drives the editor's live telemetry: the trace list reloads on every
// event it receives (debounced), and the events themselves plot the flow's
// throughput.
//
// It used to return immediately. That single line is why the Traces list never
// updated without a manual refresh and why the chart read "No data" — the
// editor opened the stream, got EOF, and nothing ever arrived.
//
// Polls rather than watches because the otel-collector exposes no watch: it
// samples the trace count each tick and emits when the flow has run, which is
// exactly the signal the editor needs to refetch.
func (s statisticsService) GetStream(req *platform.StatisticsStreamRequest, stream grpc.ServerStreamingServer[platform.StatisticsStreamResponse]) error {
	ctx := stream.Context()
	if s.trace == nil {
		<-ctx.Done() // hold it open so the editor shows "live", not an error
		return nil
	}

	const (
		interval = 2 * time.Second
		window   = tracesLookback
	)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Emit only on change, so an idle flow costs one collector read per tick and
	// never churns the editor's list.
	last := -1
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			traces, err := s.trace.ReadTraces(ctx, req.ProjectName, req.FlowName, window, 0, tracesLimit)
			if err != nil {
				continue // a transient collector blip must not kill the stream
			}
			if len(traces) == last {
				continue
			}
			last = len(traces)
			if err := stream.Send(&platform.StatisticsStreamResponse{
				Events: []*platform.StatsEvent{{
					Metric:   "traces",
					Value:    float64(len(traces)),
					Datetime: time.Now().UnixMilli(),
				}},
			}); err != nil {
				return err
			}
		}
	}
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
