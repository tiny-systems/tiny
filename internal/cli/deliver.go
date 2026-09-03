/*
tiny deliver is the source-agnostic courier: text on stdin lands in a
session's durable inbox. GitHub, Slack, cron — every event source is a
few adapter lines that pipe a message into this command; the binary knows
none of them. --ensure creates the target session on first contact, so an
empty namespace still has a brain to deliver to.
*/
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/sessions"
)

func newDeliverCmd() *cobra.Command {
	var ensure bool
	var repo string
	var envs []string
	cmd := &cobra.Command{
		Use:   "deliver <session>",
		Short: "Deliver stdin to a session's inbox (the courier event sources pipe into)",
		Long: "Reads a message from stdin and appends it to the session's durable inbox —\n" +
			"delivered into the agent's prompt within seconds, surviving restarts and\n" +
			"pauses. With --ensure the session is created if missing (--repo seeds its\n" +
			"workspace). Event sources (a GitHub workflow, a Slack bot, cron) are thin\n" +
			"adapters around this command.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			raw, err := io.ReadAll(io.LimitReader(os.Stdin, 256<<10))
			if err != nil {
				return err
			}
			msg := strings.TrimSpace(string(raw))
			if msg == "" {
				return fmt.Errorf("nothing on stdin — pipe the message in")
			}

			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			name := args[0]
			// Credentials ride as the session's labeled secret — mounted as
			// a refreshing FILE at /tiny-env (env vars freeze at container
			// start; files keep syncing across re-deliveries).
			if len(envs) > 0 {
				data := map[string]string{}
				for _, kv := range envs {
					k, v, ok := strings.Cut(kv, "=")
					if !ok || k == "" {
						return fmt.Errorf("--env wants KEY=VALUE, got %q", kv)
					}
					data[k] = v
				}
				if err := store.PublishSessionSecret(ctx, name, data); err != nil {
					return fmt.Errorf("publish env: %w", err)
				}
			}
			if ensure {
				if err := ensureSession(ctx, store, name, repo, len(envs) > 0); err != nil {
					return err
				}
			}
			if err := store.SendText(ctx, name, msg); err != nil {
				return err
			}
			fmt.Printf("  ✓ delivered to %s\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&ensure, "ensure", false, "create the session if it does not exist")
	cmd.Flags().StringVar(&repo, "repo", "", "git URL cloned into the workspace when --ensure creates the session")
	cmd.Flags().StringArrayVar(&envs, "env", nil, "KEY=VALUE delivered as a refreshing file /tiny-env/KEY (repeatable)")
	return cmd
}

// ensureSession makes sure the target exists; a created one boots idle and
// takes its briefing from the delivered messages themselves.
func ensureSession(ctx context.Context, store *sessions.Store, name, repo string, withEnv bool) error {
	se := &agentsv1.Session{}
	err := store.Kube.Client.Get(ctx, client.ObjectKey{Namespace: store.Kube.Namespace, Name: name}, se)
	if err == nil {
		return nil
	}
	opts := sessions.CreateOpts{Name: name, Repo: repo}
	if withEnv {
		opts.EnvSecret = name + "-env"
	}
	_, err = store.Create(ctx, opts)
	return err
}
