package flow

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/improbable-eng/grpc-web/go/grpcweb"
	platform "github.com/tiny-systems/platform-go"
	"google.golang.org/grpc"
)

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
func Serve(ctx context.Context, addr string, svc *Service, activeProject string, staticFS http.Handler) error {
	srv := &http.Server{Addr: addr, Handler: editorHandler(svc, activeProject, staticFS), WriteTimeout: 10 * time.Minute}
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
func editorHandler(svc *Service, activeProject string, staticFS http.Handler) http.Handler {
	grpcServer := grpc.NewServer()
	platform.RegisterFlowServiceServer(grpcServer, svc)
	// The editor also reaches for project + statistics; register minimal
	// backings so those calls return empty rather than "unknown service".
	platform.RegisterProjectServiceServer(grpcServer, projectService{svc: svc})
	platform.RegisterStatisticsServiceServer(grpcServer, statisticsService{})
	wrapped := grpcweb.WrapServer(grpcServer)

	mux := http.NewServeMux()

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
