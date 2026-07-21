package adapters

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tiny-systems/module/api/v1alpha1"
	sdktools "github.com/tiny-systems/module/pkg/tools"
	"github.com/tiny-systems/module/pkg/utils"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/tiny-systems/tiny/internal/kube"
)

// NodeEditor implements the five SDK interfaces that mutate flow state:
// NodeAdder, EdgeAdder, EdgeConfigurer, NodeSettingsConfigurer, and
// FlowModifier. Each operation translates directly to a CRD CRUD call.
//
// The v0.1.0 implementation is deliberately minimal: it handles the
// common happy path (create a node, wire an edge, set configuration)
// without the platform's richer validation (cluster-wide conflict
// detection, schema simulation, batch optimization). Error responses
// surface Kubernetes errors as-is.
type NodeEditor struct {
	kube     *kube.Client
	scenario *scenarioLookup
}

func NewNodeEditor(k *kube.Client) *NodeEditor {
	return &NodeEditor{
		kube:     k,
		scenario: &scenarioLookup{kube: k},
	}
}

// ---------- NodeAdder ----------

// AddNode creates a TinyNode CRD in the target namespace, tagged with the
// flow and project labels so the reconciler picks it up.
func (e *NodeEditor) AddNode(ctx context.Context, projectName, flowName, component, moduleName string, tracker sdktools.PositionTracker) (*sdktools.AddNodeResult, error) {
	if projectName == "" || flowName == "" {
		return nil, fmt.Errorf("project and flow required")
	}
	if component == "" || moduleName == "" {
		return nil, fmt.Errorf("component and module required")
	}

	flowPrefix, err := e.flowPrefix(ctx, flowName)
	if err != nil {
		return nil, fmt.Errorf("compute flow prefix: %w", err)
	}

	suffix, err := randSuffix(5)
	if err != nil {
		return nil, err
	}
	// Kubernetes resource names are DNS-1123 subdomains: no underscores.
	// Component names in this codebase routinely contain them
	// (http_server, slack_send, json_decode, pod_create, ...), so the
	// slug used in the TinyNode's metadata.name replaces '_' with '-'.
	// The spec.component field keeps the original form so the module
	// operator can still look the component up in its registry.
	nodeID := fmt.Sprintf("%s.%s.%s-%s", flowPrefix, moduleSlug(moduleName), componentSlug(component), suffix)

	posX, posY := nextPosition(flowName, tracker)

	node := &v1alpha1.TinyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeID,
			Namespace: e.kube.Namespace,
			Labels: map[string]string{
				v1alpha1.FlowNameLabel:    flowName,
				v1alpha1.ProjectNameLabel: projectName,
			},
			Annotations: map[string]string{
				v1alpha1.ComponentPosXAnnotation: fmt.Sprintf("%d", posX),
				v1alpha1.ComponentPosYAnnotation: fmt.Sprintf("%d", posY),
			},
		},
		Spec: v1alpha1.TinyNodeSpec{
			Module:    moduleName,
			Component: component,
		},
	}
	if err := e.kube.Client.Create(ctx, node); err != nil {
		return nil, wrapCRDError(fmt.Errorf("create TinyNode: %w", err))
	}

	if tracker != nil {
		tracker.RecordPosition(flowName, nodeID, posX, posY)
	}

	return &sdktools.AddNodeResult{
		NodeID: nodeID,
		Ports:  nil, // ports appear in Status after reconciliation
		PosX:   posX,
		PosY:   posY,
	}, nil
}

// ---------- EdgeAdder ----------

// AddEdge appends a TinyNodeEdge to the source node's Spec.Edges. The edge
// ID follows the platform convention "<fromNode>_<fromPort>-<toNode>_<toPort>".
func (e *NodeEditor) AddEdge(ctx context.Context, projectName, flowName, fromNode, fromPort, toNode, toPort string) (*sdktools.AddEdgeResult, error) {
	if fromNode == "" || fromPort == "" || toNode == "" || toPort == "" {
		return nil, fmt.Errorf("all edge endpoints required")
	}

	flowPrefix, err := e.flowPrefix(ctx, flowName)
	if err != nil {
		return nil, fmt.Errorf("compute flow prefix: %w", err)
	}

	edgeID := fmt.Sprintf("%s_%s-%s_%s", fromNode, fromPort, toNode, toPort)
	target := fmt.Sprintf("%s:%s", toNode, toPort)

	err = e.patchNode(ctx, fromNode, func(source *v1alpha1.TinyNode) error {
		for _, existing := range source.Spec.Edges {
			if existing.ID == edgeID {
				return nil // already present — idempotent
			}
		}
		source.Spec.Edges = append(source.Spec.Edges, v1alpha1.TinyNodeEdge{
			ID:     edgeID,
			Port:   fromPort,
			To:     target,
			FlowID: flowPrefix,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &sdktools.AddEdgeResult{
		EdgeID:             edgeID,
		NeedsConfiguration: true,
	}, nil
}

// ---------- EdgeConfigurer ----------

// ConfigureEdge stores the edge configuration on the TARGET node's Ports
// entry. The target node is identified by parsing the edge ID, which
// contains both endpoints.
//
// Before persisting, every {{expression}} in the config is checked
// against the SOURCE port's schema. If an expression references a
// JSONPath that does not exist in the source schema, the edge is
// rejected with a hint listing the real field names — so the LLM has
// a concrete feedback loop for self-correction.
//
// traceID is accepted but ignored in v0.1.0 — trace-based validation
// is a platform-only feature.
func (e *NodeEditor) ConfigureEdge(ctx context.Context, projectName, flowName, edgeID string, config map[string]interface{}, schema map[string]interface{}, traceID string) (*sdktools.ConfigureEdgeResult, error) {
	fromNode, fromPort, toNode, toPort, err := parseEdgeID(edgeID)
	if err != nil {
		return nil, fmt.Errorf("parse edge id: %w", err)
	}

	// Two-pass validation against the source port.
	//
	// Pass 1: structural, against the port's JSON schema.
	//   Catches typos and wrong assumptions on ports with concrete
	//   shapes (http_server.request, http_request.response, etc).
	//   Wildcard types (bare `any`) are accepted here without further
	//   checking — we have no grounds to reject.
	//
	// Pass 2: scenario fallback.
	//   For ports whose schema accepted everything in pass 1, look up
	//   the flow's active scenario and see if it has sample data for
	//   this source port. If it does, validate each JSONPath against
	//   the sample's actual JSON structure. Gives the caller a useful
	//   error on decoded / js_eval-style outputs when scenarios exist.
	sourceSchemaBytes := e.lookupPortSchema(ctx, fromNode, fromPort)
	walkResult, _ := validateEdgeExpressions(config, sourceSchemaBytes)
	if len(walkResult.Unresolved) > 0 {
		hint := formatIssues(walkResult.Unresolved, walkResult.AvailableFields)
		return &sdktools.ConfigureEdgeResult{
			Valid: false,
			Error: fmt.Sprintf("%d edge configuration expression(s) do not resolve against the source port schema", len(walkResult.Unresolved)),
			Hint:  hint,
		}, nil
	}

	if sample := e.scenario.findPortSample(ctx, projectName, fromNode, fromPort); sample != nil {
		scenarioResult, _ := validateAgainstSample(config, sample)
		if len(scenarioResult.Unresolved) > 0 {
			hint := formatIssues(scenarioResult.Unresolved, scenarioResult.AvailableFields)
			return &sdktools.ConfigureEdgeResult{
				Valid: false,
				Error: fmt.Sprintf("%d edge configuration expression(s) do not resolve against the active scenario sample for this port", len(scenarioResult.Unresolved)),
				Hint:  hint,
			}, nil
		}
	}

	configBytes, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	// Same translation as for node settings — edge schemas are
	// overlays for the target port's configurable fields, and the
	// platform reads them via $defs.
	schemaBytes, err := buildEdgeSchemaBytes(config, schema)
	if err != nil {
		return nil, fmt.Errorf("build edge schema: %w", err)
	}

	// Pass 3: strict JSON-Schema validation against the target port,
	// matching what the platform UI runs to render edge errors. Hard
	// failures (schema-shape mismatch, missing required, wrong type)
	// block persistence — shipping a broken edge config is worse than
	// rejecting at authoring time. Unverifiable failures (sim hit a
	// null leaf at a configurable-any upstream) are persisted with a
	// warning hint — the runtime will likely satisfy the schema with
	// real data, and bailing leaves the edge unconfigured (worse).
	strictErr := e.strictValidateEdge(ctx, flowName, fromNode, fromPort, toNode, toPort, configBytes, schemaBytes)
	if strictErr != nil && !utils.IsUnverifiable(strictErr) {
		return &sdktools.ConfigureEdgeResult{
			Valid: false,
			Error: "edge configuration fails the target port's schema (same check the UI runs)",
			Hint:  strictErr.Error(),
		}, nil
	}

	flowPrefix, err := e.flowPrefix(ctx, flowName)
	if err != nil {
		return nil, fmt.Errorf("compute flow prefix: %w", err)
	}

	err = e.patchNode(ctx, toNode, func(target *v1alpha1.TinyNode) error {
		upsertPortConfig(target, v1alpha1.TinyNodePortConfig{
			From:          fromNode + ":" + fromPort,
			Port:          toPort,
			Configuration: configBytes,
			Schema:        schemaBytes,
			FlowID:        flowPrefix,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	if strictErr != nil {
		// Unverifiable — persisted, but surface the caveat.
		return &sdktools.ConfigureEdgeResult{
			Valid: true,
			Hint:  "warning: " + strictErr.Error(),
		}, nil
	}
	return &sdktools.ConfigureEdgeResult{Valid: true}, nil
}

// lookupPortSchema fetches the named port's schema bytes from the
// node's Status.Ports. Returns nil if the node or port is not found or
// if the schema has not been populated yet by the controller.
func (e *NodeEditor) lookupPortSchema(ctx context.Context, nodeID, portName string) []byte {
	node := &v1alpha1.TinyNode{}
	if err := e.kube.Client.Get(ctx, types.NamespacedName{
		Namespace: e.kube.Namespace,
		Name:      nodeID,
	}, node); err != nil {
		return nil
	}
	for _, p := range node.Status.Ports {
		if p.Name == portName {
			return p.Schema
		}
	}
	return nil
}

// ---------- NodeSettingsConfigurer ----------

// ConfigureNodeSettings writes the settings bytes to the node's
// `_settings` Port entry.
func (e *NodeEditor) ConfigureNodeSettings(ctx context.Context, projectName, flowName, nodeID string, settings map[string]interface{}, schema map[string]interface{}) (*sdktools.ConfigureNodeSettingsResult, error) {
	settingsBytes, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("marshal settings: %w", err)
	}
	// The platform UI overlays configurable definitions via $defs.
	// Translate the simple caller-supplied "{field: schema}" form into
	// the canonical "{$defs: {Capitalized: {configurable, path, ...}}}"
	// shape so the form renders, and so the chain simulator can carry
	// typed values through configurable boundaries.
	schemaBytes, err := buildSettingsSchemaBytes(settings, schema)
	if err != nil {
		return nil, fmt.Errorf("build settings schema: %w", err)
	}

	var targetGeneration int64
	err = e.patchNode(ctx, nodeID, func(node *v1alpha1.TinyNode) error {
		upsertPortConfig(node, v1alpha1.TinyNodePortConfig{
			Port:          v1alpha1.SettingsPort,
			Configuration: settingsBytes,
			Schema:        schemaBytes,
		})
		// Generation auto-bumps on Update; the post-update value is one
		// higher than the fetched value. Record so we can wait for the
		// controller to reconcile and re-publish Status.Ports.
		targetGeneration = node.Generation + 1
		return nil
	})
	if err != nil {
		return nil, err
	}

	statusPorts := e.waitForObservedPorts(ctx, nodeID, targetGeneration)

	return &sdktools.ConfigureNodeSettingsResult{
		Valid: true,
		Ports: statusPorts,
	}, nil
}

// strictValidateEdge runs the SDK's full edge validator (chain-aware
// simulation + JSON-Schema validation against the target port). This is
// the same check the platform UI runs to render red markers on edges,
// so a build_flow that returns valid=true here will also render clean
// in the UI.
//
// Returns nil on success or the underlying validation error. If the
// flow's nodes can't be listed or the target schema isn't published
// yet, returns nil (lenient — we don't fail edges purely because
// kube reads or reconciliation are racing).
func (e *NodeEditor) strictValidateEdge(ctx context.Context, flowName, fromNode, fromPort, toNode, toPort string, configBytes, edgeSchemaBytes []byte) error {
	list := &v1alpha1.TinyNodeList{}
	if err := e.kube.Client.List(ctx, list,
		client.InNamespace(e.kube.Namespace),
		client.MatchingLabels{v1alpha1.FlowNameLabel: flowName},
	); err != nil {
		return nil // lenient on transient kube failures
	}
	nodesMap := make(map[string]v1alpha1.TinyNode, len(list.Items))
	for i := range list.Items {
		nodesMap[list.Items[i].Name] = list.Items[i]
	}

	sourceFull := fromNode + ":" + fromPort
	targetFull := toNode + ":" + toPort

	// If the caller supplied an explicit edge schema, validate the
	// resolved configuration against it (overrides the target port
	// schema). Otherwise validate against the target port's native
	// schema.
	if len(edgeSchemaBytes) > 0 {
		return utils.ValidateEdgeWithSchemaAndRuntimeData(ctx, nodesMap, sourceFull, configBytes, edgeSchemaBytes, nil)
	}
	return utils.ValidateEdgeWithRuntimeData(ctx, nodesMap, sourceFull, targetFull, configBytes, nil)
}

// waitForObservedPorts polls the node's Status until observedGeneration
// catches up to the post-patch generation (so Status.Ports reflects the
// new settings) or the budget runs out. Returns whatever Status.Ports
// holds at the end.
//
// Without this, build_flow returns a router's static ["out_a","out_b"]
// instead of the settings-derived ["out_<route>", "default"], and the
// model is misled into thinking its named routes were dropped.
func (e *NodeEditor) waitForObservedPorts(ctx context.Context, nodeID string, targetGeneration int64) []string {
	const (
		maxWait  = 3 * time.Second
		interval = 150 * time.Millisecond
	)
	deadline := time.Now().Add(maxWait)
	var lastPorts []string
	for {
		node := &v1alpha1.TinyNode{}
		if err := e.kube.Client.Get(ctx, types.NamespacedName{
			Namespace: e.kube.Namespace,
			Name:      nodeID,
		}, node); err == nil {
			lastPorts = lastPorts[:0]
			for _, p := range node.Status.Ports {
				lastPorts = append(lastPorts, p.Name)
			}
			if node.Status.ObservedGeneration >= targetGeneration && len(lastPorts) > 0 {
				return lastPorts
			}
		}
		if time.Now().After(deadline) {
			return lastPorts
		}
		select {
		case <-time.After(interval):
		case <-ctx.Done():
			return lastPorts
		}
	}
}

// patchNode fetches a TinyNode, applies mutate, and writes it back.
// Retries on optimistic-lock conflicts (the module controller can
// reconcile and bump resourceVersion between our get and update).
func (e *NodeEditor) patchNode(ctx context.Context, name string, mutate func(*v1alpha1.TinyNode) error) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node := &v1alpha1.TinyNode{}
		if err := e.kube.Client.Get(ctx, types.NamespacedName{
			Namespace: e.kube.Namespace,
			Name:      name,
		}, node); err != nil {
			return fmt.Errorf("get node %s: %w", name, err)
		}
		if err := mutate(node); err != nil {
			return err
		}
		if err := e.kube.Client.Update(ctx, node); err != nil {
			return err
		}
		return nil
	})
}

// ---------- FlowModifier ----------

// ApplyFlowChanges applies a batch of delete/update operations. Add
// operations are intentionally not supported here — use the dedicated
// AddNode / AddEdge methods instead so callers get typed results.
func (e *NodeEditor) ApplyFlowChanges(ctx context.Context, projectName, flowName string, operations []sdktools.FlowOperation) ([]sdktools.OperationResult, error) {
	results := make([]sdktools.OperationResult, 0, len(operations))
	for _, op := range operations {
		results = append(results, e.applyOne(ctx, flowName, op))
	}
	return results, nil
}

func (e *NodeEditor) applyOne(ctx context.Context, flowName string, op sdktools.FlowOperation) sdktools.OperationResult {
	result := sdktools.OperationResult{Op: op.Op, ID: op.ID, Success: true}

	switch op.Op {
	case "delete":
		if err := e.deleteByID(ctx, flowName, op.ID); err != nil {
			result.Success = false
			result.Error = err.Error()
		}
	default:
		result.Success = false
		result.Error = fmt.Sprintf("operation %q not supported by local MCP server; use add_node/add_edge/configure_edge directly", op.Op)
	}
	return result
}

// deleteByID deletes either a node or an edge. The distinction is made by
// the ID format: node IDs never contain underscore-separated port
// segments, edge IDs do ("node_port-node_port").
func (e *NodeEditor) deleteByID(ctx context.Context, flowName, id string) error {
	if looksLikeEdgeID(id) {
		return e.deleteEdge(ctx, id)
	}
	return e.deleteNode(ctx, flowName, id)
}

func (e *NodeEditor) deleteNode(ctx context.Context, flowName, nodeID string) error {
	node := &v1alpha1.TinyNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:      nodeID,
			Namespace: e.kube.Namespace,
		},
	}
	if err := e.kube.Client.Delete(ctx, node); err != nil && !k8serrors.IsNotFound(err) {
		return fmt.Errorf("delete node: %w", err)
	}
	// Deleting the node's own CRD drops its Ports and Edges, but every edge it
	// took part in also has a half on a sibling node: a target keeps a
	// Ports[].From pointing back here, a source keeps an Edges[].To pointing
	// here. Left behind, those become dangling edges to a node that no longer
	// exists — prune them so the flow stays consistent.
	return e.pruneNodeReferences(ctx, flowName, nodeID)
}

func (e *NodeEditor) deleteEdge(ctx context.Context, edgeID string) error {
	fromNode, fromPort, toNode, toPort, err := parseEdgeID(edgeID)
	if err != nil {
		return fmt.Errorf("parse edge id: %w", err)
	}
	// An edge is recorded on BOTH ends: the source node's Spec.Edges (routing,
	// keyed by edge ID) and the target node's Spec.Ports (receive config, keyed
	// by From). Drop both halves or a dangling reference survives.
	sourceFull := fromNode + ":" + fromPort
	if err := e.updateNode(ctx, fromNode, func(n *v1alpha1.TinyNode) bool {
		return removeEdgeByID(n, edgeID)
	}); err != nil {
		return fmt.Errorf("prune source edge: %w", err)
	}
	if err := e.updateNode(ctx, toNode, func(n *v1alpha1.TinyNode) bool {
		return removePortFrom(n, sourceFull, toPort)
	}); err != nil {
		return fmt.Errorf("prune target port: %w", err)
	}
	return nil
}

// pruneNodeReferences removes every edge half in the flow that points at
// deletedNodeID — Ports[].From on targets and Edges[].To on sources. The full
// node id (with its flowID prefix) is embedded in both, so this can never match
// a node in another flow.
func (e *NodeEditor) pruneNodeReferences(ctx context.Context, flowName, deletedNodeID string) error {
	list := &v1alpha1.TinyNodeList{}
	if err := e.kube.Client.List(ctx, list,
		client.InNamespace(e.kube.Namespace),
		client.MatchingLabels{v1alpha1.FlowNameLabel: flowName},
	); err != nil {
		return fmt.Errorf("list flow nodes: %w", err)
	}
	prefix := deletedNodeID + ":"
	for i := range list.Items {
		name := list.Items[i].Name
		if name == deletedNodeID {
			continue // already deleted
		}
		if err := e.updateNode(ctx, name, func(n *v1alpha1.TinyNode) bool {
			return removeRefs(n, prefix)
		}); err != nil {
			return fmt.Errorf("prune references on %s: %w", name, err)
		}
	}
	return nil
}

// updateNode applies mutate to the named node and writes it back if mutate
// reports a change, retrying on write conflicts. A missing node is a no-op
// (nothing to update). mutate may run more than once, so it must be idempotent.
func (e *NodeEditor) updateNode(ctx context.Context, name string, mutate func(*v1alpha1.TinyNode) bool) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		node := &v1alpha1.TinyNode{}
		if err := e.kube.Client.Get(ctx, types.NamespacedName{
			Namespace: e.kube.Namespace,
			Name:      name,
		}, node); err != nil {
			if k8serrors.IsNotFound(err) {
				return nil
			}
			return err
		}
		if !mutate(node) {
			return nil
		}
		return e.kube.Client.Update(ctx, node)
	})
}

// removeRefs drops every Ports[].From and Edges[].To on n that points at the
// deleted node (prefix "<nodeID>:"). Reports whether anything changed.
func removeRefs(n *v1alpha1.TinyNode, prefix string) bool {
	changed := false
	ports := n.Spec.Ports[:0]
	for _, p := range n.Spec.Ports {
		if strings.HasPrefix(p.From, prefix) {
			changed = true
			continue
		}
		ports = append(ports, p)
	}
	n.Spec.Ports = ports
	edges := n.Spec.Edges[:0]
	for _, ed := range n.Spec.Edges {
		if strings.HasPrefix(ed.To, prefix) {
			changed = true
			continue
		}
		edges = append(edges, ed)
	}
	n.Spec.Edges = edges
	return changed
}

// removeEdgeByID drops the Spec.Edges entry with the given id. Reports whether
// anything changed.
func removeEdgeByID(n *v1alpha1.TinyNode, edgeID string) bool {
	kept := n.Spec.Edges[:0]
	changed := false
	for _, ed := range n.Spec.Edges {
		if ed.ID == edgeID {
			changed = true
			continue
		}
		kept = append(kept, ed)
	}
	n.Spec.Edges = kept
	return changed
}

// removePortFrom drops the Spec.Ports entry fed by sourceFull ("node:port")
// into the given target port. Reports whether anything changed.
func removePortFrom(n *v1alpha1.TinyNode, sourceFull, toPort string) bool {
	kept := n.Spec.Ports[:0]
	changed := false
	for _, p := range n.Spec.Ports {
		if p.From == sourceFull && p.Port == toPort {
			changed = true
			continue
		}
		kept = append(kept, p)
	}
	n.Spec.Ports = kept
	return changed
}

// ---------- helpers ----------

// flowPrefix returns the first 8 chars of the TinyFlow's metadata.UID.
// Falls back to a stable hash-like string derived from the flow name if
// the flow resource cannot be read.
func (e *NodeEditor) flowPrefix(ctx context.Context, flowName string) (string, error) {
	flow := &v1alpha1.TinyFlow{}
	err := e.kube.Client.Get(ctx, types.NamespacedName{
		Namespace: e.kube.Namespace,
		Name:      flowName,
	}, flow)
	if err != nil {
		return "", err
	}
	uid := string(flow.UID)
	if len(uid) >= 8 {
		return strings.ReplaceAll(uid[:8], "-", ""), nil
	}
	return uid, nil
}

// moduleSlug converts a module name like "tinysystems/http-module-v0" to
// a dns-friendly slug like "tinysystems-http-module-v0".
func moduleSlug(moduleName string) string {
	return strings.ReplaceAll(moduleName, "/", "-")
}

// componentSlug converts a component name like "http_server" to a
// dns-friendly form like "http-server" suitable for a Kubernetes
// resource name. The original underscore form is kept in spec.component.
func componentSlug(component string) string {
	return strings.ReplaceAll(component, "_", "-")
}

// randSuffix returns a hex-encoded random suffix of the given length.
func randSuffix(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate suffix: %w", err)
	}
	return hex.EncodeToString(buf)[:n], nil
}

// nextPosition computes the (x, y) for a new node, using the session
// tracker to avoid overlap with nodes already added in this session.
func nextPosition(flowName string, tracker sdktools.PositionTracker) (int, int) {
	const (
		startX      = 100
		columnWidth = 300
	)
	if tracker == nil {
		return startX, 150
	}
	maxX := tracker.GetMaxX(flowName)
	posX := maxX + columnWidth
	if posX == columnWidth {
		posX = startX
	}
	posY := tracker.GetNextY(flowName, posX, columnWidth)
	return posX, posY
}

// parseEdgeID splits "<fromNode>_<fromPort>-<toNode>_<toPort>" into parts.
//
// Node IDs contain dots and hyphens but never underscores (component
// names are slugified), so we can reliably split on underscores first:
//
//   <fromNode> _ <fromPort> - <toNode> _ <toPort>
//
// Step 1: the FIRST underscore separates fromNode from the rest.
// Step 2: in what remains, the FIRST dash separates fromPort (which
//   has no dashes in practice) from the target half.
// Step 3: in the target half, the LAST underscore separates toNode
//   from toPort.
func parseEdgeID(edgeID string) (fromNode, fromPort, toNode, toPort string, err error) {
	firstUnder := strings.Index(edgeID, "_")
	if firstUnder < 0 {
		return "", "", "", "", fmt.Errorf("missing '_' after source node in edge id %q", edgeID)
	}
	fromNode = edgeID[:firstUnder]
	rest := edgeID[firstUnder+1:]

	firstDash := strings.Index(rest, "-")
	if firstDash < 0 {
		return "", "", "", "", fmt.Errorf("missing '-' between halves in edge id %q", edgeID)
	}
	fromPort = rest[:firstDash]
	rest = rest[firstDash+1:]

	lastUnder := strings.LastIndex(rest, "_")
	if lastUnder < 0 {
		return "", "", "", "", fmt.Errorf("missing '_' before target port in edge id %q", edgeID)
	}
	toNode = rest[:lastUnder]
	toPort = rest[lastUnder+1:]

	if fromNode == "" || fromPort == "" || toNode == "" || toPort == "" {
		return "", "", "", "", fmt.Errorf("empty component in edge id %q", edgeID)
	}
	return fromNode, fromPort, toNode, toPort, nil
}

// looksLikeEdgeID detects edge IDs by the "node_port-node_port" shape.
func looksLikeEdgeID(id string) bool {
	return strings.Contains(id, "_") && strings.Contains(id, "-")
}

// upsertPortConfig replaces a matching (From, Port) entry or appends.
func upsertPortConfig(node *v1alpha1.TinyNode, cfg v1alpha1.TinyNodePortConfig) {
	for i := range node.Spec.Ports {
		p := &node.Spec.Ports[i]
		if p.Port == cfg.Port && p.From == cfg.From {
			*p = cfg
			return
		}
	}
	node.Spec.Ports = append(node.Spec.Ports, cfg)
}

// marshalOptional marshals a map to JSON, returning nil bytes if the
// input map is nil or empty.
func marshalOptional(m map[string]interface{}) ([]byte, error) {
	if len(m) == 0 {
		return nil, nil
	}
	return json.Marshal(m)
}

// Unused import guard for controller-runtime client package (needed for
// interface satisfaction but not referenced after inlining).
var _ = client.ObjectKey{}

var (
	_ sdktools.NodeAdder              = (*NodeEditor)(nil)
	_ sdktools.EdgeAdder              = (*NodeEditor)(nil)
	_ sdktools.EdgeConfigurer         = (*NodeEditor)(nil)
	_ sdktools.NodeSettingsConfigurer = (*NodeEditor)(nil)
	_ sdktools.FlowModifier           = (*NodeEditor)(nil)
)
