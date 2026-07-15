package adapters

import (
	"context"

	"github.com/tiny-systems/module/api/v1alpha1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/tiny-systems/tiny/internal/kube"
)

// ScenarioGetForTest fetches a TinyScenario by resource name. Only used
// by cmd/test-scaffold to verify auto-scaffold output; the public
// ScenarioManager interface intentionally doesn't expose Get because
// tools that need scenario data should go through UpdateScenarioPort /
// ListScenarios semantics.
func ScenarioGetForTest(ctx context.Context, k *kube.Client, name string) (*v1alpha1.TinyScenario, error) {
	scenario := &v1alpha1.TinyScenario{}
	err := k.Client.Get(ctx, types.NamespacedName{
		Namespace: k.Namespace,
		Name:      name,
	}, scenario)
	if err != nil {
		return nil, err
	}
	return scenario, nil
}
