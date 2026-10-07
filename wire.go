package claudestream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Incoming frames. parseLine turns one stdout line into either a control
// frame for the session to route or a list of flattened events; the
// eventTracker then adds what needs earlier frames (tool names on results,
// file events after successful file tools).
//
// Decoding is lenient about extra fields and strict about the types of the
// fields read: a type mismatch yields an UndecodableEvent rather than a
// half-filled event.

// parsedLine is the outcome of parsing one stdout line. At most one of
// controlRequest, controlResponse, and events is set; all are empty for a
// blank line.
type parsedLine struct {
	controlRequest  *controlRequest
	controlResponse *controlResponse
	events          []Event
	// assistantError is the top-level error field of an assistant frame
	// (for example "authentication_failed"); empty otherwise.
	assistantError string
}

// wireHead is the part of every frame needed to route it.
type wireHead struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	UUID      string `json:"uuid"`
}

func (h wireHead) header() EventHeader {
	return EventHeader{SessionID: h.SessionID, UUID: h.UUID}
}

// parseLine parses one stdout line. It copies the line, so the caller may
// reuse its buffer.
func parseLine(line []byte) parsedLine {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return parsedLine{}
	}
	if !json.Valid(trimmed) {
		return eventsOnly(NonJSONLine{Line: string(trimmed)})
	}
	raw := json.RawMessage(bytes.Clone(trimmed))
	var head wireHead
	if err := json.Unmarshal(raw, &head); err != nil {
		return undecodable(EventHeader{}, raw, err)
	}
	h := head.header()

	switch head.Type {
	case "control_request":
		req, err := decodeControlRequest(raw)
		if err != nil {
			return undecodable(h, raw, err)
		}
		return parsedLine{controlRequest: &req}
	case "control_response":
		resp, err := decodeControlResponse(raw)
		if err != nil {
			return undecodable(h, raw, err)
		}
		return parsedLine{controlResponse: &resp}
	case "system":
		return parseSystem(head, raw)
	case "assistant":
		return parseAssistant(h, raw)
	case "user":
		return parseUser(h, raw)
	case "result":
		return parseResult(h, raw)
	case "stream_event":
		var w struct {
			Event           map[string]any `json:"event"`
			ParentToolUseID *string        `json:"parent_tool_use_id"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			return undecodable(h, raw, err)
		}
		return eventsOnly(StreamDelta{EventHeader: h, Event: w.Event, ParentToolUseID: w.ParentToolUseID})
	case "rate_limit", "rate_limit_event":
		var w struct {
			Info wireRateLimit `json:"rate_limit_info"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			return undecodable(h, raw, err)
		}
		return eventsOnly(w.Info.event(h))
	default:
		return eventsOnly(UnknownEvent{EventHeader: h, Raw: raw})
	}
}

func eventsOnly(events ...Event) parsedLine {
	return parsedLine{events: events}
}

func undecodable(h EventHeader, raw json.RawMessage, err error) parsedLine {
	return eventsOnly(UndecodableEvent{EventHeader: h, Raw: raw, Err: err})
}

// wireRateLimit reads both upstream rate-limit shapes: the fields of a
// system/rate_limit frame and the rate_limit_info object of a
// rate_limit_event frame. Each field is accepted in its snake_case and its
// camelCase spelling; the snake_case one wins when both are present.
type wireRateLimit struct {
	Status             string  `json:"status"`
	ResetsAtSnake      *int64  `json:"resets_at"`
	ResetsAtCamel      *int64  `json:"resetsAt"`
	RateLimitTypeSnake string  `json:"rate_limit_type"`
	RateLimitTypeCamel string  `json:"rateLimitType"`
	Utilization        float64 `json:"utilization"`
}

func (w wireRateLimit) event(h EventHeader) RateLimit {
	resetsAt := w.ResetsAtSnake
	if resetsAt == nil {
		resetsAt = w.ResetsAtCamel
	}
	rateLimitType := w.RateLimitTypeSnake
	if rateLimitType == "" {
		rateLimitType = w.RateLimitTypeCamel
	}
	return RateLimit{EventHeader: h, Status: w.Status, ResetsAt: resetsAt, RateLimitType: rateLimitType, Utilization: w.Utilization}
}

func parseSystem(head wireHead, raw json.RawMessage) parsedLine {
	h := head.header()
	switch head.Subtype {
	case "init":
		var w struct {
			Cwd               string            `json:"cwd"`
			Tools             []string          `json:"tools"`
			MCPServers        []MCPServerStatus `json:"mcp_servers"`
			Model             string            `json:"model"`
			PermissionMode    string            `json:"permissionMode"`
			ClaudeCodeVersion string            `json:"claude_code_version"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			return undecodable(h, raw, err)
		}
		return eventsOnly(SystemInit{
			EventHeader:       h,
			Cwd:               w.Cwd,
			Tools:             w.Tools,
			MCPServers:        w.MCPServers,
			Model:             w.Model,
			PermissionMode:    w.PermissionMode,
			ClaudeCodeVersion: w.ClaudeCodeVersion,
		})
	case "api_retry":
		var w struct {
			Attempt      int     `json:"attempt"`
			MaxRetries   int     `json:"max_retries"`
			RetryDelayMS float64 `json:"retry_delay_ms"`
			ErrorStatus  *int    `json:"error_status"`
			Error        string  `json:"error"`
		}
		if err := json.Unmarshal(raw, &w); err != nil {
			return undecodable(h, raw, err)
		}
		return eventsOnly(APIRetry{
			EventHeader:  h,
			Attempt:      w.Attempt,
			MaxRetries:   w.MaxRetries,
			RetryDelayMS: w.RetryDelayMS,
			ErrorStatus:  w.ErrorStatus,
			Error:        w.Error,
		})
	case "compact_boundary":
		return eventsOnly(CompactBoundary{EventHeader: h})
	case "rate_limit":
		var w wireRateLimit
		if err := json.Unmarshal(raw, &w); err != nil {
			return undecodable(h, raw, err)
		}
		return eventsOnly(w.event(h))
	default:
		return eventsOnly(UnknownEvent{EventHeader: h, Raw: raw})
	}
}

// wireBlockType reads the type of a content block.
type wireBlockType struct {
	Type string `json:"type"`
}

func parseAssistant(h EventHeader, raw json.RawMessage) parsedLine {
	var w struct {
		ParentToolUseID *string `json:"parent_tool_use_id"`
		Error           string  `json:"error"`
		Message         struct {
			Content []json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return undecodable(h, raw, err)
	}
	events := make([]Event, 0, len(w.Message.Content))
	for i, block := range w.Message.Content {
		ev, err := assistantBlockEvent(h, w.ParentToolUseID, block)
		if err != nil {
			return undecodable(h, raw, fmt.Errorf("assistant content block %d: %w", i, err))
		}
		events = append(events, ev)
	}
	return parsedLine{events: events, assistantError: w.Error}
}

func assistantBlockEvent(h EventHeader, parent *string, block json.RawMessage) (Event, error) {
	var bt wireBlockType
	if err := json.Unmarshal(block, &bt); err != nil {
		return nil, err
	}
	switch bt.Type {
	case "text":
		var b struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &b); err != nil {
			return nil, err
		}
		return AssistantText{EventHeader: h, Text: b.Text, ParentToolUseID: parent}, nil
	case "thinking":
		var b struct {
			Thinking string `json:"thinking"`
		}
		if err := json.Unmarshal(block, &b); err != nil {
			return nil, err
		}
		return Thinking{EventHeader: h, Text: b.Thinking, ParentToolUseID: parent}, nil
	case "tool_use":
		var b struct {
			ID    string         `json:"id"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		if err := json.Unmarshal(block, &b); err != nil {
			return nil, err
		}
		return ToolUse{EventHeader: h, ToolUseID: b.ID, Name: b.Name, Input: b.Input, ParentToolUseID: parent}, nil
	default:
		return UnknownContentBlock{EventHeader: h, Raw: block, ParentToolUseID: parent}, nil
	}
}

// parseUser flattens the tool_result blocks of a user frame; every other
// block, and a plain-string content, yields nothing.
func parseUser(h EventHeader, raw json.RawMessage) parsedLine {
	var w struct {
		ParentToolUseID *string `json:"parent_tool_use_id"`
		Message         struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return undecodable(h, raw, err)
	}
	content := bytes.TrimSpace(w.Message.Content)
	if len(content) == 0 || content[0] != '[' {
		return parsedLine{}
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(content, &blocks); err != nil {
		return undecodable(h, raw, err)
	}
	var events []Event
	for i, block := range blocks {
		var bt wireBlockType
		if err := json.Unmarshal(block, &bt); err != nil {
			return undecodable(h, raw, fmt.Errorf("user content block %d: %w", i, err))
		}
		if bt.Type != "tool_result" {
			continue
		}
		var b struct {
			ToolUseID string          `json:"tool_use_id"`
			Content   json.RawMessage `json:"content"`
			IsError   bool            `json:"is_error"`
		}
		if err := json.Unmarshal(block, &b); err != nil {
			return undecodable(h, raw, fmt.Errorf("user content block %d: %w", i, err))
		}
		events = append(events, ToolResult{
			EventHeader:     h,
			ToolUseID:       b.ToolUseID,
			Content:         b.Content,
			ParentToolUseID: w.ParentToolUseID,
			IsError:         b.IsError,
		})
	}
	return parsedLine{events: events}
}

func parseResult(h EventHeader, raw json.RawMessage) parsedLine {
	var w struct {
		Subtype        string                `json:"subtype"`
		IsError        bool                  `json:"is_error"`
		DurationMS     float64               `json:"duration_ms"`
		DurationAPIMS  float64               `json:"duration_api_ms"`
		NumTurns       int                   `json:"num_turns"`
		Result         string                `json:"result"`
		StopReason     string                `json:"stop_reason"`
		TotalCostUSD   float64               `json:"total_cost_usd"`
		Usage          *Usage                `json:"usage"`
		APIErrorStatus *int                  `json:"api_error_status"`
		ModelUsage     map[string]ModelUsage `json:"modelUsage"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return undecodable(h, raw, err)
	}
	modelUsage := w.ModelUsage
	if modelUsage == nil {
		modelUsage = map[string]ModelUsage{}
	}
	return eventsOnly(Result{
		EventHeader:    h,
		Subtype:        w.Subtype,
		IsError:        w.IsError,
		DurationMS:     w.DurationMS,
		DurationAPIMS:  w.DurationAPIMS,
		NumTurns:       w.NumTurns,
		Result:         w.Result,
		StopReason:     w.StopReason,
		TotalCostUSD:   w.TotalCostUSD,
		Usage:          w.Usage,
		APIErrorStatus: w.APIErrorStatus,
		ModelUsage:     modelUsage,
	})
}

// authenticationMarkers are the substrings (compared case-insensitively) of
// an assistant frame's error field that mean the profile cannot
// authenticate.
var authenticationMarkers = []string{"authentication_failed", "not logged in", "invalid authentication", "401"}

// isAuthenticationError reports whether an assistant frame's error field
// means the profile cannot authenticate.
func isAuthenticationError(assistantError string) bool {
	lower := strings.ToLower(assistantError)
	if lower == "" {
		return false
	}
	for _, marker := range authenticationMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// Control requests Claude Code sends.

// Control request subtypes the session handles.
const (
	controlSubtypeCanUseTool        = "can_use_tool"
	controlSubtypeRequestUserDialog = "request_user_dialog"
	controlSubtypeMCPMessage        = "mcp_message"
)

// controlRequest is a control_request frame:
// {"type":"control_request","request_id":R,"request":{"subtype":S,...}},
// with request_id at the top level or inside request.
type controlRequest struct {
	header    EventHeader
	requestID string
	subtype   string
	// body is the inner request object.
	body json.RawMessage
	raw  json.RawMessage
}

func decodeControlRequest(raw json.RawMessage) (controlRequest, error) {
	var w struct {
		SessionID string          `json:"session_id"`
		UUID      string          `json:"uuid"`
		RequestID string          `json:"request_id"`
		Request   json.RawMessage `json:"request"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return controlRequest{}, err
	}
	var inner struct {
		Subtype   string `json:"subtype"`
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(w.Request, &inner); err != nil {
		return controlRequest{}, fmt.Errorf("control_request request object: %w", err)
	}
	id := w.RequestID
	if id == "" {
		id = inner.RequestID
	}
	if id == "" {
		return controlRequest{}, errors.New("control_request carries no request_id at the top level or inside request")
	}
	return controlRequest{
		header:    EventHeader{SessionID: w.SessionID, UUID: w.UUID},
		requestID: id,
		subtype:   inner.Subtype,
		body:      w.Request,
		raw:       raw,
	}, nil
}

// permissionRequest decodes a can_use_tool request.
func (r controlRequest) permissionRequest() (PermissionRequest, error) {
	var w struct {
		ToolName                string           `json:"tool_name"`
		Input                   map[string]any   `json:"input"`
		DecisionReason          string           `json:"decision_reason"`
		ToolUseID               string           `json:"tool_use_id"`
		PermissionSuggestions   []map[string]any `json:"permission_suggestions"`
		Title                   string           `json:"title"`
		DisplayName             string           `json:"display_name"`
		Description             string           `json:"description"`
		DecisionReasonType      string           `json:"decision_reason_type"`
		RequiresUserInteraction bool             `json:"requires_user_interaction"`
	}
	if err := json.Unmarshal(r.body, &w); err != nil {
		return PermissionRequest{}, fmt.Errorf("can_use_tool request: %w", err)
	}
	return PermissionRequest{
		EventHeader:             r.header,
		RequestID:               r.requestID,
		ToolName:                w.ToolName,
		ToolInput:               w.Input,
		DecisionReason:          w.DecisionReason,
		ToolUseID:               w.ToolUseID,
		PermissionSuggestions:   w.PermissionSuggestions,
		Title:                   w.Title,
		DisplayName:             w.DisplayName,
		Description:             w.Description,
		DecisionReasonType:      w.DecisionReasonType,
		RequiresUserInteraction: w.RequiresUserInteraction,
	}, nil
}

// userDialogRequest decodes a request_user_dialog request.
func (r controlRequest) userDialogRequest() (UserDialogRequest, error) {
	var w struct {
		Dialog    string         `json:"dialog_kind"`
		Payload   map[string]any `json:"payload"`
		ToolUseID *string        `json:"tool_use_id"`
	}
	if err := json.Unmarshal(r.body, &w); err != nil {
		return UserDialogRequest{}, fmt.Errorf("request_user_dialog request: %w", err)
	}
	return UserDialogRequest{
		EventHeader: r.header,
		RequestID:   r.requestID,
		Dialog:      w.Dialog,
		Payload:     w.Payload,
		ToolUseID:   w.ToolUseID,
	}, nil
}

// mcpMessage is the body of an mcp_message request: a JSON-RPC message for
// one of the session's SDK MCP servers.
type mcpMessage struct {
	ServerName string
	Message    jsonRPCRequest
}

// jsonRPCRequest is a JSON-RPC 2.0 request or notification. ID is nil for a
// notification.
type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

// mcpMessage decodes an mcp_message request.
func (r controlRequest) mcpMessage() (mcpMessage, error) {
	var w struct {
		ServerName string         `json:"server_name"`
		Message    jsonRPCRequest `json:"message"`
	}
	if err := json.Unmarshal(r.body, &w); err != nil {
		return mcpMessage{}, fmt.Errorf("mcp_message request: %w", err)
	}
	return mcpMessage{ServerName: w.ServerName, Message: w.Message}, nil
}

// unknownControlRequest is the event for a request the session does not
// handle.
func (r controlRequest) unknownControlRequest() UnknownControlRequest {
	return UnknownControlRequest{EventHeader: r.header, RequestID: r.requestID, Subtype: r.subtype, Raw: r.raw}
}

// unhandledMessage is the error text sent back for a request the session
// does not handle.
func (r controlRequest) unhandledMessage() string {
	return fmt.Sprintf("claudestream does not handle control request subtype %s", r.subtype)
}

// controlResponse is a control_response frame answering one of the
// session's control requests:
// {"type":"control_response","response":{"subtype":"success"|"error","request_id":R,"response":{..},"error":M}}.
type controlResponse struct {
	header    EventHeader
	requestID string
	subtype   string
	// response is the inner success payload; nil when absent.
	response json.RawMessage
	errText  string
	raw      json.RawMessage
}

func decodeControlResponse(raw json.RawMessage) (controlResponse, error) {
	var w struct {
		SessionID string `json:"session_id"`
		UUID      string `json:"uuid"`
		Response  struct {
			Subtype   string          `json:"subtype"`
			RequestID string          `json:"request_id"`
			Response  json.RawMessage `json:"response"`
			Error     string          `json:"error"`
		} `json:"response"`
	}
	if err := json.Unmarshal(raw, &w); err != nil {
		return controlResponse{}, err
	}
	return controlResponse{
		header:    EventHeader{SessionID: w.SessionID, UUID: w.UUID},
		requestID: w.Response.RequestID,
		subtype:   w.Response.Subtype,
		response:  w.Response.Response,
		errText:   w.Response.Error,
		raw:       raw,
	}, nil
}

// err returns a *ControlError unless the response is a success.
func (r controlResponse) err() error {
	if r.subtype == "success" {
		return nil
	}
	return &ControlError{RequestID: r.requestID, Subtype: r.subtype, Message: r.errText}
}

// unknownEvent is the event for a response no request is waiting for.
func (r controlResponse) unknownEvent() UnknownEvent {
	return UnknownEvent{EventHeader: r.header, Raw: r.raw}
}

// eventTracker holds the per-session state flattening needs across frames:
// the working directory from SystemInit, and the tool uses still waiting for
// their results. The session owns one and passes every parsed event list
// through track.
type eventTracker struct {
	cwd         string
	pendingUses map[string]ToolUse
}

func newEventTracker() *eventTracker {
	return &eventTracker{pendingUses: map[string]ToolUse{}}
}

// track returns events with each ToolResult stamped with its ToolUse's name
// and, after a successful result of a Write, Edit, or MultiEdit call, the
// FileWrite or FileEdit events it implies.
func (t *eventTracker) track(events []Event) []Event {
	out := make([]Event, 0, len(events))
	for _, ev := range events {
		switch e := ev.(type) {
		case SystemInit:
			t.cwd = e.Cwd
			out = append(out, e)
		case ToolUse:
			if e.ToolUseID != "" {
				t.pendingUses[e.ToolUseID] = e
			}
			out = append(out, e)
		case ToolResult:
			use, found := t.pendingUses[e.ToolUseID]
			if !found {
				out = append(out, e)
				continue
			}
			delete(t.pendingUses, e.ToolUseID)
			e.ToolName = use.Name
			out = append(out, e)
			if !e.IsError {
				out = append(out, fileEvents(e.EventHeader, use, t.cwd)...)
			}
		default:
			out = append(out, ev)
		}
	}
	return out
}

// fileEvents derives the file events of a successful file-modifying tool
// call; nil for any other tool.
func fileEvents(h EventHeader, use ToolUse, cwd string) []Event {
	topPath := stringField(use.Input, "file_path")
	switch use.Name {
	case "Write":
		content := stringField(use.Input, "content")
		return []Event{FileWrite{EventHeader: h, Path: resolveToolPath(topPath, cwd), ContentLength: utf8.RuneCountInString(content)}}
	case "Edit":
		return []Event{FileEdit{EventHeader: h, Path: resolveToolPath(topPath, cwd)}}
	case "MultiEdit":
		edits, _ := use.Input["edits"].([]any)
		if len(edits) == 0 {
			if topPath == "" {
				return nil
			}
			return []Event{FileEdit{EventHeader: h, Path: resolveToolPath(topPath, cwd)}}
		}
		var events []Event
		seen := map[string]bool{}
		for _, edit := range edits {
			editMap, _ := edit.(map[string]any)
			path := stringField(editMap, "file_path")
			if path == "" {
				path = topPath
			}
			resolved := resolveToolPath(path, cwd)
			if resolved == "" || seen[resolved] {
				continue
			}
			seen[resolved] = true
			events = append(events, FileEdit{EventHeader: h, Path: resolved})
		}
		return events
	default:
		return nil
	}
}

// stringField returns m[key] when it is a string, else empty.
func stringField(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// resolveToolPath makes a tool's path absolute against the session's working
// directory: absolute paths are cleaned, relative ones joined to cwd, and a
// relative path with no known cwd is returned as given.
func resolveToolPath(path, cwd string) string {
	switch {
	case path == "":
		return ""
	case filepath.IsAbs(path):
		return filepath.Clean(path)
	case cwd != "":
		return filepath.Join(cwd, path)
	default:
		return path
	}
}
