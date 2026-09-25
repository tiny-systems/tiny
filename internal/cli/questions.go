/*
tiny questions lists what is waiting on a person.

Until now a pending question was visible only on the fleet screen, which
means a session can sit blocked while nobody is looking at a terminal —
five of them stacked up unnoticed the first time we ran the loop for
real. A plain list makes that visible to anything that can run a command,
and --json lets a CI job turn it into a notification without tiny needing
to know what GitHub or Slack is.

Answering stays where it is: it runs with the answerer's own credentials,
so it needs a cluster, not a comment box.
*/
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sigs.k8s.io/controller-runtime/pkg/client"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/sessions"
)

// questionOut is the shape --json emits. Stable on purpose: a workflow
// parsing it should not break when the CRD grows a field.
type questionOut struct {
	Name    string   `json:"name"`
	Session string   `json:"session"`
	Text    string   `json:"text"`
	Options []string `json:"options,omitempty"`
	AgeSecs int      `json:"ageSeconds"`
	// Origin is whatever the event source recorded on the session — an
	// issue reference, a ticket, a thread. Opaque to tiny.
	Origin string `json:"origin,omitempty"`
	// Reason distinguishes a decision ("tool") from an idle nudge
	// ("notification"); the latter appear only with --all.
	Reason string `json:"reason"`
	Answer string `json:"answerCommand"`
}

func newQuestionsCmd() *cobra.Command {
	var asJSON, all bool
	cmd := &cobra.Command{
		Use:   "questions",
		Short: "List questions waiting on a human",
		Long: "Every unanswered question in the namespace, with the command that answers it.\n\n" +
			"--json emits a stable array so an event source (a GitHub workflow, a Slack\n" +
			"hook) can post them somewhere people look. Answering still happens through\n" +
			"tiny answer, with the answerer's own credentials.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()

			snap, err := store.Load(ctx)
			if err != nil {
				return err
			}

			now := time.Now()
			var out []questionOut
			// Origins live on the Session objects, which the snapshot
			// flattens away — read them directly.
			origins := map[string]string{}
			var list agentsv1.SessionList
			if err := k.Client.List(ctx, &list, client.InNamespace(k.Namespace)); err == nil {
				for _, se := range list.Items {
					if o := se.Annotations[sessions.OriginAnnotation]; o != "" {
						origins[se.Name] = o
					}
				}
			}
			add := func(session string, q *agentsv1.Question) {
				// "notification" questions are idle nudges — attaching
				// clears them and there is nothing to decide. Listing them
				// as decisions produced notices nobody could act on, so
				// they hide behind --all.
				if !all && q.Spec.Reason == "notification" {
					return
				}
				out = append(out, questionOut{
					Name: q.Name, Session: session, Text: q.Spec.Text,
					Options: q.Spec.Options, Origin: origins[session],
					Reason:  orDefaultStr(q.Spec.Reason, "tool"),
					AgeSecs: int(now.Sub(q.CreationTimestamp.Time).Seconds()),
					Answer:  fmt.Sprintf("tiny answer %s <your answer>", q.Name),
				})
			}
			for _, row := range snap.Rows {
				if q := row.Question; q != nil {
					add(row.Name, q)
				}
			}
			for i := range snap.Loose {
				add(snap.Loose[i].Spec.Session.Name, &snap.Loose[i])
			}

			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				if out == nil {
					out = []questionOut{}
				}
				return enc.Encode(out)
			}
			if len(out) == 0 {
				fmt.Println("  nothing waiting")
				return nil
			}
			for _, q := range out {
				fmt.Printf("  %s %s %s\n", styleWarn.Render("✳"), styleTitle.Render(q.Name),
					styleSubtle.Render(q.Session+" · "+shortDur(q.AgeSecs)))
				fmt.Printf("    %s\n", firstLine(q.Text, 100))
				if len(q.Options) > 0 {
					fmt.Printf("    %s %s\n", styleKey.Render("options:"), joinNames(q.Options))
				}
				fmt.Printf("    %s\n\n", styleSubtle.Render(q.Answer))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "include idle notifications (reason=notification), not just decisions")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a stable JSON array for an event source to post")
	return cmd
}

func orDefaultStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// firstLine keeps a multi-line question to one readable line: a spawn
// request carries an entire task, and the list is an index, not the text.
func firstLine(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}

// shortDur renders an age the way the fleet screen does.
func shortDur(secs int) string {
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm", secs/60)
	case secs < 86400:
		return fmt.Sprintf("%dh", secs/3600)
	default:
		return fmt.Sprintf("%dd", secs/86400)
	}
}
