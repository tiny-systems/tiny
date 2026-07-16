package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/catalog"
	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/provision"
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
			name := args[0]
			if err := confirmTarget(fmt.Sprintf("Install module %q into:", name)); err != nil {
				return err
			}
			ctx := cmd.Context()

			cfg, err := kube.RestConfig(flagContext)
			if err != nil {
				return err
			}
			hc, err := provision.NewClient(cfg, flagNamespace, nil)
			if err != nil {
				return err
			}

			m, err := catalog.Resolve(ctx, name)
			if err != nil {
				return err
			}
			if err := provision.EnsureNamespace(ctx, cfg, flagNamespace); err != nil {
				return err
			}
			brokerURL := provision.BrokerURL(ctx, cfg, flagNamespace)
			settings := resolveSettings(ctx, cfg)

			fmt.Println()
			var release string
			if err := step(fmt.Sprintf("module: %s %s", m.FullName, styleSubtle.Render(m.Tag)), func() error {
				var e error
				release, e = hc.InstallModule(ctx, m, brokerURL, settings)
				return e
			}); err != nil {
				fmt.Println("  " + styleSubtle.Render("fresh cluster? run `tiny up` first to install the runtime, then retry."))
				return err
			}
			fmt.Printf("\n  %s %s %s\n\n", styleOK.Render("✓ installed"), styleTitle.Render(m.FullName), styleSubtle.Render("· release "+release))
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

// ----- mcp lives in mcp.go (it serves, so it's more than a stub) -----

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
