// Package installer implements sdktools.ModuleInstaller for kubeconfig mode:
// it lets an agent install a capability module on the fly, mid-build, through
// the MCP endpoint — resolving the module from the public catalog and
// helm-installing it via the exact same path `tiny install` uses. Without
// this, install_module tells the agent to go run helm itself.
package installer

import (
	"context"
	"fmt"
	"strings"

	sdktools "github.com/tiny-systems/module/pkg/tools"
	"k8s.io/client-go/rest"

	"github.com/tiny-systems/tiny/internal/catalog"
	"github.com/tiny-systems/tiny/internal/provision"
)

// ModuleInstaller resolves + helm-installs modules onto the local cluster.
type ModuleInstaller struct {
	cfg       *rest.Config
	namespace string
}

// New returns an installer bound to one cluster + namespace.
func New(cfg *rest.Config, namespace string) *ModuleInstaller {
	return &ModuleInstaller{cfg: cfg, namespace: namespace}
}

var _ sdktools.ModuleInstaller = (*ModuleInstaller)(nil)

// InstallModule resolves moduleName from the public catalog and installs it.
// version is currently ignored (always the catalog's latest); bundles aren't
// supported by the local path yet and are surfaced as a warning. Install
// failures come back as InstallResult{Success:false} (not a Go error) so the
// agent gets a clean message plus the progress transcript.
func (m *ModuleInstaller) InstallModule(ctx context.Context, moduleName, version string, bundles []string, onProgress func(sdktools.InstallProgress)) (*sdktools.InstallResult, error) {
	progress := func(stage, msg, logType string) {
		if onProgress != nil {
			onProgress(sdktools.InstallProgress{Stage: stage, Message: msg, LogType: logType})
		}
	}

	if moduleName == "" {
		return nil, fmt.Errorf("module_name required")
	}
	if len(bundles) > 0 {
		progress("resolve", "bundles are not supported by local install yet — ignoring: "+strings.Join(bundles, ", "), "warning")
	}

	progress("resolve", "resolving "+moduleName+" from the public catalog", "info")
	mod, err := catalog.Resolve(ctx, moduleName)
	if err != nil {
		return &sdktools.InstallResult{Success: false, Error: err.Error()}, nil
	}

	progress("prepare", "preparing namespace "+m.namespace, "info")
	if err := provision.EnsureNamespace(ctx, m.cfg, m.namespace); err != nil {
		return &sdktools.InstallResult{Success: false, Error: fmt.Sprintf("prepare namespace: %v", err)}, nil
	}

	hc, err := provision.NewClient(m.cfg, m.namespace, nil)
	if err != nil {
		return &sdktools.InstallResult{Success: false, Error: fmt.Sprintf("helm client: %v", err)}, nil
	}
	broker := provision.BrokerURL(ctx, m.cfg, m.namespace)

	progress("install", "installing "+mod.FullName+" ("+mod.Tag+") — this can take a minute while the image pulls", "info")
	release, err := hc.InstallModule(ctx, mod, broker)
	if err != nil {
		return &sdktools.InstallResult{Success: false, Error: err.Error()}, nil
	}

	progress("done", "installed "+mod.FullName, "success")
	return &sdktools.InstallResult{Success: true, ReleaseName: release}, nil
}
