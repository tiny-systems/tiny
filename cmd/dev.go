package cmd

import (
	"os"
	"strconv"

	"github.com/spf13/cobra"
)

// Default local ports. Two on purpose: the MCP endpoint is the machine API
// (point Claude Code / Cursor at it); the editor is the human view. Kept
// separable so each can be exposed or firewalled on its own. Overridable via
// TINY_MCP_PORT / TINY_EDITOR_PORT so a second instance can run without
// colliding with one already serving.
var (
	mcpPort    = envPort("TINY_MCP_PORT", 7776)
	editorPort = envPort("TINY_EDITOR_PORT", 7775)
)

func envPort(name string, def int) int {
	if v := os.Getenv(name); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			return p
		}
	}
	return def
}

// runDev is the headline command — bare `tiny`. It serves two things against
// your cluster: the local MCP endpoint, which you point Claude Code / Cursor
// at to build agents by prompt, and the browser canvas, which is where you
// watch what they built. `tiny edit` is the same server with the canvas
// opened for you. Needs a provisioned runtime: run `tiny up` first on a fresh
// cluster.
func runDev(cmd *cobra.Command, _ []string) error {
	if flagPrint {
		printConnect()
		return nil
	}
	return runMCP(cmd)
}
