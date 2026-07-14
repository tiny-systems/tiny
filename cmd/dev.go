package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// Default local ports. Two ports on purpose: the MCP endpoint is the
// machine API (point Claude Code / Cursor at it), the editor is the human
// view — keep them separable so you can expose or firewall each on its own.
const (
	mcpPort    = 7776
	editorPort = 7775
)

// runDev is the headline command — bare `tiny`. One process serves BOTH the
// local MCP endpoint and the browser editor against the same cluster state,
// so you prompt in your editor and watch the flow assemble itself on the
// canvas live. The mirror: the terminal drives, the browser reflects.
func runDev(cmd *cobra.Command, _ []string) error {
	fmt.Println()
	fmt.Println("  " + banner())
	ctx := targetContext()

	rows := [][2]string{
		{"runtime", clusterLine(ctx)},
		{"mcp", styleURL.Render(fmt.Sprintf("http://localhost:%d/mcp", mcpPort)) + styleSubtle.Render("   → point Claude Code / Cursor here")},
		{"editor", styleURL.Render(fmt.Sprintf("http://localhost:%d", editorPort)) + styleSubtle.Render("      → opens in your browser")},
	}
	body := ""
	for i, r := range rows {
		if i > 0 {
			body += "\n"
		}
		body += fmt.Sprintf("%-9s %s", styleKey.Render(r[0]), r[1])
	}
	fmt.Println()
	fmt.Println(styleBox.Render(body))
	fmt.Println()
	fmt.Println("  " + styleSubtle.Render("prompt in your editor; the canvas mirrors it live."))
	fmt.Println()

	// TODO(v0.2): this is the money shot — a Bubble Tea program that serves
	// the local MCP endpoint (in-process, over the shared module/pkg/tools
	// against your kubeconfig) + the editor, and streams live agent activity
	// (tool calls, nodes coming alive) into this view as Claude Code drives.
	fmt.Println("  " + styleWarn.Render("▸ v0.2") + styleSubtle.Render("  the live dev server (MCP + editor, one process) is landing next."))
	fmt.Println("  " + styleSubtle.Render("        today: `tiny status` inspects your cluster, `tiny mcp` prints the wiring."))
	fmt.Println("  " + styleSubtle.Render("        follow: https://github.com/tiny-systems/tiny"))
	fmt.Println()
	return nil
}

func clusterLine(ctx string) string {
	if ctx == "" {
		return styleWarn.Render("no cluster") + styleSubtle.Render("  (kubectl not configured — run `tiny up` after connecting one)")
	}
	return styleOK.Render("✓ ") + fmt.Sprintf("context %s", styleTitle.Render(ctx))
}
