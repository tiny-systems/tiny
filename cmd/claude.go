package cmd

import "fmt"

// claudeMCPName is what the local endpoint shows up as in Claude Code. It's
// deliberately NOT "tinysystems" — that name belongs to the hosted endpoint
// (mcp.tinysystems.io) many users already have registered. "tiny" = your
// local cluster.
const claudeMCPName = "tiny"

// printConnect shows the one command to wire the local endpoint into Claude
// Code. We just print it rather than run it: -s user scope so EVERY session
// sees it (the default 'local' scope is per-directory — the reason a new
// session saw nothing), and it's a one-time paste you stay in control of.
func printConnect() {
	url := fmt.Sprintf("http://localhost:%d/mcp", mcpPort)
	fmt.Print("\n  Connect Claude Code (one-time):\n\n")
	fmt.Println("    " + styleTitle.Render("claude mcp add -s user --transport http "+claudeMCPName+" "+url))
	fmt.Println("\n  " + styleSubtle.Render("then start a new Claude Code session · other MCP clients → "+url))
}
