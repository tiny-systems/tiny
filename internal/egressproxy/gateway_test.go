package egressproxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// upstreamRecorder stands in for api.anthropic.com and remembers what
// arrived.
func upstreamRecorder(t *testing.T) (*httptest.Server, *http.Request) {
	t.Helper()
	got := &http.Request{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = *r.Clone(r.Context())
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: ok\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func gatewayFor(t *testing.T, upstream string, creds map[string]string) *Gateway {
	t.Helper()
	dir := t.TempDir()
	for k, v := range creds {
		if err := os.WriteFile(filepath.Join(dir, k), []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	return NewGateway(u, dir)
}

func send(g *Gateway, path string, header http.Header) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"x"}`))
	req.Header = header
	rec := httptest.NewRecorder()
	g.ServeHTTP(rec, req)
	return rec
}

// The agent's placeholder never leaves the namespace: the subscription
// token replaces it, and other headers (the OAuth beta flag Claude Code
// sends with a subscription token) pass through.
func TestGatewaySwapsInTheSubscriptionToken(t *testing.T) {
	up, got := upstreamRecorder(t)
	g := gatewayFor(t, up.URL, map[string]string{OAuthTokenFile: "sk-ant-oat01-real"})
	h := http.Header{}
	h.Set("Authorization", "Bearer sk-ant-oat01-tiny-gateway-placeholder")
	h.Set("Anthropic-Beta", "oauth-2025-04-20")
	rec := send(g, "/v1/messages", h)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "data: ok") {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if a := got.Header.Get("Authorization"); a != "Bearer sk-ant-oat01-real" {
		t.Fatalf("upstream Authorization = %q", a)
	}
	if got.Header.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Fatal("the beta header must pass through")
	}
	if got.URL.Path != "/v1/messages" {
		t.Fatalf("upstream path = %q", got.URL.Path)
	}
}

func TestGatewayUsesAnAPIKeyAsXAPIKey(t *testing.T) {
	up, got := upstreamRecorder(t)
	g := gatewayFor(t, up.URL, map[string]string{APIKeyFile: "sk-ant-api03-real"})
	h := http.Header{}
	h.Set("X-Api-Key", "sk-ant-api03-tiny-gateway-placeholder")
	if rec := send(g, "/v1/messages", h); rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	if got.Header.Get("X-Api-Key") != "sk-ant-api03-real" || got.Header.Get("Authorization") != "" {
		t.Fatalf("upstream x-api-key=%q authorization=%q", got.Header.Get("X-Api-Key"), got.Header.Get("Authorization"))
	}
}

// Only the model API is relayed with the namespace's credential.
func TestGatewayRefusesOtherPaths(t *testing.T) {
	up, got := upstreamRecorder(t)
	g := gatewayFor(t, up.URL, map[string]string{OAuthTokenFile: "sk-ant-oat01-real"})
	if rec := send(g, "/api/oauth/profile", http.Header{}); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if got.URL != nil {
		t.Fatal("a refused path reached upstream")
	}
}

// No credential stored is a clear 503 naming the fix, not an anonymous
// call upstream.
func TestGatewayWithoutACredential(t *testing.T) {
	up, got := upstreamRecorder(t)
	g := gatewayFor(t, up.URL, nil)
	rec := send(g, "/v1/messages", http.Header{})
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "tiny gateway on") {
		t.Fatalf("status %d body %q", rec.Code, rec.Body.String())
	}
	if got.URL != nil {
		t.Fatal("a request without a credential reached upstream")
	}
}

// Rotating the Secret takes effect on the next request.
func TestGatewayRereadsTheCredential(t *testing.T) {
	up, got := upstreamRecorder(t)
	g := gatewayFor(t, up.URL, map[string]string{OAuthTokenFile: "sk-ant-oat01-old"})
	send(g, "/v1/messages", http.Header{})
	if err := os.WriteFile(filepath.Join(g.credDir, OAuthTokenFile), []byte("sk-ant-oat01-new"), 0o600); err != nil {
		t.Fatal(err)
	}
	send(g, "/v1/messages", http.Header{})
	if got.Header.Get("Authorization") != "Bearer sk-ant-oat01-new" {
		t.Fatalf("after rotation upstream got %q", got.Header.Get("Authorization"))
	}
}
