/*
tiny egress is the operator's window onto the hostname allow-list.

The proxy refuses anything not on the list and logs each refusal, but
until now that log lived behind kubectl and the refusal message told you
to go edit a ConfigMap by hand. `tiny egress denied` shows what your
agents reached for and were turned away from — the most useful security
signal the system produces — and `tiny egress allow` widens the list
without leaving the terminal.
*/
package cli

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tiny-systems/tiny/internal/addons"
	"github.com/tiny-systems/tiny/internal/kube"
)

func newEgressCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "egress",
		Short: "The hostname allow-list: what agents may reach, and what they were refused",
		Long: "With the hostname allow-list add-on on, every outbound connection from a\n" +
			"session goes through a proxy that filters by name. `tiny egress` shows the\n" +
			"list, `tiny egress denied` shows what agents tried and were refused, and\n" +
			"`tiny egress allow <host>` widens the list, `tiny egress remove <host>` narrows\n" +
			"it. A leading dot allows subdomains.",
		RunE: func(cmd *cobra.Command, _ []string) error { return egressList(cmd) },
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "allow <host>",
		Short: "Add a host to the allow-list (a leading dot matches subdomains)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			ap := &addons.Applier{Client: k.Client}
			added, err := ap.AllowHost(ctx, k.Namespace, args[0])
			if err != nil {
				return err
			}
			if !added {
				fmt.Printf("  · %s was already allowed\n", args[0])
				return nil
			}
			fmt.Printf("  ✓ %s allowed — live in a minute or two (kubelet syncs the ConfigMap, then the proxy re-reads)\n", args[0])
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "remove <host>",
		Short: "Take a host off the allow-list",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			removed, err := newStore(k).RemoveHost(ctx, args[0])
			if err != nil {
				return err
			}
			if !removed {
				fmt.Printf("  · %s was not on the list\n", args[0])
				return nil
			}
			fmt.Printf("  ✓ %s removed — refused in a minute or two (kubelet syncs the ConfigMap, then the proxy re-reads)\n", args[0])
			return nil
		},
	})
	var tail int64
	denied := &cobra.Command{
		Use:   "denied",
		Short: "Hosts agents tried to reach and were refused, most frequent first",
		RunE: func(cmd *cobra.Command, _ []string) error {
			k, err := sessionKube()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
			defer cancel()
			counts, latest, err := readDenials(ctx, k, tail)
			if err != nil {
				return err
			}
			if len(counts) == 0 {
				fmt.Println("  nothing refused in the last", tail, "log lines")
				return nil
			}
			type hc struct {
				host string
				n    int
			}
			var rows []hc
			for h, n := range counts {
				rows = append(rows, hc{h, n})
			}
			slices.SortFunc(rows, func(a, b hc) int {
				if a.n != b.n {
					return b.n - a.n
				}
				return strings.Compare(a.host, b.host)
			})
			fmt.Printf("  %s\n", styleWarn.Render("refused by the allow-list"))
			for _, r := range rows {
				fmt.Printf("    %-48s %s\n", r.host, styleSubtle.Render(fmt.Sprintf("×%d  last %s", r.n, latest[r.host])))
			}
			fmt.Printf("\n  %s\n", styleSubtle.Render("allow one:  tiny egress allow <host>    (an agent asking for a host nobody listed is worth a look first)"))
			return nil
		},
	}
	denied.Flags().Int64Var(&tail, "tail", 2000, "how many recent proxy log lines to scan")
	cmd.AddCommand(denied)
	return cmd
}

func egressList(cmd *cobra.Command) error {
	k, err := sessionKube()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
	defer cancel()
	ap := &addons.Applier{Client: k.Client}
	hosts := ap.AllowedHosts(ctx, k.Namespace)
	if hosts == nil {
		fmt.Println("  the hostname allow-list is off — switch it on under egress policy in namespace settings")
		return nil
	}
	fmt.Printf("  %s  %s\n", styleTitle.Render("allowed"), styleSubtle.Render(fmt.Sprintf("(%d hosts; a leading dot matches subdomains)", len(hosts))))
	for _, h := range hosts {
		fmt.Printf("    %s\n", h)
	}
	fmt.Printf("\n  %s\n", styleSubtle.Render("tiny egress denied   what agents were refused    tiny egress allow <host>   widen it"))
	return nil
}

// readDenials reports the proxy's recent refusals as the denied listing
// prints them: counts, the last time each was refused, and which ones were
// lookups the proxy never saw a connection for.
func readDenials(ctx context.Context, k *kube.Client, tail int64) (map[string]int, map[string]string, error) {
	counts, latest, viaDNS, err := readDenialsDetailed(ctx, k, tail)
	for h := range viaDNS {
		latest[h] += "  (lookup, not connect — the tool is not using HTTPS_PROXY)"
	}
	return counts, latest, err
}

func readDenialsDetailed(ctx context.Context, k *kube.Client, tail int64) (map[string]int, map[string]string, map[string]bool, error) {
	refused, err := newStore(k).Refusals(ctx, tail)
	if err != nil {
		return nil, nil, nil, err
	}
	counts := map[string]int{}
	latest := map[string]string{}
	viaDNS := map[string]bool{}
	for _, r := range refused {
		counts[r.Host] = r.Count
		if !r.Last.IsZero() {
			latest[r.Host] = r.Last.Local().Format("15:04:05")
		}
		if r.Lookup {
			viaDNS[r.Host] = true
		}
	}
	return counts, latest, viaDNS, nil
}
