package repo

import (
	"context"
	"strings"
	"testing"
)

// fakeInstalled answers Lookup from a fixed map of release name → image.
type fakeInstalled map[string]string

func (f fakeInstalled) Lookup(_ context.Context, _, release string) (string, bool, error) {
	img, ok := f[release]
	if !ok {
		return "", false, nil
	}
	return img, true, nil
}

func planFor(repo, module, version, image string) (*Resolved, *InstallPlan) {
	r := &Resolved{Repo: repo, Name: module, Version: &Version{Version: version, Image: image}}
	release, _ := r.ReleaseName()
	return r, &InstallPlan{ReleaseName: release, Namespace: "tinysystems", Image: image}
}

func TestReconcileAdoptsLegacyRelease(t *testing.T) {
	// A cluster installed before §7.1 holds `http-module-v0`. Installing the
	// qualified name would stand a second copy beside it while existing flow
	// nodes stay bound to the old one.
	r, plan := planFor("tinysystems", "http-module", "0.5.26", "ghcr.io/tiny-systems/http-module:0.5.26")
	if plan.ReleaseName != "tinysystems-http-module-v0" {
		t.Fatalf("precondition: got %q", plan.ReleaseName)
	}

	installed := fakeInstalled{"http-module-v0": "ghcr.io/tiny-systems/http-module:0.5.25"}
	if err := reconcileIdentity(context.Background(), installed, r, plan); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if plan.ReleaseName != "http-module-v0" {
		t.Fatalf("did not adopt legacy release: got %q", plan.ReleaseName)
	}
}

func TestReconcileAdoptionRenamesBundles(t *testing.T) {
	r, plan := planFor("tinysystems", "database-module", "0.6.6", "ghcr.io/tiny-systems/database-module:0.6.6")
	plan.Bundles = []BundlePlan{{Name: "pgvector", ReleaseName: plan.ReleaseName + "-pgvector"}}

	installed := fakeInstalled{"database-module-v0": "ghcr.io/tiny-systems/database-module:0.6.6"}
	if err := reconcileIdentity(context.Background(), installed, r, plan); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if plan.Bundles[0].ReleaseName != "database-module-v0-pgvector" {
		t.Fatalf("bundle not renamed with the adopted release: %q", plan.Bundles[0].ReleaseName)
	}
}

func TestReconcileRefusesAnotherPublishersRelease(t *testing.T) {
	// The dash ambiguity: "tiny-systems"+"http-module" and "tiny"+"systems-http-module"
	// generate the same release name. The occupant runs a different image, so
	// installing over it would replace someone else's module.
	r, plan := planFor("tiny-systems", "http-module", "0.5.26", "ghcr.io/tiny-systems/http-module:0.5.26")
	installed := fakeInstalled{plan.ReleaseName: "ghcr.io/acme/systems-http-module:1.0.0"}

	err := reconcileIdentity(context.Background(), installed, r, plan)
	if err == nil {
		t.Fatal("expected a refusal, got nil")
	}
	if !strings.Contains(err.Error(), "another publisher") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestReconcileUpgradesOwnRelease(t *testing.T) {
	// Same module already at the qualified name — a plain version upgrade must
	// not be mistaken for a collision.
	r, plan := planFor("tinysystems", "http-module", "0.5.26", "ghcr.io/tiny-systems/http-module:0.5.26")
	installed := fakeInstalled{plan.ReleaseName: "ghcr.io/tiny-systems/http-module:0.5.25"}

	if err := reconcileIdentity(context.Background(), installed, r, plan); err != nil {
		t.Fatalf("upgrade of our own release refused: %v", err)
	}
	if plan.ReleaseName != "tinysystems-http-module-v0" {
		t.Fatalf("release name changed unexpectedly: %q", plan.ReleaseName)
	}
}

func TestReconcileFreshInstallUnchanged(t *testing.T) {
	r, plan := planFor("tinysystems", "http-module", "0.5.26", "ghcr.io/tiny-systems/http-module:0.5.26")
	if err := reconcileIdentity(context.Background(), fakeInstalled{}, r, plan); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if plan.ReleaseName != "tinysystems-http-module-v0" {
		t.Fatalf("fresh install should keep the qualified name, got %q", plan.ReleaseName)
	}
}

func TestReconcileNilLookupIsNoop(t *testing.T) {
	r, plan := planFor("tinysystems", "http-module", "0.5.26", "ghcr.io/tiny-systems/http-module:0.5.26")
	if err := reconcileIdentity(context.Background(), nil, r, plan); err != nil {
		t.Fatalf("nil lookup should be a no-op: %v", err)
	}
	if plan.ReleaseName != "tinysystems-http-module-v0" {
		t.Fatalf("plan mutated with nil lookup: %q", plan.ReleaseName)
	}
}

func TestImageRepoStripsTagAndDigest(t *testing.T) {
	cases := map[string]string{
		"ghcr.io/tiny-systems/http-module:0.5.26":     "ghcr.io/tiny-systems/http-module",
		"ghcr.io/tiny-systems/http-module@sha256:abc": "ghcr.io/tiny-systems/http-module",
		"ghcr.io/tiny-systems/http-module":            "ghcr.io/tiny-systems/http-module",
		"localhost:5000/http-module:1.0":              "localhost:5000/http-module",
		"http-module:1.0":                             "http-module",
	}
	for in, want := range cases {
		if got := imageRepo(in); got != want {
			t.Errorf("imageRepo(%q) = %q, want %q", in, got, want)
		}
	}
}
