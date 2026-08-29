/*
tiny export is the outbox courier: the credential-less half of the git
loop. Agents cannot push — they hold no credentials at all — so they
write git bundles to /workspace/outbox/. A scheduled Actions job on the
in-cluster runner runs this command: it lifts every pending bundle out of
every session workspace over the exec API into ./outbox/ locally, where
the job's own always-alive GITHUB_TOKEN pushes the branches. Bundles are
moved to outbox/.done/ in the workspace afterwards, so nothing exports
twice.
*/
package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
	"github.com/tiny-systems/tiny/internal/sessions"
	"github.com/tiny-systems/tiny/internal/workload"
)

func newExportCmd() *cobra.Command {
	var ack string
	cmd := &cobra.Command{
		Use:    "export",
		Hidden: true, // Actions plumbing
		Short:  "Collect session outbox bundles into ./outbox (Actions courier)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := &sessions.Store{Kube: k}
			ctx, cancel := context.WithTimeout(cmd.Context(), 4*time.Minute)
			defer cancel()

			// --ack session__bundle: retire one bundle after its push.
			if ack != "" {
				session, bundle, ok := strings.Cut(ack, "__")
				if !ok {
					return fmt.Errorf("--ack wants session__bundle")
				}
				pod, err := podForSession(ctx, k, session)
				if err != nil {
					return err
				}
				return store.AckOutboxBundle(ctx, pod, bundle)
			}

			pods := &corev1.PodList{}
			if err := k.Client.List(ctx, pods, client.InNamespace(k.Namespace),
				client.MatchingLabels{"app": "tiny-session"}); err != nil {
				return err
			}
			if err := os.MkdirAll("outbox", 0o755); err != nil {
				return err
			}
			count := 0
			for i := range pods.Items {
				p := &pods.Items[i]
				if p.Status.Phase != corev1.PodRunning {
					continue
				}
				session := p.Labels[workload.SessionLabel]
				names, err := store.ListOutbox(ctx, p.Name)
				if err != nil {
					fmt.Fprintf(os.Stderr, "  ! %s: %v\n", session, err)
					continue
				}
				for _, name := range names {
					local := filepath.Join("outbox", fmt.Sprintf("%s__%s", session, name))
					if err := store.FetchOutboxBundle(ctx, p.Name, name, local); err != nil {
						fmt.Fprintf(os.Stderr, "  ! %s/%s: %v\n", session, name, err)
						continue
					}
					fmt.Printf("%s\n", local)
					count++
				}
			}
			if count == 0 {
				fmt.Fprintln(os.Stderr, "outbox empty")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&ack, "ack", "", "retire session__bundle after a successful push")
	return cmd
}

func podForSession(ctx context.Context, k *kube.Client, session string) (string, error) {
	pods := &corev1.PodList{}
	if err := k.Client.List(ctx, pods, client.InNamespace(k.Namespace),
		client.MatchingLabels{workload.SessionLabel: session}); err != nil {
		return "", err
	}
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			return pods.Items[i].Name, nil
		}
	}
	return "", fmt.Errorf("no running pod for session %s", session)
}
