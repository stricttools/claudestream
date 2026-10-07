package claudestream

import "encoding/json"

// Outgoing frames, one JSON object per stdin line. The structs mirror the
// wire forms word for word; the constructors fill in the fixed values.

// Fixed request IDs of the start-up handshake.
const (
	initializeRequestID     = "init_1"
	mcpSetServersRequestID  = "mcp_set_1"
	controlRequestIDPrefix  = "ctrl_"
	jsonRPCVersion          = "2.0"
	frameTypeUser           = "user"
	frameTypeControlRequest = "control_request"
	frameTypeControlResp    = "control_response"
)

// userFrame is a user message:
// {"type":"user","message":{"role":"user","content":S|blocks},"parent_tool_use_id":null,"session_id":ID|""}.
type userFrame struct {
	Type            string      `json:"type"`
	Message         userMessage `json:"message"`
	ParentToolUseID *string     `json:"parent_tool_use_id"`
	SessionID       string      `json:"session_id"`
}

type userMessage struct {
	Role string `json:"role"`
	// Content is a string or a []json.RawMessage of content blocks.
	Content any `json:"content"`
}

func newUserTextFrame(text, sessionID string) userFrame {
	return userFrame{Type: frameTypeUser, Message: userMessage{Role: "user", Content: text}, SessionID: sessionID}
}

func newUserBlocksFrame(blocks []json.RawMessage, sessionID string) userFrame {
	if blocks == nil {
		blocks = []json.RawMessage{}
	}
	return userFrame{Type: frameTypeUser, Message: userMessage{Role: "user", Content: blocks}, SessionID: sessionID}
}

// controlResponseFrame answers a control request Claude Code sent:
// {"type":"control_response","response":{"subtype":"success","request_id":R,"response":{..}}}
// or {"type":"control_response","response":{"subtype":"error","request_id":R,"error":M}}.
type controlResponseFrame struct {
	Type     string              `json:"type"`
	Response controlResponseBody `json:"response"`
}

type controlResponseBody struct {
	Subtype   string `json:"subtype"`
	RequestID string `json:"request_id"`
	Response  any    `json:"response,omitempty"`
	Error     string `json:"error,omitempty"`
}

func newSuccessResponse(requestID string, response any) controlResponseFrame {
	return controlResponseFrame{
		Type:     frameTypeControlResp,
		Response: controlResponseBody{Subtype: "success", RequestID: requestID, Response: response},
	}
}

func newErrorResponse(requestID, message string) controlResponseFrame {
	return controlResponseFrame{
		Type:     frameTypeControlResp,
		Response: controlResponseBody{Subtype: "error", RequestID: requestID, Error: message},
	}
}

// allowBody is {"behavior":"allow","updatedInput":{..}[,"updatedPermissions":[..]]}.
type allowBody struct {
	Behavior     string         `json:"behavior"`
	UpdatedInput map[string]any `json:"updatedInput"`
	// A nil pointer omits the key; a non-nil pointer to an empty slice sends [].
	UpdatedPermissions *[]map[string]any `json:"updatedPermissions,omitempty"`
}

// newAllowResponse allows a permission request. A nil input is sent as {};
// nil permissions omit updatedPermissions.
func newAllowResponse(requestID string, input map[string]any, permissions []map[string]any) controlResponseFrame {
	if input == nil {
		input = map[string]any{}
	}
	body := allowBody{Behavior: "allow", UpdatedInput: input}
	if permissions != nil {
		body.UpdatedPermissions = &permissions
	}
	return newSuccessResponse(requestID, body)
}

// denyBody is {"behavior":"deny","message":M}.
type denyBody struct {
	Behavior string `json:"behavior"`
	Message  string `json:"message"`
}

func newDenyResponse(requestID, message string) controlResponseFrame {
	return newSuccessResponse(requestID, denyBody{Behavior: "deny", Message: message})
}

// dialogCompletedBody is {"behavior":"completed","result":X}.
type dialogCompletedBody struct {
	Behavior string `json:"behavior"`
	Result   any    `json:"result"`
}

func newDialogCompletedResponse(requestID string, result any) controlResponseFrame {
	return newSuccessResponse(requestID, dialogCompletedBody{Behavior: "completed", Result: result})
}

// dialogCancelledBody is {"behavior":"cancelled"}.
type dialogCancelledBody struct {
	Behavior string `json:"behavior"`
}

func newDialogCancelledResponse(requestID string) controlResponseFrame {
	return newSuccessResponse(requestID, dialogCancelledBody{Behavior: "cancelled"})
}

// mcpResponseBody is {"mcp_response":{jsonrpc}}.
type mcpResponseBody struct {
	MCPResponse jsonRPCResponse `json:"mcp_response"`
}

func newMCPResponse(requestID string, response jsonRPCResponse) controlResponseFrame {
	return newSuccessResponse(requestID, mcpResponseBody{MCPResponse: response})
}

// jsonRPCResponse is a JSON-RPC 2.0 response carried inside an MCP reply.
// ID is copied from the request; a nil ID is sent as null.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// JSON-RPC error codes the SDK MCP responder uses.
const (
	jsonRPCMethodNotFound = -32601
	jsonRPCInvalidParams  = -32602
)

func newJSONRPCResult(id json.RawMessage, result any) jsonRPCResponse {
	return jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: id, Result: result}
}

func newJSONRPCError(id json.RawMessage, code int, message string) jsonRPCResponse {
	return jsonRPCResponse{JSONRPC: jsonRPCVersion, ID: id, Error: &jsonRPCError{Code: code, Message: message}}
}

// controlRequestFrame is a control request the session sends. The handshake
// requests carry request_id only inside request; generic control requests
// carry it at both levels.
type controlRequestFrame struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Request   any    `json:"request"`
}

// initializeRequest is
// {"subtype":"initialize","request_id":"init_1","hooks":{},"sdk_mcp_servers":[..][,<supported dialogs>:[..]]}.
type initializeRequest struct {
	Subtype       string         `json:"subtype"`
	RequestID     string         `json:"request_id"`
	Hooks         map[string]any `json:"hooks"`
	SDKMCPServers []string       `json:"sdk_mcp_servers"`
	// A nil pointer omits the key; a non-nil pointer to an empty slice sends [].
	SupportedDialogs *[]string `json:"supportedDialogKinds,omitempty"`
}

// newInitializeRequest builds the initialize handshake. Hooks are always
// sent empty: the session registers none. A nil supportedDialogs omits the
// key.
func newInitializeRequest(sdkMCPServers, supportedDialogs []string) controlRequestFrame {
	if sdkMCPServers == nil {
		sdkMCPServers = []string{}
	}
	req := initializeRequest{
		Subtype:       "initialize",
		RequestID:     initializeRequestID,
		Hooks:         map[string]any{},
		SDKMCPServers: sdkMCPServers,
	}
	if supportedDialogs != nil {
		req.SupportedDialogs = &supportedDialogs
	}
	return controlRequestFrame{Type: frameTypeControlRequest, Request: req}
}

// mcpSetServersRequest is
// {"subtype":"mcp_set_servers","request_id":"mcp_set_1","servers":{name:{"type":"sdk","name":name}}}.
type mcpSetServersRequest struct {
	Subtype   string                     `json:"subtype"`
	RequestID string                     `json:"request_id"`
	Servers   map[string]sdkServerConfig `json:"servers"`
}

type sdkServerConfig struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

func newMCPSetServersRequest(serverNames []string) controlRequestFrame {
	servers := make(map[string]sdkServerConfig, len(serverNames))
	for _, name := range serverNames {
		servers[name] = sdkServerConfig{Type: "sdk", Name: name}
	}
	return controlRequestFrame{
		Type:    frameTypeControlRequest,
		Request: mcpSetServersRequest{Subtype: "mcp_set_servers", RequestID: mcpSetServersRequestID, Servers: servers},
	}
}

// newControlRequest builds a generic control request (interrupt, set_model,
// and so on): request_id at the top level and inside request, with payload's
// keys merged into request. payload must not use the keys subtype or
// request_id.
func newControlRequest(requestID, subtype string, payload map[string]any) controlRequestFrame {
	request := make(map[string]any, len(payload)+2)
	for k, v := range payload {
		request[k] = v
	}
	request["subtype"] = subtype
	request["request_id"] = requestID
	return controlRequestFrame{Type: frameTypeControlRequest, RequestID: requestID, Request: request}
}
