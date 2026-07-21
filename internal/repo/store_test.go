package repo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// isolateHome points config + cache at a temp dir so tests never touch the real
// home. os.UserConfigDir/UserCacheDir derive from HOME on darwin and from the
// XDG_* vars (then HOME) on linux — set all three.
func isolateHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
}

func TestStoreAddUpdateResolve(t *testing.T) {
	isolateHome(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fixtureA))
	}))
	defer srv.Close()

	s, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Open seeds the default repo; drop it (unreachable in tests) and add ours.
	if err := s.Remove(DefaultRepoName); err != nil {
		t.Fatalf("Remove default: %v", err)
	}
	if err := s.Add("test", srv.URL); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.Add("test", srv.URL); err == nil {
		t.Fatal("expected duplicate-name error")
	}

	if err := s.Update(context.Background()); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// A fresh Store (reloads config from disk) must see the cached index.
	s2, err := Open()
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	m, err := s2.Merged()
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	r, err := m.Resolve("http-module")
	if err != nil {
		t.Fatalf("resolve after update: %v", err)
	}
	if r.Repo != "test" || r.Version.Image != "ghcr.io/tiny-systems/http-module:1.4.2" {
		t.Fatalf("resolved %s → %s, want test → …:1.4.2", r.Repo, r.Version.Image)
	}
}

func TestStoreAddRejectsNonHTTP(t *testing.T) {
	isolateHome(t)
	s, err := Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := s.Add("bad", "file:///etc/passwd"); err == nil {
		t.Fatal("expected non-http url to be rejected")
	}
}
