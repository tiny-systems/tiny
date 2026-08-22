package adapters

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/module/api/v1alpha1"

	"github.com/tiny-systems/tiny/internal/kube"
)

// NodeLister names the nodes in a project so an eval can address its trigger by
// suffix.
//
// The spec has always documented suffix addressing, and the reason is concrete:
// importing a project mints new flow-id prefixes, so every eval that hardcodes
// a full name breaks on import. The suffix is the part that survives. The
// resolver was a stub until now, so the promise held only in prose.
type NodeLister struct {
	kube *kube.Client
}

func NewNodeLister(k *kube.Client) *NodeLister { return &NodeLister{kube: k} }

// ListNodeNames returns every node name in the project.
//
// Unlike most read paths here this is NOT lenient about failures: a partial
// list would resolve a suffix against a subset, which either fails to find a
// node that exists or — worse — finds exactly one where the full project holds
// two and the address is really ambiguous.
func (l *NodeLister) ListNodeNames(ctx context.Context, project string) ([]string, error) {
	if l == nil || l.kube == nil || l.kube.Client == nil {
		return nil, fmt.Errorf("no cluster connection")
	}

	list := &v1alpha1.TinyNodeList{}
	if err := l.kube.Client.List(ctx, list,
		client.InNamespace(l.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: project},
	); err != nil {
		return nil, fmt.Errorf("list nodes in %s: %w", project, err)
	}

	names := make([]string, 0, len(list.Items))
	for i := range list.Items {
		names = append(names, list.Items[i].Name)
	}
	return names, nil
}
