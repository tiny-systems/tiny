package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// Flags end at the command's name: everything after it belongs to the
// command, including things that look like tiny's own flags.
func TestRunFlagsStopAtTheCommand(t *testing.T) {
	cmd := newRunCmd()
	if err := cmd.Flags().Parse([]string{"--image", "python:3.13-slim", "--keep", "ls", "-la", "--keep", "--image", "x"}); err != nil {
		t.Fatal(err)
	}
	if got := cmd.Flags().Args(); !slices.Equal(got, []string{"ls", "-la", "--keep", "--image", "x"}) {
		t.Fatalf("command args = %q", got)
	}
	if img, _ := cmd.Flags().GetString("image"); img != "python:3.13-slim" {
		t.Fatalf("image = %q", img)
	}
}

// An explicit -- works too, for commands whose name starts with a dash
// or for readers who like it spelled out.
func TestRunDoubleDash(t *testing.T) {
	cmd := newRunCmd()
	if err := cmd.Flags().Parse([]string{"--runtime-class", "gvisor", "--", "python3", "-c", "print(1)"}); err != nil {
		t.Fatal(err)
	}
	if got := cmd.Flags().Args(); !slices.Equal(got, []string{"python3", "-c", "print(1)"}) {
		t.Fatalf("command args = %q", got)
	}
}

func TestRunRequiresACommand(t *testing.T) {
	cmd := newRunCmd()
	cmd.SetArgs([]string{"--image", "x"})
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	if err := cmd.Execute(); err == nil {
		t.Fatal("tiny run without a command must fail")
	}
}

// Both sources at once is refused before anything touches a cluster.
func TestRunRefusesDirAndRepoTogether(t *testing.T) {
	err := runCommand(t.Context(), runOpts{dir: ".", repo: "https://example.com/x.git"}, []string{"ls"})
	if err == nil || !strings.Contains(err.Error(), "pick one") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

func TestRunRefusesAMissingDir(t *testing.T) {
	err := runCommand(t.Context(), runOpts{dir: t.TempDir() + "/nope"}, []string{"ls"})
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("want a refusal, got %v", err)
	}
}

// The command's exit code travels out as ExitError, also when wrapped.
func TestExitErrorSurvivesWrapping(t *testing.T) {
	err := fmt.Errorf("run: %w", ExitError{Code: 3})
	var exit ExitError
	if !errors.As(err, &exit) || exit.Code != 3 {
		t.Fatalf("errors.As lost the code: %v", err)
	}
}

func TestFilesCount(t *testing.T) {
	if files(1) != "1 file" || files(3) != "3 files" || files(0) != "0 files" {
		t.Fatal("files")
	}
}

func TestPluginDisplayName(t *testing.T) {
	for argv0, want := range map[string]string{
		"/Users/ana/.krew/bin/kubectl-tiny": "kubectl tiny",
		"kubectl-tiny.exe":                  "kubectl tiny",
		"/usr/local/bin/tiny":               "",
		"tiny":                              "",
	} {
		if got := pluginDisplayName(argv0); got != want {
			t.Errorf("pluginDisplayName(%q) = %q, want %q", argv0, got, want)
		}
	}
}
