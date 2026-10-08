+++
title = "Agent Definition Guide"
description = "How to write claudestream agent definitions as strict .agent.toml files, with prompt templates, tools, sandboxes, budgets, MCP servers, and stream options, and run them from Go or the command line."
nav_group = "Guides"
nav_order = 10
+++

# Agent Definition Guide

An agent definition is a reusable session setup: a system prompt template with `{variable}` placeholders, and optionally a model, tools, a sandbox, budget thresholds, external MCP servers, and stream options. The `agent` package loads definitions and starts sessions from them; `claudestream agent` runs and inspects them from the command line. Every function named here is in [the agent package reference](../agent/).

## Where definitions live

A definition is the file `.claudestream/agents/<name>.agent.toml` under a working directory, and `<name>` must equal the file's `name` key. `agent.LoadByName(cwd, name)` loads one by name, `agent.Load(path)` loads one from any path ending in `.agent.toml`, and `agent.List(cwd)` loads every definition in the directory, sorted by name. A missing directory holds no agents, and any file in it that fails to load makes `List` fail.

## The format

```toml
format_version = 2
name = "reviewer"
version = "1.0.0"
description = "Reviews a diff"
model = "sonnet"
prompt_template = "Review the changes in {repo} for {focus}."

[[tools]]
server = "review"
name = "post_comment"
description = "Post a review comment"
input_schema = { type = "object", properties = { body = { type = "string" } }, required = ["body"] }

[sandbox]
tools = ["Read", "Grep", "mcp__review__post_comment"]
write_paths = []
skip_permissions = false

[budget]
cost_thresholds = [0.5, 1.0]
turn_thresholds = [10]
token_thresholds = []

[mcp]
config_files = ["mcp.json"]
strict = true

[stream]
verbose = true
include_partial_messages = true
include_hook_events = false
replay_user_messages = false
exclude_dynamic_prompt_sections = false
```

The file is strict:

- `format_version = 2`, `name`, `version`, and `prompt_template` are required; `description` and `model` are optional, and a `model` present must not be empty.
- Unknown keys are refused everywhere outside a tool's `input_schema`.
- Every `[[tools]]` entry needs `server`, `name`, and `input_schema`, and tool names are unique within the file.
- `[sandbox]` requires `skip_permissions`; `[mcp]` requires both of its keys; `[stream]` requires all of its keys. `[budget]`'s lists are optional.

Every problem a file has is reported at once, each wrapping `agent.ErrInvalidDefinition`.

### Moving from `.agent.json`

claudestream 0.15 and earlier read JSON definitions (`<name>.agent.json`). They are not read any more, and a JSON file in the agents directory, or passed to `Load`, is refused with the steps to convert it by hand:

1. Convert each JSON key to the TOML key of the same name: top-level keys stay top-level, the `sandbox`, `budget`, `mcp`, and `stream` objects become tables, and each entry of the `tools` list becomes a `[[tools]]` table. A JSON `null` becomes an absent key.
2. Set `format_version = 2`.
3. Delete `bare` and `log_violations` from the sandbox table, and add `skip_permissions = false` to it if the key was absent.
4. Give every tool a `server`.
5. Rename the file from `<name>.agent.json` to `<name>.agent.toml`.

## Prompt templates

The template becomes the session's system prompt. A placeholder is `{name}`, where the name is letters, digits, and underscores. `agent.ResolvePrompt` substitutes every placeholder in one pass, so braces inside a value are never read as placeholders; a placeholder without a value and a variable the template has no placeholder for are both refused. `agent.Placeholders` lists a template's placeholders.

## How a definition becomes a session

`agent.BuildConfig(def, base, vars, handlers)` starts from a base `claudestream.Config` and applies the definition:

- the system prompt is the resolved template, and the session name is the agent's name;
- the tools are the definition's tool schemas, each with its handler from `handlers`, keyed by tool name (a declared tool without a handler, and a handler for a tool the definition does not declare, are refused);
- the model is the definition's when it declares one;
- the sandbox, budget, MCP, and stream options are the definition's when it has the table;
- every other field is the base's.

The base must leave the system prompt, the session name, and the tools empty, since the definition sets them. `agent.Invoke` builds the config and starts the session:

```go
def, err := agent.LoadByName(cwd, "reviewer")
if err != nil {
	return err
}
session, err := agent.Invoke(ctx, def, claudestream.Config{
	Profile:           "default",
	ClaudeBinary:      claude,
	ClaudewheelBinary: claudewheel,
	Session:           claudestream.NewSession{},
	Cwd:               cwd,
}, map[string]string{"repo": cwd, "focus": "error handling"}, map[string]agent.Handler{
	"post_comment": postComment,
})
if err != nil {
	return err
}
defer session.Close()
```

From there the session is driven as any other: see [the guide](../guide/).

## The command line

- `claudestream agent run` runs one prompt through a definition, named with `--agent-name` (looked up under `--cwd`, or the current directory) or `--agent-file`, with one `--var key=value` per placeholder. `--model` is required when the definition declares no model and refused when it declares one. A definition with a `[sandbox]` table replaces the command line's permissions, so `--skip-permissions` or `--no-skip-permissions` must match its `skip_permissions`. A definition that declares tools is refused, because their handlers exist only in Go programs.
- `claudestream agent list` lists the definitions in the agents directory with their versions and descriptions.
- `claudestream agent info` prints every field a definition declares.
- `claudestream agent validate` loads a definition as `agent run` would and reports every problem it has.

The [command line reference](../cli-agent/) lists every flag.
