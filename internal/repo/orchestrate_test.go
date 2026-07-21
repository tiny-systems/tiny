package repo

import (
	"context"
	"testing"
)

const fixtureWithFills = `
apiVersion: tiny/v2
modules:
  http-module:
    versions:
      - version: 2.3.1
        image: ghcr.io/tiny-systems/http-module:2.3.1
        chart: tinysystems-operator
        chartVersion: ">=0.2.0 <0.3.0"
        clusterFills: [ingressClass]
        values: |
          ingress:
            enabled: true
            className: "${cluster.ingressClass}"
`

func TestInstallFullPipeline(t *testing.T) {
	merged := NewMerged([]string{"r"}, map[string]*Index{"r": mustParse(t, fixtureWithFills)})
	h := &fakeHelm{}

	plan, err := Install(
		context.Background(), merged,
		"http-module", "tinysystems",
		map[string]string{"ingressClass": "nginx"},
		[]string{"none"},
		h,
	)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Coexistence-safe release name derived from the SemVer major.
	if plan.ReleaseName != "http-module-v2" {
		t.Errorf("release = %q, want http-module-v2", plan.ReleaseName)
	}
	// Exactly one helm call (module; no bundles), against the harness chart.
	if len(h.calls) != 1 {
		t.Fatalf("got %d helm calls, want 1", len(h.calls))
	}
	c := h.calls[0]
	if c.release != "http-module-v2" || c.chart != "tinysystems-operator" || c.namespace != "tinysystems" {
		t.Errorf("helm call wrong: %+v", c)
	}
	// The inline values' cluster hole was filled before reaching helm.
	ingress, _ := c.values["ingress"].(map[string]any)
	if ingress == nil || ingress["className"] != "nginx" {
		t.Errorf("rendered values missing filled className: %#v", c.values)
	}
}

func TestInstallRefusesMissingClusterValue(t *testing.T) {
	merged := NewMerged([]string{"r"}, map[string]*Index{"r": mustParse(t, fixtureWithFills)})
	h := &fakeHelm{}

	// No ingressClass provided → refuse before touching helm.
	if _, err := Install(context.Background(), merged, "http-module", "tinysystems", nil, []string{"none"}, h); err == nil {
		t.Fatal("expected refusal for missing cluster value")
	}
	if len(h.calls) != 0 {
		t.Fatalf("helm must not be called, got %d", len(h.calls))
	}
}
