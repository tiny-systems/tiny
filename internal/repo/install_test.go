package repo

import (
	"context"
	"reflect"
	"testing"
)

// fakeHelm records UpgradeInstall calls instead of touching a cluster.
type fakeHelm struct {
	calls []helmCall
	fail  bool
}
type helmCall struct {
	release, namespace, chart, version string
	values                             map[string]any
}

func (f *fakeHelm) UpgradeInstall(_ context.Context, release, namespace, chart, version string, values map[string]any) error {
	if f.fail {
		return context.Canceled
	}
	f.calls = append(f.calls, helmCall{release, namespace, chart, version, values})
	return nil
}

func TestApplyInstallsModuleThenBundles(t *testing.T) {
	plan := &InstallPlan{
		ReleaseName:  "http-module-v2",
		Namespace:    "tinysystems",
		Chart:        "tinysystems-operator",
		ChartVersion: ">=0.2.0 <0.3.0",
		Bundles: []BundlePlan{
			{Name: "pgvector", Chart: "oci://x/pgvector", ChartVersion: "0.3.1", ReleaseName: "http-module-v2-pgvector"},
		},
	}
	h := &fakeHelm{}
	if err := NewInstaller(h).Apply(context.Background(), plan, map[string]any{"image": "ghcr.io/x:2.3.1"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(h.calls) != 2 {
		t.Fatalf("got %d helm calls, want 2 (module + bundle)", len(h.calls))
	}
	if h.calls[0].release != "http-module-v2" || h.calls[0].chart != "tinysystems-operator" {
		t.Errorf("module call wrong: %+v", h.calls[0])
	}
	if h.calls[1].release != "http-module-v2-pgvector" || h.calls[1].chart != "oci://x/pgvector" {
		t.Errorf("bundle call wrong: %+v", h.calls[1])
	}
}

func TestApplyRefusesMissingFills(t *testing.T) {
	plan := &InstallPlan{ReleaseName: "x-v0", MissingFills: []string{"ingressClass"}}
	h := &fakeHelm{}
	if err := NewInstaller(h).Apply(context.Background(), plan, nil); err == nil {
		t.Fatal("expected refusal with missing fills")
	}
	if len(h.calls) != 0 {
		t.Fatalf("helm must not be called with missing fills, got %d calls", len(h.calls))
	}
}

func TestRenderValues(t *testing.T) {
	raw := []byte("image: ghcr.io/x:1.0.0\ningress:\n  className: \"${cluster.ingressClass}\"\n")
	got, err := RenderValues(raw, map[string]string{"ingressClass": "nginx"})
	if err != nil {
		t.Fatalf("RenderValues: %v", err)
	}
	want := map[string]any{
		"image": "ghcr.io/x:1.0.0",
		"ingress": map[string]any{"className": "nginx"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rendered = %#v, want %#v", got, want)
	}

	// an unfilled hole is an error, not a silent blank
	if _, err := RenderValues(raw, nil); err == nil {
		t.Error("expected error for unresolved ${cluster.ingressClass}")
	}
}
