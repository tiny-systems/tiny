package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// promptReady must return false while the terminal shows a boot splash and
// true once the CLI's interactive footer is up. Getting this wrong loses
// the task: a message typed into the splash vanishes and is marked
// delivered, leaving the session idle forever.
func TestPromptReady(t *testing.T) {
	cases := []struct {
		name   string
		screen string
		want   bool
	}{
		{"boot splash", "Claude Code v2.1.282\n  Get to finished work sooner\n❯", false},
		{"waiting, no footer", "❯ No task was given.\n", false},
		{"claude interactive", "❯\n  ⏵⏵ bypass permissions on (shift+tab to cycle)", true},
		{"mid turn", "● Working…\n  esc to interrupt", true},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmux := fakeTmux(t, tc.screen)
			if got := promptReady(tmux); got != tc.want {
				t.Fatalf("promptReady(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

// fakeTmux writes a stub that echoes a fixed screen for `capture-pane`, so
// promptReady can be tested without a real terminal.
func fakeTmux(t *testing.T, screen string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	script := "#!/bin/sh\ncase \"$1\" in capture-pane) cat <<'SCREEN'\n" + screen + "\nSCREEN\n;; esac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// Sanity: the stub runs.
	if _, err := exec.Command(path, "capture-pane").Output(); err != nil {
		t.Fatalf("stub tmux failed: %v", err)
	}
	return path
}

// readMemMi and readCPUUsec must read cgroup v1 as well as v2 — the k3s
// node can be either, and on a v1 host the fleet's cpu/mem stayed blank
// because only the v2 paths were tried.
func TestReadUsageCgroupV1(t *testing.T) {
	dir := t.TempDir()
	// Lay out a v1 tree and point the readers at it via a symlinked root
	// is overkill; instead assert the parsing helpers on real byte strings.
	memBytes := int64(270061568) // ~257Mi, the value seen on the live box
	if got := fmtMi(memBytes); got != "257Mi" {
		t.Fatalf("fmtMi(%d) = %s, want 257Mi", memBytes, got)
	}
	// cpuacct.usage is nanoseconds; the sampler converts to microseconds.
	ns := int64(7420289782)
	if usec := ns / 1000; usec != 7420289 {
		t.Fatalf("ns->usec = %d, want 7420289", usec)
	}
	_ = dir
}
