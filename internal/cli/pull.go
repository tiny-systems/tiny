/*
tiny pull is handoff's return leg: bring a session's workspace back to the
laptop. The files travel the same way they went up — through the
Kubernetes API, no inbound access to the pod, no git remote in the middle.

It never overwrites: the destination must be empty or absent. Reviewing
overnight work should not be able to eat this morning's.
*/
package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
)

func newPullCmd() *cobra.Command {
	var srcDir string
	cmd := &cobra.Command{
		Use:   "pull <session> [directory]",
		Short: "Bring a session's workspace back to this machine",
		Long: "Copies the session's working tree — committed work, uncommitted changes and\n" +
			".git — into a local directory (default: ./<session>). The directory must be\n" +
			"empty or absent; nothing local is ever overwritten.\n\n" +
			"To continue in an existing clone instead, pull to a scratch directory and\n" +
			"fetch from it: git fetch ./<session> <branch>.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			dest := name
			if len(args) == 2 {
				dest = args[1]
			}
			dest, err := filepath.Abs(dest)
			if err != nil {
				return err
			}

			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 15*time.Minute)
			defer cancel()

			snap, err := store.Load(ctx)
			if err != nil {
				return err
			}
			pod := ""
			for _, row := range snap.Rows {
				if row.Name == name {
					pod = row.Pod
					break
				}
			}
			if pod == "" {
				return fmt.Errorf("session %s has no running pod on %s — `tiny shell %s` reaches a finished session's workspace", name, store.Target(), name)
			}

			fmt.Printf("  ◌ pulling %s from %s\n", srcDir, name)
			count, err := store.PullTree(ctx, pod, srcDir, dest, nil)
			if err != nil {
				return err
			}
			fmt.Printf("  ✓ %d files landed in %s\n", count, dest)
			fmt.Printf("    review:  git -C %s log --oneline\n", dest)
			return nil
		},
	}
	cmd.Flags().StringVar(&srcDir, "from", "/workspace/repo", "directory inside the session to pull")
	return cmd
}
