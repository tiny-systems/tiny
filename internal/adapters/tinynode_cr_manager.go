package adapters

import (
	"context"
	"fmt"

	"github.com/tiny-systems/module/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// TinyNodeCRManager gives clone_solution direct TinyNode CR access — the
// full-fidelity path that mirrors the hosted installer instead of the
// piecewise editing tools.
type TinyNodeCRManager struct {
	kube *kube.Client
}

func NewTinyNodeCRManager(k *kube.Client) *TinyNodeCRManager {
	return &TinyNodeCRManager{kube: k}
}

func (m *TinyNodeCRManager) ListProjectNodeCRs(ctx context.Context, projectName string) ([]v1alpha1.TinyNode, error) {
	list := &v1alpha1.TinyNodeList{}
	if err := m.kube.Client.List(ctx, list,
		client.InNamespace(m.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName}); err != nil {
		return nil, err
	}
	return list.Items, nil
}

func (m *TinyNodeCRManager) CreateNodeCR(ctx context.Context, node *v1alpha1.TinyNode) (string, error) {
	node.Namespace = m.kube.Namespace
	if err := m.kube.Client.Create(ctx, node); err != nil {
		return "", err
	}
	if node.Name == "" {
		return "", fmt.Errorf("cluster did not assign a name to the created node")
	}
	return node.Name, nil
}

func (m *TinyNodeCRManager) GetNodeCR(ctx context.Context, name string) (*v1alpha1.TinyNode, error) {
	node := &v1alpha1.TinyNode{}
	if err := m.kube.Client.Get(ctx, client.ObjectKey{Namespace: m.kube.Namespace, Name: name}, node); err != nil {
		return nil, err
	}
	return node, nil
}

func (m *TinyNodeCRManager) UpdateNodeCR(ctx context.Context, node *v1alpha1.TinyNode) error {
	return m.kube.Client.Update(ctx, node)
}

func (m *TinyNodeCRManager) IsConflict(err error) bool {
	return apierrors.IsConflict(err)
}
