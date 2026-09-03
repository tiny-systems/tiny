/*
tiny broadcast is the fleet-wide megaphone: one message into every
unfinished session's durable inbox. The TUI's [b] key is the interactive
face; this command is the programmatic one — cron, CI, or a human with a
namespace flag telling the whole fleet the demo moved to ten.
*/
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newBroadcastCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "broadcast [message]",
		Short: "Deliver one message to every unfinished session's inbox",
		Long: "Appends the message to the durable inbox of every session that isn't Done —\n" +
			"running, paused on a usage limit, or mid-restart alike. Pass the message as\n" +
			"arguments or pipe it on stdin. Scope with --context and -n like any other\n" +
			"command.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			msg := strings.TrimSpace(strings.Join(args, " "))
			if msg == "" {
				raw, err := io.ReadAll(io.LimitReader(os.Stdin, 256<<10))
				if err != nil {
					return err
				}
				msg = strings.TrimSpace(string(raw))
			}
			if msg == "" {
				return fmt.Errorf("nothing to say — pass a message or pipe it in")
			}

			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			delivered, err := store.Broadcast(ctx, msg)
			for _, name := range delivered {
				fmt.Printf("  ✓ %s\n", name)
			}
			if err != nil {
				if len(delivered) == 0 {
					return err
				}
				fmt.Fprintf(os.Stderr, "  ✗ some sessions missed it: %v\n", err)
			}
			fmt.Printf("  ✉ delivered to %d session(s)\n", len(delivered))
			return nil
		},
	}
	return cmd
}
