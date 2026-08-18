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
		if enabled {
			// A widget renders from the node's control port. Labelling a node
			// that has none produces a widget that never appears — silence is
			// the hardest failure to trace, so refuse instead. A node that has
			// published no ports at all is still reconciling: allow it, since
			// refusing a correct request is worse than allowing a useless one.
			if err := requireControlPort(node); err != nil {
				return err
			}
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

// requireControlPort reports whether a node publishes the control port a
// dashboard widget renders from. Fails open on a node with no published ports:
// it has not reconciled yet, and that is not evidence of absence.
func requireControlPort(node *v1alpha1.TinyNode) error {
	if len(node.Status.Ports) == 0 {
		return nil
	}
	for _, p := range node.Status.Ports {
		if p.Name == v1alpha1.ControlPort {
			return nil
		}
	}
	return fmt.Errorf("node %s has no %s port, so it would never render as a widget", node.Name, v1alpha1.ControlPort)
}

var _ sdktools.DashboardWriter = (*DashboardWriter)(nil)
