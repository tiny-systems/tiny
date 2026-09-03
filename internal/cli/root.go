// Package cli is the tiny CLI: coding-agent sessions as Kubernetes workloads.
//
// The whole surface:
//
//	tiny new "task"   start a session (installs the runtime on first contact)
//	tiny              the fleet screen — who runs, who needs you
//	tiny init         install/upgrade the runtime explicitly (CI, platform habits)
//	tiny upgrade      update this binary
package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"

	"github.com/tiny-systems/tiny/internal/runtimeinstall"
	"github.com/tiny-systems/tiny/internal/workload"
)

// Persistent target selection. Every mutating command resolves the cluster
// it acts on through these, and confirms before touching it — the wrong
// kubeconfig context is the classic footgun, so the target is always shown
// and (unless --yes) confirmed.
// binaryName is the command's own name — the same "tiny" the release
// archives carry, distinct from the defaultNamespace that happens to match.
const binaryName = "tiny"

var (
	cliVersion    string
	flagContext   string
	flagProfile   string
	flagNamespace string
	flagYes       bool
)

// defaultNamespace is where sessions live unless told otherwise.
const defaultNamespace = "tiny"

// Execute runs the CLI.
func Execute(version string) error {
	cliVersion = version
	workload.SetDefaultImageTag(version)
	// client-go logs unhandled stream errors straight to stderr — over the
	// TUI. Errors we care about come back through our own calls.
	klog.SetLogger(logr.Discard())
	root := &cobra.Command{
		Use:   binaryName,
		Short: "Coding-agent sessions on your own Kubernetes",
		Long: `tiny — coding-agent sessions as Kubernetes workloads.

Start a session with a task; it runs as a pod with a persistent workspace
and keeps working when you disconnect. When the agent needs a decision, its
row lights up — answer from the fleet screen, or from anywhere with kubectl.
Your cluster, your keys, your repos.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
		// Bare tiny is the fleet screen. Every start picks the target —
		// namespaces separate groups of agents, and which group you are
		// looking at is a per-launch choice. Enter-enter keeps yesterday's.
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := pickEveryStart(); err != nil {
				return err
			}
			return runSessionsTUI(cmd.Context())
		},
	}

	root.PersistentFlags().StringVar(&flagContext, "context", "", "kubeconfig context override (first run pins it; edit the config file to change)")
	root.PersistentFlags().StringVarP(&flagNamespace, "namespace", "n", "", "namespace override (default: pinned, else \"tiny\")")
	root.PersistentFlags().BoolVarP(&flagYes, "yes", "y", false, "skip the target confirmation prompt (for CI)")
	root.PersistentFlags().StringVarP(&flagProfile, "profile", "p", "", "named target from `tiny profile list` (e.g. work, home)")

	root.AddCommand(
		newNewCmd(),
		newShellCmd(),
		newAnswerCmd(),
		newSetupCmd(),
		newInitCmd(),
		newDeliverCmd(),
		newAttachCmd(),
		newHandoffCmd(),
		newProfileCmd(),
		newBroadcastCmd(),
		newExportCmd(),
		newUpgradeCmd(),
	)
	return root.Execute()
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Install or upgrade the session runtime on the target cluster",
		Long: "Installs the two CRDs and the namespace-scoped manager (one small always-on pod).\n" +
			"tiny new does this on first contact anyway; init is for those who want setup as its\n" +
			"own explicit, reviewable step.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 120*time.Second)
			defer cancel()
			if err := confirmTarget(runtimeinstall.ConfirmPrompt); err != nil {
				return err
			}
			if err := applyRuntime(ctx, k); err != nil {
				return err
			}
			fmt.Println("  ✓ runtime installed")
			fmt.Printf("  start a session:  tiny new \"your task\" -n %s\n", k.Namespace)
			return nil
		},
	}
}

// targetContext returns the context to act on — the pinned one.
func targetContext() string {
	ctxName, _, err := resolveTarget()
	if err != nil {
		return ""
	}
	return ctxName
}

// confirmTarget prints the exact context + namespace a mutating command is
// about to touch and requires a y/N (skipped with --yes).
func confirmTarget(action string) error {
	ctx := targetContext()
	if ctx == "" {
		return fmt.Errorf("no kubeconfig context found — is kubectl configured? (set one with --context)")
	}
	_, ns, _ := resolveTarget()
	fmt.Printf("\n  %s\n", action)
	fmt.Printf("    context   %s\n", ctx)
	fmt.Printf("    namespace %s\n\n", ns)
	if flagYes {
		return nil
	}
	fmt.Print("  Continue? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	if !confirmed(answer) {
		return fmt.Errorf("aborted")
	}
	return nil
}
