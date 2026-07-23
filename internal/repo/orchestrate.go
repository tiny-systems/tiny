package repo

import "context"

// BaseValues builds the harness (operator-chart) values every module install
// needs, from the plan's identity fields. provision.BaseValues satisfies it —
// injected so this package stays free of the helm/chart dependency graph.
type BaseValues func(image, release, version, namespace, natsURL string) map[string]any

// brokerFill is the cluster setting that carries the NATS/broker URL into the
// harness base values.
const brokerFill = "brokerURL"

// Install runs the full pipeline for one module reference:
//
//	resolve   → find the module version across the merged index
//	plan      → derive release name (<repo>-<module>-v<major>), image, fills, bundles
//	reconcile → settle that name against what is already installed: adopt a
//	            legacy pre-publisher release, refuse another publisher's
//	values    → harness base (identity/plumbing) ⊕ module overlay (rbac/ingress/…),
//	            the overlay's ${cluster.*} holes filled and winning on conflicts
//	apply     → helm-install the module release + each bundle
//
// The module's values.yaml is inlined in the index (self-contained/offline).
// base + helm + installed are injected so this stays the pure orchestration
// seam. installed may be nil (fresh-cluster semantics: no adoption, no guard).
func Install(
	ctx context.Context,
	merged *Merged,
	ref, namespace string,
	clusterSettings map[string]string,
	selectedBundles []string,
	base BaseValues,
	helm Helm,
	installed InstalledModules,
) (*InstallPlan, error) {
	resolved, err := merged.Resolve(ref)
	if err != nil {
		return nil, err
	}
	plan, err := resolved.Plan(namespace, clusterSettings, selectedBundles)
	if err != nil {
		return nil, err
	}
	// Settle identity BEFORE any values are derived from the release name.
	if err := reconcileIdentity(ctx, installed, resolved, plan); err != nil {
		return nil, err
	}

	overlay, err := RenderValues([]byte(resolved.Version.Values), plan.Fills)
	if err != nil {
		return nil, err
	}
	baseVals := base(plan.Image, plan.ReleaseName, plan.Version, plan.Namespace, clusterSettings[brokerFill])
	values := mergeValues(baseVals, overlay) // overlay wins on conflicts

	if err := NewInstaller(helm).Apply(ctx, plan, values); err != nil {
		return nil, err
	}
	return plan, nil
}

// mergeValues deep-merges overlay onto base (overlay wins). Nested maps merge
// recursively; any non-map value from overlay replaces base's.
func mergeValues(base, overlay map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		if bm, ok := out[k].(map[string]any); ok {
			if om, ok := v.(map[string]any); ok {
				out[k] = mergeValues(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}
