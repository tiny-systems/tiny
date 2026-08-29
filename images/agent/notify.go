// tiny-notify posts attention events to the sidecar — curl without a curl
// dependency, because the session may run in any glibc image.
//
// Usage:
//
//	tiny-notify <message> [reason]   plain event
//	tiny-notify --stop               read the Stop-hook JSON on stdin, pull
//	                                 the tail of the agent's last turn from
//	                                 the transcript, post it as the session's
//	                                 live activity line
package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	keyReason  = "reason"
	keyMessage = "message"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: tiny-notify <message> [reason] | tiny-notify --stop")
		os.Exit(2)
	}
	var body map[string]string
	switch os.Args[1] {
	case "--usage":
		cpu, mem := readUsage()
		if cpu == "" && mem == "" {
			return // cgroup v2 not readable here; stay silent
		}
		// The tmux status bar reads this file; the sidecar gets the POST.
		_ = os.WriteFile("/workspace/.tiny/usage.txt",
			fmt.Appendf(nil, "cpu %s · mem %s", cpu, mem), 0o644)
		body = map[string]string{keyReason: "usage", "cpu": cpu, "memory": mem}
	case "--inbox":
		deliverInbox()
		return
	case "--limit":
		msg := ""
		if len(os.Args) > 2 {
			msg = strings.TrimSpace(os.Args[2])
		}
		body = map[string]string{keyReason: "limit", "message": msg}
	case "--stop":
		// A completed turn retires any pending resume-nudge: the next pod
		// replacement may nudge again.
		_ = os.Remove("/workspace/.tiny/nudge-pending")
		summary := lastTurnSummary(os.Stdin)
		if summary == "" {
			return // a turn with nothing to say updates nothing
		}
		body = map[string]string{"message": summary, "reason": "stop"}
	default:
		body = map[string]string{keyMessage: os.Args[1]}
		if len(os.Args) > 2 {
			body[keyReason] = os.Args[2]
		}
	}
	raw, _ := json.Marshal(body)
	c := &http.Client{Timeout: 5 * time.Second}
	resp, err := c.Post("http://127.0.0.1:8080/attention", "application/json", bytes.NewReader(raw))
	if err != nil {
		os.Exit(1) // best-effort: a hook must never wedge the agent
	}
	_ = resp.Body.Close()
}

// readUsage samples this container's cgroup v2 accounting: memory.current
// and the cpu.stat usage delta since the previous sample (kept beside the
// marker files). Millicores and Mi — the units a fleet reads.
func readUsage() (cpu, mem string) {
	if raw, err := os.ReadFile("/sys/fs/cgroup/memory.current"); err == nil {
		if b, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			mem = fmt.Sprintf("%dMi", b/(1024*1024))
		}
	}
	raw, err := os.ReadFile("/sys/fs/cgroup/cpu.stat")
	if err != nil {
		return cpu, mem
	}
	var usec int64
	for line := range strings.SplitSeq(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "usage_usec "); ok {
			usec, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	if usec == 0 {
		return cpu, mem
	}
	statePath := "/workspace/.tiny/usage.prev"
	now := time.Now().UnixMicro()
	if prev, err := os.ReadFile(statePath); err == nil {
		var pUsec, pWhen int64
		if _, err := fmt.Sscanf(string(prev), "%d %d", &pUsec, &pWhen); err == nil && now > pWhen {
			milli := (usec - pUsec) * 1000 / (now - pWhen)
			if milli >= 0 {
				cpu = fmt.Sprintf("%dm", milli)
			}
		}
	}
	_ = os.WriteFile(statePath, fmt.Appendf(nil, "%d %d", usec, now), 0o644)
	return cpu, mem
}

// lastTurnSummary digs the final assistant text out of the transcript the
// Stop hook points at. Any failure returns a plain marker — the hook must
// never break the agent over cosmetics.
func lastTurnSummary(stdin io.Reader) string {
	const fallback = ""
	var hook struct {
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.NewDecoder(io.LimitReader(stdin, 1<<20)).Decode(&hook); err != nil || hook.TranscriptPath == "" {
		return fallback
	}
	f, err := os.Open(hook.TranscriptPath)
	if err != nil {
		return fallback
	}
	defer func() { _ = f.Close() }()
	// Only the tail matters; transcripts grow all night.
	if st, err := f.Stat(); err == nil && st.Size() > 512*1024 {
		_, _ = f.Seek(-512*1024, io.SeekEnd)
	}
	var last string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.Contains(line, []byte(`"assistant"`)) {
			continue
		}
		var entry struct {
			Type    string `json:"type"`
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(line, &entry) != nil || entry.Type != "assistant" {
			continue
		}
		for _, c := range entry.Message.Content {
			if c.Type == "text" && strings.TrimSpace(c.Text) != "" {
				last = c.Text
			}
		}
	}
	last = strings.Join(strings.Fields(last), " ") // collapse newlines
	if last == "" {
		return fallback
	}
	if len(last) > 160 {
		last = last[:160] + "…"
	}
	return last
}

// deliverInbox fetches this session's spec.inbox from the API (raw HTTPS,
// serviceaccount token — no client-go in this tiny binary), types every
// undelivered message into the agent's prompt, and records delivered ids on
// the workspace so a replacement pod replays only what never landed.
func deliverInbox() {
	name := os.Getenv("TINY_SESSION_NAME")
	if name == "" {
		return
	}
	nsRaw, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return
	}
	token, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/token")
	if err != nil {
		return
	}
	caPool := x509.NewCertPool()
	if ca, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"); err == nil {
		caPool.AppendCertsFromPEM(ca)
	}
	host, port := os.Getenv("KUBERNETES_SERVICE_HOST"), os.Getenv("KUBERNETES_SERVICE_PORT")
	if host == "" || port == "" {
		return
	}
	url := fmt.Sprintf("https://%s/apis/agents.tinysystems.io/v1alpha1/namespaces/%s/sessions/%s",
		net.JoinHostPort(host, port), strings.TrimSpace(string(nsRaw)), name)
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	c := &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: caPool, MinVersion: tls.VersionTLS12}},
	}
	resp, err := c.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return
	}
	var session struct {
		Spec struct {
			Inbox []struct {
				ID   string `json:"id"`
				Text string `json:"text"`
			} `json:"inbox"`
		} `json:"spec"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&session); err != nil {
		return
	}
	if len(session.Spec.Inbox) == 0 {
		return
	}

	const deliveredPath = "/workspace/.tiny/inbox-delivered"
	raw, _ := os.ReadFile(deliveredPath)
	delivered := map[string]bool{}
	for id := range strings.SplitSeq(string(raw), "\n") {
		if id != "" {
			delivered[id] = true
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	tmux := filepath.Join(filepath.Dir(exe), "tmux")
	for _, msg := range session.Spec.Inbox {
		if delivered[msg.ID] || strings.TrimSpace(msg.Text) == "" {
			continue
		}
		if err := exec.Command(tmux, "send-keys", "-t", "main", "-l", "--", msg.Text).Run(); err != nil {
			return // tmux not up yet; retry next tick
		}
		if err := exec.Command(tmux, "send-keys", "-t", "main", "Enter").Run(); err != nil {
			return
		}
		f, err := os.OpenFile(deliveredPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return
		}
		_, _ = fmt.Fprintln(f, msg.ID)
		_ = f.Close()
	}
}
