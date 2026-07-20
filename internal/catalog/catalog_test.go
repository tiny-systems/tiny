package catalog

import "testing"

// TestModuleKey covers the flat-catalog lookup key derivation: a workspace
// prefix is dropped (the catalog is keyed by the bare module id), a bare name
// passes through, and the -v0 suffix is left alone (the resolve step adds it as
// a fallback).
func TestModuleKey(t *testing.T) {
	cases := map[string]string{
		"http-module":                 "http-module",
		"http-module-v0":              "http-module-v0",
		"tinysystems/http-module":     "http-module",
		"tinysystems/http-module-v0":  "http-module-v0",
		"acme/some-module-v0":         "some-module-v0",
		"":                            "",
	}
	for in, want := range cases {
		if got := moduleKey(in); got != want {
			t.Errorf("moduleKey(%q) = %q, want %q", in, got, want)
		}
	}
}
