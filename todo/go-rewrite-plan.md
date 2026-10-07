# Rewriting claudestream in Go: the plan

The owner asked for claudestream to be rewritten entirely in Go, with no decisions sent to the owner, and without running anything that uses much memory (no builds, compiles, tests, or installs; read-only and simple commands are fine). The design decisions below come from a read-only investigation, approved by the orchestrating session. miniclaude is rewritten alongside it (its plan is in miniclaude's `todo/go-rewrite-plan.md`). Implementation decisions taken during the build go into `todo/go-rewrite-plan.deviations.md` (append-only), each labeled as decided by the build or as judged by the investigation.

## Rules for the build

- Nothing memory-heavy is run: no `go build`, `go vet`, `go get`, `go mod tidy`, `strictspec gen`, tests, or installs. Each slice is checked by reading. No `_test.go` files are written; the test suites are switchover work.
- Dependencies come only from modules already in the local module cache: `github.com/stricttools/strictcli/go v0.38.0`, `github.com/stricttools/go-toml-edit v0.5.0`, `github.com/smm-h/go-toml-edit v0.4.0 // indirect`, `golang.org/x/term v0.44.0`, `golang.org/x/sys v0.47.0`, `github.com/mattn/go-runewidth v0.0.24`, `github.com/clipperhouse/uax29/v2 v2.2.0 // indirect`, `github.com/pelletier/go-toml/v2 v2.4.3`. `go.sum` lines are copied from `~/Projects/stricttools/tools/dirstat/go.sum` plus the go-toml/v2 lines from a sibling project's `go.sum`; the `require` blocks follow dirstat's `go.mod`. `go 1.25.7`, `toolchain go1.26.8`, no `go.work`.
- The strictcli API is read from the released copy at `~/go/pkg/mod/github.com/stricttools/strictcli/go@v0.38.0/` (README and `strictcli/`); the working tree at `~/Projects/stricttools/tools/strictcli/go` is newer. Options relied on: `WithEffect`, `WithDryRunUnsupported`, `WithInteractive`, `OwnsStdout`, `MemberChoiceFlag`/`MemberChoice`, `Repeatable`, `ctx.Payload`, `ctx.Document`, `ctx.Done`, `ctx.JSON`; where one is missing at v0.38.0, the v0.38.0 equivalent is used and recorded in the deviations file. `version`, `--version`, and `-v` belong to the framework.
- The Python stays untouched: `*.py`, `pyproject.toml`, `uv.lock`, `package.json`, `package-lock.json`, `.npmignore`, `bin/*.mjs`, `.rlsbl/` (changelog entries excepted), `CHANGELOG.md`, `README.md`, `CLAUDE.md`, `.strictcli/`, `.strictspec/`, `strictspec.toml`, `tests/`, `scripts/`. The editable uv install of the Python keeps working because Python ignores `.go` files.

## How the projects relate

- miniclaude is a fullscreen terminal front end over claudestream's session; it is claudestream's only code consumer within the projects.
- claudestream imports one thing from claudewheel: `claudewheel.profile.resolve_profile(profile)`, which turns a profile name into launch environment variables (`CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_OAUTH_TOKEN` read from `<profile>/.claudewheel/token.json`, the plan-tier variables, `DISABLE_AUTOUPDATER`, `DISABLE_GROWTHBOOK`, the marketplace switch; `default` resolves to an empty map).
- claudewheel launches miniclaude through its client adapter with the argv `[<miniclaude binary>, "repl", "--profile", P, ("--model", id)?, ("--permission-mode", one of bypassPermissions/default/plan/auto)?, ("--continue-session" | "--resume", ID)?]`.
- Both tools are installed as editable uv tools from these repositories; neither runs as a service.

## Profile resolution through claudewheel

Claude Code removes `CLAUDE_CODE_OAUTH_TOKEN` from its tool subprocesses (measured inside a session), so inheriting the launch environment breaks claudestream whenever an agent uses it from inside a session. Reimplementing the token store would duplicate claudewheel's authority, and a command printing the environment would put secrets on stdout.

Decision: claudestream starts Claude Code as `claudewheel profile exec --name <profile> -- <absolute claude path> <claude flags...>`. The claudewheel Go rewrite provides that command: it replaces its own process with the argv after `--`, applies the same environment a launch gets, removes the profile keys for `default`, refuses an unknown profile listing the names, and prints nothing on success. Both CLIs gain an optional `--claudewheel-binary`; when it is absent, `claudewheel` is resolved on PATH.

## Inventory of the Python (version 0.15.3, published to PyPI and npm)

| Module | Responsibility |
|---|---|
| `_async_session.py` | Session: start, the initialize and MCP handshake, turn loop, stuck detection with transparent restart, sandbox auto-responses, the SDK MCP responder, control correlation (`ctrl_<n>`), budget thresholds, cost log, callbacks, control methods |
| `_cli.py` | strictcli app |
| `_protocol.py` | NDJSON parse into typed events; flattening; derived FileWrite/FileEdit |
| `_agent.py` | `.agent.json` load and discovery, the deprecated-budget pre-check, template `{var}` resolution, agent invocation |
| `events.py`, `messages.py` | event structs, outgoing frames |
| `_process.py` | process config and flag table; process manager (spawn, stderr drain, close: stdin, 5s, SIGTERM, 5s, SIGKILL); version check (`claude -v`, minimum 2.0.0) |
| `_sync_session.py` | thread and event-loop bridge |
| `_agent_schema.py` | strictspec-generated validator |
| `_tools.py` | `@tool` decorator, JSON Schema from type hints, injection |
| `policy.py` | `Sandbox` and its decision (allow-list, then write-path containment with realpath) |
| `_options.py`, `_color.py`, `__init__.py` | options, ANSI colors, exports |

Python CLI: `send`, `stream`, `events`, `repl`, `ask`, `doctor`, `config`, `agent run|list|info|validate`; `--model` and `--profile` required on session commands.

Wire formats the Go version reproduces:

| Direction | Frame |
|---|---|
| Out | user: `{"type":"user","message":{"role":"user","content":S|blocks},"parent_tool_use_id":null,"session_id":ID|""}` |
| Out | `control_response` success wrapping `{"behavior":"allow","updatedInput":{..}[,"updatedPermissions":[..]]}`, `{"behavior":"deny","message":M}`, `{"behavior":"completed","result":X}`, or `{"behavior":"cancelled"}` |
| Out | MCP reply: inner object `{"mcp_response":{jsonrpc}}` |
| Out | initialize: `{"type":"control_request","request":{"subtype":"initialize","request_id":"init_1","hooks":{},"sdk_mcp_servers":[..][,"supportedDialogKinds":[..]]}}` |
| Out | `mcp_set_servers` (request_id `mcp_set_1`, `servers:{name:{"type":"sdk","name":name}}`) |
| Out | generic control: `request_id` both at the top level and inside `request` |
| In | `system/{init, api_retry, compact_boundary, rate_limit}`; `assistant`; `user` (only `tool_result` blocks); `result` (with `modelUsage`); `stream_event` |
| In | `control_request/{can_use_tool, request_user_dialog, mcp_message, …}` (`request_id` at the top level or inside `request`); `rate_limit`/`rate_limit_event`; `control_response` |

Defects found in the Python, fixed by the design below: `hooks` sent but never answered; unknown control requests and `mcp_message` for an unknown server never answered (the CLI waits forever); sandbox-decided permission requests still yielded as requests; FileWrite/FileEdit emitted on request rather than success; `agent run --model` not overriding; list flags comma-joined (breaks `--mcp-config a b`); MCP `initialize` ignoring the client's version; `Sandbox.bare` mapping to `--bare`, which disables token login; `config --profile` printing the OAuth token; hook and callback errors logged and dropped; non-JSON lines and unknown content blocks only warned about; the stuck restart killing long tool runs and continuing with a random message. Dead code not ported: `sandbox_to_flags`, `_liveness_probe`, `read_events`, process config fields never set from the config, `print_prompt`, agent discovery `paths`/`packages`.

## Live callers

| Caller | Change |
|---|---|
| miniclaude | its Go rewrite imports the Go library |
| `~/Projects/agentsdk` probe scripts (not a repository) | none: they keep the last PyPI release |
| `~/Projects/gamehome/scripts/game-idea-gen.sh` (already broken) | at the switchover: `claudestream ask --model M --profile P --prompt "$1" --new-session --no-skip-permissions` |
| `~/Projects/stricttools/data/tools.json` and `events.json` | at the switchover: `go install github.com/stricttools/claudestream/cmd/claudestream@v0` |
| strictspec's claudestream agent example and fixtures | falsified-texts sweep at the switchover |

## Go design

Module `github.com/stricttools/claudestream`. The root package `claudestream` is the library; `cmd/claudestream` (`main.go`, `version.go` following dirstat); `internal/cli`; `internal/printer`; a public `agent` package; `experiments/go.mod` (`module experiments`).

Library files: `config.go` (config and validation), `argv.go` (flag table), `launch.go` (claude and claudewheel resolution, version check, the `profile exec` argv), `process.go`, `wire.go`, `events.go`, `messages.go`, `session.go` (session and turn), `control.go` (pending registry, control methods, responses), `sandbox.go`, `mcp.go` (tools and the SDK MCP responder), `budget.go`, `errors.go`.

### Config

Every field explicit:

- `Model` and `Profile` required; `ClaudeBinary` and `ClaudewheelBinary` required absolute paths (the library never searches PATH; the CLIs resolve them).
- `Cwd`, `PermissionMode`, `InterceptPermissions`, `SupportedDialogs []string` (wire key `supportedDialogKinds`; nil omits it).
- `SystemPrompt`, `Tools []Tool`, `Sandbox *Sandbox`, `ExtraArgs`, `Env map[string]string` (merged over the process environment).
- `Session`, a required interface value with an unexported marker method: `NewSession{ID}` (`--session-id` when set), `ResumeSession{ID, Fork}`, or `ContinueLastSession{Fork}`.
- `SessionName` (`--name`); `Debug *DebugOptions{Enabled, Filter, File}`; `MCP *MCPOptions{ConfigFiles, Strict}` (only servers from these files); `Plugins *PluginOptions{Dirs, URLs}`; `Stream *StreamOptions` (five booleans; nil means verbose and partial messages on, the other three off); `Limits *ProcessLimits{LineLimit, ShutdownTimeout, VersionCheckTimeout}` (16 MiB, 5 s, and 2 s as named constants); `Budget *Budget`.
- `Effort`, `JSONSchema json.RawMessage`, `FallbackModel`, `Betas`, `AddDirs`, `BuiltinTools`, `Brief`, `Settings`, `SettingSources`, `Files`, `AgentName`, `AgentsJSON`, `NoPersistence`, `FromPR`.
- `RawOutput io.Writer`: every stdout line copied there verbatim before decoding.
- Dropped: hooks, the cost log path, tool context, the poll, join, health, and stuck timeouts, and the dead process fields.

`Sandbox{Tools []string (nil allows all), WritePaths []string, SkipPermissions bool}`; `bare` and `log_violations` are dropped. The decision rules are ported as they are (allow-list, then a realpath prefix check with a separator). `Tools` adds `--allowedTools`; `Tools` or `WritePaths` adds `--permission-prompt-tool stdio`.

### Argv

`[claudewheel, "profile", "exec", "--name", P, "--", claude, "--output-format", "stream-json", "--input-format", "stream-json", …]`, then the flag table from `_process.py`: value flags when non-empty, boolean flags when true; `--add-dir`, `--file`, `--mcp-config`, `--plugin-dir`, `--plugin-url`, `--betas`, `--allowedTools`, `--disallowedTools` repeat the flag per value; `--tools` stays one comma-joined value; debug is `--debug` with or without a filter; `ExtraArgs` last; each SDK tool server adds `mcp__<server>__*` to `--allowedTools`; `--permission-prompt-tool stdio` when there are tools, a sandbox restriction, or `InterceptPermissions`.

### Start

1. Validate the config (required fields, thresholds zero or more).
2. Version check: run `claude -v` directly and parse `\d+\.\d+\.\d+`; below 2.0.0, unparseable, or timed out is a hard error.
3. Spawn with stdin, stdout, and stderr as pipes, and start the reader goroutine.
4. With tools, a non-nil `SupportedDialogs`, or `InterceptPermissions`: send initialize and wait for its response; an error response is a hard error.
5. With tools: send `mcp_set_servers`, wait for its response, then for each server's `tools/list` (the context bounds every wait).

### The reader goroutine

The only reader of stdout; lines up to 16 MiB (longer is a hard error).

- A `control_response` with a pending ID resolves it; an unmatched one becomes `UnknownEvent`.
- `can_use_tool` with a sandbox gets an automatic response and yields `PermissionDecided{Request, Allowed, Message}`; without a sandbox it yields `PermissionRequest`.
- `request_user_dialog` yields `UserDialogRequest`.
- `mcp_message`: `initialize` echoes the proposed `protocolVersion` when it is one of 2025-11-25, 2025-06-18, 2025-03-26, or 2024-11-05, otherwise JSON-RPC error -32602; `notifications/initialized` and `tools/list` (with `_meta.anthropic.alwaysLoad`) are handled internally; `tools/call` runs the handler in its own goroutine (success `{content:[{type:text,text}]}`, handler error `{content:[…], isError:true}`, unknown tool -32602, unknown server or method -32601) and yields `SDKToolCall{Server, Tool, Arguments, IsError, Output}`.
- Any other `control_request` gets an error response (`{"subtype":"error","request_id":R,"error":"claudestream does not handle control request subtype X"}`) and yields `UnknownControlRequest`.
- A non-JSON line yields `NonJSONLine`; a decode type mismatch yields `UndecodableEvent{Raw, Err}`.
- Everything else is parsed, flattened, and appended to an unbounded queue; the reader never blocks.
- EOF waits for the process, fails pending controls with `ErrProcessExited{ExitCode, StderrTail}`, and queues a terminal marker.
- Stderr goes into a ring of the last 200 lines (`Session.Stderr()`); nothing is printed.

### Events

Plain structs with exported fields and an embedded `EventHeader{SessionID, UUID string}`; the `Event` interface has an unexported method, and events can be built by other packages (the miniclaude mock needs this). Types: `SystemInit`, `APIRetry`, `CompactBoundary`, `AssistantText`, `Thinking`, `ToolUse{ToolUseID, Name, Input map[string]any, ParentToolUseID *string}`, `ToolResult{…, Content json.RawMessage, ToolName (from the matching ToolUse), IsError}` with `ContentText()`, `FileWrite` and `FileEdit` (emitted after the matching successful ToolResult, paths resolved against `SystemInit.Cwd`), `StreamDelta{Event map[string]any, ParentToolUseID *string}` with `DeltaType`, `Text`, `ThinkingText`, `PartialJSON`, and `StreamEventType` helpers, `Result` (every field; `ModelUsage map[string]ModelUsage{InputTokens, OutputTokens, CacheReadInputTokens, CacheCreationInputTokens, ContextWindow, CostUSD}`), `RateLimit{Status, ResetsAt *int64, RateLimitType, Utilization}` read from both upstream shapes, `PermissionRequest` (every field in `events.py`), `PermissionDecided`, `UserDialogRequest{RequestID, Dialog (wire dialog_kind), Payload, ToolUseID *string}`, `SDKToolCall`, `UnknownControlRequest`, `BudgetThreshold`, `UnknownEvent{Raw}`, `UnknownContentBlock{Raw}`, `NonJSONLine`, `UndecodableEvent`. Messages are always flattened (no raw mode); only `can_use_tool` is accepted; upstream JSON is decoded leniently about extra fields.

### Turns

- `Send(ctx, text) (*Turn, error)` and `SendBlocks(ctx, blocks)`; a second send while a turn is open returns `ErrTurnActive`.
- `Turn.Next(ctx) (Event, error)` returns `io.EOF` after the `Result` and any `BudgetThreshold` events following it. Events queued between turns are delivered at the start of the next turn.
- The first assistant message whose error contains "not logged in", "invalid authentication", or "401" returns `ErrAuthentication`.
- A context cancellation during a turn, or `ErrAuthentication`, marks the session unusable; later sends return `ErrSessionUnusable`.
- Totals as in the Python: cost from `Result.total_cost_usd`, tokens input plus output from `Result.usage`, turns counted.
- Accessors under a mutex: `SessionID`, `Model`, `Tools`, `ClaudeVersion`, `Cwd`, `MCPServers`, `PermissionMode`, `TurnCount`, `TotalTokens`, `TotalCostUSD`, `FilesModified`, `LastResult`, `PID`, `Stderr`.
- `Close()`: close stdin, wait `ShutdownTimeout`, SIGTERM, wait, SIGKILL; no exit hook, callers defer `Close`.
- Dropped: stuck detection and retry (a wedged CLI waits until the context is cancelled: silence is not a reliable signal), callbacks, hooks, `cancel`, the sync and async split, the cost log.

### Control methods

All take a context: `Interrupt` (returns `still_queued`), `SetPermissionMode`, `SetModel(*string)` (nil omits the field), `GetContextUsage` (missing `totalTokens` or `maxTokens` is a hard error), `RespondAllow(ctx, id, input, perms)`, `RespondDeny`, `RespondDialog`, `RespondDialogCancelled`. Writes to stdin are serialized by a mutex.

### Tools and budget

`Tool{Server, Name, Description string; InputSchema map[string]any; Handler func(ctx, json.RawMessage) (string, error)}`: explicit schemas, no reflection, no injection. `Budget{CostThresholds []float64; TurnThresholds, TokenThresholds []int64}`, each threshold fired once, in the order cost, turns, tokens.

### Agents (package `agent`)

- Strict TOML at `.claudestream/agents/<name>.agent.toml`, decoded with go-toml/v2 refusing unknown fields, required fields checked after decoding.
- `format_version = 2` required (any other value refused, naming 2); `name` required and equal to the file stem; `version` and `prompt_template` required; optional `description` and `model`; optional `[[tools]]` (`name`, `description`, `input_schema` as a table, `server`), `[sandbox]` (`tools`, `write_paths`, `skip_permissions`), `[budget]` (three lists of values zero or more), `[mcp]` (`config_files`, `strict`), `[stream]` (all five required).
- `Load(path)`, `LoadByName(cwd, name)`, `List(cwd)` (sorted; any bad file is a hard error).
- `ResolvePrompt`: an unresolved placeholder, or a `--var` key the template does not contain, is refused.
- `Invoke(def, base Config, vars, handlers)`: missing handlers refused; the definition's sandbox, budget, mcp, and stream override the base; the system prompt is the resolved template; `SessionName` is the agent name.
- The JSON format and its strictspec validation are dropped. Migration is a documented manual step: convert keys one to one, set `format_version = 2`, delete `bare` and `log_violations`, rename to `.agent.toml`. No agent files exist on this machine.

### Command line (`internal/cli`)

No handler calls the effects handle; process work lives in the library.

Common session flags: `--model`/`-m` required; `--profile` required; `--prompt <text>` or `--prompt-from-stdin` (member choice, required); `--new-session`, `--resume <id>`, or `--continue-session` (member choice, required); `--skip-permissions`/`--no-skip-permissions` required (security posture); optional `--cwd`, `--system-prompt`/`-s`, `--from-pr`; optional `--claude-binary` and `--claudewheel-binary` (PATH lookup stated in help); optional `--color` (on unless `NO_COLOR` is set or the stream is not a terminal); optional `--footer`.

| Command | Classification | Behavior |
|---|---|---|
| `ask` | mutating, dry run unsupported (the subprocess's edits, commands, and spend happen outside the effects handle) | prints the final text; declares an `AskResult` payload for `--json`; `Result.is_error` exits 1 with the result text on stderr |
| `stream` | mutating, dry run unsupported | live text on stdout, markers on stderr |
| `send` | mutating, dry run unsupported | detailed human rendering (tool input as JSON, results) |
| `events` | mutating, dry run unsupported, owns stdout | raw stdout lines verbatim through `RawOutput` |
| `agent run` | mutating, dry run unsupported | `--agent-name`/`--agent-file` choice; `--var` repeatable; `--model` required only when the definition has none, refused when it has one; the session flags; refuses agents declaring tools |
| `agent list` | read-only | |
| `agent info`, `agent validate` | read-only | `--agent-name`/`--agent-file` |
| `doctor` | read-only | the resolved claude path and version against 2.0.0, and the claudewheel path; with `--profile`, runs `claudewheel profile exec --name P -- <claude> -v`; exits 1 on any failure |

Dropped: `repl` (miniclaude is the interactive client), `config` (it printed secrets), `send --raw`, `--json-output`. The printer uses miniclaude's "streamed since the last block" rule to de-duplicate deltas.

## Build order

Each slice is checked by reading: every import used and every referenced name present; every error returned or turned into an event, never dropped; wire tags matching the tables above; cross-read against the Python function each piece ports.

1. Skeleton: `go.mod` and `go.sum`, package doc, `errors.go`, `events.go`, `messages.go`, `wire.go`, `experiments/go.mod`.
2. `config.go`, `argv.go`, `sandbox.go`, `budget.go`.
3. `launch.go`, `process.go`.
4. `session.go`, `control.go`, `mcp.go`.
5. The `agent` package.
6. `internal/printer`, `internal/cli`, `cmd/claudestream`.

## The switchover (separate work)

1. claudewheel provides `profile exec`.
2. Compile, vet, and write red-green suites: port the Python tests (renderer chunking, tables, tool lines, dialogs, parsing, argv, handshake, sandbox), a Go stand-in binary replaying the cassettes under `tests/fixtures/`, an opt-in live lane.
3. Confirm the list-flag syntax and protocol against the installed Claude Code.
4. Retire the Python: delete the package, `pyproject.toml`, `uv.lock`, the npm shim, `.strictspec`, `strictspec.toml`, the conftest, and the Python CI; rlsbl targets to `["go"]`; `VERSION`; rewrite the selfdoc docs; changelog entries for every behavior change against 0.15.3; PyPI and npm stay at their last versions.
5. Release claudestream first; then miniclaude drops its `replace` and requires the release. Never a 1.x tag.
6. Swap the installs; restore `~/.local/bin/claude` or configure the binary flags.
7. The gamehome script, `data/tools.json`, and the falsified-texts sweeps.

## Risks

- Uncompiled code: type errors, unused imports, incomplete indirect requirements, and strictcli v0.38.0 API mismatches surface at the first compile.
- No session starts until `claudewheel profile exec` exists and matches the contract.
- Upstream drift: repeated list flags, accepting only `can_use_tool`, the MCP version list, and flags such as `--brief` and `--plugin-url` are unverified against the installed Claude Code.
- Python consumers (the agentsdk scripts, gamehome) stay on the Python or broken.
- Until the switchover, any rlsbl, ark, or selfdoc run here may detect the Go files.
