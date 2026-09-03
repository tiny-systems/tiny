package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// newAnswerCmd resolves a ✳ question from the command line. Since the
// manager's removal, answering IS acting: an allowed action executes here,
// with your credentials. A raw kubectl patch only delivers the words.
func newAnswerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "answer <question> <text...>",
		Short: "Answer a question (and perform its action, if it carries one)",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			if err := store.Answer(ctx, args[0], strings.Join(args[1:], " ")); err != nil {
				return err
			}
			fmt.Println("  ✓ answered", args[0])
			return nil
		},
	}
}
