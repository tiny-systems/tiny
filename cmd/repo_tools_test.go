package cmd

import (
	"context"
	"os"
	"strings"
	"testing"

	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// TestDiscoveryToolsLive exercises the decentralized discovery tools against the
// real public index + a module's GitHub README. Gated by env:
//
//	TINY_TEST_LIVE=1 go test ./cmd -run DiscoveryToolsLive -v
func TestDiscoveryToolsLive(t *testing.T) {
	if os.Getenv("TINY_TEST_LIVE") == "" {
		t.Skip("set TINY_TEST_LIVE=1 to run the live discovery test")
	}
	t.Setenv("TINY_HOME", t.TempDir()) // fresh state → default repo (public index) seeded

	ctx := context.Background()

	// list_available_modules → the index scan.
	res := listAvailableModulesTool{}.Execute(ctx, sdktools.ExecutionContext{}, nil)
	if !res.Success {
		t.Fatalf("list_available_modules failed: %s", res.Error)
	}
	out, _ := res.Output.(map[string]interface{})
	total, _ := out["total"].(int)
	if total < 6 {
		t.Fatalf("expected >=6 available modules, got %d", total)
	}
	t.Logf("list_available_modules → %d modules", total)

	// get_module_readme http-module → the detail fetch.
	rd := getModuleReadmeTool{}.Execute(ctx, sdktools.ExecutionContext{}, map[string]interface{}{"module_name": "http-module"})
	if !rd.Success {
		t.Fatalf("get_module_readme failed: %s", rd.Error)
	}
	rout, _ := rd.Output.(map[string]interface{})
	readme, _ := rout["readme"].(string)
	if len(strings.TrimSpace(readme)) == 0 {
		t.Fatal("get_module_readme returned an empty README")
	}
	t.Logf("get_module_readme(http-module) → %d bytes, source=%v", len(readme), rout["source"])
}
