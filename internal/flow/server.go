package flow

import (
	"context"
	"encoding/json"
	"net/http"
	pprof "net/http/pprof"
	"time"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	platform "github.com/tiny-systems/platform-go"
	mcpv1 "github.com/tiny-systems/platform-go/mcp/v1"
	"google.golang.org/grpc"
)

// streamKeepalive is how often an otherwise-idle server stream writes a
// keepalive frame. A grpc-web stream runs over an HTTP/1.1 request, and Go's
// server does not read that connection while the streaming handler runs — so a
// handler that only waits (never writes) never learns the browser navigated
// away. The stream context is not cancelled, the goroutine blocks forever, and
// its half-closed socket sits in CLOSE_WAIT; enough editor visits exhaust fds
// and the server stops responding. A periodic write turns client disconnect
// into a Send error, which every stream handler treats as "client gone, return".
const streamKeepalive = 15 * time.Second

// Serve runs the FlowService as a gRPC-web endpoint on addr (e.g.
// "127.0.0.1:7775") until ctx is cancelled. The editor's Connect-ES
// createGrpcWebTransport client talks to it directly — same wire protocol the
// hosted platform serves — with CORS opened for the localhost browser.
//
// activeProject is the session's fixed project (one per session), surfaced at
// /api/session so the SPA knows which project to open without a switcher.
//
// staticFS, when non-nil, serves the editor SPA for any non-gRPC request; nil
// leaves the endpoint gRPC-web-only.
func Serve(ctx context.Context, addr string, svc *Service, activeProject string, bus *ActivityBus, staticFS http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: editorHandler(svc, activeProject, bus, staticFS), WriteTimeout: 10 * time.Minute}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
			return
		}
		errCh <- nil
	}()
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(sctx)
	case err := <-errCh:
		return err
	}
}

// editorHandler builds the single HTTP handler that fronts both the gRPC-web
// FlowService and the editor SPA. Split out from Serve so it can be exercised
// in tests without binding a port or touching a cluster (the SPA and session
// routes need neither).
func editorHandler(svc *Service, activeProject string, bus *ActivityBus, staticFS http.Handler) http.Handler {
	grpcServer := grpc.NewServer()
	platform.RegisterFlowServiceServer(grpcServer, svc)
	// The editor also reaches for project + statistics; register minimal
	// backings so those calls return empty rather than "unknown service".
	platform.RegisterProjectServiceServer(grpcServer, projectService{svc: svc})
	platform.RegisterStatisticsServiceServer(grpcServer, statisticsService{trace: svc.trace})
	// The dashboard's Activity feed streams agent tool-call events from tiny's
	// MCP server via the shared bus.
	mcpv1.RegisterWorkspaceActivityServiceServer(grpcServer, workspaceActivityService{bus: bus})
	wrapped := grpcweb.WrapServer(grpcServer)

	mux := http.NewServeMux()

	// One socket for every live stream. gRPC-web spends one HTTP connection
	// per server-stream, and a browser grants six per host across all tabs —
	// see mux.go. The editor's streams share this instead.
	mux.HandleFunc("/ws", muxServices{
		flow:     svc,
		project:  projectService{svc: svc},
		stats:    statisticsService{trace: svc.trace},
		activity: workspaceActivityService{bus: bus},
	}.handler)

	// Live goroutine/heap introspection for a wedged process. localhost-only
	// by construction (the editor server binds 127.0.0.1), and the SPA's
	// catch-all below would otherwise shadow any diagnostic path — a hang
	// with no pprof means killing the process to learn anything.
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)

	// The SPA reads the active project from here; flows themselves come from
	// the gRPC FlowService (GetFlowList), so this is the only JSON endpoint.
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"project":   activeProject,
			"namespace": svc.namespace,
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if wrapped.IsGrpcWebRequest(r) || wrapped.IsAcceptableGrpcCorsRequest(r) {
			wrapped.ServeHTTP(w, r)
			return
		}
		if staticFS != nil {
			staticFS.ServeHTTP(w, r)
			return
		}
		http.NotFound(w, r)
	})

	return mux
}

// setCORS opens the endpoint to the localhost browser. It allows the editor's
// custom request headers and — critically for gRPC-web — exposes the gRPC
// trailer headers so the client can read call status.
func setCORS(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "Content-Type, X-Grpc-Web, X-User-Agent, X-Session-Id, X-Workspace-Name, X-Workspace-ID, grpc-timeout")
	h.Set("Access-Control-Expose-Headers", "Grpc-Status, Grpc-Message, Grpc-Status-Details-Bin")
}
