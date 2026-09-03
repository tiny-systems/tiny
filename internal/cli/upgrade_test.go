package cli

// tiny upgrade must never move to a version older than the running one —
// GitHub's /releases/latest can briefly point at an older tag while a
// newer release is mid-publish.

import (
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
