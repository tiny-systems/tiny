package egressproxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Credential files the gateway reads from its directory, one per kind.
// The directory is a Secret mounted into the proxy pod only.
const (
	OAuthTokenFile = "CLAUDE_CODE_OAUTH_TOKEN"
	APIKeyFile     = "ANTHROPIC_API_KEY"
)

// Gateway is the model gateway: an HTTP endpoint inside the namespace that
// forwards Claude API calls upstream and attaches the real credential on
// the way. Agent pods are given its address as ANTHROPIC_BASE_URL and hold
// only a placeholder, so a compromised session has no key to take.
//
// It forwards /v1/ only. Anything else Claude Code sends to its base URL
// is refused rather than relayed with the namespace's credential.
type Gateway struct {
	upstream *url.URL
	credDir  string
	proxy    *httputil.ReverseProxy
}

// NewGateway forwards to upstream (https://api.anthropic.com in
// production) with the credential found in credDir.
func NewGateway(upstream *url.URL, credDir string) *Gateway {
	g := &Gateway{upstream: upstream, credDir: credDir}
	g.proxy = &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(upstream)
			r.Out.Host = upstream.Host
			// Whatever the agent sent is a placeholder; never forward it.
			r.Out.Header.Del("Authorization")
			r.Out.Header.Del("X-Api-Key")
			if kind, cred := g.credential(); kind == OAuthTokenFile {
				r.Out.Header.Set("Authorization", "Bearer "+cred)
			} else if kind == APIKeyFile {
				r.Out.Header.Set("X-Api-Key", cred)
			}
		},
		// Streamed responses (server-sent events) must reach the agent as
		// they arrive, not when the turn ends.
		FlushInterval: -1,
		ErrorLog:      log.New(os.Stderr, "model gateway: ", log.LstdFlags),
	}
	return g
}

// credential reads the current credential on every request, so rotating
// the Secret takes effect without restarting the proxy. A subscription
// token wins when both are present, matching Claude Code's own order.
func (g *Gateway) credential() (kind, value string) {
	for _, k := range []string{OAuthTokenFile, APIKeyFile} {
		b, err := os.ReadFile(filepath.Join(g.credDir, k))
		if err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				return k, v
			}
		}
	}
	return "", ""
}

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.Error(w, "tiny model gateway: only /v1/ is forwarded", http.StatusNotFound)
		return
	}
	if kind, _ := g.credential(); kind == "" {
		http.Error(w, "tiny model gateway: no Claude credential is stored for this namespace — run `tiny gateway on` or `tiny setup`", http.StatusServiceUnavailable)
		return
	}
	log.Printf("model gateway: %s %s", r.Method, r.URL.Path)
	g.proxy.ServeHTTP(w, r)
}
