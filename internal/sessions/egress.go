package sessions

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tiny-systems/tiny/internal/addons"
	"github.com/tiny-systems/tiny/internal/settings"
)

// Egress modes: what a namespace's sessions may reach on the internet.
const (
	// EgressOpen is no policy at all: any address, any port.
	EgressOpen = "open"
	// EgressFenced keeps sessions to their namespace, the API server and
	// the internet on ports 80 and 443.
	EgressFenced = "fenced"
	// EgressAllowList sends every connection through the proxy, which
	// admits only the hosts on the namespace's list.
	EgressAllowList = "allowlist"
)

// Egress is a namespace's internet access as a settings screen shows it.
type Egress struct {
	Mode string `json:"mode"`
	// Enforced is the fence's state: "enforced by <cni>", a warning that
	// nothing enforces it, or "" when there is no fence.
	Enforced string `json:"enforced,omitempty"`
	// Proxy is the allow-list proxy's observed state ("running",
	// "starting", a failure), "" when the allow-list is off.
	Proxy string `json:"proxy,omitempty"`
	// Hosts is the allow-list, nil when it is off.
	Hosts []string `json:"hosts"`
}

// Refusal is one host the allow-list turned away, from the proxy's log.
type Refusal struct {
	Host  string    `json:"host"`
	Count int       `json:"count"`
	Last  time.Time `json:"last"`
	// Lookup means the session resolved the name itself instead of
	// asking the proxy: allowing the host will not help that tool.
	Lookup bool `json:"lookup"`
}

// ErrNoProxy means the allow-list proxy is not running.
var ErrNoProxy = errors.New("the egress proxy is not running: switch on the hostname allow-list first")

// EgressState reads the namespace's mode, fence state and allow-list.
func (s *Store) EgressState(ctx context.Context) (Egress, error) {
	ns, err := s.LoadSettings(ctx)
	if err != nil {
		return Egress{}, err
	}
	out := Egress{Mode: egressMode(ns), Enforced: ns.EgressState, Proxy: ns.ProxyState}
	if out.Mode == EgressAllowList {
		out.Hosts = (&addons.Applier{Client: s.Kube.Client}).AllowedHosts(ctx, s.Kube.Namespace)
		if out.Hosts == nil {
			out.Hosts = []string{}
		}
	}
	return out, nil
}

func egressMode(ns NamespaceSettings) string {
	switch {
	case !ns.Egress:
		return EgressOpen
	case ns.EgressProxy:
		return EgressAllowList
	default:
		return EgressFenced
	}
}

// SetEgressMode switches the namespace between open, fenced and the
// allow-list, applying the change the way the settings screen does. New
// connections follow at once; the allow-list starts with tiny's defaults.
func (s *Store) SetEgressMode(ctx context.Context, mode string) error {
	ns, err := s.LoadSettings(ctx)
	if err != nil {
		return err
	}
	if egressMode(ns) == mode {
		return nil
	}
	switch mode {
	case EgressOpen:
		ns.Egress, ns.EgressProxy = false, false
	case EgressFenced:
		ns.Egress, ns.EgressProxy = true, false
	case EgressAllowList:
		ns.Egress, ns.EgressProxy = true, true
	default:
		return fmt.Errorf("unknown egress mode %q", mode)
	}
	if mode != EgressAllowList {
		// The gateway holds the Claude credential inside the proxy: taking
		// the proxy away would leave sessions unable to sign in.
		cfg, err := settings.Load(ctx, s.Kube.Client, s.Kube.Namespace)
		if err != nil {
			return err
		}
		if cfg.ModelGateway != "" {
			return fmt.Errorf("the model gateway runs in the allow-list proxy: turn the gateway off first")
		}
	}
	return s.SaveSettings(ctx, ns)
}

// AllowHost adds a host to the allow-list (a leading dot admits its
// subdomains). It reports false when the host was already there.
func (s *Store) AllowHost(ctx context.Context, host string) (bool, error) {
	return (&addons.Applier{Client: s.Kube.Client}).AllowHost(ctx, s.Kube.Namespace, host)
}

// RemoveHost takes a host off the allow-list. It reports false when the
// host was not on it.
func (s *Store) RemoveHost(ctx context.Context, host string) (bool, error) {
	return (&addons.Applier{Client: s.Kube.Client}).RemoveHost(ctx, s.Kube.Namespace, host)
}

// Refusals scans the proxy's most recent log lines (tail per pod) for
// hosts it refused, most frequent first.
func (s *Store) Refusals(ctx context.Context, tail int64) ([]Refusal, error) {
	cs, err := kubernetes.NewForConfig(s.Kube.RESTConfig)
	if err != nil {
		return nil, err
	}
	pods, err := cs.CoreV1().Pods(s.Kube.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + addons.ProxyName})
	if err != nil {
		return nil, err
	}
	if len(pods.Items) == 0 {
		return nil, ErrNoProxy
	}
	byHost := map[string]*Refusal{}
	for i := range pods.Items {
		p := &pods.Items[i]
		if p.Status.Phase != corev1.PodRunning {
			continue
		}
		stream, err := cs.CoreV1().Pods(s.Kube.Namespace).GetLogs(p.Name, &corev1.PodLogOptions{TailLines: &tail, Timestamps: true}).Stream(ctx)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(stream)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			if r, ok := parseRefusal(sc.Text()); ok {
				have := byHost[r.Host]
				if have == nil {
					have = &Refusal{Host: r.Host}
					byHost[r.Host] = have
				}
				have.Count++
				have.Lookup = have.Lookup || r.Lookup
				if r.Last.After(have.Last) {
					have.Last = r.Last
				}
			}
		}
		_ = stream.Close()
	}
	out := make([]Refusal, 0, len(byHost))
	for _, r := range byHost {
		out = append(out, *r)
	}
	slices.SortFunc(out, func(a, b Refusal) int {
		if a.Count != b.Count {
			return b.Count - a.Count
		}
		return strings.Compare(a.Host, b.Host)
	})
	return out, nil
}

// parseRefusal reads one proxy log line: "<RFC3339 time> ... egress
// DENIED <method> <host[:port]>". The port is dropped so the host is
// what the allow-list takes.
func parseRefusal(line string) (Refusal, bool) {
	_, after, found := strings.Cut(line, "egress DENIED ")
	if !found {
		return Refusal{}, false
	}
	rest := strings.Fields(after)
	if len(rest) < 2 {
		return Refusal{}, false
	}
	host := rest[1]
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	r := Refusal{Host: strings.TrimSuffix(strings.ToLower(host), "."), Lookup: rest[0] == "DNS"}
	if ts := strings.Fields(line); len(ts) > 0 {
		if t, err := time.Parse(time.RFC3339Nano, ts[0]); err == nil {
			r.Last = t
		}
	}
	return r, true
}
