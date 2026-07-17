package flow

import (
	"context"
	"encoding/json"

	"github.com/tiny-systems/module/api/v1alpha1"
	platform "github.com/tiny-systems/platform-go"
	"google.golang.org/grpc"
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

	if err := stream.Send(&platform.GetProjectStreamEvent{
		ClusterInfo: &platform.ProjectClusterInfo{
			Stat:  &platform.ProjectStat{FlowsAmount: int32(len(items)), NodesAmount: int32(totalNodes)},
			Flows: items,
		},
	}); err != nil {
		return err
	}

	// Hold the stream open until the client disconnects; a one-shot snapshot is
	// enough for the local dashboard today.
	<-ctx.Done()
	return nil
}

// statisticsService stubs traces/telemetry. There's no local OTel trace store
// wired into the editor yet, so traces come back empty and the stream closes
// immediately rather than erroring the Telemetry panel.
type statisticsService struct {
	platform.UnimplementedStatisticsServiceServer
}

func (statisticsService) GetTraces(context.Context, *platform.StatisticsGetTracesRequest) (*platform.StatisticsGetTracesResponse, error) {
	return &platform.StatisticsGetTracesResponse{}, nil
}

func (statisticsService) GetStream(_ *platform.StatisticsStreamRequest, _ grpc.ServerStreamingServer[platform.StatisticsStreamResponse]) error {
	return nil
}
