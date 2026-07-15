package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/catalog"
	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/provision"
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

  1. CRDs                     — TinyModule / TinyNode / TinyFlow
  2. NATS/JetStream broker    — durable transport + run ledger
  3. OpenTelemetry collector  — execution traces
  4. core modules             — common, http, llm, kubernetes

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
	ctx := cmd.Context()

	cfg, err := kube.RestConfig(flagContext)
	if err != nil {
		return err
	}
	hc, err := provision.NewClient(cfg, flagNamespace, nil)
	if err != nil {
		return err
	}

	fmt.Println()
	if err := step("namespace "+flagNamespace, func() error { return provision.EnsureNamespace(ctx, cfg, flagNamespace) }); err != nil {
		return err
	}
	if err := step("CRDs (TinyModule / TinyNode / TinyFlow)", func() error { return hc.InstallCRDs(ctx) }); err != nil {
		return err
	}
	if err := step("NATS/JetStream broker", func() error { return hc.InstallBroker(ctx) }); err != nil {
		return err
	}
	if err := step("OpenTelemetry collector", func() error { return hc.InstallOTEL(ctx) }); err != nil {
		return err
	}

	// Resolve the authenticated broker URL now that NATS is up, and wire it
	// into every module so durable execution works on the first run.
	brokerURL := provision.BrokerURL(ctx, cfg, flagNamespace)

	for _, name := range coreModules {
		nm := name
		if err := step("module: "+nm, func() error {
			m, err := catalog.Resolve(ctx, nm)
			if err != nil {
				return err
			}
			_, err = hc.InstallModule(ctx, m, brokerURL)
			return err
		}); err != nil {
			return err
		}
	}

	fmt.Println()
	fmt.Println("  " + styleOK.Render("✓ runtime ready") + styleSubtle.Render("  · `tiny mcp` prints the client wiring; `tiny status` inspects it."))
	fmt.Println()
	return nil
}

// step runs one provisioning action, printing a ⋯ → ✓/✗ line with elapsed
// time. helm blocks (Wait) until each release is healthy, so the line sits
// on ⋯ for the duration and resolves in place.
func step(label string, fn func() error) error {
	fmt.Printf("  %s %s", styleSubtle.Render("⋯"), label)
	start := time.Now()
	if err := fn(); err != nil {
		fmt.Printf("\r  %s %s\n\n", styleWarn.Render("✗"), label)
		return err
	}
	elapsed := time.Since(start).Round(time.Second)
	fmt.Printf("\r  %s %s %s\n", styleOK.Render("✓"), label, styleSubtle.Render("("+elapsed.String()+")"))
	return nil
}
