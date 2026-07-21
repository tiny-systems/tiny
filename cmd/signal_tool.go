package cmd

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// sendSignalTool fires a payload into a node's port over NATS — the way to
// TRIGGER / RUN a flow. The SDK ships no such tool (send_signal is platform-
// side), yet the core instructions tell the model to use it; without this an
// agent builds a flow and then has no way to start it. The bundle already wires
// a NATS-backed SignalSender, so this just exposes it.
type sendSignalTool struct{}

func (sendSignalTool) Name() string { return "send_signal" }

func (sendSignalTool) Description() string {
	return "Trigger a flow by firing a signal into a node's port. The usual case: a Signal component's _control port with data {\"send\": true} to START the flow it feeds (add a \"context\" object to pass data). This is how you RUN a flow after building it — there is no other way to kick it off. Returns once the signal is published; check get_traces afterward to confirm it ran."
}

func (sendSignalTool) Schema() map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"node_id": map[string]interface{}{
				"type":        "string",
				"description": "Full node id to signal, e.g. <flow>.common-module-v0.signal-xxxx (from read_project).",
			},
			"port": map[string]interface{}{
				"type":        "string",
				"description": "Port to deliver on. Default _control (the dashboard/control port a Signal's Send button writes).",
			},
			"data": map[string]interface{}{
				"type":        "object",
				"description": "Payload. Default {\"send\": true} — the 'press Send' case. Use {\"send\": true, \"context\": {...}} to fire with data.",
			},
		},
		"required": []string{"node_id"},
	}
}

func (sendSignalTool) Execute(ctx context.Context, execCtx sdktools.ExecutionContext, input map[string]interface{}) sdktools.ToolResult {
	if execCtx.SignalSender == nil {
		return sdktools.ToolResult{Success: false, Error: "signal sender unavailable (is the cluster's NATS reachable from tiny?)"}
	}
	nodeID, _ := input["node_id"].(string)
	if nodeID == "" {
		return sdktools.ToolResult{Success: false, Error: "node_id required"}
	}
	port, _ := input["port"].(string)
	if port == "" {
		port = "_control"
	}
	data, _ := input["data"].(map[string]interface{})
	if len(data) == 0 {
		data = map[string]interface{}{"send": true} // the common "start it" case
	}
	payload, err := json.Marshal(data)
	if err != nil {
		return sdktools.ToolResult{Success: false, Error: fmt.Sprintf("marshal data: %v", err)}
	}
	if err := execCtx.SignalSender.SendSignal(ctx, execCtx.ProjectName, nodeID, port, payload, ""); err != nil {
		return sdktools.ToolResult{Success: false, Error: err.Error()}
	}
	return sdktools.ToolResult{Success: true, Output: map[string]interface{}{
		"sent": true, "node_id": nodeID, "port": port,
	}}
}
