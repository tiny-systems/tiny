package flow

import (
	"context"
	"encoding/json"

	"github.com/tiny-systems/module/pkg/utils"
	platform "github.com/tiny-systems/platform-go"

	"github.com/tiny-systems/tiny/internal/adapters"
	"github.com/tiny-systems/tiny/internal/kube"
)

// kubeClient builds a scheme-aware controller-runtime client over the
// service's rest.Config. The adapters (port inspection, signals) work against
// this rather than resource.Manager.
func (s *Service) kubeClient() (*kube.Client, error) {
	return kube.NewClientFromConfig(s.cfg, s.namespace)
}

// InspectNode returns a port's data shape — the example/simulated data the
// editor shows in a node's Debug/Config tab and uses to preview edge mappings.
// It's backed by the same PortInspector the MCP tools use: it reads the node's
// reconciled port schema + configuration and returns the example data as JSON,
// which the editor reads from response.Data.
//
// This is the narrower local inspector (the node's own ports enriched with its
// own _settings overlay), not the platform's whole-graph simulation — enough
// for the editor to populate the inspector and edge preview.
func (s *Service) InspectNode(ctx context.Context, req *platform.InspectRequest) (*platform.InspectResponse, error) {
	kc, err := s.kubeClient()
	if err != nil {
		return nil, err
	}

	result, err := adapters.NewPortInspector(kc).InspectPort(ctx, req.ProjectName, req.NodeID, req.PortName, req.TraceID)
	if err != nil {
		return nil, err
	}

	data := result.ExampleData
	if data == nil {
		data = map[string]interface{}{}
	}
	b, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return &platform.InspectResponse{Data: string(b)}, nil
}

// RunExpression evaluates an ajson expression against sample data and validates
// the result against a schema — the expression testing + edge-mapping checks in
// the editor's config panel. Pure SDK evaluation, no cluster access, so it's a
// direct passthrough to the shared evaluator the platform uses.
func (s *Service) RunExpression(ctx context.Context, req *platform.RunExpressionRequest) (*platform.RunExpressionResponse, error) {
	res, err := utils.RunExpression(&utils.RunExpressionRequest{
		Expression: req.Expression,
		Data:       req.Data,
		Schema:     req.Schema,
	})
	if err != nil {
		return nil, err
	}
	return &platform.RunExpressionResponse{
		Result:          res.Result,
		ValidSchema:     res.ValidSchema,
		ValidationError: res.ValidationError,
	}, nil
}

// PreviewEdgeMapping applies an edge's configuration mapping to sample source
// data and returns the mapped result — the live preview in the edge-config
// panel as you type a mapping. Also pure SDK evaluation.
func (s *Service) PreviewEdgeMapping(ctx context.Context, req *platform.PreviewEdgeMappingRequest) (*platform.PreviewEdgeMappingResponse, error) {
	res, err := utils.PreviewEdgeMapping(&utils.PreviewEdgeMappingRequest{
		Configuration: req.Configuration,
		SourceData:    req.SourceData,
	})
	if err != nil {
		return nil, err
	}
	return &platform.PreviewEdgeMappingResponse{
		Result: res.Result,
		Errors: res.Errors,
	}, nil
}
