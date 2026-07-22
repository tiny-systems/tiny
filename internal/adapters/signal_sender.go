package adapters

import (
	"context"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"github.com/tiny-systems/module/pkg/wire"

	"github.com/tiny-systems/tiny/internal/kube"
)

// SignalSender publishes signals via NATS. The TinySignal CRD
// fallback was removed alongside the SDK's v0.10.38 cleanup —
// the cluster's nats service must be reachable from this process
// for SendSignal to work. kube is kept on the struct because future
// adapters (port-forward setup, namespace lookups) need it.
type SignalSender struct {
	kube *kube.Client

	mu      sync.Mutex
	nc      *nats.Conn
	connect func() *nats.Conn
}

// NewSignalSender takes the connection dialed at boot (may be nil) plus a
// connect func that (re)dials on demand.
//
// Binding the connection once at boot made send_signal permanently dead
// whenever tiny started before the cluster's NATS was reachable — the common
// case when `tiny` runs against a cluster that is still provisioning, since
// nothing retries and the only cure was restarting the process. Dial lazily
// instead: a boot-time failure is no longer terminal, and a connection that
// drops later is re-established on the next signal.
func NewSignalSender(k *kube.Client, nc *nats.Conn, connect func() *nats.Conn) *SignalSender {
	return &SignalSender{kube: k, nc: nc, connect: connect}
}

// conn returns a usable connection, redialing when the cached one is absent
// or closed. Returns nil when NATS still isn't reachable.
func (s *SignalSender) conn() *nats.Conn {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.nc != nil && !s.nc.IsClosed() {
		return s.nc
	}
	if s.connect == nil {
		return nil
	}
	s.nc = s.connect()
	return s.nc
}

// SendSignal delivers data to <nodeID>:<portName> via NATS. traceID
// is accepted for compatibility with the tools.SignalSender interface
// but currently propagates through the OTel context only.
func (s *SignalSender) SendSignal(ctx context.Context, projectName, nodeID, portName string, data []byte, traceID string) error {
	_ = projectName // labels were CRD-only; NATS routing keys off node FQN
	_ = traceID     // OTel context carries the trace today

	if nodeID == "" {
		return fmt.Errorf("node id required")
	}
	if portName == "" {
		return fmt.Errorf("port name required")
	}
	nc := s.conn()
	if nc == nil {
		return fmt.Errorf("signal_sender: NATS not reachable; is the cluster's nats service running in this namespace?")
	}

	// Mark the message as an external signal (From = FromSignal) so the runner
	// decodes the payload directly (json.Unmarshal into the port type) instead
	// of treating it as an edge config-merge — the latter drops fields like the
	// signal's `send: true`, so the flow never fires.
	if _, err := wire.Publish(ctx, nc, nodeID, portName, data, wire.Options{From: wire.FromSignal}); err != nil {
		return fmt.Errorf("publish signal to %s:%s: %w", nodeID, portName, err)
	}
	return nil
}

var _ sdktools.SignalSender = (*SignalSender)(nil)
