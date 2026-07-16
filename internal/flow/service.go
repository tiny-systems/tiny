// Package flow is tiny's local implementation of the platform FlowService —
// the gRPC/gRPC-web API the browser editor talks to. It's backed by the
// cluster's TinyFlow/TinyNode CRDs (via the SDK's resource.Manager) and the
// shared SDK graph helpers (module/pkg/utils, module/pkg/schema), the same
// code the hosted platform calls. Only the RPCs the local editor needs are
// implemented; everything else falls through to UnimplementedFlowServiceServer
// (platform-only features — LLM prompt, revision history, registry browse).
package flow

import (
	"context"

	"github.com/tiny-systems/module/pkg/resource"
	platform "github.com/tiny-systems/platform-go"
	"k8s.io/client-go/rest"
)

// Service implements platform.FlowServiceServer against a local cluster.
type Service struct {
	platform.UnimplementedFlowServiceServer
	cfg       *rest.Config
	namespace string
}

// NewService binds the service to one cluster + namespace.
func NewService(cfg *rest.Config, namespace string) *Service {
	return &Service{cfg: cfg, namespace: namespace}
}

// manager builds a fresh resource.Manager per call — cheap (a typed client
// over the shared rest.Config) and keeps the service stateless.
func (s *Service) manager() (*resource.Manager, error) {
	return resource.NewManagerFromConfig(s.cfg, s.namespace)
}

// GetFlow returns flow metadata from the TinyFlow CR. The graph itself is
// streamed by GetFlowStream — the editor reads only ID/ResourceName/Meta here.
func (s *Service) GetFlow(ctx context.Context, req *platform.GetFlowRequest) (*platform.GetFlowResponse, error) {
	mgr, err := s.manager()
	if err != nil {
		return nil, err
	}
	fl, err := mgr.GetFLow(ctx, req.FlowName, s.namespace)
	if err != nil {
		return nil, err
	}
	return &platform.GetFlowResponse{
		Flow: &platform.Flow{
			ID:           fl.Name,
			ResourceName: fl.Name,
			Name:         fl.Name,
			ProjectID:    req.ProjectName,
		},
		Project: &platform.Project{
			ID:           req.ProjectName,
			Name:         req.ProjectName,
			ResourceName: req.ProjectName,
		},
	}, nil
}

// AcquireFlowLock always grants the lock: a single-user local editor has no
// contention, and the frontend gates editable rendering on this response.
func (s *Service) AcquireFlowLock(ctx context.Context, req *platform.AcquireFlowLockRequest) (*platform.AcquireFlowLockResponse, error) {
	return &platform.AcquireFlowLockResponse{Acquired: true}, nil
}

// ReleaseFlowLock is a no-op locally.
func (s *Service) ReleaseFlowLock(ctx context.Context, req *platform.ReleaseFlowLockRequest) (*platform.Nil, error) {
	return &platform.Nil{}, nil
}
