package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"k8s.io/client-go/rest"

	"github.com/tiny-systems/tiny/internal/adapters"
	"github.com/tiny-systems/tiny/internal/backend"
	backendkube "github.com/tiny-systems/tiny/internal/backend/kube"
	"github.com/tiny-systems/tiny/internal/flow"
	"github.com/tiny-systems/tiny/internal/installer"
	"github.com/tiny-systems/tiny/internal/kube"
	mcpsrv "github.com/tiny-systems/tiny/internal/mcp"
	"github.com/tiny-systems/tiny/internal/project"
	"github.com/tiny-systems/tiny/internal/prompt"
)

// publicAPIURL is the anonymous catalog the MCP server reads to discover
// installable modules and solutions — same endpoint `tiny install` uses.
const publicAPIURL = "https://api.tinysystems.io"

func runMCP(cmd *cobra.Command) error {
	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Preflight: don't serve against a cluster we can't reach or authenticate
	// to — stop with a clear message instead of endpoints that silently fail.
	cfg, err := kube.RestConfig(flagContext)
	if err != nil {
		return err
	}
	if err := kube.Ping(cfg); err != nil {
		return clusterUnreachable(err)
	}

	// A tiny session works inside one project: select or create it now so
	// both the MCP endpoint and the editor are scoped to it.
	activeProject := chooseProject(ctx, cfg)

	bundle, cleanup, err := buildKubeBundle(activeProject)
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
	if activeProject != "" {
		fmt.Printf("  %s %s\n", styleKey.Render("project"), styleTitle.Render(activeProject))
	}
	fmt.Printf("  %s %s\n", styleKey.Render("serving"), styleURL.Render(url))

	// Start the browser editor alongside the MCP endpoint (best-effort — it
	// needs the same cluster). Prompt in Claude Code, watch it here.
	editorURL := fmt.Sprintf("http://localhost:%d", editorPort)
	go func() { _ = flow.NewService(cfg, flagNamespace).ServeEditor(ctx, fmt.Sprintf("127.0.0.1:%d", editorPort), activeProject) }()
	fmt.Printf("  %s %s%s\n", styleKey.Render("editor"), styleURL.Render(editorURL), styleSubtle.Render("   → open in your browser"))

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
// clusterUnreachable turns a failed preflight into a one-line, actionable
// error — special-casing the common expired-gcloud-token case.
func clusterUnreachable(err error) error {
	ctx := targetContext()
	if strings.Contains(err.Error(), "gcloud") {
		return fmt.Errorf("can't reach cluster %q — gcloud credentials expired; run: gcloud auth login", ctx)
	}
	msg := err.Error()
	if i := strings.IndexByte(msg, '\n'); i > 0 {
		msg = msg[:i]
	}
	return fmt.Errorf("can't reach cluster %q: %s", ctx, msg)
}

// chooseProject resolves the session project. With --project it selects or
// creates that one. Otherwise it prompts ONLY to bootstrap the first project
// when the namespace is empty — because nothing works without at least one,
// and the editor's picker would have nothing to show. When projects already
// exist it doesn't nag: it serves unpinned and you pick in the editor.
// Non-interactive (CI, piped, --yes) never prompts.
func chooseProject(ctx context.Context, cfg *rest.Config) string {
	if flagProject != "" {
		if name, err := project.Ensure(ctx, cfg, flagNamespace, flagProject); err == nil {
			return name
		}
		return ""
	}
	if flagYes || !isatty.IsTerminal(os.Stdin.Fd()) {
		return ""
	}

	names, err := project.List(ctx, cfg, flagNamespace)
	if err != nil {
		return ""
	}

	// Always offer select-or-create at start (one project per session). Only
	// when the namespace is empty do we skip straight to the create input.
	const newSentinel = "\x00new"
	var choice string
	if len(names) == 0 {
		choice = newSentinel
	} else {
		opts := make([]huh.Option[string], 0, len(names)+1)
		for _, n := range names {
			opts = append(opts, huh.NewOption(n, n))
		}
		opts = append(opts, huh.NewOption("＋ Create a new project…", newSentinel))
		if err := huh.NewSelect[string]().
			Title("Project for this session").
			Description("↑↓ to move · ↵ to select · esc to skip").
			Options(opts...).
			Value(&choice).
			WithTheme(tinyHuhTheme()).
			Run(); err != nil {
			return "" // ctrl-c / esc → serve unpinned
		}
	}

	if choice == newSentinel {
		var name string
		if err := huh.NewInput().
			Title("New project").
			Description("in namespace " + flagNamespace).
			Placeholder("my-project").
			Value(&name).
			WithTheme(tinyHuhTheme()).
			Run(); err != nil {
			return ""
		}
		return ensureTyped(ctx, cfg, strings.TrimSpace(name))
	}
	return choice
}

// tinyHuhTheme is the charm form theme in tiny's indigo.
func tinyHuhTheme() *huh.Theme {
	t := huh.ThemeBase()
	indigo := lipgloss.Color("#6366f1")
	subtle := lipgloss.Color("#6b7280")
	t.Focused.Title = t.Focused.Title.Foreground(indigo).Bold(true)
	t.Focused.Description = t.Focused.Description.Foreground(subtle)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(indigo).Bold(true)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(indigo)
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(indigo)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(indigo)
	return t
}

func ensureTyped(ctx context.Context, cfg *rest.Config, name string) string {
	if name == "" {
		return ""
	}
	rn, err := project.Ensure(ctx, cfg, flagNamespace, name)
	if err != nil {
		fmt.Printf("  %s %v\n", styleWarn.Render("couldn't create project:"), err)
		return ""
	}
	return rn
}

func buildKubeBundle(activeProject string) (backend.Bundle, backend.Cleanup, error) {
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

	// Pin the session project so the agent works inside it by default (tools
	// that omit `project` inherit this) — the local mode's "one session, one
	// project" model.
	if activeProject != "" {
		bundle.ProjectName = activeProject
	}

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
