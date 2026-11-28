/*
tiny show lists and opens what a session put in front of you with the
show_user tool: screenshots, charts, reports. The file stays in the pod
until you ask; then it is read with your own access, saved under your
cache directory and opened in the default app (or saved where --out says).
*/
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/sessions"
)

func newShowCmd() *cobra.Command {
	var out string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "show <session> [n|latest]",
		Short: "List or open the files a session showed you (screenshots, charts, reports)",
		Long: "With only a session, lists the files its agent showed you with the show_user tool,\n" +
			"numbered oldest first. With a number (or latest), copies that file out of the pod\n" +
			"and opens it in your default app; --out saves it to a path instead.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			se := &agentsv1.Session{}
			if err := k.Client.Get(ctx, client.ObjectKey{Namespace: k.Namespace, Name: args[0]}, se); err != nil {
				return err
			}
			shown := se.Status.Shown
			if len(args) == 1 && out == "" {
				if asJSON {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					if shown == nil {
						shown = []agentsv1.ShownFile{}
					}
					return enc.Encode(shown)
				}
				if len(shown) == 0 {
					fmt.Printf("  %s hasn't shown any files\n", se.Name)
					return nil
				}
				for i, f := range shown {
					fmt.Printf("  %2d  %-8s  %s\n", i+1, shortAgo(time.Since(f.At.Time)), f.Path)
					if f.Caption != "" {
						fmt.Printf("      %8s  %s\n", "", f.Caption)
					}
				}
				fmt.Printf("\n  open one: tiny show %s <n>   (or latest)\n", se.Name)
				return nil
			}
			which := ""
			if len(args) == 2 {
				which = args[1]
			}
			f, err := sessions.PickShown(shown, which)
			if err != nil {
				return fmt.Errorf("%s: %w", se.Name, err)
			}
			store := newStore(k)
			snap, err := store.Load(ctx)
			if err != nil {
				return err
			}
			pod := ""
			for _, r := range snap.Rows {
				if r.Name == se.Name && r.Phase == string(agentsv1.SessionRunning) {
					pod = r.Pod
				}
			}
			if pod == "" {
				return fmt.Errorf("%s isn't running; tiny resume %s, then try again", se.Name, se.Name)
			}
			p := out
			if p == "" {
				p = filepath.Join(sessions.ShownDir(se.Name), sessions.ShownName(f))
			}
			if err := store.FetchShown(ctx, pod, f, p); err != nil {
				return err
			}
			if out != "" {
				fmt.Printf("  saved %s\n", out)
				return nil
			}
			fmt.Printf("  opening %s\n", p)
			return sessions.OpenLocal(p)
		},
	}
	cmd.Flags().StringVar(&out, "out", "", "save the file here instead of opening it (implies latest when no number is given)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "list as JSON: path, caption, at")
	return cmd
}

func shortAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
