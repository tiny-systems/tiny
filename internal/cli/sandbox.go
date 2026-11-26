/*
tiny sandbox makes every new session in the namespace run under a
sandboxed container runtime (gVisor by default), so the agent talks to a
user-space kernel instead of the node's.
*/
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newSandboxCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "sandbox [on [runtime-class]|off]",
		Short: "Run every new session under gVisor (or another sandboxed runtime)",
		Long: "With the sandbox on, every new session in the namespace runs under the named\n" +
			"RuntimeClass (gvisor when none is named): the agent's system calls go to a\n" +
			"user-space kernel, not the node's. tiny refuses a class the cluster doesn't have.\n" +
			"A session can still name its own with `tiny new --runtime-class`, or opt out\n" +
			"with `--runtime-class none`. Sessions already running keep the runtime they\n" +
			"started with.\n\n" +
			"Bare `tiny sandbox` says whether it's on.",
		Args:      cobra.MaximumNArgs(2),
		ValidArgs: []string{wordOn, wordOff},
		RunE: func(cmd *cobra.Command, args []string) error {
			store, ctx, cancel, err := modelAuthStore(cmd)
			if err != nil {
				return err
			}
			defer cancel()
			switch {
			case len(args) == 0:
				class, err := store.SandboxState(ctx)
				if err != nil {
					return err
				}
				if class == "" {
					fmt.Println("  sandbox off: new sessions run on the node's default runtime")
					return nil
				}
				fmt.Printf("  sandbox on: new sessions run under %s\n", class)
				return nil
			case args[0] == wordOn:
				class := ""
				if len(args) == 2 {
					class = args[1]
				}
				if err := confirmStart(store.Kube); err != nil {
					return err
				}
				if err := store.SandboxOn(ctx, class); err != nil {
					return err
				}
				class, _ = store.SandboxState(ctx)
				fmt.Printf("  ✓ sandbox on: new sessions run under %s\n", class)
				fmt.Println("  sessions already running keep their runtime until recreated")
				return nil
			case args[0] == wordOff && len(args) == 1:
				if err := confirmStart(store.Kube); err != nil {
					return err
				}
				if err := store.SandboxOff(ctx); err != nil {
					return err
				}
				fmt.Println("  ✓ sandbox off: new sessions run on the node's default runtime")
				return nil
			}
			return fmt.Errorf("say on [runtime-class] or off")
		},
	}
}
