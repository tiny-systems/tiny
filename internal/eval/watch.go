package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Watching.
//
// A check you have to remember to run is a check that runs the day you write
// it and never again. Watching closes that: edit an eval and it re-runs; edit
// the flow and it re-runs; upgrade a module and it re-runs. The loop an agent
// works in — change something, find out immediately — stops depending on
// anyone remembering.
//
// Two things are watched because breakage arrives from two directions. The
// evals change when someone is writing them. The cluster changes when someone
// edits a flow or a module releases, and that is the direction nothing was
// looking in: today's session found a credential that had been dead for weeks
// and an overlay that had lied since it was written.

// ChangeSource reports the state of the thing under test as an opaque string.
// Anything that changes the string triggers a re-run, so a host decides for
// itself what counts as a change worth re-checking.
type ChangeSource interface {
	Fingerprint(ctx context.Context, projectName string) (string, error)
}

// WatchOptions configures a watch loop.
type WatchOptions struct {
	// Path is the eval file or directory being watched.
	Path string

	// Project scopes the cluster fingerprint.
	Project string

	// Cluster is optional: with no source, only the eval files are watched.
	Cluster ChangeSource

	// Interval is how often the two are checked. Default 2s — often enough to
	// feel immediate, rare enough that a watch left running overnight is not a
	// load on the cluster.
	Interval time.Duration

	// Settle waits for edits to stop before re-running, so saving three files
	// in a row produces one run rather than three.
	Settle time.Duration
}

// Watch runs the evals, then re-runs them whenever the files or the cluster
// change, until the context is cancelled.
//
// run receives the reason it was triggered so the caller can say why the
// screen just changed — a re-run with no explanation reads as a glitch.
func Watch(ctx context.Context, opts WatchOptions, run func(reason string)) error {
	interval := opts.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	settle := opts.Settle
	if settle <= 0 {
		settle = 750 * time.Millisecond
	}

	files, err := fingerprintFiles(opts.Path)
	if err != nil {
		return err
	}
	cluster := opts.fingerprintCluster(ctx)

	run("first run")

	var (
		pending     bool
		pendingWhy  string
		pendingFrom time.Time
	)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}

		if nextFiles, err := fingerprintFiles(opts.Path); err == nil && nextFiles != files {
			files = nextFiles
			pending, pendingWhy, pendingFrom = true, "evals changed", time.Now()
		}
		if next := opts.fingerprintCluster(ctx); next != cluster {
			cluster = next
			// A cluster change wins the label: it is the surprising one, and
			// the reason someone will want to know why this re-ran.
			pending, pendingWhy, pendingFrom = true, "project changed", time.Now()
		}

		if pending && time.Since(pendingFrom) >= settle {
			pending = false
			run(pendingWhy)
		}
	}
}

func (o WatchOptions) fingerprintCluster(ctx context.Context) string {
	if o.Cluster == nil {
		return ""
	}
	fp, err := o.Cluster.Fingerprint(ctx, o.Project)
	if err != nil {
		// An unreachable cluster is not a change. Treating it as one would
		// re-run the whole suite every tick while the connection is down,
		// which is the moment the output is least useful.
		return "unavailable"
	}
	return fp
}

// fingerprintFiles hashes the eval files' names, sizes and modification times.
// Content hashing would be more exact and would mean reading every file every
// two seconds to catch an edit that a timestamp already reports.
func fingerprintFiles(path string) (string, error) {
	var entries []string

	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("watch %s: %w", path, err)
	}

	if !info.IsDir() {
		entries = append(entries, describeFile(path, info))
	} else {
		err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !isEvalFile(p) {
				return nil
			}
			fi, err := d.Info()
			if err != nil {
				return nil
			}
			entries = append(entries, describeFile(p, fi))
			return nil
		})
		if err != nil {
			return "", err
		}
	}

	sort.Strings(entries)
	sum := sha256.Sum256([]byte(fmt.Sprint(entries)))
	return hex.EncodeToString(sum[:8]), nil
}

func describeFile(path string, info os.FileInfo) string {
	return fmt.Sprintf("%s:%d:%d", path, info.Size(), info.ModTime().UnixNano())
}
