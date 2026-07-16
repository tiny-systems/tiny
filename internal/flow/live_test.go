package flow

import (
	"context"
	"os"
	"testing"
	"time"

	platform "github.com/tiny-systems/platform-go"
	"google.golang.org/grpc"

	"github.com/tiny-systems/tiny/internal/kube"
)

// mockStream captures what GetFlowStream sends. The embedded (nil)
// grpc.ServerStream supplies the interface methods GetFlowStream never calls;
// only Send + Context are exercised.
type mockStream struct {
	grpc.ServerStream
	ctx    context.Context
	events []*platform.GetFlowStreamResponse
}

func (m *mockStream) Send(r *platform.GetFlowStreamResponse) error {
	m.events = append(m.events, r)
	return nil
}
func (m *mockStream) Context() context.Context { return m.ctx }

// TestFlowServiceLive drives GetFlow + GetFlowStream against a real cluster.
// Gated by env so it only runs when pointed at a provisioned kind cluster:
//
//	TINY_TEST_CONTEXT=kind-... TINY_TEST_NS=tinysystems \
//	TINY_TEST_PROJECT=<proj> TINY_TEST_FLOW=<flow> go test ./internal/flow -run Live -v
func TestFlowServiceLive(t *testing.T) {
	kctx := os.Getenv("TINY_TEST_CONTEXT")
	if kctx == "" {
		t.Skip("set TINY_TEST_CONTEXT to run the live flow test")
	}
	ns := os.Getenv("TINY_TEST_NS")
	project := os.Getenv("TINY_TEST_PROJECT")
	flowName := os.Getenv("TINY_TEST_FLOW")

	cfg, err := kube.RestConfig(kctx)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}
	svc := NewService(cfg, ns)

	gf, err := svc.GetFlow(context.Background(), &platform.GetFlowRequest{ProjectName: project, FlowName: flowName})
	if err != nil {
		t.Fatalf("GetFlow: %v", err)
	}
	t.Logf("GetFlow -> ResourceName=%q Name=%q", gf.Flow.GetResourceName(), gf.Flow.GetName())

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ms := &mockStream{ctx: ctx}
	_ = svc.GetFlowStream(&platform.GetFlowStreamRequest{ProjectName: project, FlowName: flowName}, ms)

	var elements, ticks int
	for _, resp := range ms.events {
		for _, e := range resp.Events {
			if e.Type == "TICK" {
				ticks++
				continue
			}
			elements++
			if elements <= 6 {
				g := e.Graph
				if len(g) > 220 {
					g = g[:220]
				}
				t.Logf("[%s] %s: %s", e.Type, e.ID, string(g))
			}
		}
	}
	t.Logf("stream produced %d element events, %d ticks", elements, ticks)
	if elements == 0 {
		t.Errorf("expected node/edge events from the flow, got none")
	}
}
