package flow

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/tiny-systems/tiny/internal/adapters"
	"github.com/tiny-systems/tiny/internal/kube"

	platform "github.com/tiny-systems/platform-go"
)

// TestStatisticsLive proves the traces bridge end to end: it builds the real
// otel-collector trace reader against a live cluster, wires it into the editor's
// statisticsService, and calls GetTraces (+ GetTraceByID on the first result).
// Gated by env:
//
//	TINY_TEST_CONTEXT=minikube TINY_TEST_NS=tinysystems TINY_TEST_PROJECT=playground \
//	go test ./internal/flow -run StatisticsLive -v
func TestStatisticsLive(t *testing.T) {
	kctx := os.Getenv("TINY_TEST_CONTEXT")
	if kctx == "" {
		t.Skip("set TINY_TEST_CONTEXT to run the live statistics test")
	}
	ns := os.Getenv("TINY_TEST_NS")
	if ns == "" {
		ns = "tinysystems"
	}
	project := os.Getenv("TINY_TEST_PROJECT")

	cfg, err := kube.RestConfig(kctx)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}
	kc, err := kube.NewClientFromConfig(cfg, ns)
	if err != nil {
		t.Fatalf("kube client: %v", err)
	}
	reader, err := adapters.NewTraceReader(adapters.TraceReaderOptions{
		KubeClient:  kc,
		ServiceName: "tinysystems-otel-collector",
		ServicePort: 2345,
	})
	if err != nil {
		t.Fatalf("trace reader: %v", err)
	}
	defer reader.Close()

	svc := statisticsService{trace: reader}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := svc.GetTraces(ctx, &platform.StatisticsGetTracesRequest{ProjectName: project})
	if err != nil {
		t.Fatalf("GetTraces: %v", err)
	}
	t.Logf("GetTraces returned %d trace(s) (total=%d)", len(resp.Traces), resp.Total)

	if len(resp.Traces) == 0 {
		t.Skip("no traces in the collector yet — run a flow first to generate some")
	}

	first := resp.Traces[0]
	t.Logf("first trace: id=%s spans=%d errors=%d duration=%dns", first.ID, first.Spans, first.Errors, first.Duration)

	detail, err := svc.GetTraceByID(ctx, &platform.StatisticsGetTraceByIDRequest{
		ProjectName: project,
		TraceID:     first.ID,
	})
	if err != nil {
		t.Fatalf("GetTraceByID: %v", err)
	}
	if len(detail.Spans) == 0 {
		t.Fatalf("trace %s reported %d spans but GetTraceByID returned none", first.ID, first.Spans)
	}
	sp := detail.Spans[0]
	t.Logf("first span: name=%q start=%d end=%d attrs=%d", sp.Name, sp.StartTimeUnixNano, sp.EndTimeUnixNano, len(sp.Attributes))
	if sp.StartTimeUnixNano == 0 || sp.EndTimeUnixNano == 0 {
		t.Errorf("span timing missing — waterfall would be flat (start=%d end=%d)", sp.StartTimeUnixNano, sp.EndTimeUnixNano)
	}
}
