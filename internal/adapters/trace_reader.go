package adapters

import (
	"context"
	"fmt"
	"time"

	sdktools "github.com/tiny-systems/module/pkg/tools"
	"github.com/tiny-systems/module/pkg/utils"

	"github.com/tiny-systems/tiny/internal/kube"
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

// ReadTraceDetail returns the full span list for a specific trace.
func (r *TraceReader) ReadTraceDetail(ctx context.Context, projectName, traceID string) ([]sdktools.TraceSpanInfo, error) {
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

// spanToInfo converts an SDK utils.Span to the tool-facing TraceSpanInfo.
// Some fields (from/to/port) live in span attributes rather than as
// first-class fields; we pull them out here.
func spanToInfo(s utils.Span) sdktools.TraceSpanInfo {
	durationMs := float64(s.EndTimeUnixNano-s.StartTimeUnixNano) / 1_000_000

	var from, to, port string
	for _, attr := range s.Attributes {
		switch attr.Key {
		case "from":
			from = attr.Value
		case "to":
			to = attr.Value
		case "port":
			port = attr.Value
		}
	}

	events := make([]sdktools.TraceEventInfo, 0, len(s.Events))
	for _, e := range s.Events {
		data := make(map[string]string, len(e.Attributes))
		for _, a := range e.Attributes {
			data[a.Key] = a.Value
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
	}
}

var _ sdktools.TraceReader = (*TraceReader)(nil)
