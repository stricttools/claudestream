package claudestream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// supportedMCPProtocolVersions are the MCP protocol versions the SDK MCP
// servers accept in initialize; the client's proposal is echoed when it is
// one of them.
var supportedMCPProtocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// sdkMCPServerVersion is the serverInfo version every SDK MCP server
// reports.
const sdkMCPServerVersion = "1.0.0"

// MCP methods the SDK MCP servers answer.
const (
	mcpMethodInitialize  = "initialize"
	mcpMethodInitialized = "notifications/initialized"
	mcpMethodToolsList   = "tools/list"
	mcpMethodToolsCall   = "tools/call"
)

// mcpResponder holds the session's SDK MCP servers and tracks which of them
// Claude Code has listed the tools of.
type mcpResponder struct {
	// tools maps each server to its tools in declaration order; read-only
	// after construction.
	tools map[string][]Tool

	mu       sync.Mutex
	unlisted map[string]bool
	// allListed is closed once every server's tools were listed.
	allListed chan struct{}
}

func newMCPResponder(tools []Tool) *mcpResponder {
	r := &mcpResponder{
		tools:     map[string][]Tool{},
		unlisted:  map[string]bool{},
		allListed: make(chan struct{}),
	}
	for _, t := range tools {
		r.tools[t.Server] = append(r.tools[t.Server], t)
		r.unlisted[t.Server] = true
	}
	if len(r.unlisted) == 0 {
		close(r.allListed)
	}
	return r
}

func (r *mcpResponder) markListed(server string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.unlisted[server] {
		return
	}
	delete(r.unlisted, server)
	if len(r.unlisted) == 0 {
		close(r.allListed)
	}
}

// mcpInitializeResult is the result of initialize.
type mcpInitializeResult struct {
	ProtocolVersion string            `json:"protocolVersion"`
	Capabilities    mcpCapabilities   `json:"capabilities"`
	ServerInfo      mcpImplementation `json:"serverInfo"`
}

type mcpCapabilities struct {
	Tools map[string]any `json:"tools"`
}

type mcpImplementation struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// mcpToolsListResult is the result of tools/list.
type mcpToolsListResult struct {
	Tools []mcpToolListing `json:"tools"`
}

// mcpToolListing is one tool in tools/list, always with
// _meta.anthropic.alwaysLoad set to true.
type mcpToolListing struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Meta        mcpToolMeta    `json:"_meta"`
}

type mcpToolMeta struct {
	Anthropic mcpAnthropicMeta `json:"anthropic"`
}

type mcpAnthropicMeta struct {
	AlwaysLoad bool `json:"alwaysLoad"`
}

// mcpToolCallResult is the result of tools/call:
// {"content":[{"type":"text","text":T}]} with "isError":true on a handler
// error.
type mcpToolCallResult struct {
	Content []mcpTextContent `json:"content"`
	IsError bool             `json:"isError,omitempty"`
}

type mcpTextContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func newMCPToolCallResult(text string, isError bool) mcpToolCallResult {
	return mcpToolCallResult{Content: []mcpTextContent{{Type: "text", Text: text}}, IsError: isError}
}

// handleMCPMessage answers an mcp_message request for one of the SDK MCP
// servers. tools/call runs the handler in its own goroutine; everything else
// is answered here.
func (s *Session) handleMCPMessage(req controlRequest) {
	msg, err := req.mcpMessage()
	if err != nil {
		s.refuseUndecodable(req, err)
		return
	}
	rpc := msg.Message
	tools, known := s.mcp.tools[msg.ServerName]
	if !known {
		s.refuseMCP(req, newJSONRPCError(rpc.ID, jsonRPCMethodNotFound,
			fmt.Sprintf("claudestream serves no SDK MCP server named %q", msg.ServerName)))
		return
	}

	switch rpc.Method {
	case mcpMethodInitialize:
		s.answerMCP(req, mcpInitialize(msg), nil)
	case mcpMethodInitialized:
		// A notification has no ID, but the control request still needs an
		// answer; it carries ID 0, as Claude Code accepts.
		id := rpc.ID
		if isJSONNull(id) {
			id = json.RawMessage("0")
		}
		s.answerMCP(req, newJSONRPCResult(id, map[string]any{}), nil)
	case mcpMethodToolsList:
		listings := make([]mcpToolListing, 0, len(tools))
		for _, t := range tools {
			listings = append(listings, mcpToolListing{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.InputSchema,
				Meta:        mcpToolMeta{Anthropic: mcpAnthropicMeta{AlwaysLoad: true}},
			})
		}
		s.answerMCP(req, newJSONRPCResult(rpc.ID, mcpToolsListResult{Tools: listings}), func() {
			s.mcp.markListed(msg.ServerName)
		})
	case mcpMethodToolsCall:
		s.startToolCall(req, msg, tools)
	default:
		s.refuseMCP(req, newJSONRPCError(rpc.ID, jsonRPCMethodNotFound,
			fmt.Sprintf("SDK MCP server %q does not handle method %q", msg.ServerName, rpc.Method)))
	}
}

// mcpInitialize echoes the client's protocol version when it is supported;
// any other version is invalid params.
func mcpInitialize(msg mcpMessage) jsonRPCResponse {
	rpc := msg.Message
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if !isJSONNull(rpc.Params) {
		if err := json.Unmarshal(rpc.Params, &params); err != nil {
			return newJSONRPCError(rpc.ID, jsonRPCInvalidParams, fmt.Sprintf("invalid initialize params: %v", err))
		}
	}
	if !slices.Contains(supportedMCPProtocolVersions, params.ProtocolVersion) {
		return newJSONRPCError(rpc.ID, jsonRPCInvalidParams, fmt.Sprintf(
			"unsupported MCP protocol version %q; supported: %s",
			params.ProtocolVersion, strings.Join(supportedMCPProtocolVersions, ", ")))
	}
	return newJSONRPCResult(rpc.ID, mcpInitializeResult{
		ProtocolVersion: params.ProtocolVersion,
		Capabilities:    mcpCapabilities{Tools: map[string]any{}},
		ServerInfo:      mcpImplementation{Name: msg.ServerName, Version: sdkMCPServerVersion},
	})
}

// startToolCall validates a tools/call and runs the tool's handler in its
// own goroutine, which queues the SDKToolCall and then hands the answer to
// the stdin writer.
// The event is queued first so it precedes the turn's Result, which Claude
// Code can only send after it has the answer.
func (s *Session) startToolCall(req controlRequest, msg mcpMessage, tools []Tool) {
	rpc := msg.Message
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if isJSONNull(rpc.Params) {
		s.answerMCP(req, newJSONRPCError(rpc.ID, jsonRPCInvalidParams, "tools/call without params"), nil)
		return
	}
	if err := json.Unmarshal(rpc.Params, &params); err != nil {
		s.answerMCP(req, newJSONRPCError(rpc.ID, jsonRPCInvalidParams, fmt.Sprintf("invalid tools/call params: %v", err)), nil)
		return
	}
	i := slices.IndexFunc(tools, func(t Tool) bool { return t.Name == params.Name })
	if i < 0 {
		s.answerMCP(req, newJSONRPCError(rpc.ID, jsonRPCInvalidParams,
			fmt.Sprintf("SDK MCP server %q has no tool %q", msg.ServerName, params.Name)), nil)
		return
	}
	tool := tools[i]
	arguments := params.Arguments
	if isJSONNull(arguments) {
		arguments = json.RawMessage("{}")
	}

	s.handlers.Add(1)
	go func() {
		defer s.handlers.Done()
		output, handlerErr := tool.Handler(s.lifeCtx, arguments)
		call := SDKToolCall{
			EventHeader: req.header,
			Server:      msg.ServerName,
			Tool:        tool.Name,
			Arguments:   arguments,
			Output:      output,
		}
		if handlerErr != nil {
			call.IsError = true
			call.Output = handlerErr.Error()
		}
		s.queue.push(queueItem{event: call})
		s.answerMCP(req, newJSONRPCResult(rpc.ID, newMCPToolCallResult(call.Output, call.IsError)), nil)
	}()
}

// answerMCP hands a JSON-RPC response to an mcp_message request to the
// stdin writer without waiting; written, when set, runs once it is out, and
// a failure is queued for the turn.
func (s *Session) answerMCP(req controlRequest, response jsonRPCResponse, written func()) {
	s.enqueueAnswer(req, newMCPResponse(req.requestID, response), written)
}

// refuseMCP answers an mcp_message the SDK MCP servers do not handle with a
// JSON-RPC error and queues an UnknownControlRequest.
func (s *Session) refuseMCP(req controlRequest, response jsonRPCResponse) {
	s.queue.push(queueItem{event: req.unknownControlRequest()})
	s.answerMCP(req, response, nil)
}

// isJSONNull reports whether raw is absent or JSON null.
func isJSONNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}
