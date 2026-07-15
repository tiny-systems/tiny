package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// DashboardReader implements sdktools.DashboardReader by reading
// TinyWidgetPage CRDs and resolving each widget's data from the
// corresponding TinyNode status port.
type DashboardReader struct {
	kube *kube.Client
}

func NewDashboardReader(k *kube.Client) *DashboardReader {
	return &DashboardReader{kube: k}
}

// ReadDashboard reads all TinyWidgetPage CRDs for the project,
// fetches every referenced TinyNode, and extracts the current
// port configuration for each widget.
func (d *DashboardReader) ReadDashboard(ctx context.Context, projectName string) (*sdktools.DashboardData, error) {
	// List widget pages for this project
	pages := &v1alpha1.TinyWidgetPageList{}
	if err := d.kube.Client.List(ctx, pages,
		client.InNamespace(d.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName},
	); err != nil {
		return nil, wrapCRDError(fmt.Errorf("list widget pages: %w", err))
	}

	// Collect all widget refs: "nodeName:portName"
	type widgetRef struct {
		widget v1alpha1.TinyWidget
		page   string
	}
	var refs []widgetRef
	for _, page := range pages.Items {
		for _, w := range page.Spec.Widgets {
			if w.Port == "" {
				continue
			}
			refs = append(refs, widgetRef{widget: w, page: page.Name})
		}
	}

	if len(refs) == 0 {
		return &sdktools.DashboardData{
			ProjectName: projectName,
		}, nil
	}

	// Fetch unique nodes referenced by widgets
	nodeNames := make(map[string]bool)
	for _, r := range refs {
		nodeName, _ := parseWidgetPort(r.widget.Port)
		if nodeName != "" {
			nodeNames[nodeName] = true
		}
	}

	nodeMap := make(map[string]*v1alpha1.TinyNode)
	for name := range nodeNames {
		node := &v1alpha1.TinyNode{}
		key := client.ObjectKey{Namespace: d.kube.Namespace, Name: name}
		if err := d.kube.Client.Get(ctx, key, node); err != nil {
			continue // node might not exist yet
		}
		nodeMap[name] = node
	}

	// Build widgets with resolved data
	var widgets []sdktools.DashboardWidget
	for _, r := range refs {
		nodeName, portName := parseWidgetPort(r.widget.Port)
		if nodeName == "" || portName == "" {
			continue
		}

		w := sdktools.DashboardWidget{
			Name:     r.widget.Name,
			NodeName: nodeName,
			PortName: portName,
			GridX:    r.widget.GridX,
			GridY:    r.widget.GridY,
			GridW:    r.widget.GridW,
			GridH:    r.widget.GridH,
		}

		// Resolve port data from node status
		if node, ok := nodeMap[nodeName]; ok {
			for _, p := range node.Status.Ports {
				if p.Name != portName {
					continue
				}
				if len(p.Configuration) > 0 {
					var data map[string]interface{}
					if err := json.Unmarshal(p.Configuration, &data); err == nil {
						w.Data = data
					}
				}
				break
			}

			// Use component description as fallback name
			if w.Name == "" {
				w.Name = node.Status.Component.Description
			}
		}

		widgets = append(widgets, w)
	}

	return &sdktools.DashboardData{
		ProjectName: projectName,
		Widgets:     widgets,
	}, nil
}

// parseWidgetPort splits "nodeName:portName" into its parts.
func parseWidgetPort(port string) (string, string) {
	parts := strings.SplitN(port, ":", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

var _ sdktools.DashboardReader = (*DashboardReader)(nil)
