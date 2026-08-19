package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"github.com/tiny-systems/module/pkg/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// ProjectReader implements sdktools.ProjectReader by reading TinyFlow and
// TinyNode CRDs labelled with the given project name.
//
// The output shape matches what the platform returns so the same SDK tools
// that render project state (read_project) work identically against a
// local cluster.
type ProjectReader struct {
	kube *kube.Client
}

func NewProjectReader(k *kube.Client) *ProjectReader {
	return &ProjectReader{kube: k}
}

// ReadProjectElements returns all flows and nodes in the given project.
func (p *ProjectReader) ReadProjectElements(ctx context.Context, projectName string) (*sdktools.ProjectElements, error) {
	flows, err := p.listFlows(ctx, projectName)
	if err != nil {
		return nil, wrapCRDError(fmt.Errorf("list flows: %w", err))
	}

	nodes, err := p.listNodes(ctx, projectName)
	if err != nil {
		return nil, wrapCRDError(fmt.Errorf("list nodes: %w", err))
	}

	// An edge is stored in two halves: the wire on the source node, the
	// configuration on the target's matching port entry. Reading it needs
	// both, so index the nodes before walking them.
	byName := make(map[string]*v1alpha1.TinyNode, len(nodes))
	for i := range nodes {
		byName[nodes[i].Name] = &nodes[i]
	}

	elements := make([]map[string]interface{}, 0, len(nodes))
	for i := range nodes {
		elem, err := nodeToElement(&nodes[i])
		if err != nil {
			return nil, fmt.Errorf("convert node %s: %w", nodes[i].Name, err)
		}
		elements = append(elements, elem)
		elements = append(elements, edgesFromNode(&nodes[i], byName)...)
	}

	sort.Slice(elements, func(i, j int) bool {
		idI, _ := elements[i]["id"].(string)
		idJ, _ := elements[j]["id"].(string)
		return idI < idJ
	})

	return &sdktools.ProjectElements{
		Flows:    flows,
		Elements: elements,
	}, nil
}

func (p *ProjectReader) listFlows(ctx context.Context, projectName string) ([]sdktools.FlowInfo, error) {
	list := &v1alpha1.TinyFlowList{}
	err := p.kube.Client.List(ctx, list,
		client.InNamespace(p.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName},
	)
	if err != nil {
		return nil, err
	}

	out := make([]sdktools.FlowInfo, 0, len(list.Items))
	for _, f := range list.Items {
		displayName := f.Annotations[v1alpha1.FlowDescriptionAnnotation]
		if displayName == "" {
			displayName = f.Name
		}
		out = append(out, sdktools.FlowInfo{
			ResourceName: f.Name,
			DisplayName:  displayName,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ResourceName < out[j].ResourceName })
	return out, nil
}

func (p *ProjectReader) listNodes(ctx context.Context, projectName string) ([]v1alpha1.TinyNode, error) {
	list := &v1alpha1.TinyNodeList{}
	err := p.kube.Client.List(ctx, list,
		client.InNamespace(p.kube.Namespace),
		client.MatchingLabels{v1alpha1.ProjectNameLabel: projectName},
	)
	if err != nil {
		return nil, err
	}
	return list.Items, nil
}

// nodeToElement converts a TinyNode CRD to the element-map format expected
// by the SDK read_project tool. Keeps only the fields consumers care about.
func nodeToElement(n *v1alpha1.TinyNode) (map[string]interface{}, error) {
	ports := map[string][]string{"in": nil, "out": nil}
	for _, s := range n.Status.Ports {
		if s.Source {
			ports["out"] = append(ports["out"], s.Name)
			continue
		}
		ports["in"] = append(ports["in"], s.Name)
	}

	settings, err := extractSettings(n)
	if err != nil {
		return nil, err
	}

	data := map[string]interface{}{
		"component":             n.Spec.Component,
		"module":                n.Spec.Module,
		"component_description": n.Status.Component.Info,
		"label":                 n.Annotations[v1alpha1.NodeLabelAnnotation],
		"comment":               n.Annotations[v1alpha1.NodeCommentAnnotation],
		"ports":                 ports,
		"settings":              settings,
		"status":                n.Status.Status,
	}

	return map[string]interface{}{
		"id":         n.Name,
		"type":       "tinyNode",
		"flow":       n.Labels[v1alpha1.FlowNameLabel],
		"flow_title": n.Labels[v1alpha1.FlowNameLabel],
		"data":       data,
	}, nil
}

// extractSettings pulls the _settings port configuration out of node spec
// and unmarshals the stored bytes into a JSON map.
func extractSettings(n *v1alpha1.TinyNode) (map[string]interface{}, error) {
	for _, port := range n.Spec.Ports {
		if port.Port != v1alpha1.SettingsPort || len(port.Configuration) == 0 {
			continue
		}
		out := map[string]interface{}{}
		if err := json.Unmarshal(port.Configuration, &out); err != nil {
			return nil, fmt.Errorf("unmarshal settings: %w", err)
		}
		return out, nil
	}
	return nil, nil
}

// edgesFromNode emits one edge element per TinyNode.Spec.Edges entry.
func edgesFromNode(n *v1alpha1.TinyNode, byName map[string]*v1alpha1.TinyNode) []map[string]interface{} {
	if len(n.Spec.Edges) == 0 {
		return nil
	}

	out := make([]map[string]interface{}, 0, len(n.Spec.Edges))
	for _, e := range n.Spec.Edges {
		target, targetHandle := splitPortName(e.To)

		edgeData := map[string]interface{}{
			"configuration": edgeConfigFromTarget(n, e, target, targetHandle, byName),
		}
		out = append(out, map[string]interface{}{
			"id":           e.ID,
			"type":         "tinyEdge",
			"flow":         n.Labels[v1alpha1.FlowNameLabel],
			"source":       n.Name,
			"sourceHandle": e.Port,
			"target":       target,
			"targetHandle": targetHandle,
			"data":         edgeData,
		})
	}
	return out
}

// edgeConfigFromTarget reads the mapping an edge carries.
//
// It lives on the TARGET node, in the port entry whose From names this
// edge's source — one target port can be fed by several sources, each with
// its own mapping, so both halves of that pair have to match.
//
// This used to return an empty object unconditionally, which made every edge
// in a project read as unconfigured. That is indistinguishable from the
// failure the flow-building guide warns about — an edge that lost its
// configuration silently stops working — so a correct flow looked broken, and
// a genuinely broken one could not be spotted.
func edgeConfigFromTarget(source *v1alpha1.TinyNode, e v1alpha1.TinyNodeEdge, target, targetHandle string, byName map[string]*v1alpha1.TinyNode) map[string]interface{} {
	empty := map[string]interface{}{}

	targetNode := byName[target]
	if targetNode == nil {
		return empty
	}
	from := utils.GetPortFullName(source.Name, e.Port)

	for _, pc := range targetNode.Spec.Ports {
		if pc.From != from || pc.Port != targetHandle || len(pc.Configuration) == 0 {
			continue
		}
		var config map[string]interface{}
		if err := json.Unmarshal(pc.Configuration, &config); err != nil {
			return empty
		}
		return config
	}
	return empty
}

// splitPortName splits "node-id:port" into parts. Missing separator
// yields the full string as node and empty port.
func splitPortName(full string) (node, port string) {
	for i := 0; i < len(full); i++ {
		if full[i] == ':' {
			return full[:i], full[i+1:]
		}
	}
	return full, ""
}

var _ sdktools.ProjectReader = (*ProjectReader)(nil)
