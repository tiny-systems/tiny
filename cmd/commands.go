package cmd

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/provision"
	"github.com/tiny-systems/tiny/internal/repo"
)

// ----- install -----

func newInstallCmd() *cobra.Command {
	var bundles []string
	c := &cobra.Command{
		Use:   "install <module>[@version]",
		Short: "Install a capability module from the configured repos",
		Long: `Install a module (a capability an agent can use) from the module repos
tiny is configured with (see 'tiny repo'). It resolves the module from the
index and installs it via Helm — no platform required.

You rarely need this by hand: a prompt-built agent installs the modules it
needs on the fly through the MCP endpoint. Use it to pre-warm a cluster or
add something specific.`,
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
			store, err := repo.Open()
			if err != nil {
				return err
			}
			// Refresh indexes (best-effort — resolve can still run off cache).
			if err := store.Update(ctx); err != nil {
				fmt.Printf("  %s %v\n", styleWarn.Render("repo update:"), err)
			}
			merged, err := store.Merged()
			if err != nil {
				return err
			}
			if err := provision.EnsureNamespace(ctx, cfg, flagNamespace); err != nil {
				return err
			}

			settings := resolveSettings(ctx, cfg)
			cluster := map[string]string{"brokerURL": provision.BrokerURL(ctx, cfg, flagNamespace)}
			if settings.IngressClass != "" {
				cluster["ingressClass"] = settings.IngressClass
			}
			if settings.StorageClass != "" {
				cluster["storageClass"] = settings.StorageClass
			}

			fmt.Println()
			var plan *repo.InstallPlan
			if err := step("installing "+name, func() error {
				var e error
				plan, e = repo.Install(ctx, merged, name, flagNamespace, cluster, bundles, provision.BaseValues, hc, hc)
				return e
			}); err != nil {
				fmt.Println("  " + styleSubtle.Render("fresh cluster? run `tiny up` first. not in any repo? see `tiny repo add`."))
				return err
			}
			fmt.Printf("\n  %s %s %s\n\n", styleOK.Render("✓ installed"), styleTitle.Render(name), styleSubtle.Render("· release "+plan.ReleaseName))
			return nil
		},
	}
	c.Flags().StringSliceVar(&bundles, "bundle", nil, `bundles to enable (default: module defaults; "--bundle none" for zero)`)
	return c
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

			// What the guardrail quota is, and how close anything is to it. A
			// ceiling nobody can see only ever announces itself as a component
			// that mysteriously stopped working.
			if cfg, cerr := kube.RestConfig(flagContext); cerr == nil {
				if usage, qerr := provision.ReadQuota(cmd.Context(), cfg, flagNamespace); qerr == nil {
					if len(usage) == 0 {
						fmt.Printf("  %s %s\n", styleKey.Render("quota"), styleSubtle.Render("none — a flow may create as many jobs and volumes as it likes"))
					} else {
						sort.Slice(usage, func(i, j int) bool { return usage[i].Resource < usage[j].Resource })
						for _, u := range usage {
							fmt.Printf("  %s %s\n", styleKey.Render("quota"),
								styleSubtle.Render(fmt.Sprintf("%s %s of %s", u.Resource, u.Used, u.Hard)))
						}
					}
				}
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
		Use:   "edit",
		Short: "Open the web canvas against your local cluster",
		Long: `Serve the local runtime and open the canvas in your browser.

The same server bare 'tiny' runs — MCP endpoint for your editor, canvas for
you. The only difference is that this one opens the browser for you.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// The canvas can only be opened once something is listening on it,
			// and the server below blocks until Ctrl-C — so the wait runs
			// alongside it rather than before it.
			go openWhenServing(fmt.Sprintf("127.0.0.1:%d", editorPort))
			return runDev(cmd, args)
		},
	}
}

// openWhenServing opens the canvas as soon as the editor port answers, and
// gives up quietly if it never does — a browser window is a convenience, and
// failing to open one must not look like the server failed to start.
func openWhenServing(addr string) {
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			_ = conn.Close()
			openBrowser("http://" + addr)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

var _ = os.Stdout
