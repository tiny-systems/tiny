/*
tiny rm deletes sessions. A session is a workspace and a transcript, so it
asks first, the same way the fleet screen does: y/N for named sessions, a
typed "delete all" for --all. Without a terminal it refuses unless -y says
the caller meant it, because a script that deletes by accident is worse
than one that stops and says why.

Before asking, it looks in each running session's outbox: bundles still
there are work that never reached GitHub, and they go with the session.
*/
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"

	agentsv1 "github.com/tiny-systems/tiny/api/v1alpha1"
	"github.com/tiny-systems/tiny/internal/sessions"
)

const typedDeleteAll = "delete all"

func newRmCmd() *cobra.Command {
	var all bool
	cmd := &cobra.Command{
		Use:   "rm <session>... | --all",
		Short: "Delete sessions — their workspace and transcript go with them",
		RunE: func(cmd *cobra.Command, args []string) error {
			if all == (len(args) > 0) {
				return fmt.Errorf("name one or more sessions, or pass --all — not both, not neither")
			}
			interactive := stdinIsTTY()
			if !flagYes && !interactive {
				return fmt.Errorf("refusing to delete without a terminal to confirm on: pass -y if you mean it")
			}
			k, err := sessionKube()
			if err != nil {
				return err
			}
			store := newStore(k)
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()

			if all {
				list := &agentsv1.SessionList{}
				if err := k.Client.List(ctx, list); err != nil {
					return err
				}
				if len(list.Items) == 0 {
					fmt.Println("  nothing to delete")
					return nil
				}
				fmt.Printf("  %d session(s) in %s/%s, each with its workspace and transcript\n", len(list.Items), k.ContextName, k.Namespace)
				for i := range list.Items {
					warnOutbox(ctx, store, list.Items[i].Name)
				}
				if !flagYes && !typedConfirm(os.Stdin, os.Stdout, typedDeleteAll) {
					return fmt.Errorf("not deleted — type %q exactly to confirm", typedDeleteAll)
				}
				n, derr := store.DeleteAll(ctx)
				if derr != nil {
					return derr
				}
				fmt.Printf("  ✕ deleted %d session(s)\n", n)
				return nil
			}

			// Every name must exist before anything is asked or deleted: a
			// typo should stop the command, not delete the other names and
			// then fail.
			for _, name := range args {
				se := &agentsv1.Session{}
				if err := k.Client.Get(ctx, types.NamespacedName{Namespace: k.Namespace, Name: name}, se); err != nil {
					if apierrors.IsNotFound(err) {
						return fmt.Errorf("no session %q in %s/%s", name, k.ContextName, k.Namespace)
					}
					return err
				}
			}
			for _, name := range args {
				warnOutbox(ctx, store, name)
			}
			if !flagYes {
				prompt := fmt.Sprintf("delete %s? workspace and transcript go too", strings.Join(args, ", "))
				if !yesNo(os.Stdin, os.Stdout, prompt) {
					return fmt.Errorf("not deleted")
				}
			}
			for _, name := range args {
				if err := store.Delete(ctx, name); err != nil && !apierrors.IsNotFound(err) {
					return fmt.Errorf("delete %s: %w", name, err)
				}
				fmt.Printf("  ✕ %s deleted — workspace and transcript gone\n", name)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&all, "all", false, "delete every session in the namespace (asks you to type \""+typedDeleteAll+"\")")
	return cmd
}

// warnOutbox says out loud when a session still holds bundles that never
// reached the courier. A session that isn't running can't be checked, and
// says so instead of implying it's empty.
func warnOutbox(ctx context.Context, store *sessions.Store, name string) {
	pod, err := podForSession(ctx, store.Kube, name)
	if err != nil {
		fmt.Printf("  · %s isn't running, so its outbox can't be checked; `tiny resume %s` then `tiny pull %s` first if it may hold work\n", name, name, name)
		return
	}
	bundles, err := store.ListOutbox(ctx, pod)
	if err != nil || len(bundles) == 0 {
		return
	}
	fmt.Printf("  ! %s has %d unexported bundle(s) in its outbox; they are lost with it. `tiny pull %s` keeps a copy\n", name, len(bundles), name)
}

func yesNo(in io.Reader, out io.Writer, prompt string) bool {
	_, _ = fmt.Fprintf(out, "  %s [y/N] ", prompt)
	ans, _ := bufio.NewReader(in).ReadString('\n')
	ans = strings.ToLower(strings.TrimSpace(ans))
	return ans == "y" || ans == "yes"
}

func typedConfirm(in io.Reader, out io.Writer, want string) bool {
	_, _ = fmt.Fprintf(out, "  type %q to confirm: ", want)
	ans, _ := bufio.NewReader(in).ReadString('\n')
	return strings.TrimSpace(ans) == want
}
