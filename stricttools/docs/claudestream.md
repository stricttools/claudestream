+++
title = "claudestream"
description = "Drive Claude Code from Python: run it as a subprocess and read its output as typed events, with async and sync sessions, sandbox policies, and tools you define in Python"
nav_group = "API Reference"
nav_order = 1
+++

# claudestream

:-: ref path="claudestream"

## Public API

Everything below is importable directly from `claudestream` at the top level, with no need to reference internal module paths. The 47 public symbols are grouped into 9 categories covering sessions, events, messages, protocol, policy, tools, options, process management, and agent definitions.

### Sessions

- **AsyncSession** -- async context manager for streaming Claude Code events
- **SyncSession** -- synchronous wrapper that bridges the async protocol to a blocking iterator
- **ClaudeStreamError** -- base exception for session errors

### Events

Typed dataclasses representing every Claude Code stream output, with 19 event types covering the full lifecycle from session initialization through assistant responses, tool use, permission handling, and final result summaries with cost and token usage:

- **Event** -- base class for all stream events
- **SystemInit** -- first event in the stream, containing session metadata
- **ApiRetry** -- emitted before retrying a failed API call
- **CompactBoundary** -- emitted when conversation history is compacted
- **AssistantMessage** -- complete assistant response with content blocks
- **AssistantText** -- single text block extracted from an assistant message
- **ToolResultMessage** -- tool execution results returned to the model
- **ToolUse** -- single tool call extracted from an assistant message
- **ToolResult** -- single tool result
- **FileWrite** -- derived event when a Write tool succeeds
- **FileEdit** -- derived event when an Edit or MultiEdit tool succeeds
- **Thinking** -- single extended thinking block from an assistant message
- **StreamDelta** -- partial streaming token wrapping a raw API event
- **Result** -- final event in a turn with cost and usage summary
- **RateLimit** -- rate limit status change
- **PermissionRequest** -- permission request surfaced when the sandbox cannot auto-resolve
- **McpRequest** -- MCP tool call request from Claude Code
- **HookEvent** -- hook lifecycle event
- **UnknownEvent** -- forward-compatible event for unrecognized types
- **ControlResponse** -- response to a control request
- **AskResult** -- complete response from a single ask() call

### Content Blocks

- **TextBlock** -- text content block from an assistant message
- **ToolUseBlock** -- tool use content block from an assistant message
- **ThinkingBlock** -- extended thinking content block
- **ToolResultBlock** -- tool result content block
- **ContentBlock** -- union type alias for all content block types
- **Usage** -- token usage statistics for an API call

### Messages

Typed structs for the 6 message types that flow from the SDK to the Claude Code subprocess via stdin, covering user prompts, permission responses, MCP tool results, server registration, and session initialization:

- **AllowPermission** -- allow a permission request
- **DenyPermission** -- deny a permission request
- **InitializeRequest** -- SDK initialization request sent at session start
- **McpResponse** -- response to an MCP tool call request
- **McpSetServers** -- register SDK MCP servers with the Claude Code CLI
- **UserMessage** -- a user prompt sent to Claude Code via stdin

### Protocol

The NDJSON protocol layer provides 4 functions for reading events from and writing messages to the Claude Code subprocess stream. It handles JSON serialization and deserialization, event type dispatch based on the `type` field, and content block flattening that expands compound `AssistantMessage` events into individual typed events like `AssistantText` and `ToolUse`:

- **Writable** -- union type alias for all writable message types
- **flatten_event** -- expand an event into convenience events (one per content block)
- **parse_event** -- map a raw JSON dict to the correct typed Event
- **read_events** -- async generator that reads NDJSON lines and yields parsed Events
- **write_message** -- serialize a message to NDJSON and write it to the stream

### Policy

Sandbox and permission policy types that control which tools an agent can call and which filesystem paths it can write to, with declarative allow and deny rules evaluated automatically during each permission request:

- **Allow** -- allow a tool to execute
- **Deny** -- deny a tool execution
- **Sandbox** -- declarative sandbox configuration controlling tools, filesystem scope, and flags
- **create_sandbox** -- create a validated Sandbox configuration
- **BUILTIN_TOOLS** -- frozenset of Claude Code's built-in tool names

### Tools

Tool registration API that lets consumers define custom MCP tools using a decorator-based pattern, where the function's type hints and docstring are automatically converted into a JSON Schema served to Claude Code at session startup:

- **Tool** -- a user-defined tool struct served via MCP to Claude Code
- **collect_tools** -- gather all @tool-decorated functions from a module
- **tool** -- decorator factory that creates a Tool from a function's type hints and docstring

### Options

Configuration structs for session setup, covering 9 option types. The primary entry point is `SessionConfig`, which unifies all settings into a single object passed to `AsyncSession` or `SyncSession`. The remaining 8 option types control specific areas: budget limits, debug output, MCP server integration, plugin loading, stream behavior, process tuning, session resolution, and tool schemas:

- **Budget** -- cost, turn, and token limits for a session
- **DebugOptions** -- debug output configuration
- **McpOptions** -- external MCP server configuration
- **PluginOptions** -- plugin loading configuration
- **ProcessLimits** -- process-level buffer and timeout tuning
- **SessionConfig** -- unified configuration object for all session types
- **SessionResolution** -- how to resolve which session to use
- **StreamOptions** -- stream output behavior controls
- **ToolSchema** -- tool schema without handler for JSON-serializable agent definitions

### Process

Subprocess management layer that spawns the Claude Code CLI process with piped stdin, stdout, and stderr, registers it for atexit cleanup, and implements a 3-stage graceful shutdown sequence with configurable timeouts:

- **ProcessConfig** -- configuration for spawning a Claude Code subprocess
- **ProcessManager** -- manages the Claude Code subprocess lifecycle

### Agents

Agent definition and invocation API providing 5 functions for loading, discovering, and running reusable agent configurations stored as `.agent.json` files, with support for prompt template variable substitution and budget enforcement:

- **AgentDefinition** -- a complete agent definition loadable from .agent.json files
- **discover_agents** -- discover agent definitions from filesystem and package resources
- **invoke_agent** -- async context manager that creates an AsyncSession from an AgentDefinition
- **invoke_agent_sync** -- sync context manager that creates a SyncSession from an AgentDefinition
- **load_agent** -- load an AgentDefinition from a .agent.json file or by bare name
- **resolve_prompt** -- resolve {variable} placeholders in a prompt template
