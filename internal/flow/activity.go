package flow

import (
	"sync"
	"time"

	mcpv1 "github.com/tiny-systems/platform-go/mcp/v1"
	"google.golang.org/grpc"
)

// ActivityBus is a tiny in-process pub/sub for agent activity. tiny's MCP
// server publishes an event on every tool call; the editor's
// WorkspaceActivityService streams them to the dashboard's Activity feed. It
// keeps a small backlog so a feed that connects mid-session sees recent events.
type ActivityBus struct {
	mu     sync.Mutex
	subs   map[chan mcpv1.WorkspaceActivityEvent]struct{}
	recent []mcpv1.WorkspaceActivityEvent
}

const activityBacklog = 100

func NewActivityBus() *ActivityBus {
	return &ActivityBus{subs: map[chan mcpv1.WorkspaceActivityEvent]struct{}{}}
}

// PublishToolStarted announces a tool call beginning.
func (b *ActivityBus) PublishToolStarted(tool string) {
	b.publish(mcpv1.WorkspaceActivityEvent{
		Kind: "tool.call.started",
		Payload: &mcpv1.WorkspaceActivityEvent_ToolCallStarted{
			ToolCallStarted: &mcpv1.ToolCallStartedPayload{Tool: tool},
		},
	})
}

// PublishToolEnded announces a tool call finishing, WITH its outcome.
//
// The payload matters: the feed reads success/tool off it, so publishing a bare
// kind made every finished call render as an unnamed "tool failed" — the events
// were fine, the verdict was missing.
func (b *ActivityBus) PublishToolEnded(tool string, success bool, errMsg string, duration time.Duration) {
	b.publish(mcpv1.WorkspaceActivityEvent{
		Kind: "tool.call.ended",
		Payload: &mcpv1.WorkspaceActivityEvent_ToolCallEnded{
			ToolCallEnded: &mcpv1.ToolCallEndedPayload{
				Tool:       tool,
				Success:    success,
				Error:      errMsg,
				DurationMs: duration.Milliseconds(),
			},
		},
	})
}

// Publish records a bare event by kind, for activity that carries no payload.
func (b *ActivityBus) Publish(kind string) {
	b.publish(mcpv1.WorkspaceActivityEvent{Kind: kind})
}

// publish stamps the event and fans it out to live subscribers. Never blocks: a
// slow subscriber just drops the event.
func (b *ActivityBus) publish(evt mcpv1.WorkspaceActivityEvent) {
	if b == nil {
		return
	}
	evt.At = time.Now().Format(time.RFC3339)
	b.mu.Lock()
	b.recent = append(b.recent, evt)
	if len(b.recent) > activityBacklog {
		b.recent = b.recent[len(b.recent)-activityBacklog:]
	}
	for ch := range b.subs {
		select {
		case ch <- evt:
		default:
		}
	}
	b.mu.Unlock()
}

func (b *ActivityBus) subscribe() (chan mcpv1.WorkspaceActivityEvent, []mcpv1.WorkspaceActivityEvent, func()) {
	ch := make(chan mcpv1.WorkspaceActivityEvent, 64)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	backlog := append([]mcpv1.WorkspaceActivityEvent(nil), b.recent...)
	b.mu.Unlock()
	return ch, backlog, func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}
}

// workspaceActivityService streams the ActivityBus to the editor's Activity
// feed. If bus is nil (no MCP activity source) it just holds the stream open,
// so the feed shows "Live / waiting" rather than erroring.
type workspaceActivityService struct {
	mcpv1.UnimplementedWorkspaceActivityServiceServer
	bus *ActivityBus
}

func (w workspaceActivityService) Watch(_ *mcpv1.WatchWorkspaceActivityRequest, stream grpc.ServerStreamingServer[mcpv1.WorkspaceActivityEvent]) error {
	ctx := stream.Context()
	if w.bus == nil {
		<-ctx.Done()
		return nil
	}
	ch, backlog, cancel := w.bus.subscribe()
	defer cancel()

	for i := range backlog {
		if err := stream.Send(&backlog[i]); err != nil {
			return err
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case evt := <-ch:
			e := evt
			if err := stream.Send(&e); err != nil {
				return err
			}
		}
	}
}
