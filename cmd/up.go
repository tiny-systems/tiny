package cmd

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"

	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/provision"
	"github.com/tiny-systems/tiny/internal/repo"
)

// coreModules are installed by `tiny up` so a fresh cluster is immediately
// useful. Everything else installs on demand — by name, or automatically
// when a prompt-built agent needs a capability it doesn't have yet.
// coreModules are the flat catalog keys (the catalog is a flat namespace; a
// `workspace/` prefix is tolerated by Resolve but isn't part of the lookup).
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
	settings := resolveSettings(ctx, cfg)

	// Core modules come from the configured repos (default: the public index) —
	// no platform, GHCR images. This is the same path as `tiny install`.
	store, err := repo.Open()
	if err != nil {
		return err
	}
	if err := store.Update(ctx); err != nil {
		fmt.Printf("  %s %v\n", styleWarn.Render("repo update:"), err)
	}
	merged, err := store.Merged()
	if err != nil {
		return err
	}
	cluster := map[string]string{"brokerURL": brokerURL}
	if settings.IngressClass != "" {
		cluster["ingressClass"] = settings.IngressClass
	}
	if settings.StorageClass != "" {
		cluster["storageClass"] = settings.StorageClass
	}
	for _, name := range coreModules {
		nm := name
		if err := step("module: "+nm, func() error {
			_, e := repo.Install(ctx, merged, nm, flagNamespace, cluster, nil, provision.BaseValues, hc)
			return e
		}); err != nil {
			return err
		}
	}

	fmt.Println()
	fmt.Println("  " + styleOK.Render("✓ runtime ready") + styleSubtle.Render("  · `tiny mcp` prints the client wiring; `tiny status` inspects it."))
	fmt.Println()
	return nil
}

// spinnerFrames is a braille spinner — smooth, single-width, reads as
// motion even at a lazy tick.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// step runs one provisioning action and shows its progress. helm blocks
// (Wait) until each release is healthy — often 20-60s while images pull —
// so on a TTY we animate a spinner with a live elapsed counter to make it
// obvious the install is working, not hung. Off a TTY (CI, piped) we fall
// back to a single static line per step.
func step(label string, fn func() error) error {
	start := time.Now()

	if !isatty.IsTerminal(os.Stdout.Fd()) {
		if err := fn(); err != nil {
			fmt.Printf("  %s %s\n\n", styleWarn.Render("✗"), label)
			return err
		}
		fmt.Printf("  %s %s %s\n", styleOK.Render("✓"), label, styleSubtle.Render("("+elapsed(start)+")"))
		return nil
	}

	done := make(chan error, 1)
	go func() { done <- fn() }()

	ticker := time.NewTicker(90 * time.Millisecond)
	defer ticker.Stop()
	i := 0
	for {
		select {
		case err := <-done:
			fmt.Print("\r\033[K") // clear the spinner line
			if err != nil {
				fmt.Printf("  %s %s\n\n", styleWarn.Render("✗"), label)
				return err
			}
			fmt.Printf("  %s %s %s\n", styleOK.Render("✓"), label, styleSubtle.Render("("+elapsed(start)+")"))
			return nil
		case <-ticker.C:
			fmt.Printf("\r\033[K  %s %s %s",
				styleKey.Render(spinnerFrames[i%len(spinnerFrames)]),
				label, styleSubtle.Render(elapsed(start)))
			i++
		}
	}
}

func elapsed(start time.Time) string {
	return time.Since(start).Round(time.Second).String()
}

// flagSettings collects the cluster-install flags passed this invocation.
func flagSettings() provision.Settings {
	s := provision.Settings{
		IngressClass: flagIngressClass,
		DomainSuffix: flagDomain,
		StorageClass: flagStorageClass,
	}
	if flagClusterIssuer != "" {
		s.Issuer = flagClusterIssuer
		s.ClusterIssuer = true
	}
	return s
}

// resolveSettings returns the effective cluster settings for a module install:
// the namespace's saved settings overlaid with any flags passed this run — and
// persists those flags so later installs (including the agent's on-the-fly
// ones) inherit them. Set them once with `tiny up --ingress-class … …`.
func resolveSettings(ctx context.Context, cfg *rest.Config) provision.Settings {
	saved, _ := provision.LoadSettings(ctx, cfg, flagNamespace)
	fs := flagSettings()
	if !fs.Empty() {
		_ = provision.SaveSettings(ctx, cfg, flagNamespace, fs)
	}
	return saved.Merge(fs)
}
