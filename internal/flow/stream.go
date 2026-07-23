package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tiny-systems/ajson"
	"github.com/tiny-systems/module/api/v1alpha1"
	"github.com/tiny-systems/module/pkg/resource"
	"github.com/tiny-systems/module/pkg/schema"
	"github.com/tiny-systems/module/pkg/utils"
	platform "github.com/tiny-systems/platform-go"
	"google.golang.org/grpc"
	"k8s.io/apimachinery/pkg/watch"
)

// GetFlowStream is the render path: it streams the flow's nodes and edges to
// the canvas, then keeps the graph live as the cluster changes.
//
// This is a stripped buildGraphEvents — the hosted platform overlays otel
// stats, redis logs, revision notices, and lock state onto the same stream;
// all of those degrade gracefully, so locally we ship just the graph:
// WatchNodes → SDK graph maps → node/edge upserts (ADDED/MODIFIED), delete
// events for elements that disappear, and a 2s heartbeat. The heavy work —
// schema overlay and edge validation — is the SDK's, identical to the platform.
func (s *Service) GetFlowStream(req *platform.GetFlowStreamRequest, stream grpc.ServerStreamingServer[platform.GetFlowStreamResponse]) error {
	ctx := stream.Context()
	mgr, err := s.manager()
	if err != nil {
		return err
	}

	// sent tracks element IDs already on the canvas so a re-render can emit
	// DELETED for anything that vanished from the cluster.
	sent := map[string]bool{}
	render := func() error {
		events, ids, err := s.buildFlowEvents(ctx, mgr, req)
		if err != nil {
			return err
		}
		for id := range sent {
			if !ids[id] {
				events = append(events, &platform.NodeEvent{ID: id, Type: string(watch.Deleted)})
				delete(sent, id)
			}
		}
		for id := range ids {
			sent[id] = true
		}
		if len(events) == 0 {
			return nil
		}
		return stream.Send(&platform.GetFlowStreamResponse{Events: events})
	}

	if err := render(); err != nil {
		return err
	}

	w, err := mgr.WatchNodes(ctx, req.ProjectName)
	if err != nil {
		// No watch — the snapshot is already on the canvas; just heartbeat.
		return heartbeat(ctx, stream)
	}
	defer w.Stop()

	// Coalesce watch events into at most one render per window. A running flow
	// bumps node status many times a second, and render() rebuilds AND
	// revalidates the whole project graph (buildFlowEvents) every call — doing
	// that per event floods the HTTP/1.1 stream and pins the browser's main
	// thread, which is the freeze that hits the instant a flow starts running.
	// This mirrors the platform's 500ms debounced send (flow/get-stream.go),
	// but coalesces inside this select loop so every stream.Send stays on the
	// one goroutine: the platform's debounce fires its callback on a timer
	// goroutine that can race the heartbeat's Send, and grpc streams reject
	// concurrent Send — a trap not worth copying.
	const coalesceWindow = 500 * time.Millisecond
	coalesce := time.NewTimer(coalesceWindow)
	coalesce.Stop()
	defer coalesce.Stop()
	pending := false

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-w.ResultChan():
			if !ok {
				return heartbeat(ctx, stream)
			}
			// A node changed. The first event of a burst arms the render;
			// later events within the window fold into that one render.
			if !pending {
				pending = true
				coalesce.Reset(coalesceWindow)
			}
		case <-coalesce.C:
			// Re-render once for the whole burst. The FE upserts by element
			// ID, so re-emitting current state is safe.
			pending = false
			if err := render(); err != nil {
				return err
			}
		case <-ticker.C:
			if err := tick(stream); err != nil {
				return err
			}
		}
	}
}

// buildFlowEvents renders the project as canvas events, focused on the active
// flow (layer). A flow is a transparent layer of a project: the active layer's
// nodes are editable; nodes from other layers appear only when explicitly
// shared with this flow, and then only as blocked (dimmed) context; everything
// else is hidden. So we watch the WHOLE project, not one flow in isolation.
// Returns the set of element IDs present (for delete-diffing).
func (s *Service) buildFlowEvents(ctx context.Context, mgr *resource.Manager, req *platform.GetFlowStreamRequest) ([]*platform.NodeEvent, map[string]bool, error) {
	nodes, err := mgr.GetProjectNodes(ctx, req.ProjectName)
	if err != nil {
		return nil, nil, err
	}
	nodesMap := make(map[string]v1alpha1.TinyNode, len(nodes))
	for _, n := range nodes {
		nodesMap[n.Name] = n
	}

	statusPortSchemaMap, portConfigMap, edgeConfigMap, portSchemaMap, _, err := utils.GetFlowMaps(nodesMap)
	if err != nil {
		return nil, nil, err
	}
	portExampleMap := utils.GetPortExampleMap(nodesMap)

	flowName := req.FlowName
	events := make([]*platform.NodeEvent, 0, len(nodesMap)*2)
	ids := make(map[string]bool)

	for name, node := range nodesMap {
		notThisFlow := node.Labels[v1alpha1.FlowNameLabel] != flowName
		sharedWithThisFlow := utils.ContainsStr(flowName, strings.Split(node.Annotations[v1alpha1.SharedWithFlowsAnnotation], ","))
		if notThisFlow && !sharedWithThisFlow {
			continue // a node from another layer, not shared into this one
		}
		blocked := notThisFlow // shared-in nodes are context, not editable here

		nodeAsMap := utils.ApiNodeToMap(node, map[string]interface{}{"blocked": blocked}, false)
		graph, _ := json.Marshal(nodeAsMap)
		events = append(events, &platform.NodeEvent{ID: name, Type: string(watch.Added), Graph: graph})
		ids[name] = true

		for _, edge := range node.Spec.Edges {
			ids[edge.ID] = true
			edgeMap, err := buildEdge(ctx, node, edge, flowName, sharedWithThisFlow, nodesMap, statusPortSchemaMap, portConfigMap, edgeConfigMap, portSchemaMap, portExampleMap)
			if err != nil {
				continue
			}
			graph, _ := json.Marshal(edgeMap)
			events = append(events, &platform.NodeEvent{ID: edge.ID, Type: string(watch.Added), Graph: graph})
		}
	}
	return events, ids, nil
}

// buildEdge resolves an edge's configuration + overlaid schema, validates it
// against the source port (all SDK work — the same functions the platform
// calls), and renders it to the canvas map. Missing target → a broken edge the
// UI draws red.
func buildEdge(
	ctx context.Context,
	node v1alpha1.TinyNode,
	edge v1alpha1.TinyNodeEdge,
	flowName string,
	sharedWithThisFlow bool,
	nodesMap map[string]v1alpha1.TinyNode,
	statusPortSchemaMap map[string][]byte,
	portConfigMap map[string][]v1alpha1.TinyNodePortConfig,
	edgeConfigMap map[string][]utils.Destination,
	portSchemaMap map[string]*ajson.Node,
	portExampleMap map[string][]byte,
) (map[string]interface{}, error) {
	n := node.Name
	targetNodeName, targetPort := utils.ParseFullPortName(edge.To)
	targetNode, ok := nodesMap[targetNodeName]
	if !ok {
		return utils.ApiEdgeToProtoMap(&node, &edge, map[string]interface{}{
			"valid": false,
			"error": fmt.Sprintf("Target node %s does not exist", targetNodeName),
		})
	}

	from := utils.GetPortFullName(n, edge.Port)
	defs := utils.GetConfigurableDefinitions(targetNode, &from)
	defs = utils.MergeConfigurableDefinitions(defs, utils.GetConfigurableDefinitions(node, nil))

	var edgeConfiguration, edgeSchema []byte
	for _, pc := range portConfigMap[edge.To] {
		if pc.From == from && pc.Port == targetPort {
			edgeConfiguration = pc.Configuration
			edgeSchema = statusPortSchemaMap[edge.To]
			if updated, uerr := schema.UpdateWithDefinitions(edgeSchema, defs); uerr == nil {
				edgeSchema = updated
			}
			break
		}
	}

	data := map[string]interface{}{"valid": false}
	// An edge out of a shared-in node into a node that's neither in this layer
	// nor shared into it is context — block it (dimmed, non-editable).
	if sharedWithThisFlow &&
		!utils.ContainsStr(flowName, strings.Split(targetNode.Annotations[v1alpha1.SharedWithFlowsAnnotation], ",")) &&
		targetNode.Labels[v1alpha1.FlowNameLabel] != flowName {
		data["blocked"] = true
	}
	if len(edgeConfiguration) > 0 {
		data["configuration"] = json.RawMessage(edgeConfiguration)
	}
	if len(edgeSchema) > 0 {
		data["schema"] = json.RawMessage(edgeSchema)
	}

	if verr := utils.ValidateEdgeWithPrecomputedMaps(ctx, portSchemaMap, edgeConfigMap, from, edgeConfiguration, edgeSchema, nil, portExampleMap); verr != nil {
		if utils.IsUnverifiable(verr) {
			data["valid"] = true
			data["warning"] = verr.Error()
		} else {
			data["error"] = verr.Error()
		}
	} else {
		data["valid"] = true
	}

	return utils.ApiEdgeToProtoMap(&node, &edge, data)
}

// tick sends a heartbeat so the client knows the stream is alive.
func tick(stream grpc.ServerStreamingServer[platform.GetFlowStreamResponse]) error {
	return stream.Send(&platform.GetFlowStreamResponse{
		Events: []*platform.NodeEvent{{ID: "tick", Type: "TICK"}},
	})
}

// heartbeat runs a tick-only loop until the client disconnects (used when a
// cluster watch isn't available).
func heartbeat(ctx context.Context, stream grpc.ServerStreamingServer[platform.GetFlowStreamResponse]) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := tick(stream); err != nil {
				return err
			}
		}
	}
}

// NOTE: trace-select edge highlighting was removed. It read the selected
// trace's spans synchronously INSIDE the render loop, on every watch event,
// through the one shared otel port-forwarder — so a slow read starved the
// render and wedged the canvas on "Loading" (observed twice). Doing it right
// means the canonical utils.ApplyTraceStatToEdge, fetched ONCE per trace
// selection, off the render critical path — not on every node change.
