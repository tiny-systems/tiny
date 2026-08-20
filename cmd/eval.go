package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/eval"
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

			specs, err := eval.Load(path)
			if err != nil {
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

			runner := &eval.Runner{
				Project: flagProject,
				Sender:  bundle.SignalSender,
				Reader:  bundle.TraceReader,
				Settle:  settle,
			}

			fmt.Println()
			fmt.Printf("  %s %s\n\n", styleKey.Render("evals"), styleSubtle.Render(fmt.Sprintf("%d in %s · project %s", len(specs), path, flagProject)))

			failed := 0
			for _, spec := range specs {
				result := runner.Run(cmd.Context(), spec)
				printResult(result, verbose)
				if !result.Passed() {
					failed++
				}
			}

			fmt.Println()
			if failed == 0 {
				fmt.Printf("  %s %s\n\n", styleOK.Render("✓ all passed"), styleSubtle.Render(fmt.Sprintf("(%d)", len(specs))))
				return nil
			}
			fmt.Printf("  %s %s\n\n", styleWarn.Render(fmt.Sprintf("✗ %d of %d failed", failed, len(specs))), styleSubtle.Render("the claim above each failure is what stopped being true"))
			os.Exit(1)
			return nil
		},
	}

	c.Flags().StringVar(&dir, "dir", "", "directory or file holding the evals (default: ./evals)")
	c.Flags().DurationVar(&settle, "settle", 3*time.Second, "how long a run must produce no new spans before it counts as finished")
	c.Flags().BoolVarP(&verbose, "verbose", "v", false, "print the trace id of every eval, not only the failures")
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
