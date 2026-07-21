package repo

import (
	"fmt"
	"sort"
	"strings"
)

// InstallPlan is the concrete description of what installing a resolved module
// entails. It is produced with no side effects (no helm, no network), so it can
// be shown and confirmed before anything runs. The actual values-file fetch +
// hole substitution + helm calls are the installer's job (a later phase); this
// hands it every coordinate it needs.
type InstallPlan struct {
	ReleaseName  string            // <module>-v<major> — coexistence-safe
	Namespace    string            //
	Image        string            // ghcr ref
	Digest       string            // for cosign/digest verification
	Chart        string            // harness chart name
	ChartVersion string            // compatible range
	Cosign       bool              // verify before install
	Fills        map[string]string // cluster holes the module asked for → values
	MissingFills []string          // holes the module needs but the cluster didn't provide
	Bundles      []BundlePlan      // extra helm releases to install alongside
}

// BundlePlan is one bundle's install — its own helm release next to the module.
type BundlePlan struct {
	Name         string
	Chart        string
	ChartVersion string
	ReleaseName  string // <moduleRelease>-<bundle>
}

// noneBundle is the sentinel meaning "install zero bundles".
const noneBundle = "none"

// Plan turns a resolved target into an InstallPlan. clusterSettings supplies
// values for the module's cluster holes (ingressClass, storageClass, …);
// selected chooses bundles: nil → module defaults, ["none"] → zero, else
// exactly those named. It executes nothing.
func (r *Resolved) Plan(namespace string, clusterSettings map[string]string, selected []string) (*InstallPlan, error) {
	release, err := r.ReleaseName()
	if err != nil {
		return nil, err
	}
	v := r.Version

	fills := map[string]string{}
	var missing []string
	for _, hole := range v.ClusterFills {
		if val, ok := clusterSettings[hole]; ok && val != "" {
			fills[hole] = val
		} else {
			missing = append(missing, hole)
		}
	}
	sort.Strings(missing)

	selBundles, err := selectBundles(v.Bundles, selected)
	if err != nil {
		return nil, err
	}
	plans := make([]BundlePlan, 0, len(selBundles))
	for _, b := range selBundles {
		plans = append(plans, BundlePlan{
			Name:         b.Name,
			Chart:        b.Chart,
			ChartVersion: b.ChartVersion,
			ReleaseName:  release + "-" + b.Name,
		})
	}

	return &InstallPlan{
		ReleaseName:  release,
		Namespace:    namespace,
		Image:        v.Image,
		Digest:       v.Digest,
		Chart:        v.Chart,
		ChartVersion: v.ChartVersion,
		Cosign:       v.Cosign,
		Fills:        fills,
		MissingFills: missing,
		Bundles:      plans,
	}, nil
}

// selectBundles applies the bundle selection semantics (§3.5).
func selectBundles(available []Bundle, selected []string) ([]Bundle, error) {
	switch {
	case selected == nil: // module defaults
		var def []Bundle
		for _, b := range available {
			if b.DefaultOn {
				def = append(def, b)
			}
		}
		return def, nil
	case len(selected) == 1 && selected[0] == noneBundle: // explicit zero
		return nil, nil
	default: // exactly those named
		byName := make(map[string]Bundle, len(available))
		for _, b := range available {
			byName[b.Name] = b
		}
		out := make([]Bundle, 0, len(selected))
		for _, name := range selected {
			b, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("unknown bundle %q (available: %s)", name, bundleNames(available))
			}
			out = append(out, b)
		}
		return out, nil
	}
}

func bundleNames(bs []Bundle) string {
	if len(bs) == 0 {
		return "none"
	}
	names := make([]string, len(bs))
	for i, b := range bs {
		names[i] = b.Name
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
