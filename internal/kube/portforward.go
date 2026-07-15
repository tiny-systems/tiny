package kube

import (
	"context"
	"fmt"
	"sync"

	"github.com/tiny-systems/module/pkg/resource"
	"github.com/tiny-systems/module/pkg/utils"
)

// PortForwarder implements sdk utils.ClientInterface by using the SDK's
// kubectl port-forward machinery to reach in-cluster services (e.g. the
// otel-collector that holds trace data).
//
// It lazy-creates one resource.PortForwarder per namespace, keeps it open
// for the lifetime of the process, and re-uses forwarded ports across
// calls. Close() stops all active forwards.
type PortForwarder struct {
	kube *Client

	mu         sync.Mutex
	forwarders map[string]*resource.PortForwarder // keyed by namespace
}

// NewPortForwarder creates a PortForwarder tied to the given kube client.
func NewPortForwarder(kube *Client) *PortForwarder {
	return &PortForwarder{
		kube:       kube,
		forwarders: make(map[string]*resource.PortForwarder),
	}
}

// GetForwardedAddress implements utils.ClientInterface. It returns a
// localhost:port address that forwards to the requested service.
func (p *PortForwarder) GetForwardedAddress(ctx context.Context, req utils.PortForwardRequest, alias string) (string, error) {
	ns := req.Namespace
	if ns == "" {
		ns = p.kube.Namespace
	}

	forwarder, err := p.getOrCreate(ns)
	if err != nil {
		return "", err
	}

	addr, err := forwarder.ForwardService(ctx, req.ServiceName, req.Port)
	if err != nil {
		return "", fmt.Errorf("forward %s/%s:%d: %w", ns, req.ServiceName, req.Port, err)
	}
	return addr, nil
}

// Close stops all active port-forwards and releases resources.
func (p *PortForwarder) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()

	for _, f := range p.forwarders {
		f.StopAll()
	}
	p.forwarders = nil
}

func (p *PortForwarder) getOrCreate(namespace string) (*resource.PortForwarder, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if f, ok := p.forwarders[namespace]; ok {
		return f, nil
	}

	f, err := resource.CreatePortForwarderFromConfig(p.kube.RESTConfig, namespace)
	if err != nil {
		return nil, fmt.Errorf("create port forwarder for namespace %q: %w", namespace, err)
	}
	p.forwarders[namespace] = f
	return f, nil
}

// Ensure interface satisfaction at compile time.
var _ utils.ClientInterface = (*PortForwarder)(nil)
