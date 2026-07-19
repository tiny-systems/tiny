package flow

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/tiny-systems/tiny/internal/kube"
)

// TestTunnelLive proves the auto-forward end to end against a real cluster: it
// runs the reconcile loop, waits for a forward to appear, then dials
// 127.0.0.1:<port> to confirm the pod's server is now reachable from this
// machine. Gated by env so it only runs when pointed at a cluster with a live
// server node:
//
//	TINY_TEST_CONTEXT=minikube TINY_TEST_NS=tinysystems \
//	go test ./internal/flow -run TunnelLive -v
func TestTunnelLive(t *testing.T) {
	kctx := os.Getenv("TINY_TEST_CONTEXT")
	if kctx == "" {
		t.Skip("set TINY_TEST_CONTEXT to run the live tunnel test")
	}
	ns := os.Getenv("TINY_TEST_NS")
	if ns == "" {
		ns = "tinysystems"
	}

	cfg, err := kube.RestConfig(kctx)
	if err != nil {
		t.Fatalf("rest config: %v", err)
	}

	tunnel, err := NewTunnel(cfg, ns)
	if err != nil {
		t.Fatalf("new tunnel: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tunnel.Run(ctx)

	// Wait up to 20s for at least one forward to come up.
	var port int
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tunnel.mu.Lock()
		for p := range tunnel.active {
			port = p
			break
		}
		tunnel.mu.Unlock()
		if port != 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if port == 0 {
		t.Skip("no server node with a listenAddr found in the cluster — nothing to forward")
	}
	t.Logf("tunnel forwarded local port %d", port)

	// The forwarded port must accept a TCP connection on this machine.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		t.Fatalf("dial 127.0.0.1:%d (forward not reachable): %v", port, err)
	}
	_ = conn.Close()
	t.Logf("127.0.0.1:%d is reachable — pod server is exposed to localhost", port)
}
