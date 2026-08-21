package cmd

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/adapters"
	"github.com/tiny-systems/tiny/internal/replay"
)

// `tiny replay` — run a recorded message again and report what changed.
//
// The question it answers is the one that has cost the most time here: a module
// released, does this flow still behave? An eval answers it for the claims
// somebody thought to write down. A replay answers it for a run that actually
// happened, which is a much larger set and needs nobody to have predicted
// anything.

func newReplayCmd() *cobra.Command {
	var (
		at     string
		list   bool
		settle time.Duration
	)

	c := &cobra.Command{
		Use:   "replay <trace-id>",
		Short: "Send a recorded message again and report what changed",
		Long: `Re-drive a hop from a recorded run and compare what follows against
what followed the first time.

The message is published as the edge that carried it, so the runner evaluates
the edge configuration exactly as it does for live traffic — which is what
refills the fields derived from the passthrough context, including credentials.
Nothing is read out of the recording except the business payload.

The comparison is structural: which ports were reached, and what shape the data
had. Values are expected to differ — a model rewords its answer and a cluster
moves on — so a diff on values would be noise. A field that stopped arriving,
or arrived as a string where it was a number, is what a module release breaks
and what this reports.

The replay lands in the recorded trace as a branch, so both runs can be read
side by side afterwards.

CAUTION: a replay re-runs side effects. Re-driving a hop that restarted a
workload restarts it again.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			traceID := args[0]
			if flagProject == "" {
				return fmt.Errorf("no project: pass -p <project>, a trace belongs to one")
			}

			bundle, cleanup, err := buildKubeBundle(flagProject)
			if err != nil {
				return err
			}
			defer cleanup()

			reader, ok := bundle.TraceReader.(replay.Reader)
			if !ok || bundle.TraceReader == nil {
				return fmt.Errorf("this cluster connection cannot read traces — is the runtime installed? (`tiny up`)")
			}
			sender, ok := bundle.SignalSender.(*adapters.SignalSender)
			if !ok || sender == nil {
				return fmt.Errorf("this cluster connection cannot publish — is the cluster's nats service reachable?")
			}

			spans, err := reader.ReadTraceDetail(cmd.Context(), flagProject, traceID)
			if err != nil {
				return err
			}
			hops := replay.Hops(spans)
			if len(hops) == 0 {
				return fmt.Errorf("trace %s has no replayable hop — every recorded span is a trigger or carries no payload", traceID)
			}

			fmt.Println()
			if list {
				fmt.Printf("  %s %s\n\n", styleKey.Render("hops"), styleSubtle.Render(fmt.Sprintf("%d in %s", len(hops), traceID)))
				for _, h := range hops {
					fmt.Printf("    %s\n", h.String())
				}
				fmt.Printf("\n  %s\n", styleSubtle.Render("replay one with --at <node:port>, matching the right-hand side"))
				fmt.Println()
				return nil
			}

			hop, err := replay.Find(hops, at)
			if err != nil {
				return fmt.Errorf("%w (see --list)", err)
			}

			runner := &replay.Runner{
				Project:   flagProject,
				Reader:    reader,
				Publisher: sender,
				Settle:    settle,
			}

			fmt.Printf("  %s %s\n", styleKey.Render("replaying"), hop.String())
			fmt.Printf("  %s %s\n\n", styleKey.Render("of run"), styleSubtle.Render(traceID))

			res := runner.Run(cmd.Context(), traceID, hop)
			took := styleSubtle.Render(fmt.Sprintf("%.1fs", res.Duration.Seconds()))

			if res.Err != nil {
				fmt.Printf("  %s %v %s\n\n", styleWarn.Render("could not replay:"), res.Err, took)
				os.Exit(1)
			}
			if res.Unchanged() {
				fmt.Printf("  %s %s %s\n\n", styleOK.Render("✓ unchanged"), styleSubtle.Render("— every port reached, same shapes"), took)
				return nil
			}

			fmt.Printf("  %s %s\n\n", styleWarn.Render(fmt.Sprintf("✗ %d change(s)", len(res.Changes))), took)
			for _, ch := range res.Changes {
				fmt.Printf("      %s\n", ch.String())
			}
			fmt.Println()
			os.Exit(1)
			return nil
		},
	}

	c.Flags().StringVar(&at, "at", "", "re-drive the hop delivered to this port, e.g. --at budget-guard-52465:request (default: the run's first hop)")
	c.Flags().BoolVar(&list, "list", false, "list the replayable hops and exit")
	c.Flags().DurationVar(&settle, "settle", 3*time.Second, "how long the replayed branch must produce no new spans before it counts as finished")
	return c
}
