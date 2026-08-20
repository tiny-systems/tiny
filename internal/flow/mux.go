package flow

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	platform "github.com/tiny-systems/platform-go"
	mcpv1 "github.com/tiny-systems/platform-go/mcp/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

// One socket for every live stream.
//
// The editor keeps three server-streams open per page — the project (or
// flow) stream, statistics, and the activity feed — and gRPC-web runs each
// on its own HTTP request. Served over HTTP/1.1, which is what a browser
// gets on plain http, that costs three of the SIX connections a browser
// allows PER HOST across every tab. Two open tabs exhausted the pool
// exactly, and every request afterwards — including the one carrying widget
// data — queued forever: the dashboard rendered empty cards and the page
// looked frozen.
//
// A WebSocket is one connection and is not subject to that cap, so all the
// streams ride it together, addressed by a caller-chosen id. Events are the
// same protobuf messages the gRPC handlers already produce, base64-framed in
// JSON so a text frame carries them without a bespoke binary format; the
// browser decodes each with the generated message class and receives exactly
// what gRPC-web would have handed it.
//
// This is a tiny-side transport, not a protocol change: the same service
// methods serve both paths, and the hosted platform is untouched.

// muxUpgrader accepts only same-origin upgrades; tiny binds to localhost and
// serves one user, but a browser will happily connect a page from anywhere
// to a local socket, so the check stays.
var muxUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // non-browser client (tests, curl)
		}
		return origin == "http://"+r.Host || origin == "https://"+r.Host
	},
}

// muxRequest is what the browser sends: open a stream under `id`, or cancel
// one already open.
type muxRequest struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Req    json.RawMessage `json:"req"`
	Cancel bool            `json:"cancel"`
}

// muxFrame is what comes back. Exactly one of Event / Error / End is set.
type muxFrame struct {
	ID    string `json:"id"`
	Event string `json:"event,omitempty"` // base64 protobuf
	Error string `json:"error,omitempty"`
	End   bool   `json:"end,omitempty"`
}

// muxConn serialises writes: gorilla allows one writer at a time and several
// streams share this socket.
type muxConn struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

// muxWriteWait bounds ONE frame write. Writes are serialised on this socket, so
// a browser that stops reading — a suspended tab, a machine asleep — would
// otherwise block the sending goroutine forever while holding the lock, and
// every other stream sharing the socket stalls behind it. The deadline turns
// that into an error the stream can end on. (http.Server's WriteTimeout does
// not apply here: net/http clears the connection's deadlines when the
// WebSocket upgrade hijacks it.)
const muxWriteWait = 30 * time.Second

func (c *muxConn) send(f muxFrame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(muxWriteWait)); err != nil {
		return err
	}
	return c.conn.WriteJSON(f)
}

// muxStream adapts a multiplexed subscription to the grpc.ServerStreamingServer
// interface the existing handlers expect. Only Send and Context are ever
// called on a server stream; the embedded interface satisfies the rest of the
// signature without pretending to implement it.
type muxStream[T proto.Message] struct {
	grpc.ServerStream
	ctx  context.Context
	id   string
	conn *muxConn
}

func (s *muxStream[T]) Context() context.Context { return s.ctx }

func (s *muxStream[T]) Send(msg T) error {
	raw, err := proto.Marshal(msg)
	if err != nil {
		return err
	}
	return s.conn.send(muxFrame{ID: s.id, Event: base64.StdEncoding.EncodeToString(raw)})
}

// muxServices is the set of streaming backends the socket can address — the
// same values registered on the gRPC server, so both transports serve
// identical handlers.
type muxServices struct {
	flow     *Service
	project  projectService
	stats    statisticsService
	activity workspaceActivityService
}

// handler serves the socket. Each subscription runs in its own goroutine so a
// slow stream never blocks its neighbours.
func (s muxServices) handler(w http.ResponseWriter, r *http.Request) {
	conn, err := muxUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return // Upgrade already answered the request
	}
	defer func() { _ = conn.Close() }()

	mc := &muxConn{conn: conn}
	ctx, cancelAll := context.WithCancel(r.Context())
	defer cancelAll()

	var mu sync.Mutex
	cancels := map[string]context.CancelFunc{}

	for {
		var req muxRequest
		if err := conn.ReadJSON(&req); err != nil {
			return // client went away
		}
		if req.ID == "" {
			continue
		}

		if req.Cancel {
			mu.Lock()
			if cancel, ok := cancels[req.ID]; ok {
				cancel()
				delete(cancels, req.ID)
			}
			mu.Unlock()
			continue
		}

		streamCtx, cancel := context.WithCancel(ctx)
		mu.Lock()
		if prev, ok := cancels[req.ID]; ok {
			prev() // a re-subscribe on the same id replaces the old stream
		}
		cancels[req.ID] = cancel
		mu.Unlock()

		go func(req muxRequest, streamCtx context.Context) {
			err := s.serve(streamCtx, mc, req)
			mu.Lock()
			delete(cancels, req.ID)
			mu.Unlock()
			if streamCtx.Err() != nil {
				return // cancelled by the client; it isn't waiting for a reply
			}
			if err != nil {
				_ = mc.send(muxFrame{ID: req.ID, Error: err.Error()})
				return
			}
			_ = mc.send(muxFrame{ID: req.ID, End: true})
		}(req, streamCtx)
	}
}

// serve dispatches one subscription to the service method that backs it. The
// handlers are the same ones gRPC-web calls — this only changes how their
// messages reach the browser.
func (s muxServices) serve(ctx context.Context, mc *muxConn, req muxRequest) error {
	switch req.Kind {
	case "project.getStream":
		var in platform.GetProjectStreamRequest
		if err := unmarshalMuxReq(req.Req, &in); err != nil {
			return err
		}
		return s.project.GetStream(&in, &muxStream[*platform.GetProjectStreamEvent]{ctx: ctx, id: req.ID, conn: mc})

	case "flow.getFlowStream":
		var in platform.GetFlowStreamRequest
		if err := unmarshalMuxReq(req.Req, &in); err != nil {
			return err
		}
		return s.flow.GetFlowStream(&in, &muxStream[*platform.GetFlowStreamResponse]{ctx: ctx, id: req.ID, conn: mc})

	case "statistics.getStream":
		var in platform.StatisticsStreamRequest
		if err := unmarshalMuxReq(req.Req, &in); err != nil {
			return err
		}
		return s.stats.GetStream(&in, &muxStream[*platform.StatisticsStreamResponse]{ctx: ctx, id: req.ID, conn: mc})

	case "activity.watch":
		var in mcpv1.WatchWorkspaceActivityRequest
		if err := unmarshalMuxReq(req.Req, &in); err != nil {
			return err
		}
		return s.activity.Watch(&in, &muxStream[*mcpv1.WorkspaceActivityEvent]{ctx: ctx, id: req.ID, conn: mc})
	}

	log.Debug().Str("kind", req.Kind).Msg("mux: unknown stream kind")
	return errUnknownMuxKind(req.Kind)
}

// unmarshalMuxReq accepts the browser's JSON request. Field names come from
// the generated types, so the same object the gRPC-web client would send
// decodes here unchanged.
func unmarshalMuxReq(raw json.RawMessage, out interface{}) error {
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

type muxKindError string

func (e muxKindError) Error() string { return "unknown stream kind: " + string(e) }

func errUnknownMuxKind(kind string) error { return muxKindError(kind) }
