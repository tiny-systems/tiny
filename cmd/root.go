package cmd

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"
)

// Persistent target selection. Every mutating command resolves the
// cluster it acts on through these, and confirms before touching it —
// shelling helm/kubectl into the wrong context is the classic footgun,
// so the target is always shown and (unless --yes) confirmed.
var (
	flagContext    string
	flagNamespace  string
	flagYes     bool
	flagPrint   bool
	flagProject string

	// Cluster install settings — properties of the target cluster, applied to
	// module installs and persisted as tinysystems-namespace annotations.
	flagIngressClass  string
	flagDomain        string
	flagStorageClass  string
	flagClusterIssuer string
)

const defaultNamespace = "tinysystems"

func Execute(version string) error {
	root := &cobra.Command{
		Use:   "tiny",
		Short: "Self-hosted AI agents on your own Kubernetes",
		Long: `tiny — the local front door to the Tiny Systems agent runtime.

Install the runtime onto your own Kubernetes cluster, drive it by prompt
from your editor (Claude Code, Cursor, any MCP client), and watch agents
assemble themselves and run as real workloads. Your cluster, your keys,
your data.

Run with no command to start the dev server (MCP endpoint + editor).`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: false,
		// Bare `tiny` starts the dev server — the thing you do 100x a day.
		RunE: runDev,
	}

	root.PersistentFlags().StringVar(&flagContext, "context", "", "kubeconfig context to target (default: current-context)")
	root.PersistentFlags().StringVarP(&flagNamespace, "namespace", "n", defaultNamespace, "namespace to install the runtime into")
	root.PersistentFlags().BoolVarP(&flagYes, "yes", "y", false, "skip the target confirmation prompt (for CI)")
	root.PersistentFlags().StringVarP(&flagProject, "project", "p", "", "active project for this session (created if missing); scopes the MCP endpoint + editor")
	// Cluster install settings (persisted to the namespace; set once).
	root.PersistentFlags().StringVar(&flagIngressClass, "ingress-class", "", "ingress controller class for modules that expose HTTP (e.g. nginx)")
	root.PersistentFlags().StringVar(&flagDomain, "domain", "", "base domain suffix for module ingress hostnames")
	root.PersistentFlags().StringVar(&flagStorageClass, "storage-class", "", "storage class for modules that need a PVC")
	root.PersistentFlags().StringVar(&flagClusterIssuer, "cluster-issuer", "", "cert-manager ClusterIssuer name to annotate ingresses for TLS")
	// --print is local to bare `tiny` (the serve command): dump the client
	// config and exit instead of serving.
	root.Flags().BoolVar(&flagPrint, "print", false, "print the MCP client config and exit (don't serve)")

	root.AddCommand(
		newUpCmd(),
		newInstallCmd(),
		newRepoCmd(),
		newStatusCmd(),
		newEditCmd(),
		newUpgradeCmd(),
	)
	return root.Execute()
}

// targetContext returns the context to act on: the --context flag, or the
// kubeconfig's current-context.
func targetContext() string {
	if flagContext != "" {
		return flagContext
	}
	out, err := exec.Command("kubectl", "config", "current-context").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// confirmTarget prints the exact context + namespace a mutating command is
// about to touch and requires a y/N (skipped with --yes). This is the
// guard against installing into the wrong cluster.
func confirmTarget(action string) error {
	ctx := targetContext()
	if ctx == "" {
		return fmt.Errorf("no kubeconfig context found — is kubectl configured? (set one with --context)")
	}
	fmt.Printf("\n  %s\n", action)
	fmt.Printf("    context   %s\n", ctx)
	fmt.Printf("    namespace %s\n\n", flagNamespace)
	if flagYes {
		return nil
	}
	fmt.Print("  Continue? [y/N] ")
	reader := bufio.NewReader(os.Stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("aborted")
	}
	return nil
}

// kubectlArgs prefixes context/namespace onto a kubectl invocation so
// every call targets the confirmed cluster explicitly.
func kubectlArgs(args ...string) []string {
	pre := []string{}
	if ctx := targetContext(); ctx != "" {
		pre = append(pre, "--context", ctx)
	}
	pre = append(pre, "--namespace", flagNamespace)
	return append(pre, args...)
}
