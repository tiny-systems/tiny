package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/adapters"
	"github.com/tiny-systems/tiny/internal/eval"
	"github.com/tiny-systems/tiny/internal/kube"
)

// `tiny eval` — run a project's flows against pinned inputs and report what
// stopped being true.
//
// The gap it fills: nothing told you when something quietly broke. A flow that
// worked last week fails today because a module released, a schema changed, or
// a setting stopped being read — and the way that surfaces is a person opening
// the canvas and noticing. An eval turns that into a red line with a name.

func newEvalCmd() *cobra.Command {
	var (
		dir     string
		settle  time.Duration
		verbose bool
		watch   bool
	)

	c := &cobra.Command{
		Use:   "eval [file|dir]",
		Short: "Run pinned checks against the flows on your cluster",
		Long: `Fire a real trigger on the cluster, wait for the run, and assert on what
actually arrived.

Each eval names a claim — "pod-watch diagnoses a crashlooping pod" — and a
failure reports the claim, not a stack trace. Evals know nothing about any
module: they speak nodes, ports, payloads and traces, so a flow built from
components that did not exist when this was written is checked the same way.

Exit code is non-zero when anything failed, so this belongs in CI after a
module release.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := dir
			if len(args) == 1 {
				path = args[0]
			}
			if path == "" {
				path = eval.DefaultDir
			}

			// Load once up front so a broken eval file is reported before a
			// cluster connection is made — the error is about the file, and
			// pairing it with a connection failure would bury it.
			if _, err := eval.Load(path); err != nil {
				return err
			}

			if flagProject == "" {
				return fmt.Errorf("no project: pass -p <project>, the evals run against one")
			}

			bundle, cleanup, err := buildKubeBundle(flagProject)
			if err != nil {
				return err
			}
			defer cleanup()

			if bundle.SignalSender == nil || bundle.TraceReader == nil {
				return fmt.Errorf("this cluster connection cannot fire signals or read traces — is the runtime installed? (`tiny up`)")
			}

			// Watching needs to notice a flow being edited or a module being
			// upgraded, which is the direction breakage actually arrives from.
			var fingerprint eval.ChangeSource
			if watch {
				kc, err := kube.NewClient(kube.Options{Context: flagContext, Namespace: flagNamespace})
				if err != nil {
					// Falling back to watching only the files would look like
					// it was working while missing the direction breakage
					// actually comes from.
					return fmt.Errorf("watch needs a cluster connection to notice flow and module changes: %w", err)
				}
				fingerprint = adapters.NewProjectFingerprint(kc)
			}

			runner := &eval.Runner{
				Project: flagProject,
				Sender:  bundle.SignalSender,
				Reader:  bundle.TraceReader,
				Settle:  settle,
			}

			once := func() int {
				// Reloaded every pass so an edit takes effect without a
				// restart — the point of watching is not having to think
				// about when things are picked up.
				current, err := eval.Load(path)
				if err != nil {
					fmt.Printf("  %s %v\n", styleWarn.Render("evals:"), err)
					return 1
				}
				fmt.Printf("  %s %s\n\n", styleKey.Render("evals"), styleSubtle.Render(fmt.Sprintf("%d in %s · project %s", len(current), path, flagProject)))

				failed := 0
				for _, spec := range current {
					result := runner.Run(cmd.Context(), spec)
					printResult(result, verbose)
					if !result.Passed() {
						failed++
					}
				}
				fmt.Println()
				if failed == 0 {
					fmt.Printf("  %s %s\n", styleOK.Render("✓ all passed"), styleSubtle.Render(fmt.Sprintf("(%d)", len(current))))
				} else {
					fmt.Printf("  %s %s\n", styleWarn.Render(fmt.Sprintf("✗ %d of %d failed", failed, len(current))), styleSubtle.Render("the claim above each failure is what stopped being true"))
				}
				return failed
			}

			fmt.Println()
			if !watch {
				if once() > 0 {
					os.Exit(1)
				}
				fmt.Println()
				return nil
			}

			fmt.Printf("  %s %s\n\n", styleKey.Render("watching"), styleSubtle.Render("re-runs when the evals change, or when a flow or module does · ctrl-c to stop"))
			return eval.Watch(cmd.Context(), eval.WatchOptions{
				Path:    path,
				Project: flagProject,
				Cluster: fingerprint,
			}, func(reason string) {
				if reason != "first run" {
					fmt.Printf("\n  %s %s\n\n", styleKey.Render("↻"), styleSubtle.Render(reason))
				}
				once()
			})
		},
	}

	c.Flags().StringVar(&dir, "dir", "", "directory or file holding the evals (default: ./evals)")
	c.Flags().DurationVar(&settle, "settle", 3*time.Second, "how long a run must produce no new spans before it counts as finished")
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "print the trace id of every eval, not only the failures")
	c.Flags().BoolVarP(&watch, "watch", "w", false, "keep running: re-run when an eval changes, or when a flow or module does")
	return c
}

func printResult(r eval.Result, verbose bool) {
	took := styleSubtle.Render(fmt.Sprintf("%.1fs", r.Duration.Seconds()))

	if r.Err != nil {
		fmt.Printf("  %s %s %s\n", styleWarn.Render("✗"), styleTitle.Render(r.Spec.Name), took)
		fmt.Printf("      %s\n", styleWarn.Render("could not run: "+r.Err.Error()))
		return
	}

	if len(r.Failures) == 0 {
		line := fmt.Sprintf("  %s %s %s", styleOK.Render("✓"), r.Spec.Name, took)
		if verbose {
			line += " " + styleSubtle.Render(r.TraceID)
		}
		fmt.Println(line)
		return
	}

	fmt.Printf("  %s %s %s\n", styleWarn.Render("✗"), styleTitle.Render(r.Spec.Name), took)
	for _, f := range r.Failures {
		fmt.Printf("      %s\n", f.String())
	}
	// The trace is the next thing anyone will want, so hand it over rather
	// than making them go and find it.
	fmt.Printf("      %s\n", styleSubtle.Render("trace "+r.TraceID))
}
