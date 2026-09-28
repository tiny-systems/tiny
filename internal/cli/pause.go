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
	return &cobra.Command{
		Use:   "pause <session>",
		Short: "Scale a session to zero — free its cpu/memory, keep its workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			if err := store.Pause(ctx, args[0]); err != nil {
				return err
			}
			fmt.Printf("  ⏸ %s paused — workspace and transcript kept; `tiny resume %s` to bring it back\n", args[0], args[0])
			return nil
		},
	}
}

func newResumeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "resume <session>",
		Short: "Bring a paused session back — a fresh pod replays its transcript",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			if err := store.Resume(ctx, args[0]); err != nil {
				return err
			}
			fmt.Printf("  ● %s resuming — the agent picks up where it left off\n", args[0])
			return nil
		},
	}
}
