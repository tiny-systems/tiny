/*
Package egressproxy is the hostname allow-list a NetworkPolicy cannot be.

A policy selects addresses. The thing an operator actually wants to say is
"this session may reach its model API, npm and github, and nothing else",
and that is a name, behind a CDN, on addresses that rotate. So the policy
stops pointing at the internet and points here instead.

CONNECT only ever reads the request line: the hostname arrives in
plaintext before the tunnel opens, gets checked, and then bytes are
copied without being understood. No certificate is minted, no CA is
installed in the agent image, and TLS between the agent and its model is
never broken. What leaves the namespace is exactly what the list permits.

A refusal is logged with the session that caused it. An agent reaching
for a host nobody allow-listed is worth seeing.
*/
package egressproxy

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Rules is the allow-list. A bare name matches that host exactly; a name
// beginning with a dot matches any subdomain of it, so ".npmjs.org"
// admits registry.npmjs.org without admitting npmjs.org.evil.com.
type Rules struct {
	mu    sync.RWMutex
	exact map[string]bool
	suffx []string
}

// NewRules builds an allow-list from one host per line. Blank lines and
// anything after a # are ignored, so the ConfigMap can explain itself.
func NewRules(lines []string) *Rules {
	r := &Rules{exact: map[string]bool{}}
	for _, raw := range lines {
		if i := strings.IndexByte(raw, '#'); i >= 0 {
			raw = raw[:i]
		}
		h := strings.ToLower(strings.TrimSpace(raw))
		if h == "" {
			continue
		}
		if strings.HasPrefix(h, ".") {
			r.suffx = append(r.suffx, h)
			continue
		}
		r.exact[h] = true
	}
	return r
}

// Allows reports whether a host may be reached. The port is not part of
// the decision; the policy already decides which ports exist.
func (r *Rules) Allows(host string) bool {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	h = strings.Trim(h, "[]")
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.exact[h] {
		return true
	}
	for _, s := range r.suffx {
		// ".npmjs.org" admits registry.npmjs.org and npmjs.org itself.
		if strings.HasSuffix(h, s) || h == strings.TrimPrefix(s, ".") {
			return true
		}
	}
	return false
}

// Proxy serves CONNECT (https) and plain forwarding (http) against Rules.
type Proxy struct {
	mu      sync.RWMutex
	Rules   *Rules
	Dialer  *net.Dialer
	Timeout time.Duration
}

// New returns a proxy with sane timeouts.
func New(rules *Rules) *Proxy {
	return &Proxy{
		Rules:   rules,
		Dialer:  &net.Dialer{Timeout: 10 * time.Second},
		Timeout: 60 * time.Minute, // a build can hold a connection a long time
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if r.Method == http.MethodConnect {
		host = r.URL.Host
	} else if r.URL != nil && r.URL.Host != "" {
		host = r.URL.Host
	}
	if !p.rules().Allows(host) {
		log.Printf("egress DENIED %s %s", r.Method, host)
		w.Header().Set("X-Tiny-Egress", "denied")
		http.Error(w,
			fmt.Sprintf("tiny egress proxy: %q is not in this namespace's allow-list.\n"+
				"Ask a human to add it: kubectl edit configmap tiny-egress-allow\n", hostOnly(host)),
			http.StatusForbidden)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	p.forward(w, r)
}

// connect opens the tunnel and stops understanding what goes through it.
func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	upstream, err := p.Dialer.Dial("tcp", withPort(r.URL.Host, "443"))
	if err != nil {
		http.Error(w, "tiny egress proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = upstream.Close()
		http.Error(w, "tiny egress proxy: connection cannot be hijacked", http.StatusInternalServerError)
		return
	}
	client, _, err := hj.Hijack()
	if err != nil {
		_ = upstream.Close()
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		_ = client.Close()
		_ = upstream.Close()
		return
	}
	deadline := time.Now().Add(p.Timeout)
	_ = client.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)

	var wg sync.WaitGroup
	wg.Add(2)
	go pipe(&wg, upstream, client)
	go pipe(&wg, client, upstream)
	wg.Wait()
	_ = client.Close()
	_ = upstream.Close()
}

// forward relays plain http, which package managers in older images still
// use. The allow-list has already been applied.
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	if r.URL.Scheme == "" {
		r.URL.Scheme = "http"
	}
	if r.URL.Host == "" {
		r.URL.Host = r.Host
	}
	outReq, err := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
	if err != nil {
		http.Error(w, "tiny egress proxy: "+err.Error(), http.StatusBadRequest)
		return
	}
	for k, vs := range r.Header {
		if strings.EqualFold(k, "Proxy-Connection") || strings.EqualFold(k, "Proxy-Authorization") {
			continue
		}
		for _, v := range vs {
			outReq.Header.Add(k, v)
		}
	}
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(outReq)
	if err != nil {
		http.Error(w, "tiny egress proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func pipe(wg *sync.WaitGroup, dst, src net.Conn) {
	defer wg.Done()
	_, _ = io.Copy(dst, src)
	// Half-close so the other direction can drain rather than hang.
	if c, ok := dst.(*net.TCPConn); ok {
		_ = c.CloseWrite()
	}
}

func withPort(host, def string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return host
	}
	return net.JoinHostPort(host, def)
}

func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// SetRules swaps the allow-list under a running proxy.
func (p *Proxy) SetRules(r *Rules) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Rules = r
}

// rules reads the current list without racing a swap.
func (p *Proxy) rules() *Rules {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.Rules
}

// LoadRules reads an allow-list file.
func LoadRules(path string) (*Rules, error) {
	b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
	if err != nil {
		return nil, err
	}
	return NewRules(strings.Split(string(b), "\n")), nil
}

// WatchRules re-reads the file periodically. A ConfigMap edit reaches the
// pod as a changed file within a minute or so, and an operator widening
// the list should not have to restart anything to unblock a session.
func WatchRules(path string, p *Proxy, every time.Duration) {
	var last string
	for {
		time.Sleep(every)
		b, err := os.ReadFile(path) //nolint:gosec // operator-supplied path
		if err != nil {
			continue
		}
		if string(b) == last {
			continue
		}
		last = string(b)
		p.SetRules(NewRules(strings.Split(string(b), "\n")))
		log.Printf("egress allow-list reloaded from %s", path)
	}
}
