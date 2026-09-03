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
		Long: "Saves a context+namespace pair. With --context/-n those are saved; without\n" +
			"flags, the arrow-key picker opens — choose the cluster and namespace the\n" +
			"name should mean.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			var ctxName, ns string
			var err error
			if flagContext == "" {
				// No flags: pick interactively — this is how a profile is
				// born without memorizing context strings.
				ctxName, ns, err = pickTarget(currentKubeContext(), defaultNamespace)
			} else {
				ctxName, ns, err = resolveTarget()
			}
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

// pickAndOfferProfile is the interactive target journey: the arrow-key
// context picker (which proves connectivity by listing namespaces), the
// namespace list with create-new, then the offer to keep the result under
// a name. An empty name keeps the choice for this run only.
func pickAndOfferProfile() (ctxName, ns, name string, err error) {
	ctxName, ns, err = pickTarget(currentKubeContext(), defaultNamespace)
	if err != nil {
		return "", "", "", err
	}
	fmt.Printf("  save %s/%s as a profile? name it (enter skips): ", ctxName, ns)
	name = readLine()
	if name != "" {
		c := loadPinned()
		if c == nil {
			c = &pinnedConfig{}
		}
		if c.Profiles == nil {
			c.Profiles = map[string]profileTarget{}
		}
		c.Profiles[name] = profileTarget{Context: ctxName, Namespace: ns}
		c.LastProfile = name
		if err := saveConfig(c); err != nil {
			return "", "", "", err
		}
		fmt.Printf("  ✓ profile %s saved — next time: tiny -p %s\n", name, name)
	}
	return ctxName, ns, name, nil
}
