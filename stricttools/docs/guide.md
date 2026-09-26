+++
title = "Architecture Guide"
description = "How claudestream's 4-layer architecture (process, protocol, session, CLI) turns a Claude Code subprocess into typed async events with permissions."
+++

# Architecture Guide

claudestream wraps the Claude Code CLI's stream-json protocol in a four-layer Python SDK. Each layer adds structure on top of the one below it: Process spawns the subprocess, Protocol decodes its output, Session manages conversation state, and CLI exposes it all as shell commands.

## The four layers

### Process (bottom)

The process layer spawns and manages the Claude Code subprocess, mapping every session option to its corresponding CLI flag and handling lifecycle events including startup, graceful shutdown with a 3-stage sequence, and atexit cleanup for orphan prevention.

:-: ref path="claudestream._process" lang="python"

`ProcessConfig` is a frozen struct that maps every session option to its CLI flag equivalent. Its `build_argv()` method produces the full argument list, including the hardcoded `--output-format stream-json --input-format stream-json` that enables the protocol. A declarative flag registry (`_FLAG_REGISTRY`) drives the mapping: each entry is a `(field_name, cli_flag, style)` tuple where style is `"value"`, `"bool"`, or `"list"`.

`ProcessManager` owns the subprocess lifecycle. On `start()`, it spawns the process with piped stdin/stdout/stderr, registers it in a module-level `_ACTIVE_CHILDREN` set (cleaned up by an `atexit` handler), and launches a background task to drain stderr. On `close()`, it follows a three-stage shutdown sequence: close stdin, wait for exit, SIGTERM with timeout, then SIGKILL.

### Protocol (middle-lower)

The protocol layer converts raw NDJSON lines into typed Python Event objects and serializes outbound Message objects back to NDJSON, providing the bidirectional codec between the subprocess I/O streams and the SDK's typed event system.

:-: ref path="claudestream._protocol" lang="python"

The protocol layer converts between raw NDJSON lines and typed Python objects.

**Reading (subprocess to SDK):** `read_events()` is an async generator that reads lines from an `asyncio.StreamReader`, JSON-decodes each line, and calls `parse_event()`. The parser dispatches on the `type` field (`"system"`, `"assistant"`, `"user"`, `"result"`, `"control_request"`, etc.) and further on subtype fields to construct the correct `Event` subclass. Unrecognized types become `UnknownEvent` for forward compatibility.

**Writing (SDK to subprocess):** `write_message()` takes any `Writable` message (a union of `UserMessage`, `AllowPermission`, `DenyPermission`, and several others), calls its `to_dict()` method, serializes to JSON, appends a newline, and writes to the `asyncio.StreamWriter`.

**Flattening:** `flatten_event()` expands compound events into convenience events. An `AssistantMessage` with three content blocks (text, tool_use, thinking) becomes three separate events (`AssistantText`, `ToolUse`, `Thinking`). A `ToolResultMessage` is expanded into individual `ToolResult` events. Tool use blocks for `Write`, `Edit`, and `MultiEdit` also generate derived `FileWrite`/`FileEdit` events for file-tracking. Non-compound events pass through as single-element lists.

### Session (middle-upper)

The session layer manages turn-based conversation state on top of the protocol, combining process lifecycle, event parsing, sandbox-based permission interception, MCP tool serving, budget tracking, and callback dispatch into a single context manager.

:-: ref path="claudestream._async_session" lang="python"

:-: ref path="claudestream._sync_session" lang="python"

`AsyncSession` is the primary implementation. It combines the process and protocol layers with conversation state, permission handling, MCP tool serving, budget tracking, and lifecycle hooks.

`SyncSession` is a thin wrapper that runs an `AsyncSession` on a dedicated event loop thread. It bridges the async iterator to a blocking `queue.Queue`: a background coroutine drains the async iterator and puts events on the queue; the sync `send()` method polls the queue with timeouts. All property access and method calls are forwarded via `run_coroutine_threadsafe`.

### CLI (top)

The CLI layer exposes the SDK as shell commands built with strictcli, providing 8 commands for sending prompts, streaming tokens, debugging protocol events, running interactive sessions, and managing agent definitions.

:-: ref path="claudestream._cli" lang="python"

The CLI is built with strictcli and provides commands that construct a `SessionConfig` from flags and run sessions. Commands include `send` (display response events), `stream` (real-time token output via `StreamDelta`), `events` (raw protocol debug), `repl` (multi-turn interactive), `ask` (one-shot text output), `doctor` (environment health check), `config` (show resolved config), and the `agent` subcommand group.

## Event lifecycle

When you call `session.send("prompt")`, the event flows through an 8-step pipeline that transforms your prompt into a stream of typed events. The steps are: message serialization, subprocess processing, event reading, permission and MCP handling, event flattening and enrichment, file tracking, callback firing, and turn completion with budget checks:

1. **Message serialization.** The prompt is wrapped in a `UserMessage` and written to the subprocess stdin as an NDJSON line via `write_message()`.

2. **Subprocess processing.** The Claude Code CLI processes the prompt, makes API calls, and writes events to stdout as NDJSON lines. Events arrive in order: `SystemInit` (on first turn only), then interleaved `AssistantMessage` and `ToolResultMessage` events as the model thinks and uses tools, with possible `PermissionRequest` and `McpRequest` control requests, and finally a `Result` event marking the end of the turn.

3. **Event reading.** The session's `_read_turn()` method reads stdout lines, JSON-decodes them, and calls `parse_event()` to produce typed events.

4. **Permission and MCP handling.** Before yielding, the session checks each event. `PermissionRequest` events are passed to `_handle_permission()` which applies the sandbox policy. `McpRequest` events are routed to `_handle_mcp_request()` which dispatches tool calls to registered handlers.

5. **Flattening and enrichment.** Unless `raw=True` was passed, events go through `flatten_event()` to expand compound messages into individual typed events. The session then enriches flattened events: `ToolUse` events record their tool name by `tool_use_id`, and later `ToolResult` events get `tool_name` stamped from that correlation map.

6. **File tracking.** `FileWrite` and `FileEdit` events (derived during flattening) accumulate their paths in `session.files_modified`.

7. **Callback firing.** Before yielding each event, the session fires any registered callbacks for that event type.

8. **Turn completion.** When a `Result` event arrives, the session updates cumulative stats (turn count, total tokens, total cost), checks budget thresholds, writes to the cost log if configured, fires `on_turn_complete` hooks, and returns.

## The streaming model

### Async iteration

The core API is an async generator that yields typed events one at a time as they arrive from the subprocess. Each call to `AsyncSession.send()` drives one conversational turn, blocking until the subprocess produces each event and terminating when a `Result` event signals turn completion:

```python
async with AsyncSession(config) as session:
    async for event in session.send("prompt"):
        match event:
            case AssistantText(text=t):
                print(t, end="")
            case ToolUse(name=n):
                print(f"[tool: {n}]")
            case Result() as r:
                print(f"\ncost=${r.total_cost_usd:.4f}")
```

The iterator blocks until the subprocess produces the next event. A turn is complete when a `Result` event is yielded. Multi-turn conversations call `send()` multiple times on the same session.

### Raw vs. flattened mode

By default, `send()` flattens compound events into individual typed events: an `AssistantMessage` containing 3 content blocks (text, tool_use, thinking) becomes 3 separate events (`AssistantText`, `ToolUse`, `Thinking`). Passing `raw=True` disables flattening and yields the original compound events with their content block lists intact, which is useful for protocol debugging or custom renderers.

Passing `raw=True` yields the protocol-level events (`AssistantMessage`, `ToolResultMessage`) with their content block lists intact. This is useful for debugging, protocol inspection, or building custom renderers that need the full message structure.

```python
# Flattened (default): individual typed events
for event in session.send("prompt"):
    if isinstance(event, AssistantText): ...
    if isinstance(event, ToolUse): ...

# Raw: compound message events with content blocks
for event in session.send("prompt", raw=True):
    if isinstance(event, AssistantMessage):
        for block in event.content:
            if isinstance(block, TextBlock): ...
            if isinstance(block, ToolUseBlock): ...
```

### Real-time streaming

`StreamDelta` events carry partial tokens as they arrive from the API. They appear alongside the full `AssistantText` events (which contain the complete text once the message finishes). The CLI's `stream` command uses `StreamDelta.text` for real-time output and falls back to `AssistantText` if the streamed text differs:

```python
streamed = ""
for event in session.send("prompt"):
    if isinstance(event, StreamDelta) and event.text:
        streamed += event.text
        sys.stdout.write(event.text)
    elif isinstance(event, AssistantText):
        if event.text != streamed:
            sys.stdout.write(event.text)
```

### Event filtering by type

Use `isinstance` checks or structural pattern matching to filter from the 19 event types in the SDK. The type hierarchy is flat with all events inheriting directly from `Event`, so filtering requires only single-level dispatch without navigating a deep class tree or handling intermediate abstract types:

```python
from claudestream import (
    AssistantText, ToolUse, ToolResult, Thinking,
    Result, PermissionRequest, BudgetThreshold,
)

for event in session.send("prompt"):
    match event:
        case AssistantText():   ...  # Model text output
        case ToolUse():         ...  # Tool call (name + input)
        case ToolResult():      ...  # Tool output
        case Thinking():        ...  # Extended thinking
        case Result():          ...  # Turn complete
        case PermissionRequest(): ...  # Needs permission decision
        case BudgetThreshold(): ...  # Budget threshold crossed
```

### Callbacks

Register callbacks for specific event types using `session.on(EventType, handler)`. Callbacks fire during iteration before each event is yielded to the consumer, allowing side effects like logging, metrics collection, or progress reporting without modifying the main iteration loop:

```python
session.on(ToolUse, lambda e: print(f"[calling {e.name}]"))
session.on(Result, lambda e: print(f"[${e.total_cost_usd:.4f}]"))
```

## Permission handling

Permission handling has 2 modes: automatic (sandbox-driven, where the SDK resolves tool permission requests using declarative allow/deny rules) and manual (consumer-driven, where `PermissionRequest` events are surfaced to the caller for interactive decision-making).

### Automatic: sandbox policies

When a `Sandbox` is configured, the session automatically resolves permission requests by applying a 2-step check (tool allow-list, then write-path scope) without surfacing any events to the consumer. This is the default mode for agent definitions that declare a sandbox.

:-: ref path="claudestream.policy" lang="python"

When a `Sandbox` is configured, the session automatically resolves permission requests without surfacing them to the consumer. The `sandbox_decide()` function applies two checks in order:

1. **Tool allow-list.** If `sandbox.tools` is set and the tool name is not in the list, the request is denied.
2. **Write-path scope.** If `sandbox.write_paths` is set and the tool is a write tool (`Write`, `Edit`, `MultiEdit`), the file path is resolved to an absolute canonical path and checked against the allowed directories.

If both checks pass, the request is allowed. The session sends an `AllowPermission` or `DenyPermission` message back to the subprocess via stdin.

```python
from claudestream import create_sandbox, SessionConfig, SyncSession

# Only allow Read and Bash, restrict writes to one directory
sandbox = create_sandbox(
    tools=["Read", "Bash", "Write"],
    write_paths=["/home/user/project/src"],
)
config = SessionConfig(model="sonnet", profile="default", sandbox=sandbox)

with SyncSession(config) as session:
    result = session.ask("Read the README")
    # Write attempts outside /home/user/project/src are denied automatically
```

The `skip_permissions=True` option bypasses all permission prompts by passing `--dangerously-skip-permissions` to the subprocess. This is for testing only.

### Manual: consumer-driven permission handling

When `intercept_permissions=True` is set on `SessionConfig`, or when iterating with `raw=True`, the session surfaces `PermissionRequest` events containing the tool name, input parameters, and display metadata. The consumer inspects each request and responds with `respond_allow()` or `respond_deny()` to unblock the subprocess:

```python
config = SessionConfig(
    model="sonnet",
    profile="default",
    intercept_permissions=True,
)

with SyncSession(config) as session:
    for event in session.send("edit the config file", raw=True):
        if isinstance(event, PermissionRequest):
            # Inspect and decide
            if event.tool_name in ("Read", "Bash"):
                session.respond_allow(event.request_id, event.tool_input)
            else:
                session.respond_deny(event.request_id, "Not allowed")
```

`PermissionRequest` events carry rich metadata: `tool_name`, `tool_input`, `decision_reason`, `permission_suggestions`, and display fields (`title`, `display_name`, `description`) for building UI permission cards.

### User dialog requests

`UserDialogRequest` events represent blocking dialogs the CLI asks the host to render (e.g., `AskUserQuestion`). These are never auto-handled by the sandbox. Respond with `respond_dialog()` to complete the dialog or `respond_dialog_cancelled()` to let the CLI apply its default behavior.

## Common usage patterns

### One-shot ask

The simplest pattern for single-question interactions. The `ask()` method internally calls `send()`, collects all `AssistantText` events, concatenates their text content, and returns an `AskResult` containing the full response text along with cost, duration, and token usage metadata:

```python
from claudestream import SessionConfig, SyncSession

config = SessionConfig(model="sonnet", profile="default")
with SyncSession(config) as session:
    result = session.ask("What is the capital of France?")
    print(result.text)
    print(f"Cost: ${result.cost_usd:.4f}, Duration: {result.duration_ms:.0f}ms")
```

### Multi-turn conversation

The subprocess maintains full conversation state across multiple calls to `send()`, so each subsequent prompt has access to the complete history of prior turns. The session tracks cumulative cost, token usage, and turn count across the entire conversation:

```python
config = SessionConfig(model="sonnet", profile="default")
with SyncSession(config) as session:
    for event in session.send("My name is Alice."):
        pass  # drain the turn
    for event in session.send("What is my name?"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
```

### Registering custom tools

The `@tool` decorator creates a `Tool` from a function's type hints and docstring, automatically generating a JSON Schema for the input parameters. Tools are served to Claude Code via MCP during the session startup handshake, and the SDK dispatches incoming `McpRequest` events to the registered handler functions:

```python
from claudestream import tool, SessionConfig, SyncSession, AssistantText

@tool("my_server")
def search_docs(query: str, max_results: int = 5) -> str:
    """Search the documentation.

    Args:
        query: Search query string.
        max_results: Maximum results to return.
    """
    return f"Found {max_results} results for '{query}'"

config = SessionConfig(
    model="sonnet",
    profile="default",
    tools=[search_docs._tool],
)
with SyncSession(config) as session:
    for event in session.send("Search for authentication docs"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
```

### Lifecycle hooks

Register hooks for 3 lifecycle events: turn completion (fires after each `Result` with cost and turn data), errors (fires on unhandled exceptions during iteration), and session close (fires when the context manager exits):

```python
def on_done(session, result):
    print(f"Turn {session.turn_count}: {result.num_turns} turns, ${result.total_cost_usd:.4f}")

def on_error(session, exc):
    print(f"Error: {exc}")

config = SessionConfig(model="sonnet", profile="default")
with SyncSession(config) as session:
    session.on_turn_complete(on_done)
    session.on_error(on_error)
    for event in session.send("Do something"):
        pass
```

### Budget observation

Budget thresholds are informational `BudgetThreshold` events fired when cumulative cost (USD), turn count, or token count crosses any of the configured threshold values. Each threshold fires exactly once per session, and the event carries the metric name, threshold value, and current value for logging or abort decisions:

```python
from claudestream import Budget, SessionConfig, SyncSession, BudgetThreshold

config = SessionConfig(
    model="sonnet",
    profile="default",
    budget=Budget(
        cost_thresholds=[0.01, 0.05, 0.10],
        turn_thresholds=[5, 10],
    ),
)
with SyncSession(config) as session:
    for event in session.send("Do a complex task"):
        if isinstance(event, BudgetThreshold):
            print(f"Budget: {event.metric} crossed {event.threshold} (now {event.current_value})")
```

### Mid-session control

The session supports 4 mid-session control operations that modify the running subprocess without restarting it: switching the active model, changing permission modes, querying context window usage (total and maximum tokens), and interrupting a running turn to reclaim control:

```python
async with AsyncSession(config) as session:
    # Switch model mid-session
    await session.set_model("claude-opus-4-20250514")

    # Query context window usage
    usage = await session.get_context_usage()
    print(f"Context: {usage.total_tokens}/{usage.max_tokens} tokens")

    # Interrupt a running turn
    still_queued = await session.interrupt()
```

### Agent definitions

Agents are `.agent.json` files that compose a model, system prompt template, sandbox policy, budget constraints, tool schemas, and MCP configuration into a single reusable definition, loadable by name from `.claudestream/agents/` or by filesystem path:

```python
from claudestream import load_agent, invoke_agent_sync, SessionConfig, AssistantText

agent = load_agent("code_reviewer")
config = SessionConfig(model="sonnet", profile="default")

with invoke_agent_sync(agent, config, variables={"file": "main.py"}) as session:
    for event in session.send("Review this file"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
```

## Transparent recovery

When the subprocess becomes unresponsive (no events for `stuck_timeout` seconds, default 120s), the session automatically restarts it with `--resume` to preserve conversation state. The recovery sends a random continuation message ("continue", "carry on", etc.) and retries up to 3 times. The `restart_count` property tracks how many restarts have occurred.
