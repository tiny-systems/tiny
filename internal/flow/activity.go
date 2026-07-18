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

// Publish records an event (kind = e.g. "tool.call.started") and fans it out to
// live subscribers. Never blocks: a slow subscriber just drops the event.
func (b *ActivityBus) Publish(kind string) {
	if b == nil {
		return
	}
	evt := mcpv1.WorkspaceActivityEvent{At: time.Now().Format(time.RFC3339), Kind: kind}
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
