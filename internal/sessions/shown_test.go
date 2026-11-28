package sessions

import (
	"testing"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

func TestPickShown(t *testing.T) {
	shown := []agentsv1.ShownFile{{Path: "/w/a.png"}, {Path: "/w/b.png"}, {Path: "/w/c.png"}}
	for which, want := range map[string]string{"": "/w/c.png", "latest": "/w/c.png", "1": "/w/a.png", "2": "/w/b.png"} {
		f, err := PickShown(shown, which)
		if err != nil || f.Path != want {
			t.Errorf("PickShown(%q) = %q, %v; want %q", which, f.Path, err, want)
		}
	}
	for _, bad := range []string{"0", "4", "x"} {
		if _, err := PickShown(shown, bad); err == nil {
			t.Errorf("PickShown(%q) accepted", bad)
		}
	}
	if _, err := PickShown(nil, ""); err == nil {
		t.Error("PickShown on nothing accepted")
	}
}

func TestShownNameStaysAFileName(t *testing.T) {
	for p, want := range map[string]string{"/workspace/shot.png": "shot.png", "/workspace/.hidden": "hidden", "/": "shown", "/a/..": "shown"} {
		if got := ShownName(agentsv1.ShownFile{Path: p}); got != want {
			t.Errorf("ShownName(%q) = %q, want %q", p, got, want)
		}
	}
}
