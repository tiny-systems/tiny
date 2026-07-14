package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// ----- install -----

func newInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install <module>",
		Short: "Install a capability module onto the cluster",
		Long: `Install a module (a capability an agent can use) from the public
catalog — e.g. database-module, communication-module, googleapis-module.

You rarely need this by hand: a prompt-built agent installs the modules
it needs on the fly through the MCP endpoint. Use it to pre-warm a
cluster or add something specific.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := confirmTarget(fmt.Sprintf("Install module %q into:", args[0])); err != nil {
				return err
			}
			fmt.Printf("  %s resolving %s from the public catalog…\n", styleKey.Render("install"), styleTitle.Render(args[0]))
			fmt.Println("  " + styleWarn.Render("▸ v0.1") + styleSubtle.Render("  chart resolution + helm install is being lifted from the platform install path."))
			return nil
		},
	}
}

// ----- status -----

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show the runtime + installed modules on the target cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println()
			fmt.Println("  " + banner())
			ctx := targetContext()
			if ctx == "" {
				fmt.Println("\n  " + styleWarn.Render("no kubeconfig context") + styleSubtle.Render("  — configure kubectl, or pass --context"))
				return nil
			}
			fmt.Printf("\n  %s %s   %s %s\n\n", styleKey.Render("context"), styleTitle.Render(ctx), styleKey.Render("namespace"), styleTitle.Render(flagNamespace))

			// Real, cheap signal today: list the tinymodule CRs if the runtime
			// is installed. (Deeper status — node health, versions — arrives
			// with the SDK integration.)
			out, err := exec.Command("kubectl", kubectlArgs("get", "tinymodules", "-o", "name")...).CombinedOutput()
			s := strings.TrimSpace(string(out))
			switch {
			case err != nil && strings.Contains(s, "the server doesn't have a resource type"):
				fmt.Println("  " + styleWarn.Render("runtime not installed") + styleSubtle.Render("  — run `tiny up` to provision it"))
			case err != nil:
				fmt.Println("  " + styleSubtle.Render(s))
			case s == "":
				fmt.Println("  " + styleOK.Render("runtime present") + styleSubtle.Render("  · no modules installed yet"))
			default:
				n := len(strings.Split(s, "\n"))
				fmt.Printf("  %s %s\n", styleOK.Render("runtime present"), styleSubtle.Render(fmt.Sprintf("· %d module(s) installed", n)))
			}
			fmt.Println()
			return nil
		},
	}
}

// ----- mcp -----

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run the local MCP server (or print the client config)",
		Long: `Expose the runtime as an MCP endpoint your editor connects to by URL,
so Claude Code / Cursor drive agents on YOUR cluster — prompt-build,
install-on-the-fly, all local. HTTP/SSE (not stdio) on purpose: one
process can then serve the MCP endpoint AND the editor, sharing live
state — that is what the bare "tiny" dev command does.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Print("\n  Add this to your MCP client (Claude Code / Cursor):\n\n")
			snippet := fmt.Sprintf("\"tinysystems\": {\n  \"url\": \"http://localhost:%d/mcp\"\n}", mcpPort)
			fmt.Println(styleBox.Render(snippet))
			fmt.Println("\n  " + styleWarn.Render("▸ v0.2") + styleSubtle.Render("  serving the endpoint against your kubeconfig is the next milestone."))
			fmt.Println()
			return nil
		},
	}
	return cmd
}

// ----- edit -----

func newEditCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "edit [flow]",
		Short: "Open the web canvas against your local cluster",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("  " + styleSubtle.Render("the editor is served by the dev server — run `tiny` with no command."))
			return nil
		},
	}
}

var _ = os.Stdout
