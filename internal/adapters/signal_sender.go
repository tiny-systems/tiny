package adapters

import (
	"context"
	"fmt"

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
	nc   *nats.Conn
}

// NewSignalSender requires a non-nil *nats.Conn pointed at the
// cluster the kube client targets. Pass nil and the sender will
// error on every call; the backend constructor is expected to fail
// loud at boot when it can't dial NATS.
func NewSignalSender(k *kube.Client, nc *nats.Conn) *SignalSender {
	return &SignalSender{kube: k, nc: nc}
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
	if s.nc == nil {
		return fmt.Errorf("signal_sender: NATS not configured; cluster's nats service must be reachable")
	}

	// Mark the message as an external signal (From = FromSignal) so the runner
	// decodes the payload directly (json.Unmarshal into the port type) instead
	// of treating it as an edge config-merge — the latter drops fields like the
	// signal's `send: true`, so the flow never fires.
	if _, err := wire.Publish(ctx, s.nc, nodeID, portName, data, wire.Options{From: wire.FromSignal}); err != nil {
		return fmt.Errorf("publish signal to %s:%s: %w", nodeID, portName, err)
	}
	return nil
}

var _ sdktools.SignalSender = (*SignalSender)(nil)
