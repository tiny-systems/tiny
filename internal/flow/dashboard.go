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

// dashboardPageName is the single page tiny exposes. tiny has no multi-page
// dashboard; a widget is simply a node carrying the DashboardLabel.
const dashboardPageName = "default"

// buildDashboard DERIVES the dashboard from the project's nodes: every node
// labelled DashboardLabel is a widget over its control port, rendered with the
// node's live schema + data. The node is the single source of truth — a deleted
// node has no widget, with no separate store to fall out of sync (which is
// exactly what the old TinyWidgetPage approach did). Mirrors the platform.
//
// The frontend skips DashboardEvent on any response that also carries
// ClusterInfo, so the caller must send these widget events in their own
// stream message.
func buildDashboard(ctx context.Context, mgr *resource.Manager, projectName string) ([]*platform.ProjectDashboardPage, []*platform.DashboardEvent) {
	nodes, err := mgr.GetProjectNodes(ctx, projectName)
	if err != nil {
		return nil, nil
	}

	pages := []*platform.ProjectDashboardPage{{
		Name:    dashboardPageName,
		Title:   dashboardPageName,
		SortIdx: 0,
	}}
	events := make([]*platform.DashboardEvent, 0)

	for i := range nodes {
		node := nodes[i]
		if node.Labels[v1alpha1.DashboardLabel] != "true" {
			continue
		}

		var schemaBytes, dataBytes []byte
		for _, ps := range node.Status.Ports {
			if ps.Name == controlPort {
				schemaBytes = ps.Schema
				dataBytes = ps.Configuration
				break
			}
		}

		title := node.Status.Component.Description
		if title == "" {
			title = node.Name
		}

		events = append(events, &platform.DashboardEvent{
			Type: "UPDATE_WIDGET",
			Widget: &platform.Widget{
				ID:            fmt.Sprintf("%s-%s-%s", dashboardPageName, node.Name, controlPort),
				Node:          node.Name,
				Port:          controlPort,
				Title:         title,
				DefaultSchema: schemaBytes,
				Schema:        schemaBytes,
				Data:          dataBytes,
				Grid:          &platform.Grid{W: 6, H: 4},
				Pages:         []string{dashboardPageName},
			},
		})
	}

	return pages, events
}

// controlPort is the node port a dashboard widget renders — its control form.
const controlPort = "_control"
