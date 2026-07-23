package adapters

import (
	"context"
	"fmt"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	"github.com/tiny-systems/tiny/internal/kube"
)

// DashboardWriter implements sdktools.DashboardWriter by toggling the node's
// DashboardLabel.
//
// The widget IS the node: a node with DashboardLabel="true" is on the dashboard,
// and there is no separate widget object to create or clean up. Deleting the
// node removes the widget for free. This matches how the platform and the
// SaveFlow path treat it — one source of truth, the node.
type DashboardWriter struct {
	kube *kube.Client
}

func NewDashboardWriter(k *kube.Client) *DashboardWriter {
	return &DashboardWriter{kube: k}
}

// SetNodeWidget adds the node to the dashboard (label = "true") or removes it.
// portName is accepted for interface compatibility but ignored — a node's
// widget is always its control form. Returns the project name as the "page".
//
// Idempotent: setting an already-set label or clearing an already-clear one is
// a no-op, and a missing node is not an error (nothing to pin).
func (d *DashboardWriter) SetNodeWidget(ctx context.Context, projectName, nodeID, _ string, enabled bool) (string, error) {
	if nodeID == "" {
		return "", fmt.Errorf("node id is required")
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node := &v1alpha1.TinyNode{}
		if err := d.kube.Client.Get(ctx, types.NamespacedName{Namespace: d.kube.Namespace, Name: nodeID}, node); err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		has := node.Labels[v1alpha1.DashboardLabel] == "true"
		if has == enabled {
			return nil // already in the desired state
		}
		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		if enabled {
			node.Labels[v1alpha1.DashboardLabel] = "true"
		} else {
			delete(node.Labels, v1alpha1.DashboardLabel)
		}
		return d.kube.Client.Update(ctx, node)
	})
	if err != nil {
		return "", wrapCRDError(fmt.Errorf("toggle dashboard on %s: %w", nodeID, err))
	}
	return projectName, nil
}

var _ sdktools.DashboardWriter = (*DashboardWriter)(nil)
