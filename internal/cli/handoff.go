/*
tiny handoff moves the session you are IN to the cluster: the working tree
(dirty files and .git included) plus the local Claude Code transcript, into
a session that waits for them and then resumes the conversation. The train
command.
*/
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/sessions"
)

// claudeProjectSlug is how Claude Code names a project's transcript dir:
// every non-alphanumeric character of the absolute path becomes '-'.
// (Observed stable across versions; the e2e test guards it.)
func claudeProjectSlug(abs string) string {
	return regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(abs, "-")
}

// workspaceSlug is the slug the same transcript needs INSIDE the session,
// where the working tree lives at /workspace/repo.
const workspaceSlug = "-workspace-repo"

func newHandoffCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "handoff",
		Short: "Move this directory's Claude Code session to the cluster and keep it running there",
		Long: "Ships the current directory (uncommitted changes and .git included) and its\n" +
			"local Claude Code transcript into a new session, which resumes the\n" +
			"conversation in the cluster. Stop the local claude first: two copies of one\n" +
			"transcript tell two different stories.",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			dir, err = filepath.Abs(dir)
			if err != nil {
				return err
			}

			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			transcriptDir := filepath.Join(home, ".claude", "projects", claudeProjectSlug(dir))
			latest, err := latestTranscript(transcriptDir)
			if err != nil {
				return fmt.Errorf("no local Claude Code session found for %s: %w", dir, err)
			}
			fmt.Printf("  transcript: %s\n", filepath.Base(latest))

			// Two writers, one story: the local claude must be stopped first.
			if time.Since(mtime(latest)) < 90*time.Second {
				fmt.Print("  the local session wrote seconds ago — stopped it already? [y/N] ")
				if !confirmed(readLine()) {
					return fmt.Errorf("stop the local claude (ctrl+c twice), then rerun")
				}
			}

			k, err := sessionKube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()
			if err := ensureRuntime(ctx, k); err != nil {
				return err
			}
			store := newStore(k)

			se, err := store.Create(ctx, sessions.CreateOpts{Name: name, Handoff: true})
			if err != nil {
				return err
			}
			fmt.Printf("  ◌ session %s created on %s — waiting for its pod\n", se.Name, store.Target())
			pod, err := waitForSession(ctx, store, se.Name)
			if err != nil {
				return err
			}

			fmt.Println("  ◌ shipping the working tree (this can take a while on big repos)")
			var count int
			if err := store.PushTree(ctx, pod, "/workspace/repo", dir, func(string) { count++ }); err != nil {
				return err
			}
			fmt.Printf("  ✓ %d files landed in /workspace/repo\n", count)

			fmt.Println("  ◌ shipping the transcript")
			if err := store.PushTree(ctx, pod, "/workspace/.claude/projects/"+workspaceSlug, transcriptDir, nil); err != nil {
				return err
			}
			if err := store.MarkHandoffComplete(ctx, pod); err != nil {
				return err
			}
			fmt.Printf("  ✓ handed off — the session resumes in the cluster\n")
			fmt.Printf("    watch:   tiny\n    attach:  tiny attach %s\n", se.Name)
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "session name (generated when omitted)")
	return cmd
}

func latestTranscript(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".jsonl" {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	if len(files) == 0 {
		return "", fmt.Errorf("no transcripts in %s", dir)
	}
	slices.SortFunc(files, func(a, b string) int { return mtime(b).Compare(mtime(a)) })
	return files[0], nil
}

func mtime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}
