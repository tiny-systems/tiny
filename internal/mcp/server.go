// Package mcp implements a minimal Model Context Protocol server.
//
// The package is transport-agnostic: Server.HandleFrame takes a raw
// JSON-RPC byte slice in and returns the response bytes (or nil for
// notifications) — transports (stdio, HTTP) wrap it with their own
// I/O loop. This split lets the desktop client embed the same MCP
// surface inside a Wails process while the CLI keeps its stdio
// behaviour against Claude Desktop, Cursor, and Claude Code.
//
// Protocol reference:
//   - https://spec.modelcontextprotocol.io
//
// Supported methods:
//   - initialize
//   - notifications/initialized
//   - tools/list
//   - tools/call
//   - ping
package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdktools "github.com/tiny-systems/module/pkg/tools"
)

// protocolVersion is the MCP version advertised in initialize responses.
const protocolVersion = "2024-11-05"

// Server holds the tool registry + execution context and dispatches
// MCP JSON-RPC frames. Stateless across calls (no per-session state)
// — transports can share a single Server across many concurrent
// connections.
type Server struct {
	registry     *sdktools.Registry
	execCtx      sdktools.ExecutionContext
	serverName   string
	serverVer    string
	instructions string

	// OnActivity, if set, is called at the start of each tool call with the
	// tool name; it returns a function invoked when the call finishes, carrying
	// that call's outcome. Lets a host render live activity — a spinner per
	// call, and whether it actually worked. Optional.
	//
	// The outcome is passed because the host cannot infer it: a failing tool
	// still returns normally here, so a host given no result reports every
	// finished call identically. That is how the Activity feed came to paint
	// every completed call red.
	OnActivity func(tool string) (done func(success bool, errMsg string))
}

// NewServer returns a server bound to the given registry + execution
// context. Both are owned by the caller and assumed to live for the
// server's lifetime.
func NewServer(registry *sdktools.Registry, execCtx sdktools.ExecutionContext) *Server {
	return &Server{
		registry:   registry,
		execCtx:    execCtx,
		serverName: "tinysystems",
		serverVer:  "0.1.0",
		// Server-level steer returned on `initialize` so the client/model knows
		// what this is and reaches for these tools (instead of writing code or
		// kubectl) the moment the user asks to build/run something on a cluster.
		instructions: "You are connected to a local Tiny Systems runtime that talks directly to the user's Kubernetes cluster. When the user asks to build, install, or run an agent/flow/endpoint on their cluster, DO IT WITH THESE TOOLS — assemble flows as real workloads — do not write code, YAML, or kubectl yourself. Call get_instructions first for the full workflow (projects, flows, module discovery + install, running + tracing). Modules install decentrally from configured repos: discover installables with list_available_modules, read a candidate with get_module_readme, then install_module.",
	}
}

// SetInstructions overrides the server-level instructions returned on
// `initialize` (the model's first steer about what this server is for).
func (s *Server) SetInstructions(text string) {
	if text != "" {
		s.instructions = text
	}
}

// SetServerInfo overrides the name/version advertised on `initialize`.
// Useful when the same Server is embedded in another binary (desktop
// client) so the MCP client knows it isn't talking to the standalone
// mcp-server CLI.
func (s *Server) SetServerInfo(name, version string) {
	if name != "" {
		s.serverName = name
	}
	if version != "" {
		s.serverVer = version
	}
}

// HandleFrame decodes one JSON-RPC frame, dispatches it, and returns
// the response bytes ready to write back to the client. Returns nil
// when the frame is a notification (no response expected). Returns
// an error only for malformed input the transport should care about
// — JSON-RPC errors are encoded into the returned bytes per spec.
func (s *Server) HandleFrame(ctx context.Context, frame []byte) ([]byte, error) {
	var req rpcRequest
	if err := json.Unmarshal(frame, &req); err != nil {
		return marshalResponse(rpcResponse{
			JSONRPC: "2.0",
			Error:   &rpcError{Code: errParse, Message: "parse error", Data: err.Error()},
		})
	}

	isNotification := len(req.ID) == 0

	switch req.Method {
	case "initialize":
		return marshalResponse(rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: initializeResult{
				ProtocolVersion: protocolVersion,
				Capabilities:    capabilities{Tools: toolsCapability{ListChanged: false}},
				ServerInfo:      serverInfo{Name: s.serverName, Version: s.serverVer},
				Instructions:    s.instructions,
			},
		})

	case "notifications/initialized":
		return nil, nil

	case "ping":
		if isNotification {
			return nil, nil
		}
		return marshalResponse(rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]interface{}{},
		})

	case "tools/list":
		return marshalResponse(rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  toolsListResult{Tools: s.buildToolList()},
		})

	case "tools/call":
		result := s.callTool(ctx, req.Params)
		return marshalResponse(rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		})
	}

	if isNotification {
		return nil, nil
	}
	return marshalResponse(rpcResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Error:   &rpcError{Code: errMethodNotFound, Message: "method not found: " + req.Method},
	})
}

// --- JSON-RPC types ---

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  interface{}     `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// JSON-RPC standard error codes
const (
	errParse          = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInternal       = -32603
)

// --- MCP payloads ---

type initializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	Capabilities    capabilities `json:"capabilities"`
	ServerInfo      serverInfo   `json:"serverInfo"`
	Instructions    string       `json:"instructions,omitempty"`
}

// capabilities advertises what this server supports. The "tools" key
// must always be present (even as an empty object) for MCP clients
// like Claude Code to call tools/list. An omitted "tools" is
// interpreted as "no tool support" and tools are never loaded.
type capabilities struct {
	Tools toolsCapability `json:"tools"`
}

type toolsCapability struct {
	ListChanged bool `json:"listChanged"`
}

type serverInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type toolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
}

type toolsListResult struct {
	Tools []toolDefinition `json:"tools"`
}

type toolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolCallResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// --- tool dispatch ---

func (s *Server) buildToolList() []toolDefinition {
	tools := s.registry.List()
	defs := make([]toolDefinition, 0, len(tools))
	for _, t := range tools {
		schema := t.Schema()
		injectProjectFlowParams(t.Name(), schema)
		defs = append(defs, toolDefinition{
			Name:        t.Name(),
			Description: t.Description(),
			InputSchema: schema,
		})
	}
	return defs
}

// callTool decodes tools/call params and runs the named tool against
// the execution context.
//
// The project/flow parameters are dual-use: they are lifted into
// execCtx so SDK tools that read them from context (add_node,
// add_edge, configure_edge, build_flow, etc.) see them there; and
// they are left in the input map so tools that treat `flow` or
// `project` as their own first-class argument (delete_flow, others
// that might appear later) also see them. Tools that care about
// neither just ignore the extra keys.
func (s *Server) callTool(ctx context.Context, raw json.RawMessage) toolCallResult {
	var params toolCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return errorResult(fmt.Sprintf("invalid params: %v", err))
	}
	if params.Arguments == nil {
		params.Arguments = map[string]interface{}{}
	}

	// Refuse an argument this server never advertised, rather than accepting
	// and discarding it. The schema checked is the one the caller was shown,
	// host-injected parameters included.
	if tool, ok := s.registry.Get(params.Name); ok {
		advertised := tool.Schema()
		injectProjectFlowParams(tool.Name(), advertised)
		if err := checkArguments(tool.Name(), advertised, params.Arguments); err != nil {
			return errorResult(err.Error())
		}
	}

	projectName, _ := params.Arguments["project"].(string)
	flowName, _ := params.Arguments["flow"].(string)

	execCtx := s.execCtx
	// A tiny session is bound to ONE project (`tiny -p <name>`), and the
	// editor only ever shows that project. Taking the caller's name here let
	// a model invent one — the work landed in a project nothing was serving,
	// so the dashboard showed 0 widgets and 0 flows while the nodes sat
	// perfectly healthy under a name the user never chose.
	// A tiny session serves exactly one project, chosen with `tiny -p`, and
	// that choice wins over whatever a caller passes. Substituting it in
	// silence was the problem: a caller asking about project A got an answer
	// about project B with nothing to indicate it, so a flow could be built
	// somewhere its author never intended. The override stands; it now says so.
	var substitutedProject string
	if execCtx.ProjectName == "" {
		execCtx.ProjectName = projectName
	} else if projectName != "" && projectName != execCtx.ProjectName {
		substitutedProject = projectName
		params.Arguments["project"] = execCtx.ProjectName
	}
	execCtx.FlowName = flowName

	var finish func(success bool, errMsg string)
	if s.OnActivity != nil {
		finish = s.OnActivity(params.Name)
	}
	result := s.registry.Execute(ctx, execCtx, params.Name, params.Arguments)
	if finish != nil {
		finish(result.Success, result.Error)
	}

	if substitutedProject != "" {
		result = noteProjectSubstitution(result, substitutedProject, execCtx.ProjectName)
	}

	text, err := marshalToolOutput(result)
	if err != nil {
		return errorResult(fmt.Sprintf("marshal tool output: %v", err))
	}

	return toolCallResult{
		Content: []contentBlock{{Type: "text", Text: text}},
		IsError: !result.Success,
	}
}

// noteProjectSubstitution tells the caller that the project it named was not
// the one used, and that this session cannot be pointed elsewhere. Reported on
// every affected call rather than once, because the caller that needs to hear
// it is the one reading this particular answer.
func noteProjectSubstitution(result sdktools.ToolResult, asked, used string) sdktools.ToolResult {
	note := fmt.Sprintf(
		"you asked for project %q, but this session serves %q and was started with `tiny -p %s` — "+
			"the answer above is about %q. To work on %q, restart tiny with `-p %s`.",
		asked, used, used, used, asked, asked)

	if !result.Success {
		result.Error = result.Error + " (" + note + ")"
		return result
	}
	out, ok := result.Output.(map[string]interface{})
	if !ok {
		// Not a shape we can annotate: say it as the whole answer's context
		// rather than dropping it.
		result.Output = map[string]interface{}{"result": result.Output, "project_note": note}
		return result
	}
	out["project_note"] = note
	result.Output = out
	return result
}

func marshalToolOutput(result sdktools.ToolResult) (string, error) {
	if !result.Success {
		payload := map[string]interface{}{"error": result.Error}
		if result.Output != nil {
			payload["output"] = result.Output
		}
		b, err := json.Marshal(payload)
		return string(b), err
	}
	b, err := json.Marshal(result.Output)
	return string(b), err
}

func errorResult(msg string) toolCallResult {
	return toolCallResult{
		Content: []contentBlock{{Type: "text", Text: fmt.Sprintf(`{"error": %q}`, msg)}},
		IsError: true,
	}
}

func marshalResponse(resp rpcResponse) ([]byte, error) {
	return json.Marshal(resp)
}

// --- schema helpers ---

// injectProjectFlowParams adds project/flow string params to tools
// that operate on flow state. This mirrors the hosted platform's
// MCP handler.
func injectProjectFlowParams(toolName string, schema map[string]interface{}) {
	if !needsProjectFlow(toolName) {
		return
	}

	props, _ := schema["properties"].(map[string]interface{})
	if props == nil {
		props = map[string]interface{}{}
		schema["properties"] = props
	}

	if _, ok := props["project"]; !ok {
		props["project"] = map[string]interface{}{
			"type":        "string",
			"description": "Project resource name. This session is bound to one project (tiny -p <name>) and always uses it — do not invent a name; anything else is ignored.",
		}
	}
	if needsFlow(toolName) {
		if _, ok := props["flow"]; !ok {
			props["flow"] = map[string]interface{}{
				"type":        "string",
				"description": "Flow resource name (TinyFlow CRD)",
			}
		}
	}

	required, _ := schema["required"].([]interface{})
	if !stringInList(required, "project") {
		required = append(required, "project")
	}
	if needsFlow(toolName) && !stringInList(required, "flow") {
		required = append(required, "flow")
	}
	schema["required"] = required
}

func needsProjectFlow(name string) bool {
	switch name {
	case "read_project",
		"create_flow", "delete_flow",
		"edit_flow",
		"build_flow", "clone_solution",
		"get_node_port_schema",
		"send_signal",
		"get_traces", "get_trace_detail",
		"scenarios":
		return true
	}
	return false
}

func needsFlow(name string) bool {
	switch name {
	// clone_solution is deliberately absent: a solution is a whole
	// project (many flows) — the clone creates its own flows.
	case "delete_flow",
		"edit_flow",
		"build_flow", "get_traces":
		return true
	}
	return false
}

func stringInList(list []interface{}, s string) bool {
	for _, item := range list {
		if v, ok := item.(string); ok && v == s {
			return true
		}
	}
	return false
}
