package flow

import (
	"context"
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
// staticFS, when non-nil, serves the editor SPA for any non-gRPC request
// (that's Slice 3; nil for now leaves the endpoint gRPC-web-only).
func Serve(ctx context.Context, addr string, svc *Service, staticFS http.Handler) error {
	grpcServer := grpc.NewServer()
	platform.RegisterFlowServiceServer(grpcServer, svc)

	wrapped := grpcweb.WrapServer(grpcServer)

	mux := http.NewServeMux()
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

	srv := &http.Server{Addr: addr, Handler: mux, WriteTimeout: 10 * time.Minute}
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
