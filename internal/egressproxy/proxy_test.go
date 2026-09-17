package egressproxy

import (
	"bufio"
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestRulesMatching(t *testing.T) {
	r := NewRules([]string{
		"api.anthropic.com",
		".npmjs.org # the registry and its CDN",
		"",
		"# a comment alone",
	})
	cases := map[string]bool{
		"api.anthropic.com":         true,
		"API.Anthropic.com":         true,
		"api.anthropic.com:443":     true,
		"registry.npmjs.org":        true,
		"npmjs.org":                 true,
		"evil.com":                  false,
		"api.anthropic.com.evil.io": false,
		// The suffix rule must not admit a lookalike parent domain.
		"npmjs.org.evil.com": false,
		"# a comment alone":  false,
	}
	for host, want := range cases {
		if got := r.Allows(host); got != want {
			t.Errorf("Allows(%q) = %v, want %v", host, got, want)
		}
	}
}

// The refusal has to say what to do about it: an agent blocked at 3am is
// useless if the message is just "403".
func TestDeniedHostExplainsItself(t *testing.T) {
	p := New(NewRules([]string{"api.anthropic.com"}))
	req := httptest.NewRequest(http.MethodConnect, "//exfil.example.com:443", nil)
	req.URL.Host = "exfil.example.com:443"
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "exfil.example.com") {
		t.Errorf("refusal does not name the host: %q", body)
	}
	if !strings.Contains(body, "allow-list") {
		t.Errorf("refusal does not explain why: %q", body)
	}
}

// The point of CONNECT: the proxy must tunnel bytes without terminating
// TLS, so the agent's connection to its model is never decrypted.
func TestConnectTunnelsWithoutBreakingTLS(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hello from upstream"))
	}))
	defer upstream.Close()
	upHost := strings.TrimPrefix(upstream.URL, "https://")

	proxy := httptest.NewServer(New(NewRules([]string{hostOnly(upHost)})))
	defer proxy.Close()

	// Speak CONNECT by hand, then run TLS through the tunnel.
	conn, err := net.Dial("tcp", strings.TrimPrefix(proxy.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, "CONNECT "+upHost+" HTTP/1.1\r\nHost: "+upHost+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status = %d, want 200", resp.StatusCode)
	}

	tlsConn := tls.Client(conn, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // test server's self-signed cert
	if err := tlsConn.Handshake(); err != nil {
		t.Fatalf("TLS through the tunnel failed: %v", err)
	}
	if _, err := io.WriteString(tlsConn, "GET / HTTP/1.1\r\nHost: "+upHost+"\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(tlsConn)
	if err != nil && !strings.Contains(err.Error(), "closed") {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "hello from upstream") {
		t.Fatalf("tunnel did not carry the response: %q", string(out))
	}
}

// A denied host must not reach the network at all, not merely be logged.
func TestDeniedConnectNeverDials(t *testing.T) {
	var dialed bool
	p := New(NewRules([]string{"allowed.example"}))
	p.Dialer = &net.Dialer{}
	// Any dial would have to go through connect(); assert we never got there
	// by checking the upstream listener sees nothing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		if c, aerr := ln.Accept(); aerr == nil {
			dialed = true
			_ = c.Close()
		}
	}()

	req := httptest.NewRequest(http.MethodConnect, "//"+ln.Addr().String(), nil)
	req.URL.Host = ln.Addr().String()
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if dialed {
		t.Fatal("proxy dialed a host that is not on the allow-list")
	}
}

// Plain http is forwarded for images whose package manager still uses it,
// and the same list applies.
func TestPlainHTTPRespectsTheList(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("plain ok"))
	}))
	defer up.Close()
	upHost := strings.TrimPrefix(up.URL, "http://")

	proxy := httptest.NewServer(New(NewRules([]string{hostOnly(upHost)})))
	defer proxy.Close()

	pu, _ := url.Parse(proxy.URL)
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pu)}}
	resp, err := client.Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	if string(b) != "plain ok" {
		t.Fatalf("body = %q", string(b))
	}
}
