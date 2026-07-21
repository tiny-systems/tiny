package repo

import (
	"reflect"
	"testing"
)

func resolvedFixture() *Resolved {
	return &Resolved{
		Name: "http-module",
		Version: &Version{
			Version:      "2.3.1",
			Image:        "ghcr.io/tiny-systems/http-module:2.3.1",
			Digest:       "sha256:abc",
			Chart:        "tinysystems-operator",
			ChartVersion: ">=0.2.0 <0.3.0",
			Cosign:       true,
			ClusterFills: []string{"ingressClass"},
			Bundles: []Bundle{
				{Name: "pgvector", Chart: "oci://x/pgvector", ChartVersion: "0.3.1", DefaultOn: false},
				{Name: "metrics", Chart: "oci://x/metrics", ChartVersion: "1.0.0", DefaultOn: true},
			},
		},
	}
}

func TestPlanBasics(t *testing.T) {
	p, err := resolvedFixture().Plan("tinysystems", map[string]string{"ingressClass": "nginx"}, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if p.ReleaseName != "http-module-v2" {
		t.Errorf("release = %q, want http-module-v2", p.ReleaseName)
	}
	if p.Image != "ghcr.io/tiny-systems/http-module:2.3.1" || !p.Cosign {
		t.Errorf("image/cosign wrong: %+v", p)
	}
	if !reflect.DeepEqual(p.Fills, map[string]string{"ingressClass": "nginx"}) {
		t.Errorf("fills = %v", p.Fills)
	}
	if len(p.MissingFills) != 0 {
		t.Errorf("unexpected missing fills: %v", p.MissingFills)
	}
	// nil selection → module defaults → only the DefaultOn bundle (metrics)
	if len(p.Bundles) != 1 || p.Bundles[0].Name != "metrics" {
		t.Errorf("default bundles = %+v, want [metrics]", p.Bundles)
	}
	if p.Bundles[0].ReleaseName != "http-module-v2-metrics" {
		t.Errorf("bundle release = %q", p.Bundles[0].ReleaseName)
	}
}

func TestPlanMissingFill(t *testing.T) {
	p, err := resolvedFixture().Plan("tinysystems", nil, []string{"none"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !reflect.DeepEqual(p.MissingFills, []string{"ingressClass"}) {
		t.Errorf("missing = %v, want [ingressClass]", p.MissingFills)
	}
	if len(p.Bundles) != 0 {
		t.Errorf(`["none"] should install zero bundles, got %+v`, p.Bundles)
	}
}

func TestPlanExplicitBundles(t *testing.T) {
	p, err := resolvedFixture().Plan("tinysystems", map[string]string{"ingressClass": "nginx"}, []string{"pgvector"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(p.Bundles) != 1 || p.Bundles[0].Name != "pgvector" {
		t.Errorf("explicit bundles = %+v, want [pgvector]", p.Bundles)
	}

	if _, err := resolvedFixture().Plan("tinysystems", nil, []string{"nope"}); err == nil {
		t.Error("expected error for unknown bundle")
	}
}
