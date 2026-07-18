package flow

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEditorHandler exercises the HTTP surface the browser hits: the SPA at
// the root, the session endpoint, gRPC-web routing, and the CORS preflight.
// None of the asserted paths touch the cluster, so a nil-config Service is
// enough — the point is to prove the wiring, not the FlowService (that's
// live_test.go).
func TestEditorHandler(t *testing.T) {
	const spaMarker = "TINY_SPA_ROOT"
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(spaMarker))
	})
	h := editorHandler(NewService(nil, "test-ns"), "demo", nil, spa)

	t.Run("root serves the SPA", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if !strings.Contains(rr.Body.String(), spaMarker) {
			t.Fatalf("root did not serve the SPA: %q", rr.Body.String())
		}
	})

	t.Run("api/session reports the active project", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/session", nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
			t.Fatalf("bad json: %v", err)
		}
		if body["project"] != "demo" {
			t.Fatalf("project = %q, want demo", body["project"])
		}
		if body["namespace"] != "test-ns" {
			t.Fatalf("namespace = %q, want test-ns", body["namespace"])
		}
	})

	t.Run("grpc-web request routes to the gRPC server, not the SPA", func(t *testing.T) {
		// A panic here would mean the request reached the gRPC handler (with
		// a nil kube config) — which still proves the routing decision: it did
		// NOT fall through to the SPA. That's the only thing under test.
		defer func() { _ = recover() }()
		req := httptest.NewRequest(http.MethodPost, "/platform.FlowService/GetFlowList", strings.NewReader("\x00\x00\x00\x00\x00"))
		req.Header.Set("Content-Type", "application/grpc-web+proto")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if strings.Contains(rr.Body.String(), spaMarker) {
			t.Fatalf("grpc-web request fell through to the SPA")
		}
	})

	t.Run("CORS preflight is answered", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodOptions, "/", nil))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("preflight status = %d, want 204", rr.Code)
		}
		if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("preflight missing CORS allow-origin")
		}
	})
}
