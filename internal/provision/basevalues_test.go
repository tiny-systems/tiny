package provision

import "testing"

func TestBaseValues(t *testing.T) {
	m := BaseValues("ghcr.io/tiny-systems/http-module:2.3.1", "http-module-v2", "2.3.1", "tinysystems", "nats://tiny-nats:4222")

	if m["fullnameOverride"] != "http-module-v2" {
		t.Errorf("fullnameOverride = %v", m["fullnameOverride"])
	}
	if m["natsURL"] != "nats://tiny-nats:4222" {
		t.Errorf("natsURL = %v", m["natsURL"])
	}
	mgr := m["controllerManager"].(map[string]any)["manager"].(map[string]any)
	img := mgr["image"].(map[string]any)
	if img["repository"] != "ghcr.io/tiny-systems/http-module" || img["tag"] != "2.3.1" {
		t.Errorf("image = %v", img)
	}
	args := mgr["args"].([]string)
	if !contains(args, "--name=http-module-v2") || !contains(args, "--version=2.3.1") || !contains(args, "--namespace=tinysystems") {
		t.Errorf("args missing identity flags: %v", args)
	}

	// empty broker → natsURL key absent
	if _, ok := BaseValues("x:1", "r-v0", "1", "ns", "")["natsURL"]; ok {
		t.Error("natsURL should be absent when broker url is empty")
	}
}

func TestSplitImage(t *testing.T) {
	cases := []struct{ in, repo, tag string }{
		{"ghcr.io/tiny-systems/http-module:2.3.1", "ghcr.io/tiny-systems/http-module", "2.3.1"},
		{"localhost:5000/x", "localhost:5000/x", ""},
		{"repo/x", "repo/x", ""},
		{"localhost:5000/x:1.0.0", "localhost:5000/x", "1.0.0"},
	}
	for _, c := range cases {
		r, tg := splitImage(c.in)
		if r != c.repo || tg != c.tag {
			t.Errorf("splitImage(%q) = (%q,%q), want (%q,%q)", c.in, r, tg, c.repo, c.tag)
		}
	}
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
