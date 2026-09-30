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
	"bufio"
	"context"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

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
			"`tiny egress allow <host>` widens the list. A leading dot allows subdomains.",
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

// readDenials scans the proxy's recent log for refusals. The proxy writes
// one line per refusal — "egress DENIED <method> <host>" — so counting
// them is the whole job.
func readDenials(ctx context.Context, k *kube.Client, tail int64) (map[string]int, map[string]string, error) {
	counts, latest, viaDNS, err := readDenialsDetailed(ctx, k, tail)
	for h := range viaDNS {
		latest[h] += "  (lookup, not connect — the tool is not using HTTPS_PROXY)"
	}
	return counts, latest, err
}

func readDenialsDetailed(ctx context.Context, k *kube.Client, tail int64) (map[string]int, map[string]string, map[string]bool, error) {
	cs, err := kubernetes.NewForConfig(k.RESTConfig)
	if err != nil {
		return nil, nil, nil, err
	}
	pods, err := cs.CoreV1().Pods(k.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + addons.ProxyName})
	if err != nil {
		return nil, nil, nil, err
	}
	if len(pods.Items) == 0 {
		return nil, nil, nil, fmt.Errorf("the egress proxy is not running — switch on the hostname allow-list in namespace settings")
	}
	counts := map[string]int{}
	latest := map[string]string{}
	viaDNS := map[string]bool{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		stream, err := cs.CoreV1().Pods(k.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{TailLines: &tail, Timestamps: true}).Stream(ctx)
		if err != nil {
			return nil, nil, nil, err
		}
		sc := bufio.NewScanner(stream)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := sc.Text()
			_, after, found := strings.Cut(line, "egress DENIED ")
			if !found {
				continue
			}
			rest := strings.Fields(after)
			if len(rest) < 2 {
				continue
			}
			// The proxy logs host:port; the allow-list matches by host, so
			// strip the port and the line is pasteable into `allow`.
			host := rest[1]
			if h, _, err := net.SplitHostPort(host); err == nil {
				host = h
			}
			// A refused LOOKUP is a different animal from a refused
			// connect: something in the session tried to resolve the name
			// itself instead of handing it to the proxy, so allow-listing
			// the host will not help it.
			if rest[0] == "DNS" {
				viaDNS[host] = true
			}
			counts[host]++
			// the RFC3339 timestamp is the first field of the line
			if ts := strings.Fields(line); len(ts) > 0 {
				if t, perr := time.Parse(time.RFC3339Nano, ts[0]); perr == nil {
					latest[host] = t.Local().Format("15:04:05")
				}
			}
		}
		_ = stream.Close()
	}
	return counts, latest, viaDNS, nil
}
