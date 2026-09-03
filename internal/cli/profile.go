/*
tiny profile names targets: `tiny -p work` instead of remembering which
GKE context string is which. A profile is a context+namespace pair in the
same config file as the pin; the pin remains the no-flag default.
*/
package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Named targets: save work/home once, use with -p",
	}
	cmd.AddCommand(newProfileSaveCmd(), newProfileListCmd(), newProfileDeleteCmd())
	return cmd
}

func newProfileSaveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save <name>",
		Short: "Save the current (or flagged) target under a name",
		Long: "Saves a context+namespace pair. With --context/-n those are saved; without,\n" +
			"the target the command resolves to (the pin, or the kubeconfig's current\n" +
			"context) is what gets the name.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			ctxName, ns, err := resolveTarget()
			if err != nil {
				return err
			}
			if ctxName == "" {
				return fmt.Errorf("cannot save a profile from inside a pod")
			}
			c := loadPinned()
			if c == nil {
				c = &pinnedConfig{}
			}
			if c.Profiles == nil {
				c.Profiles = map[string]profileTarget{}
			}
			c.Profiles[name] = profileTarget{Context: ctxName, Namespace: ns}
			if err := saveConfig(c); err != nil {
				return err
			}
			fmt.Printf("  ✓ profile %s → %s/%s\n", name, ctxName, ns)
			fmt.Printf("    use it:  tiny -p %s\n", name)
			return nil
		},
	}
}

func newProfileListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List saved profiles",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := loadPinned()
			if c == nil || len(c.Profiles) == 0 {
				fmt.Println("  no profiles — save one: tiny profile save work --context <ctx> -n <ns>")
				return nil
			}
			for name, p := range c.Profiles {
				fmt.Printf("  %-12s %s/%s\n", name, p.Context, p.Namespace)
			}
			if c.Context != "" {
				fmt.Printf("  %-12s %s/%s  (no-flag default)\n", "(pinned)", c.Context, c.Namespace)
			}
			return nil
		},
	}
}

func newProfileDeleteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c := loadPinned()
			if c == nil || c.Profiles[args[0]].Context == "" {
				return fmt.Errorf("no profile %q", args[0])
			}
			delete(c.Profiles, args[0])
			if err := saveConfig(c); err != nil {
				return err
			}
			fmt.Printf("  ✓ profile %s deleted\n", args[0])
			return nil
		},
	}
}
