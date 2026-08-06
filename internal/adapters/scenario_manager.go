package adapters

import (
	"context"
	"encoding/json"
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
// Scenarios are scoped per project via the project-name label.
// CreateScenarioFromTrace pins a real execution: it reads the trace's
// output-port spans through the TraceReader, redacts credential-shaped
// values (sample data lands in etcd and in exports — an agent flow's
// context carries the apiKey on every hop), and stores one port entry
// per span payload.
type ScenarioManager struct {
	kube   *kube.Client
	traces sdktools.TraceReader
}

func NewScenarioManager(k *kube.Client, traces sdktools.TraceReader) *ScenarioManager {
	return &ScenarioManager{kube: k, traces: traces}
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

// CreateScenarioFromTrace pins a trace as a scenario: one port entry per
// output-port span payload, secrets redacted before anything is persisted.
func (s *ScenarioManager) CreateScenarioFromTrace(ctx context.Context, projectName, name, traceID string) (*sdktools.ScenarioItem, error) {
	if s.traces == nil {
		return nil, fmt.Errorf("trace reader unavailable — create the scenario without trace_id, then update_scenario to populate port data")
	}
	if traceID == "" {
		return nil, fmt.Errorf("trace id required")
	}
	spans, err := s.traces.ReadTraceDetail(ctx, projectName, traceID)
	if err != nil {
		return nil, fmt.Errorf("read trace %s: %w", traceID, err)
	}
	ports := sdktools.ExtractScenarioPorts(spans)
	if len(ports) == 0 {
		return nil, fmt.Errorf("trace %s has no output-port payloads to pin — run the flow with data first", traceID)
	}

	item, err := s.CreateEmptyScenario(ctx, projectName, name)
	if err != nil {
		return nil, err
	}
	for port, payload := range ports {
		data, err := json.Marshal(sdktools.RedactSecrets(payload))
		if err != nil {
			return nil, fmt.Errorf("marshal sample for %s: %w", port, err)
		}
		if err := s.UpdateScenarioPort(ctx, projectName, item.ResourceName, port, data); err != nil {
			return nil, fmt.Errorf("write sample for %s: %w", port, err)
		}
	}
	item.PortCount = len(ports)
	return item, nil
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

// ApplyScenario writes a scenario with pre-built port samples — the
// clone_solution path applying a solution's shipped scenarios. Reuses an
// existing scenario with the same display name in the project (idempotent
// re-clone) or creates one.
func (s *ScenarioManager) ApplyScenario(ctx context.Context, projectName, name string, ports []v1alpha1.ScenarioPortData) error {
	if projectName == "" || name == "" {
		return fmt.Errorf("project and scenario name required")
	}

	list := &v1alpha1.TinyScenarioList{}
	if err := s.kube.Client.List(ctx, list,
		client.InNamespace(s.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName}); err != nil {
		return fmt.Errorf("list scenarios: %w", err)
	}
	for i := range list.Items {
		sc := &list.Items[i]
		if sc.Annotations[v1alpha1.ScenarioNameAnnotation] != name {
			continue
		}
		sc.Spec.Ports = ports
		if err := s.kube.Client.Update(ctx, sc); err != nil {
			return fmt.Errorf("update scenario %q: %w", name, err)
		}
		return nil
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
		Spec: v1alpha1.TinyScenarioSpec{Ports: ports},
	}
	if err := s.kube.Client.Create(ctx, scenario); err != nil {
		return fmt.Errorf("create scenario %q: %w", name, err)
	}
	return nil
}
