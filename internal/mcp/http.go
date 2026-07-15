package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPTransport serves MCP JSON-RPC over plain HTTP POST. Each POST
// to /mcp carries one JSON-RPC frame in the body and gets one frame
// back in the response (or 204 for notifications).
//
// This is what the desktop client embeds — a localhost-only listener
// the local Claude Desktop / Cursor process connects to instead of
// spawning a separate mcp-server binary over stdio. Same Server
// underneath: same registry, same execution context, same tool
// dispatch.
//
// Streaming-HTTP / SSE for server-initiated notifications is a
// future addition. Today's tool set is pure request/response so
// POST-only covers the spec's required transport.
type HTTPTransport struct {
	server *Server
	addr   string

	// CORS allow-origin for the response header. Defaults to a wildcard;
	// the desktop client should override to the actual MCP-host origin
	// once embedded (avoids letting random web pages prompt the local
	// MCP endpoint).
	AllowOrigin string

	// ReadTimeout / WriteTimeout cap per-request I/O. Defaults are
	// loose because tools/call can run for minutes (install_module,
	// build_flow). Override per-deployment as needed.
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
}

// NewHTTPTransport returns a transport that listens on addr (e.g.
// "127.0.0.1:9876") and dispatches MCP frames to server. Defaults
// to localhost-only — the embedded use case never wants to expose
// the endpoint beyond the host machine.
func NewHTTPTransport(server *Server, addr string) *HTTPTransport {
	return &HTTPTransport{
		server:       server,
		addr:         addr,
		AllowOrigin:  "*",
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Minute,
	}
}

// Run starts the HTTP listener and blocks until ctx is cancelled or
// the server errors. On cancel the server shuts down gracefully with
// a 5-second drain window.
func (t *HTTPTransport) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", t.handleMCP)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Addr:         t.addr,
		Handler:      mux,
		ReadTimeout:  t.ReadTimeout,
		WriteTimeout: t.WriteTimeout,
	}

	errCh := make(chan error, 1)
	go func() {
		err := srv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (t *HTTPTransport) handleMCP(w http.ResponseWriter, r *http.Request) {
	if t.AllowOrigin != "" {
		w.Header().Set("Access-Control-Allow-Origin", t.AllowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "content-type, mcp-session-id")
	}
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4*1024*1024))
	if err != nil {
		http.Error(w, fmt.Sprintf("read body: %v", err), http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	response, err := t.server.HandleFrame(r.Context(), body)
	if err != nil {
		http.Error(w, fmt.Sprintf("handle frame: %v", err), http.StatusInternalServerError)
		return
	}
	if response == nil {
		// Notification — JSON-RPC says no response body. MCP HTTP
		// transport guidance is 204 No Content.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(response)
}
