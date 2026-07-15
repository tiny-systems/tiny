package cmd

import "github.com/spf13/cobra"

// Default local ports. Two on purpose: the MCP endpoint is the machine API
// (point Claude Code / Cursor at it); the editor is the human view. Kept
// separable so each can be exposed or firewalled on its own. editorPort is
// reserved for the browser editor that lands with the full dev server.
const (
	mcpPort    = 7776
	editorPort = 7775
)

// runDev is the headline command — bare `tiny`. It serves the local MCP
// endpoint against your cluster, so you point Claude Code / Cursor at it and
// build agents by prompt. The browser editor — the other half of the dev
// server — lands in a later release; until then bare `tiny` is the live MCP
// endpoint (the same thing `tiny mcp` serves). Needs a provisioned runtime:
// run `tiny up` first on a fresh cluster.
func runDev(cmd *cobra.Command, _ []string) error {
	return runMCP(cmd)
}
