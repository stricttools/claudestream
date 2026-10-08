+++
title = "Guide"
description = "How a claudestream session runs Claude Code: starting it, turns and typed events, permissions, sandboxes, tools served over MCP, budgets, and control."
+++

# Guide

This guide follows one session from its configuration to `Close`. Every type and function it names is in [the library reference](../claudestream/).

## Configuration

`Config` describes one session. `Model`, `Profile`, `ClaudeBinary`, `ClaudewheelBinary`, and `Session` are required; every other field is optional, and its zero value adds nothing to Claude Code's command line. `Config.Validate` reports every problem at once, each wrapping `ErrInvalidConfig`, and `Start` refuses a config it rejects. Nothing is filled in by guessing:

- The binaries are absolute paths, and the library never searches `PATH`. `ResolveOnPath` finds a program on `PATH` for a caller that wants that, refusing one found only through a relative `PATH` entry.
- `Session` chooses the conversation: `NewSession{}` (or `NewSession{ID: ...}` to choose its ID), `ResumeSession{ID: ...}`, or `ContinueLastSession{}`, the last two with `Fork` to continue under a new session ID.
- `Profile` names the claudewheel profile. Claude Code is started as `claudewheel profile exec --name <profile> -- <claude> ...`, which gives it the profile's environment.

## Starting a session

`Start` runs `claude -v` first and refuses a Claude Code older than `MinimumClaudeVersion`, one whose version cannot be read, and one that does not answer within the version check timeout. It then starts the process with three pipes. When the session serves tools, routes permission prompts, or declares the dialogs it supports, `Start` sends Claude Code's `initialize` request and, with tools, registers the SDK MCP servers and waits until Claude Code listed each server's tools. The context bounds every wait.

Two goroutines run for the life of the session:

- The reader is the only reader of stdout. It decodes each line, answers what the session answers itself (sandbox decisions, SDK MCP requests, control requests the session does not handle), resolves the session's own control requests, and queues every event without limit, so it never waits for the consumer.
- The writer is the only writer of stdin. User messages, control requests, permission and dialog answers, and the reader's automatic answers are written in the order they were submitted.

A line longer than the line limit (`DefaultLineLimit` unless `Limits` sets another) is a hard error. Stderr is kept, not printed: `Session.Stderr` returns its last lines, and an `ErrProcessExited` carries them when the process exits while something still waited on it.

## Turns and events

`Session.Send` (or `SendBlocks`, for content blocks) starts a turn and returns a `Turn`. Call `Turn.Next` until it returns `io.EOF`, which comes after the turn's `Result` and any `BudgetThreshold` events that follow it. A second send while a turn is open returns `ErrTurnActive`; events that arrive between turns are delivered at the start of the next one.

Every stdout line becomes one or more events. Assistant and user messages are flattened into one event per content block (`AssistantText`, `Thinking`, `ToolUse`, `ToolResult`), and a successful file-writing tool call also yields `FileWrite` or `FileEdit`. A frame the session does not recognize is never dropped: it becomes `UnknownEvent`, `UnknownContentBlock`, `NonJSONLine`, or `UndecodableEvent`.

A turn ends in an error rather than `io.EOF` when Claude Code reports an authentication failure (`ErrAuthentication`), when the process exits (`*ErrProcessExited`), when an automatic answer cannot be written, or when the context ends. Each of these makes the session unusable: later sends return `ErrSessionUnusable`, or the exit error once the process exited.

The session keeps running totals: `TurnCount`, `TotalTokens`, `TotalCostUSD`, `FilesModified`, and `LastResult`. `SessionID`, `Model`, `Tools`, `MCPServers`, `PermissionMode`, `ClaudeVersion`, and `Cwd` report what Claude Code announced at the start.

## Permissions

Claude Code asks before running a tool that needs permission. There are three ways to answer:

- **Skip the checks.** `Sandbox{SkipPermissions: true}` passes `--dangerously-skip-permissions`, and Claude Code asks nothing.
- **Let a sandbox decide.** A `Sandbox` with `Tools` (an allow-list) or `WritePaths` (the files and directories `Write`, `Edit`, and `MultiEdit` may write inside, resolved through symlinks) answers every request itself, and the turn yields `PermissionDecided` with the decision instead of a request.
- **Decide yourself.** With `InterceptPermissions` (or tools and no sandbox), the turn yields `PermissionRequest`; answer it with `RespondAllow` or `RespondDeny` while the turn is still being read. A request left unanswered keeps Claude Code waiting.

Dialogs work the same way: `SupportedDialogs` names the dialogs the consumer renders, a `UserDialogRequest` asks for one, and `RespondDialog` or `RespondDialogCancelled` answers it.

## Tools

`Config.Tools` serves Go functions to Claude Code over the SDK MCP channel. Each `Tool` names its server, its name, a description, the JSON Schema of its arguments, and a handler; the model sees it as `mcp__<server>__<name>`, and the session adds every server's tools to `--allowedTools`. A handler runs in its own goroutine with a context that ends when the session closes. Its text becomes the tool's result and its error an error result; either way the turn yields `SDKToolCall` before Claude Code sees the answer.

```go
cfg.Tools = []claudestream.Tool{{
	Server:      "weather",
	Name:        "lookup",
	Description: "Look up the current weather for a city",
	InputSchema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
		"required":   []any{"city"},
	},
	Handler: func(ctx context.Context, arguments json.RawMessage) (string, error) {
		var in struct{ City string }
		if err := json.Unmarshal(arguments, &in); err != nil {
			return "", err
		}
		return "22 degrees in " + in.City, nil
	},
}}
```

`MCP` loads external MCP servers from configuration files instead, with `Strict` to use only those.

## Budgets

`Budget` lists thresholds on the session's cost in US dollars, its turn count, and its token count. Each threshold yields one `BudgetThreshold` event the first time its total reaches it, after the `Result` of that turn. Budgets observe: they never stop the session, so a consumer that wants a hard limit stops sending.

## Control methods

While a session runs, `Interrupt` stops the current turn, `SetModel` and `SetPermissionMode` change the session's model and permission mode, and `GetContextUsage` reports how full the context window is. Each takes a context, waits for Claude Code's answer, and returns a `*ControlError` when Claude Code refuses.

## Closing

Always `Close` a session. It stops the writer, closes stdin, and waits for the process to exit, sending SIGTERM and then SIGKILL after each wait of the shutdown timeout; then it waits for the reader and for tool handlers to return. Every operation after `Close` returns `ErrSessionClosed`.

## The command line

The `claudestream` command wraps one session per run. `ask` prints the reply's final text (with `--json`, the reply and its metadata as the document's payload), `stream` writes the text as it arrives with markers on stderr, `send` renders the whole turn, `events` copies Claude Code's stdout lines verbatim, and `agent run` runs an [agent definition](../agents-guide/). `doctor` checks the binaries and, with `--profile`, starts Claude Code through the profile once. A turn that Claude Code ends with an error exits 1 with the result text on stderr. The command cannot answer permission or dialog requests, so it never asks for them: pass `--skip-permissions` to let Claude Code run every tool, or `--no-skip-permissions` to have tools that need permission refused.
