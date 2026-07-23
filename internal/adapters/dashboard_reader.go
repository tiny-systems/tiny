package adapters

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// DashboardReader implements sdktools.DashboardReader.
//
// The dashboard is DERIVED from the nodes themselves: a node is a widget iff it
// carries the DashboardLabel. The node is the single source of truth — delete
// the node and its widget is gone, with nothing else to clean up. This mirrors
// the platform (project/get-stream.go), and replaces an earlier design that
// kept a separate TinyWidgetPage store which went stale when a node was deleted
// from another surface.
type DashboardReader struct {
	kube *kube.Client
}

func NewDashboardReader(k *kube.Client) *DashboardReader {
	return &DashboardReader{kube: k}
}

// ReadDashboard lists the project's dashboard-labelled nodes and renders each
// as a widget over its _control port.
func (d *DashboardReader) ReadDashboard(ctx context.Context, projectName string) (*sdktools.DashboardData, error) {
	nodes := &v1alpha1.TinyNodeList{}
	if err := d.kube.Client.List(ctx, nodes,
		client.InNamespace(d.kube.Namespace),
		client.MatchingLabels{
			v1alpha1.ProjectNameLabel: projectName,
			v1alpha1.DashboardLabel:   "true",
		},
	); err != nil {
		return nil, wrapCRDError(fmt.Errorf("list dashboard nodes: %w", err))
	}

	widgets := make([]sdktools.DashboardWidget, 0, len(nodes.Items))
	for i := range nodes.Items {
		node := &nodes.Items[i]
		w := sdktools.DashboardWidget{
			Name:     node.Status.Component.Description,
			NodeName: node.Name,
			PortName: controlPort,
		}
		if w.Name == "" {
			w.Name = node.Name
		}
		// Live control-port data, if the node has reconciled it.
		for _, p := range node.Status.Ports {
			if p.Name == controlPort && len(p.Configuration) > 0 {
				var data map[string]interface{}
				if err := json.Unmarshal(p.Configuration, &data); err == nil {
					w.Data = data
				}
				break
			}
		}
		widgets = append(widgets, w)
	}

	return &sdktools.DashboardData{ProjectName: projectName, Widgets: widgets}, nil
}

// controlPort is the port a dashboard widget renders — the node's control form.
const controlPort = "_control"

var _ sdktools.DashboardReader = (*DashboardReader)(nil)
