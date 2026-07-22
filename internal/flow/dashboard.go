package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

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
			// TinyWidget.Port is a REF — "<nodeName>:<portName>" — while
			// TinyWidget.Name is the widget's display title. Reading Port as a
			// bare port name and Name as the node meant this lookup always
			// missed, so Schema and Data went out nil and every widget rendered
			// as an empty "{}" with no controls (no Send button, no fields).
			nodeName, portName := splitWidgetRef(w.Port)
			if portName == "" {
				portName = "_control"
			}

			var schemaBytes, dataBytes []byte
			node, ok := nodeByName[nodeName]
			if ok {
				for _, ps := range node.Status.Ports {
					if ps.Name == portName {
						schemaBytes = ps.Schema
						dataBytes = ps.Configuration
						break
					}
				}
			}

			// Fall back to the component description, then the node name, so a
			// widget pinned without an explicit title still reads sensibly.
			title := w.Name
			if title == "" && ok {
				title = node.Status.Component.Description
			}
			if title == "" {
				title = nodeName
			}

			events = append(events, &platform.DashboardEvent{
				Type: "UPDATE_WIDGET",
				Widget: &platform.Widget{
					ID:            fmt.Sprintf("%s-%s-%s", page.Name, nodeName, portName),
					Node:          nodeName,
					Port:          portName,
					Title:         title,
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

// splitWidgetRef splits a TinyWidget.Port ref ("<nodeName>:<portName>") into
// its parts. A ref with no colon is treated as a bare node name, leaving the
// port empty so the caller applies its default.
func splitWidgetRef(ref string) (node, port string) {
	if i := strings.LastIndex(ref, ":"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}
