package flow

import (
	"context"
	"encoding/json"
	"time"

	"github.com/tiny-systems/module/api/v1alpha1"
	platform "github.com/tiny-systems/platform-go"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/watch"
)

// The editor's EditorClient has four service slices. tiny fully backs
// FlowService; ProjectService is backed here for the project dashboard, and
// StatisticsService is a graceful no-op (no local trace store yet).
//
// RunsService and WorkspaceActivityService are deliberately absent: neither
// exists in tiny's pinned platform-go, so the runs + activity-feed panels stay
// empty until that's bumped.

// projectService backs the project dashboard shell. It reuses the flow
// Service's cluster access (manager + buildFlowEvents).
type projectService struct {
	platform.UnimplementedProjectServiceServer
	svc *Service
}

func (p projectService) GetConfiguration(context.Context, *platform.GetProjectConfigurationRequest) (*platform.GetProjectConfigurationResponse, error) {
	return &platform.GetProjectConfigurationResponse{}, nil
}

// GetStream drives the project dashboard: it sends one snapshot with the
// project's flows (each carrying its graph for the preview thumbnail) plus
// flow/node counts, then holds the stream open. Widgets/resources/pages are
// empty for now — the flows list is what the shell needs to browse and open
// flows.
func (p projectService) GetStream(req *platform.GetProjectStreamRequest, stream grpc.ServerStreamingServer[platform.GetProjectStreamEvent]) error {
	ctx := stream.Context()
	mgr, err := p.svc.manager()
	if err != nil {
		return err
	}
	flows, err := mgr.GetFlowList(ctx, req.ProjectName)
	if err != nil {
		return err
	}

	items := make([]*platform.FlowListItem, 0, len(flows))
	totalNodes := 0
	for _, f := range flows {
		name := f.Annotations[v1alpha1.FlowDescriptionAnnotation]
		if name == "" {
			name = f.Name
		}

		graphBytes := []byte("{}")
		events, _, evErr := p.svc.buildFlowEvents(ctx, mgr, &platform.GetFlowStreamRequest{
			ProjectName: req.ProjectName,
			FlowName:    f.Name,
		})
		if evErr == nil {
			graph := map[string][]json.RawMessage{"nodes": {}, "edges": {}}
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
					totalNodes++
				}
			}
			if b, mErr := json.Marshal(graph); mErr == nil {
				graphBytes = b
			}
		}

		items = append(items, &platform.FlowListItem{Flow: &platform.Flow{
			ID:           f.Name,
			Name:         name,
			ResourceName: f.Name,
			ProjectID:    req.ProjectName,
			Graph:        graphBytes,
		}})
	}

	pages, widgetEvents := buildDashboard(ctx, mgr, req.ProjectName)

	// The shell branches on event Type. First the project configuration — this
	// is what sets project.value and clears the "Loading…" header.
	if err := stream.Send(&platform.GetProjectStreamEvent{
		Type: "INIT_PROJECT_CONFIGURATION",
		Configuration: &platform.GetProjectConfigurationResponse{
			Project: &platform.Project{
				ID:           req.ProjectName,
				Name:         req.ProjectName,
				ResourceName: req.ProjectName,
			},
		},
	}); err != nil {
		return err
	}

	// Then the cluster snapshot (flows/counts/pages) — this clears loading and
	// fills the Flows/Nodes tab.
	if err := stream.Send(&platform.GetProjectStreamEvent{
		Type: "INIT_PROJECT",
		ClusterInfo: &platform.ProjectClusterInfo{
			Stat:  &platform.ProjectStat{FlowsAmount: int32(len(items)), NodesAmount: int32(totalNodes)},
			Flows: items,
			Pages: pages,
		},
	}); err != nil {
		return err
	}

	// Finally the widgets, in their own message (the shell skips DashboardEvent
	// on any message that also carries ClusterInfo).
	if len(widgetEvents) > 0 {
		if err := stream.Send(&platform.GetProjectStreamEvent{DashboardEvent: widgetEvents}); err != nil {
			return err
		}
	}

	// Keep the dashboard live, exactly as the platform does (project/get-stream.go):
	// watch the project's nodes and push a widget event per change. A dashboard
	// node changing → UPDATE_WIDGET (fresh control-port data); a dashboard node
	// deleted → DELETE_WIDGET so the widget disappears instead of lingering.
	// WatchNodes is a k8s informer, and each event is built from the node already
	// in hand — no otel forwarder, no per-event network read.
	w, err := mgr.WatchNodes(ctx, req.ProjectName)
	if err != nil {
		<-ctx.Done() // no watch available — snapshot stands until disconnect
		return nil
	}
	defer w.Stop()

	// Track which nodes are currently shown as widgets, seeded from the snapshot.
	// This lets a widget be removed both when its node is DELETED and when the
	// node loses its dashboard label ("Add to dashboard" unchecked + saved) —
	// the platform handles only deletion, so there an unchecked widget lingers
	// until reload. Tracking also avoids emitting a delete for every non-widget
	// node's routine reconcile.
	widgetNodes := make(map[string]bool)
	for _, e := range widgetEvents {
		if e.Widget != nil {
			widgetNodes[e.Widget.Node] = true
		}
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.ResultChan():
			// A closed channel would otherwise hot-spin (the platform's bug):
			// wait for disconnect instead.
			if !ok {
				<-ctx.Done()
				return nil
			}
			node, isNode := ev.Object.(*v1alpha1.TinyNode)
			if !isNode || node == nil {
				continue
			}
			isWidget := ev.Type != watch.Deleted && node.Labels[v1alpha1.DashboardLabel] == "true"

			var event *platform.DashboardEvent
			switch {
			case isWidget:
				event = updateWidgetEvent(*node)
				widgetNodes[node.Name] = true
			case widgetNodes[node.Name]:
				// Was a widget, now isn't (deleted or unlabelled) → remove it.
				event = deleteWidgetEvent(*node)
				delete(widgetNodes, node.Name)
			default:
				continue // not a widget and never was — nothing to send
			}
			// Return (not break) on send error: the client is gone.
			if err := stream.Send(&platform.GetProjectStreamEvent{DashboardEvent: []*platform.DashboardEvent{event}}); err != nil {
				return err
			}
		case <-heartbeat.C:
			// keep the stream visibly alive between changes
		}
	}
}

// statisticsService (traces) moved to statistics.go, now backed by the
// otel-collector trace reader.
