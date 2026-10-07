package claudestream

import (
	"encoding/json"
	"strings"
)

// Event is one item a turn yields. The concrete types are the structs in this
// file; every one of them embeds [EventHeader], which implements the
// interface, so events can be built by other packages (a mock session, a
// test) by filling in the struct.
type Event interface {
	// Header returns the session ID and UUID of the upstream frame the event
	// came from; both are empty for events with no upstream frame.
	Header() EventHeader
	isEvent()
}

// EventHeader carries the identifiers of the upstream frame an event came
// from.
type EventHeader struct {
	SessionID string
	UUID      string
}

// Header returns the header itself.
func (h EventHeader) Header() EventHeader { return h }

func (EventHeader) isEvent() {}

// MCPServerStatus is one MCP server as listed in the session's init frame.
type MCPServerStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// SystemInit is the first frame of every session, describing it.
type SystemInit struct {
	EventHeader
	Cwd               string
	Tools             []string
	MCPServers        []MCPServerStatus
	Model             string
	PermissionMode    string
	ClaudeCodeVersion string
}

// APIRetry is emitted before Claude Code retries a failed API call.
type APIRetry struct {
	EventHeader
	// Attempt counts from 1.
	Attempt      int
	MaxRetries   int
	RetryDelayMS float64
	// ErrorStatus is the HTTP status of the failed call; nil when the failure
	// was not an HTTP error.
	ErrorStatus *int
	Error       string
}

// CompactBoundary is emitted when Claude Code compacts the conversation
// history.
type CompactBoundary struct {
	EventHeader
}

// AssistantText is one text block of an assistant message.
type AssistantText struct {
	EventHeader
	Text string
	// ParentToolUseID is the tool use that started the subagent this message
	// belongs to; nil at the top level.
	ParentToolUseID *string
}

// Thinking is one extended-thinking block of an assistant message.
type Thinking struct {
	EventHeader
	Text            string
	ParentToolUseID *string
}

// ToolUse is one tool call of an assistant message.
type ToolUse struct {
	EventHeader
	ToolUseID       string
	Name            string
	Input           map[string]any
	ParentToolUseID *string
}

// ToolResult is one tool result returned to the model.
type ToolResult struct {
	EventHeader
	ToolUseID string
	// Content is the wire value as sent: a JSON string or an array of
	// content blocks. ContentText renders it as text.
	Content         json.RawMessage
	ParentToolUseID *string
	// ToolName is the name of the matching ToolUse earlier in the session;
	// empty when no ToolUse with this ID was seen.
	ToolName string
	IsError  bool
}

// ContentText renders the result content as text: a string as itself; an
// array block by block, text blocks as their text and any other block as its
// JSON, joined by newlines; anything else as its JSON.
func (r ToolResult) ContentText() string {
	if len(r.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(r.Content, &s); err == nil {
		return s
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(r.Content, &blocks); err != nil {
		return string(r.Content)
	}
	parts := make([]string, 0, len(blocks))
	for _, block := range blocks {
		var text struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(block, &text); err == nil && text.Type == "text" {
			parts = append(parts, text.Text)
			continue
		}
		parts = append(parts, string(block))
	}
	return strings.Join(parts, "\n")
}

// FileWrite follows the successful ToolResult of a Write tool call.
type FileWrite struct {
	EventHeader
	// Path is absolute when the tool's path was absolute or the session's
	// working directory was known.
	Path string
	// ContentLength counts the characters (Unicode code points) written.
	ContentLength int
}

// FileEdit follows the successful ToolResult of an Edit or MultiEdit tool
// call, once per distinct path edited.
type FileEdit struct {
	EventHeader
	Path string
}

// StreamDelta is one partial-message streaming event from the API.
type StreamDelta struct {
	EventHeader
	Event           map[string]any
	ParentToolUseID *string
}

func (d StreamDelta) delta() map[string]any {
	delta, _ := d.Event["delta"].(map[string]any)
	return delta
}

func (d StreamDelta) deltaString(deltaType, key string) string {
	delta := d.delta()
	if t, _ := delta["type"].(string); t != deltaType {
		return ""
	}
	s, _ := delta[key].(string)
	return s
}

// StreamEventType returns the streaming event's type (message_start,
// content_block_delta, and so on); empty when absent.
func (d StreamDelta) StreamEventType() string {
	t, _ := d.Event["type"].(string)
	return t
}

// DeltaType returns the delta's type (text_delta, input_json_delta, and so
// on); empty when the event carries no delta.
func (d StreamDelta) DeltaType() string {
	t, _ := d.delta()["type"].(string)
	return t
}

// Text returns the text of a text_delta; empty for any other delta.
func (d StreamDelta) Text() string { return d.deltaString("text_delta", "text") }

// ThinkingText returns the text of a thinking_delta; empty for any other
// delta.
func (d StreamDelta) ThinkingText() string { return d.deltaString("thinking_delta", "thinking") }

// PartialJSON returns the fragment of an input_json_delta; empty for any
// other delta.
func (d StreamDelta) PartialJSON() string { return d.deltaString("input_json_delta", "partial_json") }

// Usage counts tokens. In a Result it is cumulative for the session.
type Usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// ModelUsage is one model's entry in a Result's per-model usage.
type ModelUsage struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	ContextWindow            int64   `json:"contextWindow"`
	CostUSD                  float64 `json:"costUSD"`
}

// Result ends a turn.
type Result struct {
	EventHeader
	Subtype       string
	IsError       bool
	DurationMS    float64
	DurationAPIMS float64
	NumTurns      int
	Result        string
	StopReason    string
	// TotalCostUSD is cumulative for the session.
	TotalCostUSD float64
	// Usage is nil when the frame carried none.
	Usage *Usage
	// APIErrorStatus is the HTTP status when the turn ended in an API
	// error.
	APIErrorStatus *int
	// ModelUsage is keyed by model ID; empty when the frame carried none.
	ModelUsage map[string]ModelUsage
}

// RateLimit reports a change in the account's rate-limit status.
type RateLimit struct {
	EventHeader
	Status string
	// ResetsAt is a Unix time in seconds; nil when not reported.
	ResetsAt      *int64
	RateLimitType string
	Utilization   float64
}

// PermissionRequest asks the consumer whether a tool may run. Answer it with
// the session's RespondAllow or RespondDeny. A session with a Sandbox never
// yields it; it answers itself and yields PermissionDecided.
type PermissionRequest struct {
	EventHeader
	RequestID      string
	ToolName       string
	ToolInput      map[string]any
	DecisionReason string
	ToolUseID      string
	// PermissionSuggestions are rule updates the consumer may pass back
	// with an allow.
	PermissionSuggestions   []map[string]any
	Title                   string
	DisplayName             string
	Description             string
	DecisionReasonType      string
	RequiresUserInteraction bool
}

// PermissionDecided reports a permission request the session's Sandbox
// answered.
type PermissionDecided struct {
	EventHeader
	Request PermissionRequest
	Allowed bool
	// Message is the denial reason sent to the model; empty when allowed.
	Message string
}

// UserDialogRequest asks the consumer to render a blocking dialog (for
// example AskUserQuestion). Answer it with the session's RespondDialog or
// RespondDialogCancelled.
type UserDialogRequest struct {
	EventHeader
	RequestID string
	// Dialog names the dialog to render; an open set.
	Dialog string
	// Payload is dialog-specific.
	Payload map[string]any
	// ToolUseID is the tool use the dialog belongs to; nil when none.
	ToolUseID *string
}

// SDKToolCall reports a call to one of the session's Tools, after its
// handler returned and the answer was sent.
type SDKToolCall struct {
	EventHeader
	Server    string
	Tool      string
	Arguments json.RawMessage
	IsError   bool
	// Output is the handler's text, or its error message when IsError.
	Output string
}

// UnknownControlRequest reports a control request the session does not
// handle; the session has already answered it with an error response.
type UnknownControlRequest struct {
	EventHeader
	RequestID string
	Subtype   string
	Raw       json.RawMessage
}

// BudgetMetric names what a budget threshold measures.
type BudgetMetric string

const (
	BudgetMetricCost   BudgetMetric = "cost"
	BudgetMetricTurns  BudgetMetric = "turns"
	BudgetMetricTokens BudgetMetric = "tokens"
)

// BudgetThreshold reports that a Budget threshold was crossed. It is
// informational: the session keeps going.
type BudgetThreshold struct {
	EventHeader
	Metric       BudgetMetric
	Threshold    float64
	CurrentValue float64
}

// UnknownEvent is an upstream frame of a type or subtype this package does
// not model, or a control response no request is waiting for.
type UnknownEvent struct {
	EventHeader
	Raw json.RawMessage
}

// UnknownContentBlock is an assistant content block of a type this package
// does not model.
type UnknownContentBlock struct {
	EventHeader
	Raw             json.RawMessage
	ParentToolUseID *string
}

// NonJSONLine is a stdout line that is not JSON.
type NonJSONLine struct {
	EventHeader
	Line string
}

// UndecodableEvent is a JSON stdout line whose fields do not have the types
// this package expects.
type UndecodableEvent struct {
	EventHeader
	Raw json.RawMessage
	Err error
}
