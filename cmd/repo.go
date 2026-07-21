package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/repo"
)

// newRepoCmd is the `tiny repo` group: manage the module repos tiny resolves
// installs from (Helm-style). See docs/design/module-distribution-v2.md.
func newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage module repos (Helm-style: static indexes, add your own)",
	}
	// Install lives at the top level as `tiny install` (the repo model is now
	// the real install path); `tiny repo` is just repo management.
	cmd.AddCommand(newRepoListCmd(), newRepoAddCmd(), newRepoRemoveCmd(), newRepoUpdateCmd(), newRepoIndexCmd())
	return cmd
}

func newRepoListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured repos",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := repo.Open()
			if err != nil {
				return err
			}
			repos := s.List()
			fmt.Println()
			if len(repos) == 0 {
				fmt.Println("  " + styleWarn.Render("no repos configured") + styleSubtle.Render("  — `tiny repo add <name> <url>`"))
				fmt.Println()
				return nil
			}
			for _, r := range repos {
				fmt.Printf("  %s  %s\n", styleTitle.Render(r.Name), styleSubtle.Render(r.URL))
			}
			fmt.Println("\n  " + styleSubtle.Render("`tiny repo update` to refresh their indexes."))
			return nil
		},
	}
}

func newRepoAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <url>",
		Short: "Add a module repo (index URL)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := repo.Open()
			if err != nil {
				return err
			}
			if err := s.Add(args[0], args[1]); err != nil {
				return err
			}
			fmt.Printf("\n  %s added repo %s %s\n", styleOK.Render("✓"), styleTitle.Render(args[0]), styleSubtle.Render(args[1]))
			fmt.Println("  " + styleSubtle.Render("`tiny repo update` to fetch its index."))
			return nil
		},
	}
}

func newRepoRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <name>",
		Aliases: []string{"rm"},
		Short:   "Remove a module repo",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := repo.Open()
			if err != nil {
				return err
			}
			if err := s.Remove(args[0]); err != nil {
				return err
			}
			fmt.Printf("\n  %s removed repo %s\n", styleOK.Render("✓"), styleTitle.Render(args[0]))
			return nil
		},
	}
}

func newRepoIndexCmd() *cobra.Command {
	var out string
	c := &cobra.Command{
		Use:   "index <dir>",
		Short: "Generate an index.yaml from module.yaml manifests (like `helm repo index`)",
		Long: `Build a repo index from the module.yaml files under <dir>. This is how a
repo is published with zero platform involvement: author your module.yaml,
run this, and host the result as a static file.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			idx, err := repo.GenerateFromDir(args[0])
			if err != nil {
				return err
			}
			data, err := repo.MarshalIndex(idx, time.Now().UTC().Format(time.RFC3339))
			if err != nil {
				return err
			}
			if out == "" {
				fmt.Print(string(data))
				return nil
			}
			if err := os.WriteFile(out, data, 0o644); err != nil {
				return err
			}
			fmt.Printf("\n  %s wrote %s %s\n", styleOK.Render("✓"), styleTitle.Render(out), styleSubtle.Render(fmt.Sprintf("(%d module(s))", len(idx.Modules))))
			return nil
		},
	}
	c.Flags().StringVarP(&out, "output", "o", "", "write to a file instead of stdout")
	return c
}

func newRepoUpdateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "update",
		Short: "Fetch the latest index from every configured repo",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := repo.Open()
			if err != nil {
				return err
			}
			fmt.Println("\n  " + styleSubtle.Render("updating repo indexes…"))
			if err := s.Update(cmd.Context()); err != nil {
				// Partial success is possible — report but don't hide it.
				fmt.Printf("  %s %v\n", styleWarn.Render("some repos failed:"), err)
				return nil
			}
			fmt.Println("  " + styleOK.Render("✓") + " indexes updated")
			return nil
		},
	}
}
