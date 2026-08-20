package adapters

import (
	"context"

	"github.com/tiny-systems/module/pkg/evals"
	sdktools "github.com/tiny-systems/module/pkg/tools"

	"github.com/tiny-systems/tiny/internal/eval"
)

// EvalRunner gives an agent the same check the CLI runs.
//
// The point is the loop, not the tool: an agent that builds a flow can now run
// it and find out whether it works, in one call, instead of firing a signal,
// pulling a trace and deciding by eye — the sequence that was skipped every
// time, which is why flows shipped unverified.
//
// Firing and observing live here because they need the cluster. The verdict
// lives in the SDK's evals package, so this host and the platform reach the
// same conclusion about the same run.
type EvalRunner struct {
	runner *eval.Runner
}

func NewEvalRunner(sender eval.Sender, reader eval.Reader) *EvalRunner {
	return &EvalRunner{runner: &eval.Runner{Sender: sender, Reader: reader}}
}

func (e *EvalRunner) RunEval(ctx context.Context, projectName string, spec evals.Spec) (sdktools.EvalOutcome, error) {
	// The project comes from the caller rather than construction: one bundle
	// serves whatever project the session is bound to.
	runner := *e.runner
	runner.Project = projectName

	result := runner.Run(ctx, spec)
	if result.Err != nil {
		return sdktools.EvalOutcome{}, result.Err
	}

	outcome := sdktools.EvalOutcome{
		Passed:   len(result.Failures) == 0,
		TraceID:  result.TraceID,
		Observed: result.Observed,
		Errors:   result.Observed.Errors,
		Usage:    result.Observed.Usage,
	}
	for _, f := range result.Failures {
		outcome.Failures = append(outcome.Failures, f.String())
	}
	return outcome, nil
}

var _ sdktools.EvalRunner = (*EvalRunner)(nil)
