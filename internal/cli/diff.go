/*
tiny diff shows a session's blast radius: the files it has touched, how
far, on what branch, and what waits in its outbox. With no session named,
it reports the whole fleet and flags files two sessions are both editing —
the collision a fleet screen cannot show you.
*/
package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/sessions"
)

func newDiffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff [session]",
		Short: "What a session changed — files, lines, branch, pending bundles",
		Long: "Reads the working tree live from the session's pod: committed work on its\n" +
			"branch plus anything still dirty. With no session named, every running\n" +
			"session is reported and files touched by more than one are flagged.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			snap, err := store.Load(ctx)
			if err != nil {
				return err
			}

			var rows []sessions.Row
			for _, row := range snap.Rows {
				if len(args) == 1 && row.Name != args[0] {
					continue
				}
				rows = append(rows, row)
			}
			if len(args) == 1 && len(rows) == 0 {
				return fmt.Errorf("no session named %s on %s", args[0], store.Target())
			}
			if len(rows) == 0 {
				fmt.Println("  no sessions")
				return nil
			}

			prints := map[string]sessions.Footprint{}
			for _, row := range rows {
				if row.Pod == "" {
					continue // no pod, no workspace to read
				}
				f := store.Footprint(ctx, row.Pod)
				prints[row.Name] = f
				printFootprint(row.Name, f, len(rows) == 1)
			}

			// The team question: who is about to collide with whom.
			if over := sessions.Overlap(prints); len(over) > 0 {
				paths := make([]string, 0, len(over))
				for p := range over {
					paths = append(paths, p)
				}
				slices.Sort(paths)
				fmt.Printf("\n  %s\n", styleWarn.Render("touched by more than one session"))
				for _, p := range paths {
					names := over[p]
					slices.Sort(names)
					fmt.Printf("    %-40s %s\n", p, styleSubtle.Render(joinNames(names)))
				}
			}
			return nil
		},
	}
}

func printFootprint(name string, f sessions.Footprint, detail bool) {
	if f.Err != "" {
		fmt.Printf("  %s  %s\n", styleTitle.Render(name), styleSubtle.Render(f.Err))
		return
	}
	head := fmt.Sprintf("  %s", styleTitle.Render(name))
	if f.Branch != "" {
		head += styleSubtle.Render("  on " + f.Branch)
	}
	switch {
	case len(f.Files) == 0:
		fmt.Println(head + styleSubtle.Render("  — nothing changed yet"))
	default:
		fmt.Printf("%s  %s\n", head,
			fmt.Sprintf("%d files %s", len(f.Files),
				styleOK.Render(fmt.Sprintf("+%d", f.Added))+styleSubtle.Render("/")+styleWarn.Render(fmt.Sprintf("-%d", f.Deleted))))
	}
	if len(f.Bundles) > 0 {
		fmt.Printf("    %s %s\n", styleKey.Render("outbox:"), joinNames(f.Bundles))
	}
	// One session named: show the files. Whole fleet: keep it a summary.
	if detail {
		for _, c := range f.Files {
			mark := " "
			if c.Staged {
				mark = styleOK.Render("●") // committed on the branch
			}
			size := fmt.Sprintf("+%d/-%d", c.Added, c.Deleted)
			if c.Added < 0 || c.Deleted < 0 {
				size = "binary"
			}
			fmt.Printf("    %s %-46s %s\n", mark, c.Path, styleSubtle.Render(size))
		}
	}
}

func joinNames(names []string) string { return strings.Join(names, ", ") }
