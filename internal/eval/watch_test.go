package eval

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeCluster struct {
	mu    sync.Mutex
	value string
	err   error
}

func (f *fakeCluster) set(v string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.value = v
}

func (f *fakeCluster) fail(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeCluster) Fingerprint(context.Context, string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.value, f.err
}

// runs records why the loop re-ran, which is the part a person reads.
type runs struct {
	mu      sync.Mutex
	reasons []string
}

func (r *runs) record(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append(r.reasons, reason)
}

func (r *runs) all() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reasons...)
}

func (r *runs) waitFor(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := r.all(); len(got) >= n {
			return got
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("only %d runs after waiting: %v", len(r.all()), r.all())
	return nil
}

func evalDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.yaml"), "name: a\ntrigger: {node: n1}\n")
	return dir
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func startWatch(t *testing.T, opts WatchOptions, r *runs) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	opts.Interval = 10 * time.Millisecond
	opts.Settle = 10 * time.Millisecond
	go func() {
		if err := Watch(ctx, opts, r.record); err != nil {
			t.Error(err)
		}
	}()
	return cancel
}

// The first run happens immediately: a watch that shows nothing until
// something changes leaves you staring at a blank screen wondering whether it
// is working.
func TestWatchRunsOnceUpFront(t *testing.T) {
	r := &runs{}
	cancel := startWatch(t, WatchOptions{Path: evalDir(t)}, r)
	defer cancel()

	if got := r.waitFor(t, 1); got[0] != "first run" {
		t.Fatalf("reasons = %v", got)
	}
}

func TestEditingAnEvalRerunsIt(t *testing.T) {
	dir := evalDir(t)
	r := &runs{}
	cancel := startWatch(t, WatchOptions{Path: dir}, r)
	defer cancel()
	r.waitFor(t, 1)

	write(t, filepath.Join(dir, "a.yaml"), "name: a changed\ntrigger: {node: n1}\n")

	if got := r.waitFor(t, 2); got[1] != "evals changed" {
		t.Fatalf("reasons = %v", got)
	}
}

func TestAddingAnEvalRerunsIt(t *testing.T) {
	dir := evalDir(t)
	r := &runs{}
	cancel := startWatch(t, WatchOptions{Path: dir}, r)
	defer cancel()
	r.waitFor(t, 1)

	write(t, filepath.Join(dir, "b.yaml"), "name: b\ntrigger: {node: n2}\n")
	r.waitFor(t, 2)
}

// The direction breakage actually arrives from: nobody touched the evals, a
// module released or a flow was edited, and the claims are now untested.
func TestAChangedProjectRerunsTheEvals(t *testing.T) {
	cluster := &fakeCluster{value: "v1"}
	r := &runs{}
	cancel := startWatch(t, WatchOptions{Path: evalDir(t), Cluster: cluster}, r)
	defer cancel()
	r.waitFor(t, 1)

	cluster.set("v2")

	if got := r.waitFor(t, 2); got[1] != "project changed" {
		t.Fatalf("reasons = %v", got)
	}
}

// A cluster that stays put must not look like one that keeps changing, or the
// suite runs forever against a project nobody touched.
func TestAnUnchangedProjectDoesNotRerun(t *testing.T) {
	cluster := &fakeCluster{value: "v1"}
	r := &runs{}
	cancel := startWatch(t, WatchOptions{Path: evalDir(t), Cluster: cluster}, r)
	defer cancel()
	r.waitFor(t, 1)

	time.Sleep(150 * time.Millisecond)
	if got := r.all(); len(got) != 1 {
		t.Fatalf("re-ran with nothing changed: %v", got)
	}
}

// An unreachable cluster is not a change. Treating it as one re-runs the whole
// suite every tick at exactly the moment the output is least useful.
func TestAnUnreachableClusterIsNotAChange(t *testing.T) {
	cluster := &fakeCluster{value: "v1"}
	r := &runs{}
	cancel := startWatch(t, WatchOptions{Path: evalDir(t), Cluster: cluster}, r)
	defer cancel()
	r.waitFor(t, 1)

	cluster.fail(context.DeadlineExceeded)
	time.Sleep(60 * time.Millisecond)
	before := len(r.all())
	time.Sleep(150 * time.Millisecond)

	// One re-run when it goes away is honest; a stream of them is not.
	if after := len(r.all()); after > before {
		t.Fatalf("kept re-running while the cluster was unreachable: %d then %d", before, after)
	}
}

func TestWatchStopsWhenCancelled(t *testing.T) {
	r := &runs{}
	cancel := startWatch(t, WatchOptions{Path: evalDir(t)}, r)
	r.waitFor(t, 1)
	cancel()

	time.Sleep(50 * time.Millisecond)
	before := len(r.all())
	time.Sleep(100 * time.Millisecond)
	if after := len(r.all()); after != before {
		t.Fatalf("kept running after cancel: %d then %d", before, after)
	}
}
