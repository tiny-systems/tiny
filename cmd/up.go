package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// coreModules are installed by `tiny up` so a fresh cluster is immediately
// useful. Everything else installs on demand — by name, or automatically
// when a prompt-built agent needs a capability it doesn't have yet.
var coreModules = []string{
	"common-module",
	"http-module",
	"llm-module",
	"kubernetes-module",
}

func newUpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Provision the runtime on your cluster (broker + operator + core modules)",
		Long: `Provision the Tiny Systems runtime onto the target cluster:

  1. NATS/JetStream broker    — durable transport + run ledger
  2. operator + CRDs          — reconciles agents as real workloads
  3. core modules             — common, http, llm, kubernetes

Works on an empty cluster (kind, k3s, EKS, GKE — anything you can kubectl
into). Everything past the core set installs on demand. The target
context + namespace are shown and confirmed before anything is applied.`,
		RunE: runUp,
	}
}

func runUp(cmd *cobra.Command, _ []string) error {
	if err := confirmTarget("Provision the Tiny Systems runtime into:"); err != nil {
		return err
	}

	// TODO(v0.1): drive these steps against the confirmed context/namespace.
	// The broker + operator are helm installs from the public chart index;
	// modules resolve their own chart coordinates from the module catalog
	// (the same resolution the platform's install job does — lift it from
	// module/pkg + the install task rather than hardcoding chart names).
	steps := []string{
		"NATS/JetStream broker (durable transport + run ledger)",
		"operator + CRDs (reconciles agents into workloads)",
	}
	for _, m := range coreModules {
		steps = append(steps, "module: "+m)
	}

	fmt.Println("  Planned:")
	for _, s := range steps {
		fmt.Printf("    • %s\n", s)
	}
	fmt.Println()
	fmt.Println("  ▸ v0.1: provisioning is being wired to the public chart index +")
	fmt.Println("    module catalog. Until then, `tiny status` inspects the target and")
	fmt.Println("    `tiny mcp` runs against a cluster that already has the runtime.")
	fmt.Println("    Follow along: https://github.com/tiny-systems/tiny")
	return nil
}
