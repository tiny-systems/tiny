package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const manifestHTTP = `
name: http-module
source: github.com/tiny-systems/http-module
description: HTTP client + server
category: net
versions:
  - version: 1.4.1
    image: ghcr.io/tiny-systems/http-module:1.4.1
  - version: 2.0.0
    image: ghcr.io/tiny-systems/http-module:2.0.0
  - version: 1.4.2
    image: ghcr.io/tiny-systems/http-module:1.4.2
`

const manifestCommon = `
name: common-module
versions:
  - version: 0.7.7
    image: ghcr.io/tiny-systems/common-module:0.7.7
`

func TestBuildIndexSortsVersionsDesc(t *testing.T) {
	m, err := ParseManifest([]byte(manifestHTTP))
	if err != nil {
		t.Fatalf("ParseManifest: %v", err)
	}
	idx, err := BuildIndex([]Manifest{*m})
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	got := []string{}
	for _, v := range idx.Modules["http-module"].Versions {
		got = append(got, v.Version)
	}
	want := []string{"2.0.0", "1.4.2", "1.4.1"} // newest-first
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("versions = %v, want %v", got, want)
	}
}

func TestBuildIndexRejectsDuplicates(t *testing.T) {
	m, _ := ParseManifest([]byte(manifestHTTP))
	if _, err := BuildIndex([]Manifest{*m, *m}); err == nil {
		t.Fatal("expected duplicate-module error")
	}
}

func TestParseManifestRejectsEmpty(t *testing.T) {
	if _, err := ParseManifest([]byte("source: x\n")); err == nil {
		t.Fatal("expected error for manifest with no name")
	}
	if _, err := ParseManifest([]byte("name: x\n")); err == nil {
		t.Fatal("expected error for manifest with no versions")
	}
}

func TestGenerateInlinesSiblingValues(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "common")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, ManifestFile), []byte(manifestCommon), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "values.yaml"),
		[]byte("ingress:\n  className: \"${cluster.ingressClass}\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	idx, err := GenerateFromDir(dir)
	if err != nil {
		t.Fatalf("GenerateFromDir: %v", err)
	}
	v := idx.Modules["common-module"].Versions[0]
	if !strings.Contains(v.Values, "className") {
		t.Fatalf("sibling values.yaml not inlined: %q", v.Values)
	}
}

func TestGenerateFromDirRoundTrips(t *testing.T) {
	dir := t.TempDir()
	write := func(sub, body string) {
		d := filepath.Join(dir, sub)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, ManifestFile), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("http", manifestHTTP)
	write("common", manifestCommon)

	idx, err := GenerateFromDir(dir)
	if err != nil {
		t.Fatalf("GenerateFromDir: %v", err)
	}
	if len(idx.Modules) != 2 {
		t.Fatalf("got %d modules, want 2", len(idx.Modules))
	}

	// Marshal → Parse must round-trip and resolve.
	data, err := MarshalIndex(idx, "2026-07-21T00:00:00Z")
	if err != nil {
		t.Fatalf("MarshalIndex: %v", err)
	}
	reparsed, err := ParseIndex(data)
	if err != nil {
		t.Fatalf("ParseIndex(generated): %v", err)
	}
	m := NewMerged([]string{"r"}, map[string]*Index{"r": reparsed})
	r, err := m.Resolve("http-module")
	if err != nil {
		t.Fatalf("resolve from generated index: %v", err)
	}
	if r.Version.Version != "2.0.0" {
		t.Fatalf("latest = %s, want 2.0.0", r.Version.Version)
	}
}
