/*
tiny attach joins a session's terminal directly — the same tmux attach the
fleet screen's enter performs, without opening the fleet screen first.
Detach with ctrl-q d; the agent keeps working.
*/
package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/sessions"
)

func newAttachCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "attach <session>",
		Short: "Attach to a session's terminal (detach with ctrl-q d)",
		Long: "Joins the session's tmux directly — the real agent CLI, same as pressing\n" +
			"enter on the fleet screen. Detaching (ctrl-q d) never stops the agent.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := &sessions.Store{Kube: k}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			snap, err := store.Load(ctx)
			cancel()
			if err != nil {
				return err
			}
			name := args[0]
			for _, row := range snap.Rows {
				if row.Name != name {
					continue
				}
				if row.Pod == "" {
					return fmt.Errorf("session %s has no running pod (state: %s) — a finished session's terminal is gone; try `tiny shell %s` for its workspace", name, row.Phase, name)
				}
				return store.Attach(cmd.Context(), name, row.Pod)
			}
			return fmt.Errorf("no session named %s on %s", name, store.Target())
		},
	}
}
