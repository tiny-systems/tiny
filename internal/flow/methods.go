package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/utils"
	platform "github.com/tiny-systems/platform-go"

	"github.com/tiny-systems/tiny/internal/adapters"
	"github.com/tiny-systems/tiny/internal/kube"
	"k8s.io/apimachinery/pkg/types"
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

// RunAction fires data into a node's port — the editor's "run"/test action on
// a control widget. Delegates to the injected NATS-backed signal sender.
func (s *Service) RunAction(ctx context.Context, req *platform.RunActionRequest) (*platform.Nil, error) {
	if s.signal == nil {
		return nil, fmt.Errorf("node-fire unavailable: signal sender not configured")
	}
	if err := s.signal.SendSignal(ctx, req.ProjectName, req.NodeID, req.PortName, req.Data, ""); err != nil {
		return nil, err
	}
	return &platform.Nil{}, nil
}

// GetComponents lists the installed modules' components for the editor's
// add-component palette. Each item carries a minimal node graph (module +
// component); the operator fills in ports on reconcile after the node is
// created, and the stream updates the editor.
func (s *Service) GetComponents(ctx context.Context, req *platform.GetComponentsRequest) (*platform.GetComponentsResponse, error) {
	mgr, err := s.manager()
	if err != nil {
		return nil, err
	}
	mods, err := mgr.GetInstalledComponents(ctx)
	if err != nil {
		return nil, err
	}

	items := make([]*platform.ComponentNodeItem, 0)
	for _, m := range mods {
		for _, c := range m.Components {
			graph, _ := json.Marshal(map[string]interface{}{
				"type": "tinyNode",
				"data": map[string]interface{}{
					"module":    m.Name,
					"component": c.Name,
					"label":     c.Name,
				},
			})
			items = append(items, &platform.ComponentNodeItem{
				Component: &platform.Component{
					Name:        c.Name,
					Description: c.Description,
					Info:        c.Info,
					Tags:        c.Tags,
				},
				Module:    &platform.ModuleVersion{ID: m.Name, Version: m.Version},
				Installed: true,
				Graph:     graph,
			})
		}
	}
	return &platform.GetComponentsResponse{Components: items}, nil
}

// ListScenarios returns the flow's scenarios for the editor's scenario
// switcher. Local flows have none beyond the implicit Default, so this returns
// empty — enough to stop the switcher's on-mount call from erroring.
func (s *Service) ListScenarios(ctx context.Context, req *platform.ListScenariosRequest) (*platform.ListScenariosResponse, error) {
	return &platform.ListScenariosResponse{}, nil
}

// UndeployFlow deletes a flow (layer) and all the TinyNodes that belong to it —
// the editor's flow delete/undeploy action. Locally undeploy IS delete: there's
// no separate deployed/undeployed state, so it removes the flow outright.
func (s *Service) UndeployFlow(ctx context.Context, req *platform.UndeployFlowRequest) (*platform.Nil, error) {
	kc, err := s.kubeClient()
	if err != nil {
		return nil, err
	}
	if err := adapters.NewFlowLifecycle(kc).DeleteFlow(ctx, "", req.FlowID); err != nil {
		return nil, err
	}
	return &platform.Nil{}, nil
}

// RenameFlow updates a flow's human-readable name. That name lives in the
// TinyFlow's description annotation (createFlow writes it there and the editor
// reads it), so a rename is a one-field annotation update — no resources move,
// the flow's resource name and node bindings are untouched.
//
// The editor sends the new name as JSON in RenameForm.Data under "newName",
// matching the platform's form, so the same dialog drives both. Without this
// handler the menu's Rename action hit an unregistered method and failed
// silently.
func (s *Service) RenameFlow(ctx context.Context, req *platform.RenameFlowRequest) (*platform.Nil, error) {
	if req.RenameForm == nil {
		return nil, fmt.Errorf("rename form is required")
	}
	var form struct {
		NewName string `json:"newName"`
	}
	if err := json.Unmarshal(req.RenameForm.Data, &form); err != nil {
		return nil, fmt.Errorf("parse rename form: %w", err)
	}
	newName := strings.TrimSpace(form.NewName)
	if newName == "" {
		return nil, fmt.Errorf("new name is required")
	}
	if len(newName) > 100 {
		newName = newName[:100]
	}

	kc, err := s.kubeClient()
	if err != nil {
		return nil, err
	}
	flow := &v1alpha1.TinyFlow{}
	if err := kc.Client.Get(ctx, types.NamespacedName{Namespace: s.namespace, Name: req.FlowName}, flow); err != nil {
		return nil, fmt.Errorf("get flow %s: %w", req.FlowName, err)
	}
	if flow.Annotations == nil {
		// A flow created without a description has no annotation map; the SDK's
		// RenameFlow assigns into it unconditionally and would panic here.
		flow.Annotations = map[string]string{}
	}
	flow.Annotations[v1alpha1.FlowDescriptionAnnotation] = newName
	if err := kc.Client.Update(ctx, flow); err != nil {
		return nil, fmt.Errorf("rename flow: %w", err)
	}
	return &platform.Nil{}, nil
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
