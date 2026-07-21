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
		name  string
		major int
		want  string
	}{
		// clean name + major → coexistence coordinate
		{"http-module", 2, "http-module-v2"},
		{"http-module", 0, "http-module-v0"},
		// legacy -vN key must not double-suffix
		{"http-module-v0", 0, "http-module-v0"},
		{"common-module-v0", 1, "common-module-v1"},
	}
	for _, c := range cases {
		if got := ReleaseName(c.name, c.major); got != c.want {
			t.Errorf("ReleaseName(%q, %d) = %q, want %q", c.name, c.major, got, c.want)
		}
	}
}

func TestResolvedReleaseName(t *testing.T) {
	r := &Resolved{Name: "http-module", Version: &Version{Version: "2.3.1"}}
	got, err := r.ReleaseName()
	if err != nil {
		t.Fatalf("ReleaseName: %v", err)
	}
	if got != "http-module-v2" {
		t.Fatalf("got %q, want http-module-v2", got)
	}
}
