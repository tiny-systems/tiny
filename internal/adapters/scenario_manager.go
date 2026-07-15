package adapters

import (
	"context"
	"fmt"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// ScenarioManager implements sdktools.ScenarioManager by CRUD'ing
// TinyScenario CRDs in the target namespace.
//
// Scenarios are scoped per project via the project-name label. The
// v0.1.0 implementation does NOT support CreateScenarioFromTrace —
// capturing trace data into a scenario requires coordination with the
// otel-collector that is beyond the v0.1.0 scope. Use CreateEmptyScenario
// plus UpdateScenarioPort instead.
type ScenarioManager struct {
	kube *kube.Client
}

func NewScenarioManager(k *kube.Client) *ScenarioManager {
	return &ScenarioManager{kube: k}
}

func (s *ScenarioManager) CreateEmptyScenario(ctx context.Context, projectName, name string) (*sdktools.ScenarioItem, error) {
	if projectName == "" {
		return nil, fmt.Errorf("project name required")
	}
	if name == "" {
		return nil, fmt.Errorf("scenario name required")
	}

	scenario := &v1alpha1.TinyScenario{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "scenario-",
			Namespace:    s.kube.Namespace,
			Labels: map[string]string{
				v1alpha1.ProjectNameLabel: projectName,
			},
			Annotations: map[string]string{
				v1alpha1.ScenarioNameAnnotation: name,
			},
		},
	}
	if err := s.kube.Client.Create(ctx, scenario); err != nil {
		return nil, fmt.Errorf("create TinyScenario: %w", err)
	}

	return &sdktools.ScenarioItem{
		ResourceName: scenario.Name,
		Name:         name,
		PortCount:    0,
	}, nil
}

// CreateScenarioFromTrace is not supported in v0.1.0.
func (s *ScenarioManager) CreateScenarioFromTrace(ctx context.Context, projectName, name, traceID string) (*sdktools.ScenarioItem, error) {
	return nil, fmt.Errorf("creating scenarios from traces is not supported by the local MCP server in v0.1.0; " +
		"use create_scenario without trace_id, then update_scenario to populate port data")
}

func (s *ScenarioManager) DeleteScenario(ctx context.Context, projectName, resourceName string) error {
	scenario := &v1alpha1.TinyScenario{
		ObjectMeta: metav1.ObjectMeta{
			Name:      resourceName,
			Namespace: s.kube.Namespace,
		},
	}
	if err := s.kube.Client.Delete(ctx, scenario); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete TinyScenario: %w", err)
	}
	return nil
}

func (s *ScenarioManager) ListScenarios(ctx context.Context, projectName string) ([]sdktools.ScenarioItem, error) {
	list := &v1alpha1.TinyScenarioList{}
	err := s.kube.Client.List(ctx, list,
		client.InNamespace(s.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName},
	)
	if err != nil {
		return nil, fmt.Errorf("list TinyScenarios: %w", err)
	}

	out := make([]sdktools.ScenarioItem, 0, len(list.Items))
	for _, sc := range list.Items {
		name := sc.Annotations[v1alpha1.ScenarioNameAnnotation]
		if name == "" {
			name = sc.Name
		}
		out = append(out, sdktools.ScenarioItem{
			ResourceName: sc.Name,
			Name:         name,
			PortCount:    len(sc.Spec.Ports),
		})
	}
	return out, nil
}

func (s *ScenarioManager) UpdateScenarioPort(ctx context.Context, projectName, resourceName, port string, data []byte) error {
	scenario := &v1alpha1.TinyScenario{}
	err := s.kube.Client.Get(ctx, types.NamespacedName{
		Namespace: s.kube.Namespace,
		Name:      resourceName,
	}, scenario)
	if err != nil {
		return fmt.Errorf("get TinyScenario: %w", err)
	}

	upsertScenarioPort(scenario, port, data)

	if err := s.kube.Client.Update(ctx, scenario); err != nil {
		return fmt.Errorf("update TinyScenario: %w", err)
	}
	return nil
}

// upsertScenarioPort replaces the port entry if it exists, appends otherwise.
func upsertScenarioPort(s *v1alpha1.TinyScenario, port string, data []byte) {
	for i := range s.Spec.Ports {
		if s.Spec.Ports[i].Port == port {
			s.Spec.Ports[i].Data = data
			return
		}
	}
	s.Spec.Ports = append(s.Spec.Ports, v1alpha1.ScenarioPortData{
		Port: port,
		Data: data,
	})
}

var _ sdktools.ScenarioManager = (*ScenarioManager)(nil)
