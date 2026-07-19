package flow

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/tiny-systems/module/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// Tunnel keeps the user's localhost in sync with the servers running inside the
// cluster. A server component (e.g. http_server) binds a random port INSIDE its
// module's controller-manager pod and reports it as a `listenAddr` of
// http://localhost:<port> on the node's _control port. That address is the
// pod's loopback — nothing on the user's machine can reach it. Tunnel watches
// for those listen addresses and port-forwards each pod:<port> to
// 127.0.0.1:<port> (the same port), so the URL the editor already shows just
// works in the browser. Forwards start when a server comes up and stop when it
// goes away or its pod is replaced.
type Tunnel struct {
	kc        *kube.Client
	cfg       *rest.Config
	namespace string

	mu     sync.Mutex
	active map[int]tunnelEntry // local port -> live forward
	warned map[int]string      // local port -> last error logged (dedupes noise)
}

type tunnelEntry struct {
	module string
	pod    string
	stop   func()
	done   <-chan struct{} // closed when the forward exits (stopped or dropped)
}

var errNoRunningPod = errors.New("no running controller-manager pod")

// NewTunnel builds a Tunnel over the same cluster/namespace the editor serves.
// It creates its own kube client (scheme-aware, lists both TinyNodes and Pods).
func NewTunnel(cfg *rest.Config, namespace string) (*Tunnel, error) {
	kc, err := kube.NewClientFromConfig(cfg, namespace)
	if err != nil {
		return nil, err
	}
	return &Tunnel{
		kc:        kc,
		cfg:       cfg,
		namespace: namespace,
		active:    map[int]tunnelEntry{},
		warned:    map[int]string{},
	}, nil
}

// Run reconciles forwards every few seconds until ctx is cancelled, then tears
// every forward down. Blocking — call it in its own goroutine.
func (t *Tunnel) Run(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()

	t.reconcile(ctx)
	for {
		select {
		case <-ctx.Done():
			t.stopAll()
			return
		case <-ticker.C:
			t.reconcile(ctx)
		}
	}
}

// reconcile diffs the servers that should be forwarded against the ones that
// are, starting and stopping forwards to match.
func (t *Tunnel) reconcile(ctx context.Context) {
	desired := t.desiredForwards(ctx)
	if desired == nil {
		return // list failed; keep existing forwards, try again next tick
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Start (or rebuild) forwards for live servers. A forward is rebuilt when
	// its pod was replaced (restart) or its stream dropped — the latter is what
	// keeps the tunnel alive against a remote cluster, where port-forwards die
	// on idle timeouts and API-server rollouts.
	for port, module := range desired {
		pod, err := t.findPod(ctx, module)
		if err != nil {
			t.warn(port, "find pod for "+module+": "+err.Error())
			continue
		}
		if e, ok := t.active[port]; ok {
			dropped := isClosed(e.done)
			if e.pod == pod && !dropped {
				continue // already forwarded to the right pod, still alive
			}
			e.stop()
			delete(t.active, port)
			if dropped {
				log.Printf("tunnel: localhost:%d dropped, reconnecting", port)
			}
		}
		stop, done, err := kube.ForwardPodPort(ctx, t.cfg, t.namespace, pod, port, port)
		if err != nil {
			t.warn(port, "forward :"+strconv.Itoa(port)+": "+err.Error())
			continue
		}
		t.active[port] = tunnelEntry{module: module, pod: pod, stop: stop, done: done}
		delete(t.warned, port)
		log.Printf("tunnel: localhost:%d → %s (%s)", port, pod, module)
	}

	// Stop forwards whose server is gone.
	for port, e := range t.active {
		if _, ok := desired[port]; !ok {
			e.stop()
			delete(t.active, port)
			delete(t.warned, port)
			log.Printf("tunnel: stopped localhost:%d (%s)", port, e.module)
		}
	}
}

// desiredForwards lists TinyNodes and returns the local ports that should be
// forwarded, mapped to the module whose pod hosts each. Returns nil (not an
// empty map) if the list fails, so reconcile can tell "nothing to forward"
// apart from "couldn't look".
func (t *Tunnel) desiredForwards(ctx context.Context) map[int]string {
	var nodes v1alpha1.TinyNodeList
	if err := t.kc.Client.List(ctx, &nodes, client.InNamespace(t.namespace)); err != nil {
		return nil
	}
	desired := map[int]string{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		module := n.Status.Module.Name
		if module == "" {
			continue
		}
		for _, ps := range n.Status.Ports {
			for _, port := range localListenPorts(ps.Configuration) {
				desired[port] = module
			}
		}
	}
	return desired
}

// findPod returns the name of a Running controller-manager pod for the module.
// The provisioner labels each module's pod with app.kubernetes.io/instance set
// to the module's slug (== node.Status.Module.Name).
func (t *Tunnel) findPod(ctx context.Context, module string) (string, error) {
	var pods corev1.PodList
	if err := t.kc.Client.List(ctx, &pods,
		client.InNamespace(t.namespace),
		client.MatchingLabels{
			"app.kubernetes.io/instance": module,
			"control-plane":              "controller-manager",
		},
	); err != nil {
		return "", err
	}
	for i := range pods.Items {
		if pods.Items[i].Status.Phase == corev1.PodRunning {
			return pods.Items[i].Name, nil
		}
	}
	return "", errNoRunningPod
}

// warn logs an error for a port once, suppressing repeats of the same message
// (reconcile runs every few seconds — a persistent problem must not spam).
func (t *Tunnel) warn(port int, msg string) {
	if t.warned[port] == msg {
		return
	}
	t.warned[port] = msg
	log.Printf("tunnel: %s", msg)
}

// isClosed reports whether a done channel has fired (the forward exited),
// without blocking.
func isClosed(done <-chan struct{}) bool {
	if done == nil {
		return false
	}
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func (t *Tunnel) stopAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for port, e := range t.active {
		e.stop()
		delete(t.active, port)
	}
}

// localListenPorts pulls the ports from a port-config's `listenAddr` field
// (http://localhost:<port> entries). Server components report where they bound
// here; everything else has no such field and yields nothing.
func localListenPorts(configuration []byte) []int {
	if len(configuration) == 0 {
		return nil
	}
	var cfg struct {
		ListenAddr json.RawMessage `json:"listenAddr"`
	}
	if err := json.Unmarshal(configuration, &cfg); err != nil || len(cfg.ListenAddr) == 0 {
		return nil
	}
	// listenAddr is usually a []string, but tolerate a bare string too.
	var addrs []string
	if err := json.Unmarshal(cfg.ListenAddr, &addrs); err != nil {
		var one string
		if json.Unmarshal(cfg.ListenAddr, &one) != nil {
			return nil
		}
		addrs = []string{one}
	}

	var ports []int
	for _, a := range addrs {
		if p, ok := loopbackPort(a); ok {
			ports = append(ports, p)
		}
	}
	return ports
}

// loopbackPort extracts the port from a URL whose host is loopback. Non-local
// addresses (a real hostname/IP the browser can already reach) are skipped.
func loopbackPort(addr string) (int, bool) {
	u, err := url.Parse(addr)
	if err != nil || u.Host == "" {
		return 0, false
	}
	switch u.Hostname() {
	case "localhost", "127.0.0.1", "0.0.0.0", "::1":
	default:
		return 0, false
	}
	p, err := strconv.Atoi(u.Port())
	if err != nil || p <= 0 {
		return 0, false
	}
	return p, true
}
