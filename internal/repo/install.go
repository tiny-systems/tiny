package repo

import (
	"context"
	"fmt"
	"strings"

	"sigs.k8s.io/yaml"
)

// Helm is the subset of helm the installer needs. provision.Client will satisfy
// it via a thin adapter at cutover time; tests fake it. Keeping the executor
// behind this interface is what makes install planning testable without a
// cluster and keeps this package free of the helm dependency graph.
type Helm interface {
	// UpgradeInstall installs or upgrades release in namespace from chart at
	// version (a range or exact), with the given merged values.
	UpgradeInstall(ctx context.Context, release, namespace, chart, version string, values map[string]any) error
}

// Installer applies an InstallPlan: the module's harness release plus one helm
// release per selected bundle. It performs no resolution or planning — hand it
// a plan and the module's rendered values.
type Installer struct {
	helm Helm
}

// NewInstaller returns an installer over a Helm backend.
func NewInstaller(h Helm) *Installer { return &Installer{helm: h} }

// Apply installs the module release and its bundles. values is the module's
// rendered values overlay (see RenderValues). It refuses to run with unfilled
// cluster holes so a half-configured install can't reach the cluster.
func (i *Installer) Apply(ctx context.Context, plan *InstallPlan, values map[string]any) error {
	if len(plan.MissingFills) > 0 {
		return fmt.Errorf("missing cluster settings: %s (set them on `tiny up`)", strings.Join(plan.MissingFills, ", "))
	}
	if err := i.helm.UpgradeInstall(ctx, plan.ReleaseName, plan.Namespace, plan.Chart, plan.ChartVersion, values); err != nil {
		return fmt.Errorf("install %s: %w", plan.ReleaseName, err)
	}
	for _, b := range plan.Bundles {
		if err := i.helm.UpgradeInstall(ctx, b.ReleaseName, plan.Namespace, b.Chart, b.ChartVersion, nil); err != nil {
			return fmt.Errorf("install bundle %s: %w", b.Name, err)
		}
	}
	return nil
}

// RenderValues fills a module's raw values.yaml holes (`${cluster.<name>}`) from
// the resolved cluster settings and parses the result. An unresolved hole is an
// error, not a silent blank — a half-filled values file must never reach helm.
func RenderValues(raw []byte, fills map[string]string) (map[string]any, error) {
	s := string(raw)
	for k, v := range fills {
		s = strings.ReplaceAll(s, "${cluster."+k+"}", v)
	}
	if i := strings.Index(s, "${cluster."); i >= 0 {
		ph := s[i:]
		if end := strings.IndexByte(ph, '}'); end >= 0 {
			ph = ph[:end+1]
		}
		return nil, fmt.Errorf("unresolved cluster value %s — provide it in cluster settings", ph)
	}
	var out map[string]any
	if err := yaml.Unmarshal([]byte(s), &out); err != nil {
		return nil, fmt.Errorf("parse values: %w", err)
	}
	return out, nil
}
