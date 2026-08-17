package flow

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tiny-systems/module/api/v1alpha1"
	platform "github.com/tiny-systems/platform-go"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// The placeholder ID the editor assigns to a freshly-dropped node before the
// backend gives it a real one (see FlowAddComponent). SaveFlow generates the
// real ID; the stream then drops the placeholder.
const placeholderNodeID = "00000000000000000000"

// flowViewportAnnotation stores the editor viewport (x/y/zoom) on the TinyFlow
// so it restores on reload.
const flowViewportAnnotation = "tinysystems.io/editor-viewport"

// graphElement is the subset of a vue-flow element the editor persists. A
// node has type "tinyNode" with position + data{module, component, handles};
// an edge has type "tinyEdge" with source/target handles + data{configuration,
// schema}.
type graphElement struct {
	ID           string                 `json:"id"`
	Type         string                 `json:"type"`
	Source       string                 `json:"source"`
	Target       string                 `json:"target"`
	SourceHandle string                 `json:"sourceHandle"`
	TargetHandle string                 `json:"targetHandle"`
	Position     *graphPos              `json:"position"`
	Data         map[string]interface{} `json:"data"`
}

type graphPos struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

type graphPayload struct {
	Elements []graphElement `json:"elements"`
}

// SaveFlow reconciles the editor's whole-graph save into cluster CRs. The
// editor sends every element of the flow; SaveFlow diffs that against the
// TinyNodes this flow owns and creates / updates / deletes to match.
//
// It is deliberately conservative: it only ever touches nodes labelled for
// THIS flow. Nodes owned by other flows (shared into this one) are left
// untouched even if they appear in the graph, and are never deleted.
func (s *Service) SaveFlow(ctx context.Context, req *platform.SaveFlowRequest) (*platform.SaveFlowResponse, error) {
	kc, err := s.kubeClient()
	if err != nil {
		return nil, err
	}
	return saveFlow(ctx, kc, req)
}

// saveFlow is SaveFlow's body with the cluster client passed in explicitly.
// Resolving the client is the only thing SaveFlow does on its own, and it
// needs a live rest.Config; taking *kube.Client as an argument is what lets
// the cross-layer save semantics be tested against a fake client (matching the
// rest of the package, where helpers like flowGraphJSON take their client).
func saveFlow(ctx context.Context, kc *kube.Client, req *platform.SaveFlowRequest) (*platform.SaveFlowResponse, error) {
	var payload graphPayload
	if err := json.Unmarshal(req.Graph, &payload); err != nil {
		return nil, fmt.Errorf("parse graph: %w", err)
	}

	prefix, err := flowPrefix(ctx, kc, req.FlowName)
	if err != nil {
		return nil, fmt.Errorf("flow prefix: %w", err)
	}

	// Current state: everything in the project, split into "owned by this
	// flow" (candidates for update/delete) and "known elsewhere" (skip).
	all := &v1alpha1.TinyNodeList{}
	if err := kc.Client.List(ctx, all, client.InNamespace(kc.Namespace)); err != nil {
		return nil, fmt.Errorf("list nodes: %w", err)
	}
	ownedByFlow := map[string]v1alpha1.TinyNode{}
	knownNames := map[string]bool{}
	for _, n := range all.Items {
		knownNames[n.Name] = true
		if n.Labels[v1alpha1.FlowNameLabel] == req.FlowName {
			ownedByFlow[n.Name] = n
		}
	}

	// Build desired nodes from the graph. Placeholder/blank IDs become real
	// IDs; edges are attached to their source, edge config to their target.
	desired := map[string]*v1alpha1.TinyNode{}
	idRemap := map[string]string{} // graph id -> final id

	for _, el := range payload.Elements {
		if el.Type == "tinyEdge" || el.Source != "" {
			continue // handled in the edge pass
		}
		module, _ := el.Data["module"].(string)
		component, _ := el.Data["component"].(string)
		if module == "" || component == "" {
			continue // not a real component node
		}

		id := el.ID
		if id == "" || id == placeholderNodeID {
			suffix, err := randHex(5)
			if err != nil {
				return nil, err
			}
			id = fmt.Sprintf("%s.%s.%s-%s", prefix, strings.ReplaceAll(module, "/", "-"), strings.ReplaceAll(component, "_", "-"), suffix)
		}
		idRemap[el.ID] = id

		// Shared node (exists in the project but not owned by this flow):
		// leave it alone entirely.
		if _, owned := ownedByFlow[id]; !owned && knownNames[id] {
			continue
		}

		node := &v1alpha1.TinyNode{
			ObjectMeta: metav1.ObjectMeta{
				Name:      id,
				Namespace: kc.Namespace,
				Labels: map[string]string{
					v1alpha1.FlowNameLabel:    req.FlowName,
					v1alpha1.ProjectNameLabel: req.ProjectName,
				},
				Annotations: map[string]string{},
			},
			Spec: v1alpha1.TinyNodeSpec{Module: module, Component: component},
		}
		if el.Position != nil {
			node.Annotations[v1alpha1.ComponentPosXAnnotation] = fmt.Sprintf("%d", int(el.Position.X))
			node.Annotations[v1alpha1.ComponentPosYAnnotation] = fmt.Sprintf("%d", int(el.Position.Y))
		}
		// The settings dialog's "Shared node" toggle writes the target flows
		// into data.shared_with_flows; persist it to the annotation the render
		// path reads (stream.go). Without this the toggle was dropped on save.
		if shared, ok := el.Data["shared_with_flows"].(string); ok && shared != "" {
			node.Annotations[v1alpha1.SharedWithFlowsAnnotation] = shared
		}
		// "Add to dashboard" is a LABEL on the node — the node is the single
		// source of truth for its widget (delete the node, the widget is gone).
		// The dashboard is derived by listing nodes with this label; there is no
		// separate widget store to keep in sync.
		if dash, _ := el.Data["dashboard"].(string); dash == "true" {
			node.Labels[v1alpha1.DashboardLabel] = "true"
		}
		// Node settings live on the _settings handle in data.handles.
		if cfg, schema, ok := settingsFromHandles(el.Data); ok {
			node.Spec.Ports = append(node.Spec.Ports, v1alpha1.TinyNodePortConfig{
				Port:          v1alpha1.SettingsPort,
				Configuration: cfg,
				Schema:        schema,
				FlowID:        prefix,
			})
		}
		desired[id] = node
	}

	// Edge pass: wire edges onto their source node, edge config onto the
	// target node's ports. An endpoint may be a node shared in from another
	// layer, which this flow does not own — those writes accumulate here and
	// are applied separately.
	foreignEdges := map[string][]v1alpha1.TinyNodeEdge{}
	foreignPorts := map[string][]v1alpha1.TinyNodePortConfig{}

	for _, el := range payload.Elements {
		if el.Source == "" || el.Target == "" {
			continue
		}
		src := remap(idRemap, el.Source)
		tgt := remap(idRemap, el.Target)
		srcNode, srcOK := desired[src]
		tgtNode, tgtOK := desired[tgt]
		// Flows are layers of one picture, so an edge may legitimately cross
		// them: a node shared in from another layer is a valid endpoint. The
		// edge lives on its SOURCE and the configuration on its TARGET, so
		// one of those writes can land on a node this flow does not own —
		// collected here and patched below. Only an edge with an end that
		// exists nowhere in the project is meaningless.
		if (!srcOK && !knownNames[src]) || (!tgtOK && !knownNames[tgt]) {
			continue
		}

		edgeID := fmt.Sprintf("%s_%s-%s_%s", src, el.SourceHandle, tgt, el.TargetHandle)
		edge := v1alpha1.TinyNodeEdge{
			ID:     edgeID,
			Port:   el.SourceHandle,
			To:     fmt.Sprintf("%s:%s", tgt, el.TargetHandle),
			FlowID: prefix,
		}
		if srcOK {
			srcNode.Spec.Edges = append(srcNode.Spec.Edges, edge)
		} else {
			foreignEdges[src] = append(foreignEdges[src], edge)
		}

		cfg, schema := edgeConfig(el.Data)
		// From MUST carry the source node:port — the SDK runner selects a
		// port config by exact (From, Port) match (runner.go getPortConfig),
		// so a config saved without From never applies and the edge's
		// mapping silently evaporates on every editor save.
		portConfig := v1alpha1.TinyNodePortConfig{
			Port:          el.TargetHandle,
			From:          fmt.Sprintf("%s:%s", src, el.SourceHandle),
			Configuration: cfg,
			Schema:        schema,
			FlowID:        prefix,
		}
		if tgtOK {
			tgtNode.Spec.Ports = append(tgtNode.Spec.Ports, portConfig)
		} else {
			foreignPorts[tgt] = append(foreignPorts[tgt], portConfig)
		}
	}

	// Apply: create new, update existing (owned), delete owned-but-gone.
	for id, node := range desired {
		existing, ok := ownedByFlow[id]
		if !ok {
			if err := kc.Client.Create(ctx, node); err != nil {
				return nil, fmt.Errorf("create node %s: %w", id, err)
			}
			continue
		}
		// A node's spec carries what EVERY layer wired onto it, each entry
		// tagged with the flow that created it. This save owns only its own
		// slice; another layer's edges and edge configs are preserved
		// verbatim. Replacing the spec wholesale used to delete them, so
		// saving one flow quietly unwired another.
		for _, e := range existing.Spec.Edges {
			if e.FlowID != "" && e.FlowID != prefix {
				node.Spec.Edges = append(node.Spec.Edges, e)
			}
		}
		for _, pc := range existing.Spec.Ports {
			if pc.FlowID != "" && pc.FlowID != prefix {
				node.Spec.Ports = append(node.Spec.Ports, pc)
			}
		}
		existing.Spec = node.Spec
		if existing.Annotations == nil {
			existing.Annotations = map[string]string{}
		}
		for k, v := range node.Annotations {
			existing.Annotations[k] = v
		}
		// "Shared node" unchecked → the annotation must be REMOVED, not just
		// left. The merge above only copies keys that are set, so an unset
		// shared list would otherwise persist forever.
		if _, set := node.Annotations[v1alpha1.SharedWithFlowsAnnotation]; !set {
			delete(existing.Annotations, v1alpha1.SharedWithFlowsAnnotation)
		}
		// Same for the dashboard label: unchecked must clear it, and the merge
		// only ever adds.
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		if node.Labels[v1alpha1.DashboardLabel] == "true" {
			existing.Labels[v1alpha1.DashboardLabel] = "true"
		} else {
			delete(existing.Labels, v1alpha1.DashboardLabel)
		}
		if err := kc.Client.Update(ctx, &existing); err != nil {
			return nil, fmt.Errorf("update node %s: %w", id, err)
		}
	}

	// Cross-layer writes. A node shared in from another flow can hold this
	// flow's edges (when it is an edge's source) or edge configs (when it is
	// the target), so wiring across layers means writing to a node this flow
	// does not own. Rewrite only the slice tagged with this flow — dropping
	// what the canvas no longer shows, keeping every other layer's — and
	// never touch the node's own settings or labels.
	for i := range all.Items {
		node := all.Items[i]
		if _, owned := ownedByFlow[node.Name]; owned {
			continue
		}
		addEdges, addPorts := foreignEdges[node.Name], foreignPorts[node.Name]

		keptEdges := make([]v1alpha1.TinyNodeEdge, 0, len(node.Spec.Edges))
		dropped := false
		for _, e := range node.Spec.Edges {
			if e.FlowID == prefix {
				dropped = true
				continue
			}
			keptEdges = append(keptEdges, e)
		}
		keptPorts := make([]v1alpha1.TinyNodePortConfig, 0, len(node.Spec.Ports))
		for _, pc := range node.Spec.Ports {
			// From == "" is the node's own settings, which belongs to the
			// node regardless of which layer is being saved.
			if pc.FlowID == prefix && pc.From != "" {
				dropped = true
				continue
			}
			keptPorts = append(keptPorts, pc)
		}
		if !dropped && len(addEdges) == 0 && len(addPorts) == 0 {
			continue
		}
		node.Spec.Edges = append(keptEdges, addEdges...)
		node.Spec.Ports = append(keptPorts, addPorts...)
		if err := kc.Client.Update(ctx, &node); err != nil {
			return nil, fmt.Errorf("update shared node %s: %w", node.Name, err)
		}
	}

	for name, node := range ownedByFlow {
		if _, keep := desired[name]; keep {
			continue
		}
		n := node
		if err := kc.Client.Delete(ctx, &n); err != nil {
			return nil, fmt.Errorf("delete node %s: %w", name, err)
		}
	}

	return &platform.SaveFlowResponse{}, nil
}

// SaveFlowMeta persists the editor viewport (x/y/zoom) as an annotation on the
// TinyFlow so it restores on reload.
func (s *Service) SaveFlowMeta(ctx context.Context, req *platform.SaveFlowMetaRequest) (*platform.Nil, error) {
	kc, err := s.kubeClient()
	if err != nil {
		return nil, err
	}
	if req.Meta == nil {
		return &platform.Nil{}, nil
	}
	metaBytes, err := req.Meta.MarshalJSON()
	if err != nil {
		return nil, err
	}
	flow := &v1alpha1.TinyFlow{}
	if err := kc.Client.Get(ctx, types.NamespacedName{Namespace: kc.Namespace, Name: req.FlowName}, flow); err != nil {
		return nil, err
	}
	if flow.Annotations == nil {
		flow.Annotations = map[string]string{}
	}
	flow.Annotations[flowViewportAnnotation] = string(metaBytes)
	if err := kc.Client.Update(ctx, flow); err != nil {
		return nil, err
	}
	return &platform.Nil{}, nil
}

// ---- helpers ----

// flowPrefix is the first 8 hex chars of the TinyFlow UID — the shared node-ID
// namespace the reconciler and editor both use.
func flowPrefix(ctx context.Context, kc *kube.Client, flowName string) (string, error) {
	flow := &v1alpha1.TinyFlow{}
	if err := kc.Client.Get(ctx, types.NamespacedName{Namespace: kc.Namespace, Name: flowName}, flow); err != nil {
		return "", err
	}
	uid := string(flow.UID)
	if len(uid) >= 8 {
		return strings.ReplaceAll(uid[:8], "-", ""), nil
	}
	return uid, nil
}

func remap(m map[string]string, id string) string {
	if v, ok := m[id]; ok {
		return v
	}
	return id
}

func randHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// settingsFromHandles extracts the _settings handle's configuration + schema
// (JSON bytes) from a node's data.handles array.
func settingsFromHandles(data map[string]interface{}) (cfg, schema []byte, ok bool) {
	handles, _ := data["handles"].([]interface{})
	for _, h := range handles {
		hm, _ := h.(map[string]interface{})
		if hm == nil {
			continue
		}
		if id, _ := hm["id"].(string); id != v1alpha1.SettingsPort {
			continue
		}
		cfg = rawOrMarshal(hm["configuration"])
		schema = rawOrMarshal(hm["schema"])
		return cfg, schema, cfg != nil || schema != nil
	}
	return nil, nil, false
}

// edgeConfig pulls the configuration + schema (JSON bytes) off an edge's data.
func edgeConfig(data map[string]interface{}) (cfg, schema []byte) {
	if data == nil {
		return nil, nil
	}
	return rawOrMarshal(data["configuration"]), rawOrMarshal(data["schema"])
}

// rawOrMarshal returns a JSON string value as-is, or marshals any other value.
// The editor stores config/schema as either a JSON string or an object.
func rawOrMarshal(v interface{}) []byte {
	if v == nil {
		return nil
	}
	if s, ok := v.(string); ok {
		if s == "" {
			return nil
		}
		return []byte(s)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return b
}
