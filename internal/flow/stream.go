package flow

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
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
			// A node in the project changed — re-render this flow. The FE
			// upserts by element ID, so re-emitting current state is safe.
			_ = render()
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

	// When a trace is selected, mark the edges that actually carried data in it
	// so the canvas can draw the execution path. Without this the editor asks
	// for a TraceID, gets an unannotated graph back, and nothing highlights.
	traceEdges := s.traceEdgeMap(ctx, req.ProjectName, req.TraceID)

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
			edgeMap, err := buildEdge(ctx, node, edge, flowName, sharedWithThisFlow, nodesMap, statusPortSchemaMap, portConfigMap, edgeConfigMap, portSchemaMap, portExampleMap, traceEdges)
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
	traceEdges map[string]edgeTrace,
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
	if t, ok := traceEdges[from+"->"+edge.To]; ok {
		data["trace"] = t
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

// edgeTrace is one edge's participation in the selected trace: where the hop
// fell in execution order, and how long it took. The canvas reads both —
// sequence as the step number, latency as the label on the highlighted edge.
type edgeTrace struct {
	Sequence int     `json:"sequence"`
	Latency  float64 `json:"latency"`
}

// traceEdgeMap keys a trace's spans by "<from>-><to>" so buildEdge can mark the
// edges that carried data. Spans are ordered by start time, which is the order
// the flow actually executed in.
//
// Best-effort: no trace reader, no trace selected, or an unreadable trace all
// yield nil, and the graph renders unannotated exactly as before.
func (s *Service) traceEdgeMap(ctx context.Context, projectName, traceID string) map[string]edgeTrace {
	if traceID == "" || s.trace == nil {
		return nil
	}
	// Bounded: this runs inside the flow-render path and shares one
	// port-forwarder with everything else that reads traces. A slow collector
	// must degrade to an unannotated graph, never stall the render.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	spans, err := s.trace.ReadTraceSpans(ctx, projectName, traceID)
	if err != nil || len(spans) == 0 {
		return nil
	}
	sort.SliceStable(spans, func(i, j int) bool {
		return spans[i].StartTimeUnixNano < spans[j].StartTimeUnixNano
	})

	out := make(map[string]edgeTrace, len(spans))
	seq := 0
	for _, sp := range spans {
		var from, to string
		for _, a := range sp.Attributes {
			switch a.Key {
			case "from":
				from = a.Value
			case "to":
				to = a.Value
			}
		}
		if from == "" || to == "" {
			continue // an entry span carries no edge — nothing to highlight
		}
		seq++
		out[from+"->"+to] = edgeTrace{
			Sequence: seq,
			Latency:  float64(sp.EndTimeUnixNano-sp.StartTimeUnixNano) / 1e6,
		}
	}
	return out
}
