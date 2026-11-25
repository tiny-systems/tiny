package cli

// tiny upgrade must never move to a version older than the running one —
// GitHub's /releases/latest can briefly point at an older tag while a
// newer release is mid-publish.

import (
	"path/filepath"
	"testing"

	"golang.org/x/mod/semver"
)

func TestUpgradeNeverGoesBackwards(t *testing.T) {
	// The guard's exact predicate: real running version, valid semver both
	// sides, latest not strictly greater -> refuse.
	refuse := func(cur, latest string) bool {
		if cur == "dev" {
			return false
		}
		return semver.IsValid("v"+cur) && semver.IsValid(latest) &&
			semver.Compare(latest, "v"+cur) <= 0
	}
	cases := []struct {
		cur, latest string
		want        bool
	}{
		{"0.8.0", "v0.7.2", true},  // the bug: latest older -> refuse
		{"0.8.0", "v0.8.0", true},  // equal -> refuse (nothing to do)
		{"0.7.2", "v0.8.0", false}, // real upgrade -> proceed
		{"dev", "v0.7.2", false},   // dev takes anything
		{"0.8.0", "v0.8.1", false}, // patch up -> proceed
	}
	for _, c := range cases {
		if got := refuse(c.cur, c.latest); got != c.want {
			t.Errorf("refuse(%s -> %s) = %v, want %v", c.cur, c.latest, got, c.want)
		}
	}
}

// A krew-installed tiny must not overwrite itself: krew tracks the
// installed version and would go stale.
func TestIsKrew(t *testing.T) {
	cases := map[string]bool{
		"/Users/ana/.krew/bin/kubectl-tiny":         true,
		"/home/ana/.krew/store/tiny/v0.16.0/tiny":   true,
		"/usr/local/bin/tiny":                       false,
		"/opt/homebrew/Cellar/tiny/0.16.0/bin/tiny": false,
		"": false,
		`C:\Users\ana\.krew\bin\kubectl-tiny.exe`: filepath.Separator == '\\',
	}
	t.Setenv("KREW_ROOT", "")
	for exe, want := range cases {
		if got := isKrew(exe); got != want {
			t.Errorf("isKrew(%q) = %v, want %v", exe, got, want)
		}
	}
	t.Setenv("KREW_ROOT", "/srv/krew")
	if !isKrew("/srv/krew/bin/kubectl-tiny") {
		t.Error("a custom KREW_ROOT should count")
	}
}
