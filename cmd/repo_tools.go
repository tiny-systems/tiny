package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	sdktools "github.com/tiny-systems/module/pkg/tools"

	"github.com/tiny-systems/tiny/internal/repo"
)

// The decentralized module-discovery tools. They replace the platform's
// search_modules: an agent scans the repo index (cheap, all modules) to
// shortlist, then fetches a candidate's README (from its source repo) before
// installing. Both read the local repo state (~/.tiny) directly, so they need
// nothing from the execution bundle.

// listAvailableModulesTool — the discovery scan layer.
type listAvailableModulesTool struct{}

func (listAvailableModulesTool) Name() string { return "list_available_modules" }

func (listAvailableModulesTool) Description() string {
	return "List modules AVAILABLE to install from the configured repos (the decentralized index): name, one-line description, category, source repo, and latest version. Use this to discover a capability the cluster doesn't have yet — scan the descriptions to shortlist candidates, then fetch a candidate's full docs with get_module_readme before installing. For modules ALREADY installed (and their component schemas), use list_modules / get_module_info instead."
}

func (listAvailableModulesTool) Schema() map[string]interface{} {
	return map[string]interface{}{"type": "object", "properties": map[string]interface{}{}}
}

func (listAvailableModulesTool) Execute(ctx context.Context, _ sdktools.ExecutionContext, _ map[string]interface{}) sdktools.ToolResult {
	store, err := repo.Open()
	if err != nil {
		return sdktools.ToolResult{Success: false, Error: err.Error()}
	}
	_ = store.Update(ctx) // best-effort refresh; List still works off cache
	merged, err := store.Merged()
	if err != nil {
		return sdktools.ToolResult{Success: false, Error: err.Error()}
	}
	mods := merged.List()
	return sdktools.ToolResult{Success: true, Output: map[string]interface{}{
		"modules": mods,
		"total":   len(mods),
	}}
}

// getModuleReadmeTool — the detail layer: fetch a module's README markdown.
type getModuleReadmeTool struct{}

func (getModuleReadmeTool) Name() string { return "get_module_readme" }

func (getModuleReadmeTool) Description() string {
	return "Fetch the full README (markdown) for a module by name, from its source repo — the detail you read before deciding to install (or to understand an installed module you want to use). Pair it with list_available_modules: shortlist from the index, then read the README of each candidate."
}

func (getModuleReadmeTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"module_name": map[string]interface{}{
				"type":        "string",
				"description": "Module name, e.g. http-module (as listed by list_available_modules).",
			},
		},
		"required": []string{"module_name"},
	}
}

func (getModuleReadmeTool) Execute(ctx context.Context, _ sdktools.ExecutionContext, input map[string]interface{}) sdktools.ToolResult {
	name, _ := input["module_name"].(string)
	if name == "" {
		return sdktools.ToolResult{Success: false, Error: "module_name required"}
	}
	store, err := repo.Open()
	if err != nil {
		return sdktools.ToolResult{Success: false, Error: err.Error()}
	}
	merged, err := store.Merged()
	if err != nil {
		return sdktools.ToolResult{Success: false, Error: err.Error()}
	}
	res, err := merged.Resolve(name)
	if err != nil {
		return sdktools.ToolResult{Success: false, Error: err.Error()}
	}
	url := res.Module.ReadmeURL()
	if url == "" {
		return sdktools.ToolResult{Success: false, Error: fmt.Sprintf("module %q has no fetchable README (source: %q)", name, res.Module.Source)}
	}
	md, err := fetchText(ctx, url)
	if err != nil {
		return sdktools.ToolResult{Success: false, Error: fmt.Sprintf("fetch README: %v", err)}
	}
	return sdktools.ToolResult{Success: true, Output: map[string]interface{}{
		"name":   name,
		"source": res.Module.Source,
		"readme": md,
	}}
}

// fetchText GETs a text resource with a short timeout and a size cap.
func fetchText(ctx context.Context, url string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10)) // 512 KiB
	return string(b), err
}
