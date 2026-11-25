/*
tiny run runs one command in a session box and exits with its exit code:
no agent, no model, no credentials. The box is the one agents get — the
restricted container profile, the namespace's egress policy and
allow-list, node placement, resource limits, and the runtime class when
asked for (gVisor, Kata). It is the way to run code you trust least: an
untrusted repository's tests, a generated script.

The command's output streams to stdout; tiny's own progress goes to
stderr, so `tiny run ... > out.txt` captures only the command. The session
is deleted afterwards unless --keep, and Ctrl-C deletes it too.
*/
package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/sessions"
)

// ExitError ends the process with a command's exit code and no message of
// its own: the command already said what went wrong.
type ExitError struct{ Code int }

func (e ExitError) Error() string { return fmt.Sprintf("exit status %d", e.Code) }

// interruptedCode is the conventional exit code after Ctrl-C (128+SIGINT).
const interruptedCode = 130

type runOpts struct {
	name, image, repo, dir, cpu, memory, runtimeClass string
	user                                              int64
	unconfined, keep, detach                          bool
}

func newRunCmd() *cobra.Command {
	var o runOpts
	cmd := &cobra.Command{
		Use:   "run [flags] [--] <command> [args...]",
		Short: "Run one command in a sandboxed session and exit with its exit code",
		Long: "Runs a command once in a session box — no agent, no model, no credentials — and\n" +
			"streams its output. The box is the one agents get: restricted container profile,\n" +
			"the namespace's egress allow-list, resource limits, and --runtime-class for gVisor\n" +
			"or Kata. For code you trust least: an untrusted repo's tests, a generated script.\n\n" +
			"--repo clones a git URL (the image needs git) and --dir ships a local folder; the\n" +
			"command then runs inside it. Flags go before the command; everything after the\n" +
			"command's name is passed to it untouched.\n\n" +
			"Output goes to stdout and tiny's progress to stderr. The session is deleted when\n" +
			"the command ends, or on Ctrl-C, unless --keep. With --detach, tiny starts it and\n" +
			"returns at once.",
		Example: "  tiny run --image python:3.13-slim --runtime-class gvisor -- python3 -c 'print(42)'\n" +
			"  tiny run --image node:22 --repo https://github.com/acme/app -- npm test\n" +
			"  tiny run --image golang:1.26 --dir . -- go test ./...",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCommand(cmd.Context(), o, args)
		},
	}
	// Flags stop at the command's name, so `tiny run ls -la` passes -la to ls.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVar(&o.name, "name", "", "session name (generated when omitted)")
	cmd.Flags().StringVar(&o.image, "image", "", "image to run the command in; it needs sh (default: the tiny agent image)")
	cmd.Flags().StringVar(&o.repo, "repo", "", "git URL cloned into the workspace first; the command runs inside it (the image needs git)")
	cmd.Flags().StringVar(&o.dir, "dir", "", "local folder shipped into the workspace first, as it stands; the command runs inside it")
	cmd.Flags().StringVar(&o.runtimeClass, "runtime-class", "", "sandboxed runtime the cluster provides, e.g. gvisor or kata (the RuntimeClass must exist)")
	cmd.Flags().StringVar(&o.cpu, "cpu", "", "CPU request (e.g. 2, 500m)")
	cmd.Flags().StringVar(&o.memory, "memory", "", "memory request and limit (e.g. 4Gi)")
	cmd.Flags().Int64Var(&o.user, "user", 0, "uid to run as, for images wired to a specific user")
	cmd.Flags().BoolVar(&o.unconfined, "unconfined", false, "lift the seccomp/capability hardening (rootless buildah, podman)")
	cmd.Flags().BoolVar(&o.keep, "keep", false, "keep the session afterwards, to inspect it with tiny shell")
	cmd.Flags().BoolVarP(&o.detach, "detach", "d", false, "start the command and return without waiting; implies --keep")
	return cmd
}

func runCommand(parent context.Context, o runOpts, command []string) error {
	if o.dir != "" && o.repo != "" {
		return fmt.Errorf("--dir and --repo both name what to run in; pick one")
	}
	if o.dir != "" {
		abs, err := filepath.Abs(o.dir)
		if err != nil {
			return err
		}
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return fmt.Errorf("--dir %s is not a directory", o.dir)
		}
		o.dir = abs
	}
	k, err := sessionKube()
	if err != nil {
		return err
	}
	if err := confirmStart(k); err != nil {
		return err
	}
	// Ctrl-C and SIGTERM stop the wait and lead to cleanup, instead of
	// leaving a session behind.
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()

	setup, cancelSetup := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelSetup()
	if err := ensureRuntime(setup, k); err != nil {
		return err
	}
	store := newStore(k)
	se, err := store.Create(setup, sessions.CreateOpts{
		Name: o.name, Repo: o.repo, Image: o.image,
		CPU: o.cpu, Memory: o.memory, User: o.user,
		Unconfined: o.unconfined, RuntimeClass: o.runtimeClass,
		Command: command,
		Handoff: o.dir != "",
	})
	if err != nil {
		return err
	}
	logf("  ◌ session %s created on %s", se.Name, store.Target())
	keep := o.keep || o.detach
	cleanup := func() {
		if keep {
			return
		}
		c, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := store.Delete(c, se.Name); err != nil {
			logf("  ! could not delete session %s: %v (tiny rm %s)", se.Name, err, se.Name)
			return
		}
		logf("  ◌ session %s deleted", se.Name)
	}

	// fail reports why the run stopped, then removes the session.
	fail := func(err error) error {
		err = interrupted(ctx, err)
		cleanup()
		return err
	}

	pod, err := waitForSessionTo(setup, store, se.Name, os.Stderr, "running")
	if err != nil {
		return fail(err)
	}
	if o.dir != "" {
		logf("  ◌ shipping %s", o.dir)
		shipped := 0
		if err := store.PushTree(setup, pod, "/workspace/repo", o.dir, func(string) { shipped++ }); err != nil {
			cleanup()
			return interrupted(ctx, err)
		}
		if err := store.MarkHandoffComplete(setup, pod); err != nil {
			cleanup()
			return interrupted(ctx, err)
		}
		logf("  ✓ %s landed in /workspace/repo", files(shipped))
	}
	if o.detach {
		logf("  started; follow it with:  kubectl -n %s logs -f deploy/%s-agent", k.Namespace, se.Name)
		logf("  look around:  tiny shell %s    remove:  tiny rm %s", se.Name, se.Name)
		return nil
	}

	code, err := followCommand(ctx, store, pod)
	if err != nil {
		return fail(err)
	}
	if code != 0 {
		logf("  ✗ exited %d", code)
	}
	if keep {
		logf("  kept: tiny shell %s to look around, tiny rm %s when done", se.Name, se.Name)
	}
	cleanup()
	if code != 0 {
		return ExitError{Code: code}
	}
	return nil
}

func files(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// followCommand streams the command's output until its exit code is
// recorded, and returns that code.
func followCommand(ctx context.Context, store *sessions.Store, pod string) (int, error) {
	logCtx, stopLogs := context.WithCancel(ctx)
	streamed := make(chan error, 1)
	go func() { streamed <- store.StreamLogs(logCtx, pod, os.Stdout) }()
	defer func() {
		stopLogs()
		<-streamed
	}()

	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-tick.C:
		}
		code, done, err := store.CommandResult(ctx, pod)
		if err != nil {
			if ctx.Err() != nil {
				return 0, ctx.Err()
			}
			continue // the pod may be restarting; the next tick asks again
		}
		if done {
			// The last lines can trail the exit-code file by a moment.
			select {
			case <-ctx.Done():
			case <-time.After(700 * time.Millisecond):
			}
			return code, nil
		}
	}
}

// interrupted turns a cancelled wait after Ctrl-C into the conventional
// exit code instead of a "context canceled" error.
func interrupted(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		logf("  interrupted")
		return ExitError{Code: interruptedCode}
	}
	return err
}

func logf(format string, a ...any) { fmt.Fprintf(os.Stderr, format+"\n", a...) }
