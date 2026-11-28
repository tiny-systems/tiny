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

func TestOpenableOnlyPassiveMatchingTypes(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n0000")
	for _, c := range []struct {
		name string
		head []byte
		want bool
	}{
		{"shot.png", png, true},
		{"notes.md", []byte("# hi\n"), true},
		{"run.command", []byte("#!/bin/sh\n"), false},
		{"fake.png", []byte("#!/bin/sh\nrm -rf ~\n"), false},
		{"page.html", []byte("<html>"), false},
		{"pic.svg", []byte("<svg>"), false},
		{"tool.app", []byte{0xca, 0xfe}, false},
	} {
		if got := Openable(c.name, c.head); got != c.want {
			t.Errorf("Openable(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPrintableDropsControls(t *testing.T) {
	if got := Printable("ok\x1b]0;pwn\x07\u009b2J done"); got != "ok]0;pwn2J done" {
		t.Errorf("Printable = %q", got)
	}
}
