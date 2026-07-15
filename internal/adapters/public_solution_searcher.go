package adapters

import (
	"context"
	"fmt"

	platformapi "github.com/tiny-systems/platform-api"
	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// PublicSolutionSearcher implements sdktools.SolutionSearcher by calling
// the Tiny Systems REST API at /v1/solutions. When constructed without a
// dev key, only public solutions are visible. With a dev key, the
// platform widens scope to include workspace-private solutions owned by
// the key's workspace.
type PublicSolutionSearcher struct {
	client *platformapi.ClientWithResponses
}

// NewPublicSolutionSearcher builds an adapter against the given server
// URL and optional dev key. Pass an empty devKey for anonymous access.
func NewPublicSolutionSearcher(serverURL, devKey string) (*PublicSolutionSearcher, error) {
	client, err := NewPlatformClient(serverURL, devKey)
	if err != nil {
		return nil, fmt.Errorf("init platform-api client: %w", err)
	}
	return &PublicSolutionSearcher{client: client}, nil
}

// SearchSolutions calls GET /v1/solutions/search and returns summaries.
// keyword and tags are both optional; limit is clamped by the server to
// [1, 100].
func (p *PublicSolutionSearcher) SearchSolutions(ctx context.Context, keyword string, tags []string, limit int) ([]sdktools.SolutionSummary, error) {
	params := &platformapi.SearchPublicSolutionsParams{}
	if keyword != "" {
		q := keyword
		params.Q = &q
	}
	if len(tags) > 0 {
		t := tags
		params.Tags = &t
	}
	if limit > 0 {
		l := limit
		params.Limit = &l
	}

	resp, err := p.client.SearchPublicSolutionsWithResponse(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("search solutions: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("search solutions: unexpected status %d", resp.StatusCode())
	}

	out := make([]sdktools.SolutionSummary, 0, len(resp.JSON200.Results))
	for _, r := range resp.JSON200.Results {
		out = append(out, sdktools.SolutionSummary{
			UUID:        r.Uuid,
			Title:       r.Title,
			Description: r.Description,
			Tags:        derefTags(r.Tags),
		})
	}
	return out, nil
}

// GetSolution calls GET /v1/solutions/{uuid} and returns the full details.
// Returns nil (no error) when the server reports 404 so callers can
// distinguish "not found" from transport errors.
func (p *PublicSolutionSearcher) GetSolution(ctx context.Context, uuid string) (*sdktools.SolutionDetails, error) {
	resp, err := p.client.GetPublicSolutionWithResponse(ctx, uuid)
	if err != nil {
		return nil, fmt.Errorf("get solution: %w", err)
	}
	if resp.StatusCode() == 404 {
		return nil, nil
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("get solution: unexpected status %d", resp.StatusCode())
	}

	body := resp.JSON200
	details := &sdktools.SolutionDetails{
		UUID:        body.Uuid,
		Title:       body.Title,
		Description: body.Description,
		Tags:        derefTags(body.Tags),
		Flows:       make([]sdktools.SolutionFlow, 0, len(body.Flows)),
	}
	if body.Variables != nil {
		details.Variables = *body.Variables
	}

	for _, f := range body.Flows {
		flow := sdktools.SolutionFlow{
			Title: f.Title,
			Nodes: make([]sdktools.SolutionNode, 0, len(f.Nodes)),
			Edges: make([]sdktools.SolutionEdge, 0, len(f.Edges)),
		}
		for _, n := range f.Nodes {
			node := sdktools.SolutionNode{
				ID:        n.Id,
				Component: n.Component,
				Module:    n.Module,
			}
			if n.Settings != nil {
				node.Settings = *n.Settings
			}
			if n.Position != nil {
				node.Position = *n.Position
			}
			flow.Nodes = append(flow.Nodes, node)
		}
		for _, e := range f.Edges {
			edge := sdktools.SolutionEdge{
				Source:       e.Source,
				SourceHandle: e.SourceHandle,
				Target:       e.Target,
				TargetHandle: e.TargetHandle,
			}
			if e.Configuration != nil {
				edge.Configuration = *e.Configuration
			}
			flow.Edges = append(flow.Edges, edge)
		}
		details.Flows = append(details.Flows, flow)
	}
	return details, nil
}

// derefTags flattens the generated client's optional *[]string into a
// plain []string slice, which is what the SDK types expect.
func derefTags(tags *[]string) []string {
	if tags == nil {
		return nil
	}
	return *tags
}

var _ sdktools.SolutionSearcher = (*PublicSolutionSearcher)(nil)
