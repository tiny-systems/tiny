// Package installer implements sdktools.ModuleInstaller for kubeconfig mode:
// it lets an agent install a capability module on the fly, mid-build, through
// the MCP endpoint — resolving from the configured repos (default: the public
// index) and helm-installing via the exact same repo-model path as
// `tiny install`. No platform. Without this, install_module tells the agent to
// go run helm itself.
package installer

import (
	"context"
	"fmt"

	sdktools "github.com/tiny-systems/module/pkg/tools"
	"k8s.io/client-go/rest"

	"github.com/tiny-systems/tiny/internal/provision"
	"github.com/tiny-systems/tiny/internal/repo"
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

// InstallModule resolves moduleName from the configured repos and installs it
// via the repo model (resolve → plan → harness base ⊕ overlay → helm) — the
// same path as `tiny install`, no platform. version pins a version (empty =
// latest); bundles select optional bundles (nil = module defaults, ["none"] =
// zero). Install failures come back as InstallResult{Success:false} (not a Go
// error) so the agent gets a clean message plus the progress transcript.
func (m *ModuleInstaller) InstallModule(ctx context.Context, moduleName, version string, bundles []string, onProgress func(sdktools.InstallProgress)) (*sdktools.InstallResult, error) {
	progress := func(stage, msg, logType string) {
		if onProgress != nil {
			onProgress(sdktools.InstallProgress{Stage: stage, Message: msg, LogType: logType})
		}
	}
	if moduleName == "" {
		return nil, fmt.Errorf("module_name required")
	}

	progress("resolve", "resolving "+moduleName+" from the configured repos", "info")
	store, err := repo.Open()
	if err != nil {
		return &sdktools.InstallResult{Success: false, Error: err.Error()}, nil
	}
	if err := store.Update(ctx); err != nil {
		progress("resolve", "repo update: "+err.Error(), "warning") // resolve can still run off cache
	}
	merged, err := store.Merged()
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

	// Cluster-provided values (ingress/storage/broker) — same holes `tiny
	// install` fills, from the namespace's saved settings.
	settings, _ := provision.LoadSettings(ctx, m.cfg, m.namespace)
	cluster := map[string]string{"brokerURL": provision.BrokerURL(ctx, m.cfg, m.namespace)}
	if settings.IngressClass != "" {
		cluster["ingressClass"] = settings.IngressClass
	}
	if settings.StorageClass != "" {
		cluster["storageClass"] = settings.StorageClass
	}

	ref := moduleName
	if version != "" {
		ref = moduleName + "@" + version
	}
	progress("install", "installing "+ref+" — this can take a minute while the image pulls", "info")
	plan, err := repo.Install(ctx, merged, ref, m.namespace, cluster, bundles, provision.BaseValues, hc)
	if err != nil {
		return &sdktools.InstallResult{Success: false, Error: err.Error()}, nil
	}

	progress("done", "installed "+ref+" (release "+plan.ReleaseName+")", "success")
	return &sdktools.InstallResult{Success: true, ReleaseName: plan.ReleaseName}, nil
}
