package flow

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/resource"
	platform "github.com/tiny-systems/platform-go"
)

// flowGraphJSON builds a flow's { nodes, edges } graph as JSON — the shape the
// editor's FlowPreview renders for thumbnails. Returns the bytes and the node
// count. "{}" on any error (preview just shows nothing).
func flowGraphJSON(ctx context.Context, svc *Service, mgr *resource.Manager, projectName, flowName string) ([]byte, int) {
	events, _, err := svc.buildFlowEvents(ctx, mgr, &platform.GetFlowStreamRequest{
		ProjectName: projectName,
		FlowName:    flowName,
	})
	if err != nil {
		return []byte("{}"), 0
	}
	graph := map[string][]json.RawMessage{"nodes": {}, "edges": {}}
	nodes := 0
	for _, e := range events {
		if len(e.Graph) == 0 {
			continue
		}
		var probe map[string]json.RawMessage
		if json.Unmarshal(e.Graph, &probe) != nil {
			continue
		}
		if _, isEdge := probe["source"]; isEdge {
			graph["edges"] = append(graph["edges"], e.Graph)
		} else {
			graph["nodes"] = append(graph["nodes"], e.Graph)
			nodes++
		}
	}
	b, err := json.Marshal(graph)
	if err != nil {
		return []byte("{}"), nodes
	}
	return b, nodes
}

// buildDashboard reads the project's widget pages and turns each exposed
// node-port widget into a platform.Widget, resolving its schema + data from the
// referenced node's reconciled port. Returns the dashboard pages (for the page
// switcher) and the widget events (each UPDATE_WIDGET) the shell renders.
//
// The frontend skips DashboardEvent on any response that also carries
// ClusterInfo, so the caller must send these widget events in their own
// stream message.
func buildDashboard(ctx context.Context, mgr *resource.Manager, projectName string) ([]*platform.ProjectDashboardPage, []*platform.DashboardEvent) {
	widgetPages, err := mgr.GetProjectPageWidgets(ctx, projectName)
	if err != nil {
		return nil, nil
	}

	nodes, _ := mgr.GetProjectNodes(ctx, projectName)
	nodeByName := make(map[string]v1alpha1.TinyNode, len(nodes))
	for _, n := range nodes {
		nodeByName[n.Name] = n
	}

	pages := make([]*platform.ProjectDashboardPage, 0, len(widgetPages))
	events := make([]*platform.DashboardEvent, 0)

	for i, page := range widgetPages {
		pages = append(pages, &platform.ProjectDashboardPage{
			Name:    page.Name,
			Title:   page.Name,
			SortIdx: int32(i),
		})

		for _, w := range page.Spec.Widgets {
			port := w.Port
			if port == "" {
				port = "_control"
			}

			var schemaBytes, dataBytes []byte
			if node, ok := nodeByName[w.Name]; ok {
				for _, ps := range node.Status.Ports {
					if ps.Name == port {
						schemaBytes = ps.Schema
						dataBytes = ps.Configuration
						break
					}
				}
			}

			events = append(events, &platform.DashboardEvent{
				Type: "UPDATE_WIDGET",
				Widget: &platform.Widget{
					ID:            fmt.Sprintf("%s-%s-%s", page.Name, w.Name, port),
					Node:          w.Name,
					Port:          port,
					Title:         w.Name,
					DefaultSchema: schemaBytes,
					Schema:        schemaBytes,
					Data:          dataBytes,
					Grid: &platform.Grid{
						X: int32(w.GridX),
						Y: int32(w.GridY),
						W: int32(w.GridW),
						H: int32(w.GridH),
					},
					Pages: []string{page.Name},
				},
			})
		}
	}

	return pages, events
}
