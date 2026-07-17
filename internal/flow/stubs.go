package flow

import (
	"context"

	platform "github.com/tiny-systems/platform-go"
	"google.golang.org/grpc"
)

// The editor's EditorClient has four service slices. tiny fully backs
// FlowService; ProjectService and StatisticsService are registered here with
// just the couple of methods the editor calls so those panels degrade
// gracefully (empty results) instead of failing with "unknown service".
//
// RunsService is deliberately absent: it doesn't exist in tiny's pinned
// platform-go, so the runs panel stays unimplemented until that's bumped.

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
	// No local statistics stream — close cleanly so the panel shows "no data".
	return nil
}

// projectService stubs the project slice. The editor's add-component form reads
// project configuration defaults; locally there are none, so return empty.
type projectService struct {
	platform.UnimplementedProjectServiceServer
}

func (projectService) GetConfiguration(context.Context, *platform.GetProjectConfigurationRequest) (*platform.GetProjectConfigurationResponse, error) {
	return &platform.GetProjectConfigurationResponse{}, nil
}
