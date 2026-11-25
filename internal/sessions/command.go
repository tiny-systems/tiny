package sessions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	utilexec "k8s.io/client-go/util/exec"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
)

// commandAgent is the session agent that runs one command and no model.
const commandAgent = "command"

// commandDoneFile is where the entrypoint records a command session's exit
// code (images/agent/entrypoint.sh). Present means finished.
const commandDoneFile = "/workspace/.tiny/command-done"

// readDone reads the exit code with shell builtins only: a command
// session's image is arbitrary and may have no cat, but it always has the
// sh the entrypoint runs under.
var readDone = []string{"sh", "-c", `IFS= read -r c < ` + commandDoneFile + ` && echo "$c"`}

// CommandResult reports whether a command session's command has finished,
// and its exit code. Not finished yet is (0, false, nil).
func (s *Store) CommandResult(ctx context.Context, pod string) (code int, done bool, err error) {
	var out, errOut bytes.Buffer
	if err := s.execStream(ctx, pod, agentContainer, readDone, nil, &out, &errOut); err != nil {
		var exit utilexec.CodeExitError
		if errors.As(err, &exit) {
			return 0, false, nil // the file isn't there yet
		}
		return 0, false, err
	}
	return parseExitCode(out.String())
}

func parseExitCode(raw string) (int, bool, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false, fmt.Errorf("unreadable exit code %q", strings.TrimSpace(raw))
	}
	return n, true, nil
}

// StreamLogs copies the session's agent-container output to w until the
// context ends. For a command session that is the command's stdout and
// stderr, interleaved as the container wrote them.
func (s *Store) StreamLogs(ctx context.Context, pod string, w io.Writer) error {
	restClient, err := restClientFor(setGroupVersion(s.Kube.RESTConfig))
	if err != nil {
		return err
	}
	rc, err := restClient.Get().
		Resource("pods").Name(pod).Namespace(s.Kube.Namespace).
		SubResource("log").
		VersionedParams(&corev1.PodLogOptions{Container: agentContainer, Follow: true}, scheme.ParameterCodec).
		Stream(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = rc.Close() }()
	_, err = io.Copy(w, rc)
	if ctx.Err() != nil {
		return nil // stopped on purpose
	}
	return err
}

// commandCache remembers command results for the fleet screen, which
// refreshes every couple of seconds: a finished result never changes, and
// an unfinished one is rechecked at most every recheckAfter.
type commandCache struct {
	mu      sync.Mutex
	results map[string]cachedResult // by pod name
}

type cachedResult struct {
	code    int
	done    bool
	checked time.Time
}

const recheckAfter = 10 * time.Second

// lookup returns a cached result and whether it is still fresh enough to
// use without asking the pod again.
func (c *commandCache) lookup(pod string, now time.Time) (cachedResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.results[pod]
	if !ok {
		return cachedResult{}, false
	}
	return r, r.done || now.Sub(r.checked) < recheckAfter
}

func (c *commandCache) store(pod string, r cachedResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.results == nil {
		c.results = map[string]cachedResult{}
	}
	c.results[pod] = r
}

// fillCommandResults marks finished command sessions on the fleet screen:
// ✓ exited 0, ✗ exited N. Only running command sessions are asked, and a
// failed read leaves the row as it was.
func (s *Store) fillCommandResults(ctx context.Context, rows []Row, now time.Time) {
	for i := range rows {
		r := &rows[i]
		if !r.Command || r.Pod == "" || r.Phase != string(agentsv1.SessionRunning) {
			continue
		}
		res, fresh := s.commands.lookup(r.Pod, now)
		if !fresh {
			code, done, err := s.CommandResult(ctx, r.Pod)
			if err != nil {
				continue
			}
			res = cachedResult{code: code, done: done, checked: now}
			s.commands.store(r.Pod, res)
		}
		applyCommandResult(r, res)
	}
}

// applyCommandResult is the display rule, split out for tests.
func applyCommandResult(r *Row, res cachedResult) {
	switch {
	case !res.done:
		if r.Title == "" {
			r.Title = "running its command"
		}
	case res.code == 0:
		r.Phase, r.Exit, r.Title = string(agentsv1.SessionDone), &res.code, "exited 0"
	default:
		r.Phase, r.Exit, r.Title = string(agentsv1.SessionFailed), &res.code, fmt.Sprintf("exited %d", res.code)
	}
}
