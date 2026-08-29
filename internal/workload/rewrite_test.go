package workload

// The tag colon in "golang:1.26" once read as a registry host and the
// rewrite silently skipped every single-segment image — the exact images
// the cache exists for.

import "testing"

func TestRewriteThroughCache(t *testing.T) {
	const ip = "10.0.0.7"
	cases := map[string]string{
		"golang:1.26":                "10.0.0.7:5000/library/golang:1.26",
		"busybox":                    "10.0.0.7:5000/library/busybox",
		"tinygo/tinygo:0.31":         "10.0.0.7:5000/tinygo/tinygo:0.31",
		"ghcr.io/tiny-systems/agent": "ghcr.io/tiny-systems/agent",
		"quay.io/x/y:z":              "quay.io/x/y:z",
		"localhost/dev:1":            "localhost/dev:1",
		"registry:5000/team/img:tag": "registry:5000/team/img:tag",
	}
	for in, want := range cases {
		if got := rewriteThroughCache(in, ip); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}
