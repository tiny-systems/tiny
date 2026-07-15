package cmd

import (
	"os/exec"
	"strings"
)

// claudeMCPName is what the local endpoint shows up as in Claude Code. It's
// deliberately NOT "tinysystems" — that name belongs to the hosted endpoint
// (mcp.tinysystems.io) many users already have registered, and we must not
// collide with or clobber it. "tiny" = your local cluster.
const claudeMCPName = "tiny"

// registerWithClaude best-effort adds the local MCP endpoint to Claude Code
// via its CLI, so bare `tiny` gets you connected with no manual paste — the
// single-command flow. Never fatal: if the claude CLI isn't installed, found
// is false and the caller prints the manual snippet instead.
//
// Idempotency keys on the endpoint URL, not the server name — the user may
// already run the hosted "tinysystems" server, and matching by name would
// wrongly treat that as "already connected" (or, on add, overwrite it).
//
// status is a short human line for the startup banner; found reports whether
// the claude CLI exists at all.
func registerWithClaude(url string) (status string, found bool) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return "", false
	}

	// `claude mcp list` prints "name: url (TRANSPORT) - status" per line, so
	// the exact local URL appearing means this endpoint is already wired.
	if out, err := exec.Command(claude, "mcp", "list").CombinedOutput(); err == nil {
		if strings.Contains(string(out), url) {
			return "Claude Code already connected (mcp: " + claudeMCPName + ")", true
		}
	}

	out, err := exec.Command(claude, "mcp", "add", "--transport", "http", claudeMCPName, url).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "couldn't auto-connect Claude Code — add it by hand: " + msg, true
	}
	return "connected to Claude Code (mcp: " + claudeMCPName + ")", true
}
