package adapters

import (
	"context"
	"fmt"

	sdktools "github.com/tiny-systems/module/pkg/tools"

	"github.com/tiny-systems/tiny/internal/replay"
)

// ReplayRunner gives an agent the same check `tiny replay` runs.
//
// One implementation behind both, because a diff that disagreed with the CLI's
// would be worse than no diff: the agent would fix what the command says is
// fine, or the reverse.
type ReplayRunner struct {
	reader    replay.Reader
	publisher replay.Publisher
}

func NewReplayRunner(reader replay.Reader, publisher replay.Publisher) *ReplayRunner {
	return &ReplayRunner{reader: reader, publisher: publisher}
}

func (r *ReplayRunner) ReplayRun(ctx context.Context, projectName, traceID, at string) (sdktools.ReplayOutcome, error) {
	spans, err := r.reader.ReadTraceDetail(ctx, projectName, traceID)
	if err != nil {
		return sdktools.ReplayOutcome{}, err
	}
	hops := replay.Hops(spans)
	if len(hops) == 0 {
		return sdktools.ReplayOutcome{}, fmt.Errorf("run %s has no replayable hop — every recorded span is a trigger or carries no payload", traceID)
	}
	hop, err := replay.Find(hops, at)
	if err != nil {
		return sdktools.ReplayOutcome{}, err
	}

	runner := &replay.Runner{Project: projectName, Reader: r.reader, Publisher: r.publisher}
	res := runner.Run(ctx, traceID, hop)
	if res.Err != nil {
		return sdktools.ReplayOutcome{}, res.Err
	}

	outcome := sdktools.ReplayOutcome{
		Hop:       hop.String(),
		Unchanged: res.Unchanged(),
		Compared:  res.Compared,
	}
	for _, c := range res.Changes {
		outcome.Changes = append(outcome.Changes, c.String())
	}
	return outcome, nil
}

var _ sdktools.ReplayRunner = (*ReplayRunner)(nil)
