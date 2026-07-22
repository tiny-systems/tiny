package repo

import "testing"

func TestMajor(t *testing.T) {
	ok := map[string]int{
		"2.3.1":     2,
		"v2.3.1":    2,
		"0.5.24":    0,
		"1.0.0-rc1": 1,
		"10.2.0":    10,
		"3":         3,
		"2-rc1":     2,
	}
	for in, want := range ok {
		got, err := Major(in)
		if err != nil {
			t.Errorf("Major(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("Major(%q) = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "v", "abc", "-1.0.0", "x.y.z"} {
		if _, err := Major(bad); err == nil {
			t.Errorf("Major(%q) = nil error, want error", bad)
		}
	}
}

func TestReleaseName(t *testing.T) {
	cases := []struct {
		repo  string
		name  string
		major int
		want  string
	}{
		// clean name + major → coexistence coordinate
		{"", "http-module", 2, "http-module-v2"},
		{"", "http-module", 0, "http-module-v0"},
		// legacy -vN key must not double-suffix
		{"", "http-module-v0", 0, "http-module-v0"},
		{"", "common-module-v0", 1, "common-module-v1"},

		// publisher is the second coordinate (design §7.1): the same module
		// name from two publishers must not collapse onto one release.
		{"tinysystems", "http-module", 0, "tinysystems-http-module-v0"},
		{"acme", "http-module", 0, "acme-http-module-v0"},
		{"acme", "http-module", 2, "acme-http-module-v2"},
		// legacy -vN key still must not double-suffix once prefixed
		{"acme", "http-module-v0", 1, "acme-http-module-v1"},
		// publisher names may contain dashes — tiny-systems does
		{"tiny-systems", "http-module", 0, "tiny-systems-http-module-v0"},
	}
	for _, c := range cases {
		if got := ReleaseName(c.repo, c.name, c.major); got != c.want {
			t.Errorf("ReleaseName(%q, %q, %d) = %q, want %q", c.repo, c.name, c.major, got, c.want)
		}
	}
}

// Two publishers shipping the same module name must land on different releases —
// this is the whole point of the second coordinate.
func TestReleaseNameSeparatesPublishers(t *testing.T) {
	a := ReleaseName("tinysystems", "http-module", 0)
	b := ReleaseName("acme", "http-module", 0)
	if a == b {
		t.Fatalf("publishers collapsed onto one release name: %q", a)
	}
}

// Dashes are allowed, so two different pairs CAN generate one name. That is a
// known, accepted property: the name is an opaque identity and the installer
// distinguishes them by the authoritative repo/module labels. Pinned here so
// nobody "fixes" it by banning dashes — tiny-systems would fail that rule.
func TestReleaseNameAmbiguityIsAccepted(t *testing.T) {
	if ReleaseName("tiny-systems", "http-module", 0) != ReleaseName("tiny", "systems-http-module", 0) {
		t.Skip("generated names no longer collide — the label-based collision guard may be redundant")
	}
}

func TestResolvedReleaseName(t *testing.T) {
	r := &Resolved{Repo: "tinysystems", Name: "http-module", Version: &Version{Version: "2.3.1"}}
	got, err := r.ReleaseName()
	if err != nil {
		t.Fatalf("ReleaseName: %v", err)
	}
	if got != "tinysystems-http-module-v2" {
		t.Fatalf("got %q, want tinysystems-http-module-v2", got)
	}
}
