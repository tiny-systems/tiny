package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/tiny-systems/tiny/internal/sessions"
)

// waitForSession narrates a session's birth: it polls until the pod runs and
// prints each stage once, so the first-ever run (a multi-minute image pull)
// reads as progress instead of a silent stall. Returns the pod name.
func waitForSession(ctx context.Context, store *sessions.Store, name string) (string, error) {
	var last string
	report := func(stage string) {
		if stage != last && stage != "" {
			fmt.Printf("  ◌ %s\n", stage)
			last = stage
		}
	}
	for {
		stage, pod, failure, err := store.Birth(ctx, name)
		if err != nil {
			return "", err
		}
		if failure != "" {
			return "", fmt.Errorf("session %s failed to start: %s", name, failure)
		}
		if stage == "" { // running — but a broken image dies within a second; let it settle
			settled := true
			for range 3 {
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(time.Second):
				}
				st, _, fail, err := store.Birth(ctx, name)
				if err != nil {
					return "", err
				}
				if fail != "" {
					return "", fmt.Errorf("session %s failed to start: %s", name, fail)
				}
				if st != "" {
					settled = false
					break
				}
			}
			if !settled {
				continue
			}
			fmt.Printf("  ● agent up (%s)\n", pod)
			return pod, nil
		}
		report(stage)
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("timed out waiting for session %s (last stage: %s)", name, last)
		case <-time.After(time.Second):
		}
	}
}
