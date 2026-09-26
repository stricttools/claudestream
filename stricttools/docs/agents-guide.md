+++
title = "Agent Definition Guide"
description = "How to define, configure, and run reusable agents in claudestream using .agent.json files, prompt templates, budget limits, sandboxes, and the CLI."
nav_group = "Guides"
nav_order = 10
+++

# Agent Definition Guide

Agents are reusable, self-contained session configurations stored as `.agent.json` files. An agent definition bundles a model, system prompt template, tool schemas, sandbox policy, budget constraints, MCP configuration, and stream options into a single file that can be invoked by name from the CLI or programmatically.

The agent system separates *what* the agent does (defined in the `.agent.json` file) from *how* it is invoked (the CLI flags or Python API call), so the same agent can be run by different callers without re-specifying its configuration.

## File location and discovery

Agent definitions live in `.claudestream/agents/` relative to the working directory. Each file follows the naming convention `<name>.agent.json`, where `<name>` must match the `name` field inside the JSON document. The `discover_agents` function scans this directory at runtime and returns all valid definitions sorted alphabetically:

```
.claudestream/
  agents/
    reviewer.agent.json
    summarizer.agent.json
    translator.agent.json
```

The `discover_agents` function scans this directory and returns all valid definitions sorted by name. It also supports additional search paths and Python package resources as secondary sources. When the same agent name appears in multiple sources, the first occurrence wins and later duplicates are logged as warnings.

## The `.agent.json` format

An agent definition is a JSON document validated against a strictspec schema (format version 1). The schema enforces required fields, type constraints, and value ranges at load time. Invalid documents produce an `AgentValidationError` with pinned diagnostic codes and paths.

### Complete field reference

The `AgentDefinition` struct has 10 fields covering identity, prompt, model selection, sandbox policy, budget limits, tool schemas, MCP server integration, and stream output behavior. Only `name`, `prompt_template`, and `version` are required; all others are optional overrides.

:-: table-schema path="claudestream/_agent.py" target="AgentDefinition"

### Minimal example

```json
{
  "format_version": 1,
  "name": "greeter",
  "version": "1.0.0",
  "prompt_template": "You are a friendly greeter. Always respond with enthusiasm.",
  "model": "sonnet"
}
```

### Full example

```json
{
  "format_version": 1,
  "name": "code-reviewer",
  "version": "1.2.0",
  "description": "Reviews code changes for correctness and style",
  "prompt_template": "You are a code reviewer for the {project} project. Review the following diff and report issues.\n\nLanguage: {language}",
  "model": "opus",
  "sandbox": {
    "tools": ["Read", "Bash", "Grep", "Glob"],
    "write_paths": ["/tmp/reviews"],
    "bare": false,
    "log_violations": true,
    "skip_permissions": false
  },
  "budget": {
    "cost_thresholds": [0.50, 1.00, 2.00],
    "turn_thresholds": [10, 25],
    "token_thresholds": [50000, 100000]
  },
  "tools": [
    {
      "name": "lint",
      "description": "Run the project linter on a file",
      "input_schema": {
        "type": "object",
        "properties": {
          "file_path": { "type": "string", "description": "Path to lint" }
        },
        "required": ["file_path"]
      },
      "server": "lint_server"
    }
  ],
  "mcp": {
    "config_files": [".mcp/servers.json"],
    "strict": true
  },
  "stream": {
    "verbose": true,
    "include_partial_messages": true,
    "include_hook_events": false,
    "replay_user_messages": false,
    "exclude_dynamic_prompt_sections": true
  }
}
```

### The `format_version` gate

Every `.agent.json` document must include a top-level integer `format_version` field set to `1`. This is the strictspec schema gate -- it is distinct from the agent's own `version` field (which is a user-facing semantic version string). Documents with a missing or wrong-typed `format_version` fail validation before any other field is checked.

## Prompt templates and variable substitution

The `prompt_template` field is a system prompt string with `{variable}` placeholders that are resolved at invocation time by passing a `variables` dictionary. Resolution follows a 4-step process: identify all template variables, substitute provided values, verify no original variables remain unresolved, and leave any curly-brace patterns introduced by substituted values untouched.

### How resolution works

1. All `{word}` patterns in the original template are identified as template variables.
2. Each key in the provided variables dict replaces its corresponding `{key}` placeholder with the value.
3. After substitution, any original template variables that remain unresolved cause a `ValueError`.
4. Curly-brace patterns introduced by substituted values (e.g., `{rects}` inside a TypeScript snippet injected as a variable value) are left untouched -- they are not treated as template variables.

### CLI usage

Pass template variables from the command line using repeatable `--var key=value` flags. Each flag sets one variable, and all variables referenced in the prompt template must be provided or the command fails with a resolution error:

```
claudestream agent run reviewer "Review this PR" \
  --var project=myapp \
  --var language=python
```

### Programmatic usage

```python
from claudestream import load_agent, invoke_agent_sync, SessionConfig

agent = load_agent("reviewer")
config = SessionConfig(model="sonnet", profile="default")

with invoke_agent_sync(agent, config, variables={"project": "myapp", "language": "python"}) as session:
    for event in session.send("Review the latest changes"):
        ...
```

## Budget enforcement

The `budget` object defines threshold lists for cost (USD), turns, and tokens. These are not hard limits -- they trigger `BudgetThreshold` events when crossed, allowing the caller to decide how to respond (log, warn, abort).

:-: table-schema path="claudestream/_options.py" target="Budget"

All threshold values must be non-negative. The `validate_budget` function checks this and raises `ValueError` for any negative value.

### Example

```json
{
  "budget": {
    "cost_thresholds": [0.10, 0.50, 1.00],
    "turn_thresholds": [5, 15],
    "token_thresholds": [10000, 50000]
  }
}
```

This fires a `BudgetThreshold` event when cost crosses $0.10, $0.50, or $1.00; when the turn count crosses 5 or 15; and when token usage crosses 10,000 or 50,000.

### Handling threshold events

```python
from claudestream import BudgetThreshold

for event in session.send(prompt):
    if isinstance(event, BudgetThreshold):
        print(f"Budget alert: {event.metric} crossed {event.threshold} (current: {event.current_value})")
```

## Sandbox configuration

The `sandbox` object restricts what tools the agent can use and where it can write, with 5 configurable fields covering tool allow-lists, write-path scoping, CLAUDE.md suppression, violation logging, and permission bypass. When present in the agent definition, it fully overrides any sandbox set on the `SessionConfig`.

:-: table-schema path="claudestream/policy.py" target="Sandbox"

### Tool allow-list

When `tools` is set, only the listed tools are permitted. Any tool call not in the list is denied with a message. When `tools` is `null` or absent, all tools are allowed.

### Write path scoping

When `write_paths` is set, write tools (`Write`, `Edit`, `MultiEdit`) are restricted to paths within the listed directories. Paths are resolved to absolute, symlink-free canonical form before comparison. A write to a path outside the allowed scope is denied.

### Example

```json
{
  "sandbox": {
    "tools": ["Read", "Bash", "Grep"],
    "write_paths": ["/home/user/project/src"],
    "log_violations": true
  }
}
```

## Tool and MCP configuration

### Tool schemas

The `tools` array declares tools the agent can use. Each `ToolSchema` entry has 4 fields: a unique name used in MCP tool calls, an optional human-readable description shown to the model, an optional JSON Schema defining the input parameters, and an optional MCP server name identifying which server hosts the tool.

:-: table-schema path="claudestream/_options.py" target="ToolSchema"

When running an agent programmatically, tool schemas must be paired with handler functions via the `tool_handlers` dictionary. Every tool listed in the definition must have a corresponding handler, or `invoke_agent_sync` / `invoke_agent` raises `ValueError`.

```python
from claudestream import load_agent, invoke_agent_sync, SessionConfig

def lint_handler(file_path: str) -> str:
    return "No issues found"

agent = load_agent("code-reviewer")
config = SessionConfig(model="sonnet", profile="default")

with invoke_agent_sync(agent, config, tool_handlers={"lint": lint_handler}) as session:
    for event in session.send("Check the code"):
        ...
```

### MCP server configuration

The `mcp` object points to external MCP server configuration files and controls strict mode for server name resolution. It has 2 required fields: `config_files` (a list of paths to JSON configuration files defining available servers) and `strict` (a boolean controlling whether unknown server names cause errors or are silently ignored).

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `config_files` | `list[str]` | yes | Paths to MCP server configuration files |
| `strict` | `bool` | yes | Reject unknown MCP server names instead of ignoring them |

When `strict` is `true`, any MCP tool call referencing an unknown server name is an error. When `false`, unknown servers are silently ignored.

### Stream options

The `stream` object controls event stream behavior with 5 required boolean fields that configure verbose output, partial message streaming, hook event inclusion, user message replay on session resume, and dynamic prompt section exclusion.

| Field | Type | Required | Description |
| --- | --- | --- | --- |
| `verbose` | `bool` | yes | Emit verbose protocol output |
| `include_partial_messages` | `bool` | yes | Stream incremental message fragments |
| `include_hook_events` | `bool` | yes | Include hook lifecycle events |
| `replay_user_messages` | `bool` | yes | Re-emit prior user messages when resuming |
| `exclude_dynamic_prompt_sections` | `bool` | yes | Omit dynamic system prompt sections |

All fields are required when the `stream` object is present.

## Configuration merging

When an agent is invoked, the agent definition's fields override the base `SessionConfig` for the fields they set. The merging rule is: if the definition has a non-`None` value for a field, it wins; otherwise the config's value is used. This applies to `model`, `sandbox`, `budget`, `mcp`, and `stream`.

The resolved `prompt_template` (after variable substitution) becomes the session's `system_prompt`. The agent's `name` is used as the session name for session resolution.

Fields that exist only on `SessionConfig` (e.g., `profile`, `cwd`, `binary`, `extra_args`, `env`, `effort`, `debug`) always come from the config and cannot be set in an agent definition.

## Running agents via CLI

The `agent` command group provides 4 subcommands for running, listing, inspecting, and validating agent definitions from the command line without writing any Python code. Each subcommand loads the `.agent.json` file, validates it against the strictspec schema (format version 1), and performs its operation.

### `agent run`

Load an agent definition from a `.agent.json` file or by bare name, resolve prompt template variables, and invoke the agent with a user message. Accepts 6 flags for variable substitution, model override, profile selection, working directory, footer display, and color output:

```
claudestream agent run <name-or-path> "<prompt>" [flags]
```

The first argument is either a bare agent name (resolved from `.claudestream/agents/`) or a path to a `.agent.json` file. The second argument is the user message.

Flags:
- `--var key=value` -- set a template variable (repeatable)
- `--model` / `-m` -- override the model from the definition
- `--profile` -- claudewheel profile to use
- `--cwd` -- working directory
- `--footer` / `--no-footer` -- show/hide cost and timing on stderr (default: show)
- `--color` / `--no-color` -- enable/disable colored output (default: enabled)

A model must be specified either in the definition or via `--model`; providing neither is an error.

```
claudestream agent run summarizer "Summarize this repository" --model sonnet
claudestream agent run translator "Translate to French" --var target_lang=fr
claudestream agent run ./custom/review.agent.json "Check this code" --var project=myapp
```

### `agent list`

Scan the `.claudestream/agents/` directory in the current working directory (or the directory specified by `--cwd`) and print a table of all discovered agents with their name, schema version, and description columns:

```
claudestream agent list [--cwd <dir>]
```

Prints a table with columns: NAME, VERSION, DESCRIPTION.

### `agent info`

Load and display the complete configuration of an agent definition, printing every configured field including name, version, description, model, budget thresholds, sandbox policy, tool schemas, MCP config, and stream options:

```
claudestream agent info <name-or-path>
```

Prints every configured field: name, version, description, model, budget thresholds, sandbox policy, tool schemas, MCP config, and stream options.

### `agent validate`

Check an agent definition for structural and semantic correctness against the strictspec schema (format version 1), verifying required fields, type constraints, non-negative budget values, well-formed tool schemas, and non-empty prompt templates:

```
claudestream agent validate <name-or-path>
```

Validates:
- strictspec schema conformance (format version, required fields, types)
- Budget thresholds are non-negative
- Tool schemas have valid `input_schema` objects
- Prompt template is non-empty

Reports specific errors on failure or prints a success confirmation.

## Programmatic API

### Loading agents

```python
from claudestream import load_agent

# By bare name (looks in .claudestream/agents/)
agent = load_agent("reviewer")

# By path
agent = load_agent("path/to/custom.agent.json")

# With explicit working directory
agent = load_agent("reviewer", cwd="/home/user/project")
```

### Discovering agents

```python
from claudestream._agent import discover_agents

# Default directory
agents = discover_agents()

# Custom search paths and package resources
agents = discover_agents(
    cwd="/project",
    paths=["extra/agents"],
    packages=["my_agents_package"],
)
```

### Sync invocation

```python
from claudestream import load_agent, invoke_agent_sync, SessionConfig, AssistantText

agent = load_agent("reviewer")
config = SessionConfig(model="sonnet", profile="default")

with invoke_agent_sync(agent, config, variables={"project": "myapp"}) as session:
    for event in session.send("Review the code"):
        if isinstance(event, AssistantText):
            print(event.text, end="")
```

### Async invocation

```python
from claudestream._agent import invoke_agent

async with invoke_agent(agent, config, variables={"project": "myapp"}) as session:
    async for event in session.send("Review the code"):
        ...
```

## Deprecated fields

The budget fields `max_cost_usd`, `max_turns`, and `max_tokens` are deprecated. Agent definitions using these fields fail with a migration error directing you to replace them with the threshold-list fields (`cost_thresholds`, `turn_thresholds`, `token_thresholds`).
