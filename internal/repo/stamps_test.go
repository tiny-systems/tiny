package repo

import (
	"os"
	"path/filepath"
	"testing"
)

// A fetch that returns the previous copy from a CDN edge looks exactly like a
// fetch that worked. Reporting when the index was generated is the difference
// between "you have the latest" and "you have whatever was served".
func TestStampsReportWhatTheCacheHolds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	cache := filepath.Join(dir, ".tiny", "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	index := `apiVersion: tiny/v2
generated: '2026-08-20T21:19:43Z'
modules:
  a-module:
    source: github.com/x/a
    versions:
      - version: 1.0.0
  b-module:
    source: github.com/x/b
    versions:
      - version: 2.0.0
`
	if err := os.WriteFile(filepath.Join(cache, "tinysystems.yaml"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}

	s := &Store{cfg: &Config{Repos: []Repo{{Name: "tinysystems", URL: "https://example.test/index.yaml"}}}}
	stamps := s.Stamps()

	if len(stamps) != 1 {
		t.Fatalf("%d stamps", len(stamps))
	}
	if stamps[0].Generated != "2026-08-20T21:19:43Z" {
		t.Errorf("generated = %q", stamps[0].Generated)
	}
	if stamps[0].Modules != 2 {
		t.Errorf("modules = %d", stamps[0].Modules)
	}
}

// A repo that was never fetched must say so rather than report an empty index
// as if it were a real one.
func TestStampsSayWhenThereIsNoCache(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &Store{cfg: &Config{Repos: []Repo{{Name: "missing", URL: "https://example.test/index.yaml"}}}}

	stamps := s.Stamps()
	if len(stamps) != 1 || stamps[0].Generated != "" || stamps[0].Modules != 0 {
		t.Fatalf("stamps = %+v", stamps)
	}
}
