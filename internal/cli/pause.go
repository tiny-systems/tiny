/*
tiny pause / tiny resume scale a session's workload to zero and back.

The point is capacity: a session you are not actively working still holds
its cpu and memory. Pausing frees those and keeps everything that matters
— the workspace volume and the transcript. Resuming brings the pod back,
and the agent replays the transcript from where it stopped, the same as
any pod restart.
*/
package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

func newPauseCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "pause [session]",
		Short: "Scale a session to zero — free its cpu/memory, keep its workspace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return fmt.Errorf("name a session, or pass --all — not both, not neither")
			}
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			if all {
				n, perr := store.PauseAll(ctx)
				if perr != nil {
					return perr
				}
				fmt.Printf("  ⏸ paused %d session(s) — workspaces kept; `tiny resume --all` to bring them back\n", n)
				return nil
			}
			if err := store.Pause(ctx, args[0]); err != nil {
				return err
			}
			fmt.Printf("  ⏸ %s paused — workspace and transcript kept; `tiny resume %s` to bring it back\n", args[0], args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "pause every running session")
	return cmd
}

func newResumeCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "resume [session]",
		Short: "Bring a paused session back — a fresh pod replays its transcript",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) == 1) {
				return fmt.Errorf("name a session, or pass --all — not both, not neither")
			}
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			if all {
				n, rerr := store.ResumeAll(ctx)
				if rerr != nil {
					return rerr
				}
				fmt.Printf("  ● resumed %d session(s)\n", n)
				return nil
			}
			if err := store.Resume(ctx, args[0]); err != nil {
				return err
			}
			fmt.Printf("  ● %s resuming — the agent picks up where it left off\n", args[0])
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "resume every paused session")
	return cmd
}
