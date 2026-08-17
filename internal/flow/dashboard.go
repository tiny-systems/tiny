package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/tiny-systems/module/pkg/utils"
	"sort"
	"strconv"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/resource"
	"github.com/tiny-systems/module/pkg/schema"
	platform "github.com/tiny-systems/platform-go"
)

// flowGraphJSON builds a flow's { nodes, edges } graph as JSON — the shape the
// editor's FlowPreview renders for thumbnails. Returns the bytes and the node
// count. "{}" on any error (preview just shows nothing).
func flowGraphJSON(ctx context.Context, svc *Service, mgr *resource.Manager, projectName, flowName string) ([]byte, int) {
	events, _, err := svc.buildFlowEvents(ctx, mgr, &platform.GetFlowStreamRequest{
		ProjectName: projectName,
		FlowName:    flowName,
	}, nil)
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

// dashboardPageName is the page widgets land on when a project has no
// TinyWidgetPage of its own — a project that never opened the dashboard
// editor still has somewhere to render.
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

	// Pages are TinyWidgetPage resources — the same model the platform uses,
	// where a page owns its widget entries and each entry carries the grid
	// placement. A widget may belong to SEVERAL pages, so membership is read
	// per page rather than stored on the node.
	pageList, _ := mgr.GetProjectPageWidgets(ctx, projectName)

	pages := make([]*platform.ProjectDashboardPage, 0, len(pageList))
	// port full name -> the pages showing it, and the placement to render.
	memberOf := map[string][]string{}
	placement := map[string]v1alpha1.TinyWidget{}

	for i := range pageList {
		page := pageList[i]
		title := page.Annotations[v1alpha1.PageTitleAnnotation]
		if title == "" {
			title = page.Name
		}
		idx, _ := strconv.Atoi(page.Annotations[v1alpha1.PageSortIdxAnnotation])
		pages = append(pages, &platform.ProjectDashboardPage{
			Name:    title,
			Title:   title,
			SortIdx: int32(idx),
		})
		for _, w := range page.Spec.Widgets {
			memberOf[w.Port] = append(memberOf[w.Port], title)
			if _, seen := placement[w.Port]; !seen {
				placement[w.Port] = w
			}
		}
	}

	sort.SliceStable(pages, func(i, j int) bool { return pages[i].SortIdx < pages[j].SortIdx })

	events := make([]*platform.DashboardEvent, 0)
	var needDefault bool

	for i := range nodes {
		node := nodes[i]
		if node.Labels[v1alpha1.DashboardLabel] != "true" {
			continue
		}
		port := utils.GetPortFullName(node.Name, controlPort)
		on := memberOf[port]
		if len(on) == 0 {
			// A node labelled for the dashboard that no page claims yet —
			// the common case right after a build, before anyone arranged
			// the layout.
			on = []string{dashboardPageName}
			needDefault = true
		}
		w, hasPlacement := placement[port]
		events = append(events, updateWidgetEvent(node, on, w, hasPlacement))
	}

	if needDefault || len(pages) == 0 {
		has := false
		for _, p := range pages {
			if p.Name == dashboardPageName {
				has = true
				break
			}
		}
		if !has {
			pages = append([]*platform.ProjectDashboardPage{{
				Name: dashboardPageName, Title: dashboardPageName, SortIdx: 0,
			}}, pages...)
		}
	}

	return pages, events
}

// controlPort is the node port a dashboard widget renders — its control form.
const controlPort = "_control"

// widgetID is the stable id the editor upserts a widget by. It must match
// between the initial snapshot and later watch events, or an update would add a
// duplicate instead of replacing.
func widgetID(node v1alpha1.TinyNode) string {
	return fmt.Sprintf("%s-%s", node.Name, controlPort)
}

// portFromWidgetID is widgetID's inverse: the browser identifies a widget by
// the DOM-safe dashed id (GridStack addresses elements with `#id`, and a
// colon there is a CSS selector), but a page stores placements under the
// port's real name. Saving the browser's id verbatim wrote a key the read
// path can never match, so a widget silently lost its placement AND its page
// membership on the next load.
func portFromWidgetID(id string) string {
	suffix := "-" + controlPort
	if strings.HasSuffix(id, suffix) {
		return strings.TrimSuffix(id, suffix) + ":" + controlPort
	}
	return id
}

// updateWidgetEvent renders one dashboard-labelled node as an UPDATE_WIDGET,
// carrying its live control-port schema + data. Used both for the initial
// snapshot and for each realtime change.
func updateWidgetEvent(node v1alpha1.TinyNode, pages []string, placed v1alpha1.TinyWidget, hasPlacement bool) *platform.DashboardEvent {
	var schemaBytes, dataBytes []byte
	for _, ps := range node.Status.Ports {
		if ps.Name == controlPort {
			schemaBytes = ps.Schema
			dataBytes = ps.Configuration
			break
		}
	}
	// The status schema is the component's NATIVE schema by design; schemas a
	// person or agent authored on the node (Spec.Ports) overlay at read time.
	// The canvas path does this merge (utils.GetFlowMaps) — the widget path
	// must too, or a custom trigger form (masked API-key field, titles) never
	// reaches the dashboard.
	if len(schemaBytes) > 0 {
		if merged, err := schema.UpdateWithDefinitions(schemaBytes, utils.GetConfigurableDefinitions(node, nil)); err == nil {
			schemaBytes = merged
		}
	}

	// Title: the page's own name for this widget wins (that is what the user
	// typed in the dashboard editor), then the node's label, then the
	// component's description.
	title := placed.Name
	if title == "" {
		title = node.Annotations[v1alpha1.NodeLabelAnnotation]
	}
	if title == "" {
		title = node.Status.Component.Description
	}
	if title == "" {
		title = node.Name
	}

	// Placement comes from the page entry. Only a widget nobody has arranged
	// falls back to a default size, and half-width so several fit a row.
	grid := &platform.Grid{W: 3, H: 4}
	if hasPlacement {
		grid = &platform.Grid{
			X: int32(placed.GridX),
			Y: int32(placed.GridY),
			W: int32(placed.GridW),
			H: int32(placed.GridH),
		}
		if grid.W == 0 {
			grid.W = 3
		}
		if grid.H == 0 {
			grid.H = 4
		}
	}

	return &platform.DashboardEvent{
		Type: "UPDATE_WIDGET",
		Widget: &platform.Widget{
			ID:            widgetID(node),
			Node:          node.Name,
			Port:          controlPort,
			Title:         title,
			DefaultSchema: schemaBytes,
			Schema:        schemaBytes,
			Data:          dataBytes,
			Grid:          grid,
			Pages:         pages,
		},
	}
}

// deleteWidgetEvent tells the editor to drop a node's widget — sent when a
// dashboard node is deleted or loses its label. Same id as the update so the
// editor removes the right one.
func deleteWidgetEvent(node v1alpha1.TinyNode) *platform.DashboardEvent {
	return &platform.DashboardEvent{
		Type: "DELETE_WIDGET",
		Widget: &platform.Widget{
			ID:    widgetID(node),
			Node:  node.Name,
			Port:  controlPort,
			Pages: []string{dashboardPageName},
		},
	}
}

// widgetEventForNode builds one node's widget event with its real page
// membership and placement — the watch path's equivalent of the snapshot,
// so a live update cannot silently move a widget back to the default page.
func widgetEventForNode(ctx context.Context, mgr *resource.Manager, projectName string, node v1alpha1.TinyNode) *platform.DashboardEvent {
	port := utils.GetPortFullName(node.Name, controlPort)

	var on []string
	var placed v1alpha1.TinyWidget
	var hasPlacement bool

	if pageList, err := mgr.GetProjectPageWidgets(ctx, projectName); err == nil {
		for i := range pageList {
			page := pageList[i]
			title := page.Annotations[v1alpha1.PageTitleAnnotation]
			if title == "" {
				title = page.Name
			}
			for _, w := range page.Spec.Widgets {
				if w.Port != port {
					continue
				}
				on = append(on, title)
				if !hasPlacement {
					placed, hasPlacement = w, true
				}
			}
		}
	}
	if len(on) == 0 {
		on = []string{dashboardPageName}
	}
	return updateWidgetEvent(node, on, placed, hasPlacement)
}
