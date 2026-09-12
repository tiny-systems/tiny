package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/runtimeinstall"
	"github.com/tiny-systems/tiny/internal/sessions"
)

// The session-runtime commands: the product's new front door.
//
//	tiny new "fix the flaky test"   start a session (installs the runtime on
//	                                first contact, one confirm)
//	tiny sessions                   the fleet screen: ✳ needs-you rows,
//	                                attach, answer, start, delete

func sessionKube() (*kube.Client, error) {
	ctxName, ns, err := resolveTarget()
	if err != nil {
		return nil, err
	}
	return kube.NewClient(kube.Options{
		Context:   ctxName,
		Namespace: ns,
	})
}

// ensureRuntime installs CRDs + manager when absent. One confirmed touch;
// re-running is a server-side-apply no-op, so it doubles as upgrade.
func ensureRuntime(ctx context.Context, k *kube.Client) error {
	if runtimeinstall.Installed(ctx, k) {
		return nil
	}
	if err := confirmTarget(runtimeinstall.ConfirmPrompt); err != nil {
		return err
	}
	if err := runtimeinstall.Apply(ctx, k.RESTConfig, k.Namespace); err != nil {
		return err
	}
	fmt.Println("  ✓ runtime installed")
	return nil
}

func newNewCmd() *cobra.Command {
	var name, repo, dir, image, cpu, memory, agent, model string
	var user int64
	var noAttachHint bool
	cmd := &cobra.Command{
		Use:   "new [task]",
		Short: "Start a coding-agent session on the cluster",
		Long: "Start a session: the task becomes a Session object, the controller runs it as a pod\n" +
			"with a persistent workspace, and it keeps working when you disconnect. Installs the\n" +
			"runtime on first contact (asks once).\n\n" +
			"Without a task the session boots idle and you are attached straight into the\n" +
			"agent's terminal — talk to it like a local one, detach with ctrl-q d.\n\n" +
			"--repo clones from git; --dir ships a local folder as it stands, uncommitted\n" +
			"changes and .git included — the way to move work to a cluster that cannot\n" +
			"reach your git remote, or that is not pushed anywhere yet.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			task := strings.TrimSpace(strings.Join(args, " "))
			if dir != "" && repo != "" {
				return fmt.Errorf("--dir and --repo both name a workspace source; pick one")
			}
			if dir != "" {
				abs, aerr := filepath.Abs(dir)
				if aerr != nil {
					return aerr
				}
				if st, serr := os.Stat(abs); serr != nil || !st.IsDir() {
					return fmt.Errorf("--dir %s is not a directory", dir)
				}
				dir = abs
			}
			k, err := sessionKube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 5*time.Minute)
			defer cancel()
			if err := ensureRuntime(ctx, k); err != nil {
				return err
			}
			store := newStore(k)
			se, err := store.Create(ctx, sessions.CreateOpts{
				Name: name, Task: task, Repo: repo,
				Image: image, Agent: agent, Model: model,
				CPU: cpu, Memory: memory, User: user,
				// A shipped folder arrives after the pod starts, so the
				// entrypoint must wait for it instead of starting the agent
				// on an empty workspace.
				Handoff: dir != "",
			})
			if err != nil {
				return err
			}
			fmt.Printf("  ◌ session %s created on %s\n", se.Name, store.Target())
			pod, err := waitForSession(ctx, store, se.Name)
			if err != nil {
				return err
			}
			if dir != "" {
				fmt.Printf("  ◌ shipping %s\n", dir)
				var files int
				if err := store.PushTree(ctx, pod, "/workspace/repo", dir, func(string) { files++ }); err != nil {
					return err
				}
				if err := store.MarkHandoffComplete(ctx, pod); err != nil {
					return err
				}
				fmt.Printf("  ✓ %d files landed in /workspace/repo\n", files)
			}
			if task == "" {
				fmt.Println("  attaching — detach with ctrl-q d")
				return store.Attach(cmd.Context(), se.Name, pod)
			}
			if !noAttachHint {
				fmt.Printf("    watch and answer:  tiny\n")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "session name (generated when omitted)")
	cmd.Flags().StringVar(&repo, "repo", "", "git URL cloned into the workspace")
	cmd.Flags().StringVar(&dir, "dir", "", "local folder shipped into the workspace as it stands (uncommitted changes and .git included)")
	cmd.Flags().StringVar(&image, "image", "", "session image — any glibc-based image with git (golang:1.26, your dev image); default: the tiny agent image")
	cmd.Flags().StringVar(&agent, "agent", "", "coding agent to run: claude (default) or codex")
	cmd.Flags().StringVar(&model, "model", "", "model override for the agent (claude --model / codex -m)")
	cmd.Flags().StringVar(&cpu, "cpu", "", "CPU request (e.g. 2, 500m)")
	cmd.Flags().StringVar(&memory, "memory", "", "memory request and limit (e.g. 4Gi)")
	cmd.Flags().Int64Var(&user, "user", 0, "uid to run as, for images wired to a specific user (buildah: 1000)")
	cmd.Flags().BoolVar(&noAttachHint, "quiet", false, "skip the follow-up hint")
	return cmd
}

// runSessionsTUI is also what bare `tiny` runs.
func runSessionsTUI(parent context.Context) error {
	k, err := sessionKube()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	if err := ensureRuntime(ctx, k); err != nil {
		return err
	}
	store := newStore(k)
	_, err = tea.NewProgram(sessions.NewModel(store), tea.WithAltScreen()).Run()
	return err
}

// applyRuntime is init's unconditional install/upgrade.
func applyRuntime(ctx context.Context, k *kube.Client) error {
	return runtimeinstall.Apply(ctx, k.RESTConfig, k.Namespace)
}

func newShellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell <session>",
		Short: "Open a shell on a session's workspace (works on finished sessions too)",
		Long: "Starts a small inspection pod mounting the session's persistent workspace and\n" +
			"drops you into it. The agent is not disturbed; finished sessions work too —\n" +
			"their workspace outlives their pod. The pod is removed when you exit.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Minute)
			defer cancel()
			store := newStore(k)
			fmt.Println("  ◌ starting inspection shell…")
			pod, err := store.EnsureShellPod(ctx, args[0])
			if err != nil {
				return err
			}
			c := exec.Command("kubectl", "exec", "-it", "-n", k.Namespace, pod, "--", "bash")
			c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
			runErr := c.Run()
			// Best effort cleanup; the 12h self-reap covers the rest.
			cleanupCtx, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel2()
			_ = store.DeleteShellPod(cleanupCtx, args[0])
			return runErr
		},
	}
}
