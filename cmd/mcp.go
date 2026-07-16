package cmd

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	sdktools "github.com/tiny-systems/module/pkg/tools"

	"github.com/tiny-systems/tiny/internal/adapters"
	"github.com/tiny-systems/tiny/internal/backend"
	backendkube "github.com/tiny-systems/tiny/internal/backend/kube"
	"github.com/tiny-systems/tiny/internal/installer"
	"github.com/tiny-systems/tiny/internal/kube"
	mcpsrv "github.com/tiny-systems/tiny/internal/mcp"
	"github.com/tiny-systems/tiny/internal/prompt"
)

// publicAPIURL is the anonymous catalog the MCP server reads to discover
// installable modules and solutions — same endpoint `tiny install` uses.
const publicAPIURL = "https://api.tinysystems.io"

func newMCPCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the local MCP endpoint against your cluster",
		Long: `Expose your cluster's runtime as an MCP endpoint that Claude Code /
Cursor connect to by URL. Prompt-build agents, read projects, inspect
traces — all against YOUR cluster, no hosted account.

The endpoint speaks MCP over HTTP (POST /mcp) on localhost. Point your
AI client at it with the snippet printed on start. Runs until Ctrl-C.

  tiny mcp --print   just print the client config and exit`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if printOnly {
				printMCPConfig()
				return nil
			}
			return runMCP(cmd)
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the MCP client config snippet and exit (don't serve)")
	return cmd
}

func runMCP(cmd *cobra.Command) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	bundle, cleanup, err := buildKubeBundle()
	if err != nil {
		return err
	}
	defer cleanup()

	server := mcpsrv.NewServer(buildRegistry(), bundle)
	transport := mcpsrv.NewHTTPTransport(server, fmt.Sprintf("127.0.0.1:%d", mcpPort))

	url := fmt.Sprintf("http://localhost:%d/mcp", mcpPort)

	fmt.Println()
	fmt.Println("  " + banner())
	fmt.Printf("\n  %s %s   %s %s\n",
		styleKey.Render("context"), styleTitle.Render(targetContext()),
		styleKey.Render("namespace"), styleTitle.Render(flagNamespace))
	fmt.Printf("  %s %s\n", styleKey.Render("serving"), styleURL.Render(url))

	// Single-command connection: wire the endpoint into Claude Code for the
	// user unless they opted out. Falls back to the paste snippet when the
	// claude CLI isn't installed (or for other MCP clients).
	if flagNoRegister {
		printMCPConfig()
	} else if status, found := registerWithClaude(url); found {
		fmt.Printf("  %s %s\n", styleOK.Render("✓"), status)
		fmt.Println("  " + styleSubtle.Render("other MCP clients → "+url))
	} else {
		printMCPConfig()
	}

	fmt.Println("\n  " + styleSubtle.Render("Ctrl-C to stop."))
	fmt.Println()

	return transport.Run(ctx)
}

// buildKubeBundle assembles the execution context the MCP tools run against:
// the local cluster via kubeconfig, plus the public catalog for discovery.
// NATS + OTEL locations match what `tiny up` installs (tinysystems-nats /
// tinysystems-otel-collector in the target namespace), not the platform's
// defaults — otherwise signals and traces would look in the wrong place.
func buildKubeBundle() (backend.Bundle, backend.Cleanup, error) {
	bundle, cleanup, err := backendkube.New(backendkube.Options{
		Context:       flagContext,
		Namespace:     flagNamespace,
		OtelService:   "tinysystems-otel-collector",
		OtelPort:      2345,
		NatsNamespace: flagNamespace,
		NatsService:   "tinysystems-nats",
		NatsPort:      4222,
	})
	if err != nil {
		return backend.Bundle{}, nil, err
	}
	solutionSearcher, err := adapters.NewPublicSolutionSearcher(publicAPIURL, "")
	if err != nil {
		cleanup()
		return backend.Bundle{}, nil, fmt.Errorf("init solution searcher: %w", err)
	}
	moduleCatalogPublic, err := adapters.NewPublicModuleCatalog(publicAPIURL, "")
	if err != nil {
		cleanup()
		return backend.Bundle{}, nil, fmt.Errorf("init module catalog: %w", err)
	}
	bundle.SolutionSearcher = solutionSearcher
	bundle.PublicModuleCatalog = moduleCatalogPublic

	// Install-on-the-fly: let the agent helm-install modules through the
	// endpoint, the same path `tiny install` uses. Best-effort — if the
	// kubeconfig can't be resolved here, install_module falls back to telling
	// the agent to run `tiny install` by hand.
	if cfg, err := kube.RestConfig(flagContext); err == nil {
		bundle.ModuleInstaller = installer.New(cfg, flagNamespace)
	}

	return bundle, cleanup, nil
}

// buildRegistry registers the tool set an agent uses to read and build on
// the local cluster. Mirrors the mcp-server's public tool set; install/
// uninstall return a "run helm / tiny install" hint in kubeconfig mode.
func buildRegistry() *sdktools.Registry {
	r := sdktools.NewRegistry()
	r.Register(sdktools.NewGetInstructionsTool(sdktools.CorePrompt + "\n\n" + prompt.PublicAppendix))
	r.Register(sdktools.NewListModulesTool())
	r.Register(sdktools.NewGetComponentInfoTool())
	r.Register(sdktools.NewListProjectsTool())
	r.Register(sdktools.NewReadProjectTool())
	r.Register(sdktools.NewCreateFlowTool())
	r.Register(sdktools.NewDeleteFlowTool())
	r.Register(sdktools.NewEditFlowTool())
	r.Register(sdktools.NewBuildFlowTool())
	r.Register(sdktools.NewGetNodePortSchemaTool())
	r.Register(sdktools.NewGetTracesTool())
	r.Register(sdktools.NewGetTraceDetailTool())
	r.Register(sdktools.NewScenariosTool())
	r.Register(sdktools.NewSearchSolutionsTool())
	r.Register(sdktools.NewGetSolutionTool())
	r.Register(sdktools.NewCloneSolutionTool())
	r.Register(sdktools.NewSearchModulesTool())
	r.Register(sdktools.NewGetModuleInfoTool())
	r.Register(sdktools.NewGetDashboardTool())
	r.Register(sdktools.NewInstallModuleTool())
	r.Register(sdktools.NewUninstallModuleTool())
	return r
}

func printMCPConfig() {
	fmt.Print("\n  Add this to your MCP client (Claude Code / Cursor):\n\n")
	snippet := fmt.Sprintf("\"tinysystems\": {\n  \"url\": \"http://localhost:%d/mcp\"\n}", mcpPort)
	fmt.Println(styleBox.Render(snippet))
}
