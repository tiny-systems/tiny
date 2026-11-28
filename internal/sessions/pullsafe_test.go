package sessions

import (
	"archive/tar"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPullSkipsEveryGitPath(t *testing.T) {
	dest := t.TempDir()
	raw := buildTar(t, []tarEntry{
		{name: "README.md", typ: tar.TypeReg, body: "hi"},
		{name: ".git/hooks/post-checkout", typ: tar.TypeReg, body: "pwn"},
		{name: ".git/config", typ: tar.TypeReg, body: "[core]\n\tfsmonitor = pwn\n"},
		{name: ".GIT/config", typ: tar.TypeReg, body: "pwn"},
		{name: "vendor/lib/.Git/HEAD", typ: tar.TypeReg, body: "ref: refs/heads/main\n"},
		{name: "sub/.git", typ: tar.TypeReg, body: "gitdir: /elsewhere\n"},
	})
	n, err := untarInto(raw, dest, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("wrote %d files, want only README.md", n)
	}
	for _, p := range []string{".git", ".GIT", "vendor/lib/.Git", "sub/.git"} {
		if _, err := os.Lstat(filepath.Join(dest, p)); err == nil {
			t.Errorf("%s was written", p)
		}
	}
}

func TestRemoveRepoLikeDeletesBareAndStrayGitDirs(t *testing.T) {
	dest := t.TempDir()
	write := func(p, body string) {
		t.Helper()
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dest, p)), 0o755)
		if err := os.WriteFile(filepath.Join(dest, p), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("fixtures/bare.git/HEAD", "ref: refs/heads/main\n")
	write("fixtures/bare.git/config", "[core]\n\tfsmonitor = pwn\n")
	write("fixtures/bare.git/objects/info/packs", "")
	write("odd/head", "x") // lower case, beside commondir
	write("odd/commondir", "/elsewhere\n")
	write("src/HEAD.md", "not a repo")
	write("src/refs/notes.txt", "not a repo either: no HEAD")
	n, err := removeRepoLike(dest)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("removed %d, want 2", n)
	}
	for _, gone := range []string{"fixtures/bare.git", "odd"} {
		if _, err := os.Lstat(filepath.Join(dest, gone)); err == nil {
			t.Errorf("%s survived", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(dest, "src", "refs", "notes.txt")); err != nil {
		t.Error("an ordinary folder was removed")
	}
}

// TestRebuildHistoryFromBundle runs real git: a repo with a hostile hook
// and config is bundled, and the rebuilt copy has its history and none of
// its executable parts.
func TestRebuildHistoryFromBundle(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx := context.Background()
	src := t.TempDir()
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(src, "init", "-q", "-b", "work")
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(src, "add", "a.txt")
	run(src, "commit", "-q", "-m", "first")
	run(src, "config", "core.fsmonitor", "touch /tmp/tiny-pwned")
	head := run(src, "rev-parse", "HEAD")
	bundle := filepath.Join(t.TempDir(), "b.bundle")
	run(src, "bundle", "create", bundle, "--all")

	dest := t.TempDir()
	// the pulled working tree: committed file plus an uncommitted edit
	if err := os.WriteFile(filepath.Join(dest, "a.txt"), []byte("one\ntwo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := rebuildHistory(ctx, dest, bundle, head, "work", "https://github.com/x/y", nil); err != nil {
		t.Fatal(err)
	}
	if got := run(dest, "rev-parse", "--abbrev-ref", "HEAD"); got != "work" {
		t.Errorf("branch = %q", got)
	}
	if got := run(dest, "status", "--porcelain"); got != "M a.txt" {
		t.Errorf("status = %q, want the uncommitted edit", got)
	}
	if got := run(dest, "remote", "get-url", "origin"); got != "https://github.com/x/y" {
		t.Errorf("origin = %q", got)
	}
	cfg, _ := os.ReadFile(filepath.Join(dest, ".git", "config"))
	if strings.Contains(string(cfg), "fsmonitor") {
		t.Errorf("source config leaked: %s", cfg)
	}
}

func TestRebuildHistoryIgnoresAHostileOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, o := range []string{"ext::sh -c pwn", `"ext::sh"`, "fd::3"} {
		if plainValue.MatchString(o) && safeURL.MatchString(o) {
			t.Errorf("%q would be added as origin", o)
		}
	}
}

func TestRebuildHistoryFromAShallowClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	run := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t", "GIT_ALLOW_PROTOCOL=file")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %s", args, out)
		}
		return strings.TrimSpace(string(out))
	}
	origin := t.TempDir()
	run(origin, "init", "-q", "-b", "main")
	for _, msg := range []string{"one", "two", "three"} {
		if err := os.WriteFile(filepath.Join(origin, "f.txt"), []byte(msg), 0o644); err != nil {
			t.Fatal(err)
		}
		run(origin, "add", ".")
		run(origin, "commit", "-q", "-m", msg)
	}
	clone := filepath.Join(t.TempDir(), "c")
	run(".", "clone", "-q", "--depth", "1", "file://"+origin, clone)
	shallowFile, _ := os.ReadFile(filepath.Join(clone, ".git", "shallow"))
	bundle := filepath.Join(t.TempDir(), "b.bundle")
	run(clone, "bundle", "create", bundle, "--all")

	dest := t.TempDir()
	if err := os.WriteFile(filepath.Join(dest, "f.txt"), []byte("three"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := rebuildHistory(context.Background(), dest, bundle, run(clone, "rev-parse", "HEAD"), "main", "", strings.Fields(string(shallowFile)))
	if err != nil {
		t.Fatal(err)
	}
	if got := run(dest, "log", "--format=%s"); got != "three" {
		t.Errorf("log = %q", got)
	}
	if got := run(dest, "status", "--porcelain"); got != "" {
		t.Errorf("status = %q", got)
	}
}

func TestIsGitNameCatchesFilesystemAliases(t *testing.T) {
	for _, n := range []string{".git", ".GIT", ".git.", ".git ", ".g\u200cit", "\ufeff.git", "GIT~1", ".Git.. "} {
		if !isGitName(n) {
			t.Errorf("%q not caught", n)
		}
	}
	for _, n := range []string{"git", ".github", ".gitignore", "x.git"} {
		if isGitName(n) {
			t.Errorf("%q caught by mistake", n)
		}
	}
}

func TestRebuildHistoryRefusesAnExistingGitDir(t *testing.T) {
	dest := t.TempDir()
	if err := os.Mkdir(filepath.Join(dest, ".Git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := rebuildHistory(context.Background(), dest, "/nonexistent", "", "", "", nil); err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Errorf("err = %v, want a refusal", err)
	}
}
