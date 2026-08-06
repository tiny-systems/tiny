package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	sdkutils "github.com/tiny-systems/module/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/authstore"
	"github.com/tiny-systems/tiny/internal/kube"
)

// solutionExport mirrors the platform's solution export contract
// (services/solution-export on the platform side): the same JSON that
// GET /v1/solutions/export produces and POST /v1/solutions/import accepts.
type solutionExport struct {
	Version     int                      `json:"version"`
	Type        string                   `json:"type"`
	UpdateSlug  string                   `json:"updateSlug,omitempty"`
	Title       string                   `json:"title"`
	Description string                   `json:"description"`
	Tags        []string                 `json:"tags"`
	TinyFlows   []exportFlow             `json:"tinyFlows"`
	Elements    []map[string]interface{} `json:"elements"`
	Pages       []exportPage             `json:"pages"`
	Scenarios   []exportScenario         `json:"scenarios,omitempty"`
}

type exportFlow struct {
	ResourceName string `json:"resourceName"`
	Name         string `json:"name"`
}

type exportPage struct {
	Name    string         `json:"name"`
	Title   string         `json:"title"`
	SortIdx int            `json:"sortIdx"`
	Widgets []exportWidget `json:"widgets"`
}

type exportWidget struct {
	Port        string          `json:"port"`
	Name        string          `json:"name"`
	GridX       int             `json:"gridX"`
	GridY       int             `json:"gridY"`
	GridW       int             `json:"gridW"`
	GridH       int             `json:"gridH"`
	SchemaPatch json.RawMessage `json:"schemaPatch,omitempty"`
}

type exportScenario struct {
	Name  string                      `json:"name"`
	Ports []v1alpha1.ScenarioPortData `json:"ports"`
}

func newPublishCmd() *cobra.Command {
	var (
		apiBase     string
		title       string
		description string
		tags        []string
		updateSlug  string
	)
	c := &cobra.Command{
		Use:   "publish",
		Short: "Publish the current project as a solution on tinysystems.io",
		Long: `Publish packages the project's flows, nodes, dashboard pages and scenarios
into a solution export and uploads it to the platform, where it goes live in
the public solutions catalog (unlist it from your dashboard anytime).

Auth: 'tiny login' once, or a developer key via --key / the TINY_API_KEY
environment variable (mint one in the dashboard under Setup → Developer keys).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			// Auth resolution: explicit key flag, then env, then the
			// OIDC session from `tiny login`.
			key := cmd.Flag("key").Value.String()
			if key == "" {
				key = os.Getenv("TINY_API_KEY")
			}
			workspace := cmd.Flag("workspace").Value.String()
			if key == "" {
				creds, err := freshAccessToken(cmd.Context())
				if errors.Is(err, authstore.ErrNotLoggedIn) {
					return fmt.Errorf("not signed in — run `tiny login` (robots: --key or TINY_API_KEY)")
				}
				if err != nil {
					return fmt.Errorf("stored session unusable: %v — run `tiny login` again", err)
				}
				key = creds.AccessToken
				if workspace == "" {
					workspace = creds.Workspace
				}
			}
			if flagProject == "" {
				return fmt.Errorf("no project: pass --project/-p")
			}

			k, err := kube.NewClient(kube.Options{Context: flagContext, Namespace: flagNamespace})
			if err != nil {
				return err
			}

			export, err := buildSolutionExport(ctx, k, flagProject, title, description, tags)
			if err != nil {
				return err
			}
			if updateSlug != "" {
				export.UpdateSlug = updateSlug
				// On update, empty copy fields mean "keep what's there" —
				// the server only applies non-empty values, so dashboard-
				// edited title/description/tags survive unless the flags
				// were explicitly passed.
				if !cmd.Flags().Changed("title") {
					export.Title = ""
				}
				if !cmd.Flags().Changed("description") {
					export.Description = ""
				}
				if !cmd.Flags().Changed("tags") {
					export.Tags = nil
				}
			}
			if len(export.TinyFlows) == 0 {
				return fmt.Errorf("project %q has no flows to publish", flagProject)
			}
			if err := validateSolutionForPublish(ctx, k, flagProject, export.Scenarios); err != nil {
				return err
			}

			body, err := json.Marshal(export)
			if err != nil {
				return err
			}

			subject := export.Title
			if updateSlug != "" {
				subject = updateSlug
			}
			verb := "publishing"
			if updateSlug != "" {
				verb = "updating"
			}
			fmt.Printf("  %s %s — %d flows, %d elements, %d pages, %d scenarios\n",
				verb, styleTitle.Render(subject), len(export.TinyFlows), len(export.Elements), len(export.Pages), len(export.Scenarios))

			req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v1/solutions/import", bytes.NewReader(body))
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			if workspace != "" {
				req.Header.Set("X-Workspace", workspace)
			}

			httpClient := &http.Client{Timeout: 60 * time.Second}
			resp, err := httpClient.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

			if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
				return fmt.Errorf("publish failed (%s): %s", resp.Status, bytes.TrimSpace(respBody))
			}

			var out struct {
				Slug  string `json:"slug"`
				Title string `json:"title"`
				URL   string `json:"url"`
			}
			_ = json.Unmarshal(respBody, &out)
			if updateSlug != "" {
				fmt.Printf("\n  %s %s\n", styleTitle.Render("updated:"), out.Title)
				if out.URL != "" {
					fmt.Printf("  %s\n\n  Same URL, new revision — the previous one stays revertable in your dashboard.\n", out.URL)
				}
				return nil
			}
			fmt.Printf("\n  %s %s\n", styleTitle.Render("published:"), out.Title)
			if out.URL != "" {
				fmt.Printf("  %s\n\n  It is live in the public catalog — unlist it from your dashboard anytime.\n", out.URL)
			}
			return nil
		},
	}
	c.Flags().String("key", "", "developer key (or TINY_API_KEY); omit to use `tiny login` session")
	c.Flags().String("workspace", "", "workspace slug (only needed with several workspaces)")
	c.Flags().StringVar(&apiBase, "api", "https://api.tinysystems.io", "platform API base URL")
	c.Flags().StringVar(&title, "title", "", "solution title (default: project name)")
	c.Flags().StringVar(&description, "description", "", "solution description (default: project description)")
	c.Flags().StringSliceVar(&tags, "tags", nil, "tags, comma separated")
	c.Flags().StringVar(&updateSlug, "update", "", "update an existing solution by its slug (a new content revision on the same URL) instead of creating a new entry; title/description/tags change only when their flags are passed")
	return c
}

// buildSolutionExport assembles the export the same way the platform's
// BuildExport does: all project nodes rendered to graph elements via the
// SDK's non-minimal NodesToGraph (handles carry schema + configuration),
// each element stamped with its flow's resource name.
func buildSolutionExport(ctx context.Context, k *kube.Client, projectName, title, description string, tags []string) (*solutionExport, error) {
	// flows
	flowList := &v1alpha1.TinyFlowList{}
	if err := k.Client.List(ctx, flowList,
		client.InNamespace(k.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName}); err != nil {
		return nil, fmt.Errorf("list flows: %w", err)
	}

	export := &solutionExport{
		Version:     1,
		Type:        "solution",
		Title:       title,
		Description: description,
		Tags:        tags,
	}
	for _, f := range flowList.Items {
		name := f.Annotations[v1alpha1.FlowDescriptionAnnotation]
		if name == "" {
			name = f.Name
		}
		export.TinyFlows = append(export.TinyFlows, exportFlow{ResourceName: f.Name, Name: name})
	}
	sort.Slice(export.TinyFlows, func(i, j int) bool { return export.TinyFlows[i].ResourceName < export.TinyFlows[j].ResourceName })

	if export.Title == "" {
		export.Title = projectName
	}
	if export.Description == "" {
		proj := &v1alpha1.TinyProject{}
		if err := k.Client.Get(ctx, client.ObjectKey{Namespace: k.Namespace, Name: projectName}, proj); err == nil {
			export.Description = proj.Spec.Description
			if export.Title == projectName && proj.Spec.Description != "" {
				// keep title as project name; description from CR
			}
		}
	}

	// nodes → elements
	nodeList := &v1alpha1.TinyNodeList{}
	if err := k.Client.List(ctx, nodeList,
		client.InNamespace(k.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName}); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	allNodes := make(map[string]v1alpha1.TinyNode, len(nodeList.Items))
	for _, n := range nodeList.Items {
		allNodes[n.Name] = n
	}
	nodeElements, edgeElements, err := sdkutils.NodesToGraphWithOptions(allNodes, nil, false)
	if err != nil {
		return nil, fmt.Errorf("render graph: %w", err)
	}
	for _, elem := range nodeElements {
		if m, ok := elem.(map[string]interface{}); ok {
			if id, _ := m["id"].(string); id != "" {
				if node, exists := allNodes[id]; exists {
					m["flow"] = node.Labels[v1alpha1.FlowNameLabel]
				}
			}
			export.Elements = append(export.Elements, m)
		}
	}
	for _, elem := range edgeElements {
		if m, ok := elem.(map[string]interface{}); ok {
			if src, _ := m["source"].(string); src != "" {
				if node, exists := allNodes[src]; exists {
					m["flow"] = node.Labels[v1alpha1.FlowNameLabel]
				}
			}
			export.Elements = append(export.Elements, m)
		}
	}

	// Secrets die here, before the export leaves the machine. Components
	// whose settings absorb runtime data (debug mirrors the last message)
	// would otherwise publish whatever flowed through the graph — API keys
	// included, in both port configurations and schema defaults.
	sdktools.RedactGraphElements(export.Elements)

	// dashboard pages
	pageList := &v1alpha1.TinyWidgetPageList{}
	if err := k.Client.List(ctx, pageList,
		client.InNamespace(k.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName}); err == nil {
		for _, p := range pageList.Items {
			title := p.Annotations[v1alpha1.PageTitleAnnotation]
			if title == "" {
				title = p.Name
			}
			sortIdx, _ := strconv.Atoi(p.Annotations[v1alpha1.PageSortIdxAnnotation])
			page := exportPage{Name: p.Name, Title: title, SortIdx: sortIdx, Widgets: []exportWidget{}}
			for _, w := range p.Spec.Widgets {
				page.Widgets = append(page.Widgets, exportWidget{
					Port: w.Port, Name: w.Name,
					GridX: w.GridX, GridY: w.GridY, GridW: w.GridW, GridH: w.GridH,
					SchemaPatch: json.RawMessage(w.SchemaPatch),
				})
			}
			export.Pages = append(export.Pages, page)
		}
	}
	if export.Pages == nil {
		export.Pages = []exportPage{}
	}

	// scenarios
	scenarioList := &v1alpha1.TinyScenarioList{}
	if err := k.Client.List(ctx, scenarioList,
		client.InNamespace(k.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName}); err == nil {
		for _, s := range scenarioList.Items {
			name := s.Annotations[v1alpha1.ScenarioNameAnnotation]
			if name == "" {
				name = s.Name
			}
			export.Scenarios = append(export.Scenarios, exportScenario{Name: name, Ports: s.Spec.Ports})
		}
	}

	if export.Elements == nil {
		export.Elements = []map[string]interface{}{}
	}
	return export, nil
}

// validateSolutionForPublish enforces the publish contract: a solution must
// ship at least one scenario with sample data, and every edge must validate
// against those samples — the same simulation a fresh install's editor runs
// on its canvas. Without the gate a solution can publish with a graph that
// paints red badges the moment someone installs it; the author is the only
// person who can produce the passing samples, so publishing is where the
// requirement bites.
func validateSolutionForPublish(ctx context.Context, k *kube.Client, projectName string, scenarios []exportScenario) error {
	scenarioData := map[string][]byte{}
	for _, sc := range scenarios {
		for _, p := range sc.Ports {
			if len(p.Data) == 0 {
				continue
			}
			if _, seen := scenarioData[p.Port]; !seen {
				scenarioData[p.Port] = p.Data
			}
		}
	}
	if len(scenarioData) == 0 {
		return fmt.Errorf(`project %q has no scenarios — a solution must ship verification samples so a fresh install validates green.
  Run the flow once (send_signal), then pin the passing trace as a scenario:
    scenarios(action=create, trace_id=<id from get_traces>)
  and publish again`, projectName)
	}

	nodeList := &v1alpha1.TinyNodeList{}
	if err := k.Client.List(ctx, nodeList,
		client.InNamespace(k.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName}); err != nil {
		return fmt.Errorf("list nodes for validation: %w", err)
	}
	nodesMap := make(map[string]v1alpha1.TinyNode, len(nodeList.Items))
	for _, n := range nodeList.Items {
		nodesMap[n.Name] = n
	}

	var problems []string
	for _, node := range nodesMap {
		for _, edge := range node.Spec.Edges {
			from := sdkutils.GetPortFullName(node.Name, edge.Port)
			targetNodeName, targetPort := sdkutils.ParseFullPortName(edge.To)
			var edgeConfiguration []byte
			if target, ok := nodesMap[targetNodeName]; ok {
				for _, pc := range target.Spec.Ports {
					if pc.From == from && pc.Port == targetPort {
						edgeConfiguration = pc.Configuration
						break
					}
				}
			}
			err := sdkutils.ValidateEdgeWithRuntimeData(ctx, nodesMap, from, edge.To, edgeConfiguration, scenarioData)
			if err != nil && !sdkutils.IsUnverifiable(err) {
				problems = append(problems, fmt.Sprintf("%s -> %s: %v", from, edge.To, err))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf(`solution does not simulate green against its scenarios — %d edge(s) fail:
  %s
Fix the flow (or re-pin scenarios from a passing trace) and publish again`, len(problems), strings.Join(problems, "\n  "))
	}
	return nil
}
