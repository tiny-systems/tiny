package repo

import "context"

// Install runs the full pipeline for one module reference:
//
//	resolve  → find the module version across the merged index
//	plan     → derive release name (<module>-v<major>), image, fills, bundles
//	render   → fill the module values.yaml's ${cluster.*} holes
//	apply    → helm-install the module release + each bundle
//
// It returns the plan (for reporting) and performs the helm side through the
// given backend. rawValues is the module's values.yaml (with ${cluster.*}
// holes); where that comes from — inlined in the index vs fetched from the repo
// — is the next step, deliberately kept out of here so this stays the pure
// orchestration seam. Nothing calls it yet; the `install` command flip is a
// later step.
func Install(
	ctx context.Context,
	merged *Merged,
	ref, namespace string,
	clusterSettings map[string]string,
	selectedBundles []string,
	rawValues []byte,
	helm Helm,
) (*InstallPlan, error) {
	resolved, err := merged.Resolve(ref)
	if err != nil {
		return nil, err
	}
	plan, err := resolved.Plan(namespace, clusterSettings, selectedBundles)
	if err != nil {
		return nil, err
	}
	values, err := RenderValues(rawValues, plan.Fills)
	if err != nil {
		return nil, err
	}
	if err := NewInstaller(helm).Apply(ctx, plan, values); err != nil {
		return nil, err
	}
	return plan, nil
}
