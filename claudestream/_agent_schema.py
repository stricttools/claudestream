# strictspec generated validator. DO NOT EDIT.
#
# strictspec generator: 0.4.0
# schema:              AgentDefinition (format_version 1)
# regenerate:          strictspec gen --manifest strictspec.toml
#
# Released under the MIT license (unencumbered). This file is machine-generated;
# edit the schema and regenerate, never this file.
# ruff: noqa
from __future__ import annotations

from dataclasses import dataclass, replace

import strictspec
from strictspec import Diagnostic, Value

# GENERATED_BY is the strictspec release that produced this file. It is
# INFORMATIONAL: pairing is on GENERATED_CODE_FORMAT below, so a later release
# of the runtime reads this file unchanged, and no tool may derive a dependency
# floor from this string.
GENERATED_BY = "0.4.0"
# GENERATED_CODE_FORMAT is the shape of generated code this file was written to.
# The runtime pairing guard hard-errors unless this format is one the linked
# runtime reads; the remedy is regeneration.
GENERATED_CODE_FORMAT = 2
SCHEMA_FORMAT_VERSION = 1

# _EMBEDDED_SCHEMA carries the compiled schema (and its imported type-definition
# files and scalar manifest) so the validator is self-contained and does no IO.
_EMBEDDED_SCHEMA = {
    "agent-definition.schema.toml": "# strictspec schema -- claudestream AgentDefinition (GREENFIELD)\n#\n# At-rest documents: .claudestream/agents/<name>.agent.json\n# Field set read from claudestream/_agent.py (AgentDefinition) and _options.py\n# (ToolSchema, Budget, McpOptions, StreamOptions) + policy.py (Sandbox).\n#\n# integer `format_version` gate is NET-NEW: claudestream had no at-rest version gate.\n# The agent's own string `version` field coexists with the strictspec integer\n# `format_version` gate -- the three-way version disambiguation (document format_version\n# vs schema meta_version vs the agent's own semantic version). `version` is NOT dead\n# weight: it is user-facing (rendered by `claudestream agent list/info/validate`).\n\nname = \"AgentDefinition\"\nmeta_version = 1\nformat_version = 1\ndocument_syntax = \"json\"\nrole = \"schema\"\nroot = \"AgentDefinition\"\ndescription = \"A complete claudestream agent definition, loadable from a .agent.json file.\"\n\n[types.AgentDefinition]\ntype = \"record\"\n\n[types.AgentDefinition.fields.name]\ntype = \"string\"\nrequired = true\nmin_length = 1\ndescription = \"Agent identifier, also used as the session name.\"\n\n[types.AgentDefinition.fields.prompt_template]\ntype = \"string\"\nrequired = true\nmin_length = 1\ndescription = \"System prompt with {variable} placeholders to resolve.\"\n\n[types.AgentDefinition.fields.version]\ntype = \"string\"\nrequired = true\ndescription = \"The agent definition's own semantic version (distinct from the strictspec format_version gate).\"\n\n[types.AgentDefinition.fields.description]\ntype = \"string\"\nrequired = false\ndescription = \"Human-readable summary (source default \\\"\\\" -> optional-absent).\"\n\n[types.AgentDefinition.fields.tools]\ntype = \"array\"\nrequired = false\ndescription = \"Tool schemas the agent can use; absent means no tools (source None -> optional-absent).\"\n[types.AgentDefinition.fields.tools.item]\ntype = \"ToolSchema\"\n\n[types.AgentDefinition.fields.sandbox]\ntype = \"Sandbox\"\nrequired = false\n\n[types.AgentDefinition.fields.budget]\ntype = \"Budget\"\nrequired = false\n\n[types.AgentDefinition.fields.model]\ntype = \"string\"\nrequired = false\ndescription = \"Model override; absent falls back to SessionConfig.model.\"\n\n[types.AgentDefinition.fields.mcp]\ntype = \"McpOptions\"\nrequired = false\n\n[types.AgentDefinition.fields.stream]\ntype = \"StreamOptions\"\nrequired = false\n\n# --- named types ---\n\n[types.ToolSchema]\ntype = \"record\"\ndescription = \"Tool schema without handler -- JSON-serializable.\"\n[types.ToolSchema.fields.name]\ntype = \"string\"\nrequired = true\nmin_length = 1\ndescription = \"Unique tool identifier used in MCP tool calls.\"\n[types.ToolSchema.fields.description]\ntype = \"string\"\nrequired = false\n[types.ToolSchema.fields.input_schema]\ntype = \"opaque\"\nrequired = false\nunchecked = true\nunchecked_reason = \"An arbitrary JSON Schema describing a tool's input parameters; strictspec never introspects a consumer's tool input schema. A typo inside the blob is invisible to strictspec by declaration and is inventoried by `strictspec check`.\"\ndescription = \"JSON Schema defining the tool's input parameters.\"\n[types.ToolSchema.fields.server]\ntype = \"string\"\nrequired = false\ndescription = \"MCP server name that hosts this tool.\"\n\n[types.Budget]\ntype = \"record\"\ndescription = \"Cost/turn/token threshold lists. All values non-negative (validate_budget).\"\n[types.Budget.fields.cost_thresholds]\ntype = \"array\"\nrequired = false\ndescription = \"USD amounts that trigger BudgetThreshold events.\"\n[types.Budget.fields.cost_thresholds.item]\ntype = \"number\"\nmin = 0\n[types.Budget.fields.turn_thresholds]\ntype = \"array\"\nrequired = false\n[types.Budget.fields.turn_thresholds.item]\ntype = \"integer\"\nmin = 0\n[types.Budget.fields.token_thresholds]\ntype = \"array\"\nrequired = false\n[types.Budget.fields.token_thresholds.item]\ntype = \"integer\"\nmin = 0\n\n[types.Sandbox]\ntype = \"record\"\ndescription = \"Declarative sandbox configuration.\"\n[types.Sandbox.fields.tools]\ntype = \"array\"\nrequired = false\ndescription = \"Tool allow-list; absent means all tools allowed (source None -> optional-absent).\"\n[types.Sandbox.fields.tools.item]\ntype = \"string\"\n[types.Sandbox.fields.bare]\ntype = \"boolean\"\nrequired = false\ndescription = \"Suppress CLAUDE.md loading (source default false -> optional-absent).\"\n[types.Sandbox.fields.write_paths]\ntype = \"array\"\nrequired = false\n[types.Sandbox.fields.write_paths.item]\ntype = \"string\"\n[types.Sandbox.fields.log_violations]\ntype = \"boolean\"\nrequired = false\n[types.Sandbox.fields.skip_permissions]\ntype = \"boolean\"\nrequired = false\n\n[types.McpOptions]\ntype = \"record\"\ndescription = \"External MCP server configuration. Fields have no source defaults -> required when mcp is present.\"\n[types.McpOptions.fields.config_files]\ntype = \"array\"\nrequired = true\n[types.McpOptions.fields.config_files.item]\ntype = \"string\"\n[types.McpOptions.fields.strict]\ntype = \"boolean\"\nrequired = true\n\n[types.StreamOptions]\ntype = \"record\"\ndescription = \"Stream output behavior. Fields have no source defaults -> required when stream is present.\"\n[types.StreamOptions.fields.verbose]\ntype = \"boolean\"\nrequired = true\n[types.StreamOptions.fields.include_partial_messages]\ntype = \"boolean\"\nrequired = true\n[types.StreamOptions.fields.include_hook_events]\ntype = \"boolean\"\nrequired = true\n[types.StreamOptions.fields.replay_user_messages]\ntype = \"boolean\"\nrequired = true\n[types.StreamOptions.fields.exclude_dynamic_prompt_sections]\ntype = \"boolean\"\nrequired = true\n",
}
_EMBEDDED_MAIN_FILE = "agent-definition.schema.toml"

# Pairing: this file's generated-code format must be one the runtime reads. This
# runs at import, so a runtime that cannot read it hard-errors before any
# validation is attempted.
strictspec.require_generated_code_format(GENERATED_CODE_FORMAT, GENERATED_BY)
_program = strictspec.compile_embedded(_EMBEDDED_SCHEMA, _EMBEDDED_MAIN_FILE)


def validate_bytes(input: bytes, syntax: str) -> tuple[AgentDefinition | None, tuple[Diagnostic, ...]]:
    """RAW-BYTES entry point: lossless parse of input in the given syntax
    ("json" | "toml" | "jsonl"), then validate. Returns the typed root value
    (None when any diagnostic fired) and the ordered diagnostics.
    """
    return validate_bytes_with_evidence(input, syntax, None)


def validate_bytes_with_evidence(input: bytes, syntax: str, evidence: dict | None) -> tuple[AgentDefinition | None, tuple[Diagnostic, ...]]:
    """validate_bytes plus cross-document resolver evidence for the constraint
    vocabulary.
    """
    result = _program.validate_with_evidence(input, syntax, evidence)
    if not result.valid:
        return None, result.diagnostics
    v = strictspec.load_value(input, syntax)
    return _bind_AgentDefinition(v), result.diagnostics


def validate_value(v: Value) -> tuple[AgentDefinition | None, tuple[Diagnostic, ...]]:
    """TAGGED-VALUE entry point: validate an already-parsed tagged document value
    (from strictspec.load_value or a typed constructor). Raw untagged dicts are
    never accepted.
    """
    result = _program.validate_value(v)
    if not result.valid:
        return None, result.diagnostics
    return _bind_AgentDefinition(v), result.diagnostics


@dataclass(frozen=True, kw_only=True)
class AgentDefinition:
    """Frozen typed binding of the "AgentDefinition" record. Immutable; use with_* for
    copy-on-write.
    """

    name: str
    prompt_template: str
    version: str
    description: str
    tools: list[ToolSchema]
    sandbox: Sandbox | None = None
    budget: Budget | None = None
    model: str
    mcp: McpOptions | None = None
    stream: StreamOptions | None = None

    def with_name(self, v: str) -> AgentDefinition:
        return replace(self, name=v)

    def with_prompt_template(self, v: str) -> AgentDefinition:
        return replace(self, prompt_template=v)

    def with_version(self, v: str) -> AgentDefinition:
        return replace(self, version=v)

    def with_description(self, v: str) -> AgentDefinition:
        return replace(self, description=v)

    def with_tools(self, v: list[ToolSchema]) -> AgentDefinition:
        return replace(self, tools=v)

    def with_sandbox(self, v: Sandbox | None) -> AgentDefinition:
        return replace(self, sandbox=v)

    def with_budget(self, v: Budget | None) -> AgentDefinition:
        return replace(self, budget=v)

    def with_model(self, v: str) -> AgentDefinition:
        return replace(self, model=v)

    def with_mcp(self, v: McpOptions | None) -> AgentDefinition:
        return replace(self, mcp=v)

    def with_stream(self, v: StreamOptions | None) -> AgentDefinition:
        return replace(self, stream=v)


def _bind_AgentDefinition(v: Value) -> AgentDefinition | None:
    if v.kind() != strictspec.Kind.RECORD:
        return None
    f_name = v.field("name")
    f_prompt_template = v.field("prompt_template")
    f_version = v.field("version")
    f_description = v.field("description")
    f_tools = v.field("tools")
    f_sandbox = v.field("sandbox")
    f_budget = v.field("budget")
    f_model = v.field("model")
    f_mcp = v.field("mcp")
    f_stream = v.field("stream")
    return AgentDefinition(
        name=(f_name[0].string()[0] if f_name[1] else ""),
        prompt_template=(f_prompt_template[0].string()[0] if f_prompt_template[1] else ""),
        version=(f_version[0].string()[0] if f_version[1] else ""),
        description=(f_description[0].string()[0] if f_description[1] else ""),
        tools=([_bind_ToolSchema(e) for e in f_tools[0].items()] if f_tools[1] else []),
        sandbox=(_bind_Sandbox(f_sandbox[0]) if f_sandbox[1] else None),
        budget=(_bind_Budget(f_budget[0]) if f_budget[1] else None),
        model=(f_model[0].string()[0] if f_model[1] else ""),
        mcp=(_bind_McpOptions(f_mcp[0]) if f_mcp[1] else None),
        stream=(_bind_StreamOptions(f_stream[0]) if f_stream[1] else None),
    )


@dataclass(frozen=True, kw_only=True)
class ToolSchema:
    """Frozen typed binding of the "ToolSchema" record. Immutable; use with_* for
    copy-on-write.
    """

    name: str
    description: str
    input_schema: Value
    server: str

    def with_name(self, v: str) -> ToolSchema:
        return replace(self, name=v)

    def with_description(self, v: str) -> ToolSchema:
        return replace(self, description=v)

    def with_input_schema(self, v: Value) -> ToolSchema:
        return replace(self, input_schema=v)

    def with_server(self, v: str) -> ToolSchema:
        return replace(self, server=v)


def _bind_ToolSchema(v: Value) -> ToolSchema | None:
    if v.kind() != strictspec.Kind.RECORD:
        return None
    f_name = v.field("name")
    f_description = v.field("description")
    f_input_schema = v.field("input_schema")
    f_server = v.field("server")
    return ToolSchema(
        name=(f_name[0].string()[0] if f_name[1] else ""),
        description=(f_description[0].string()[0] if f_description[1] else ""),
        input_schema=(f_input_schema[0] if f_input_schema[1] else Value(None, "json")),
        server=(f_server[0].string()[0] if f_server[1] else ""),
    )


@dataclass(frozen=True, kw_only=True)
class Budget:
    """Frozen typed binding of the "Budget" record. Immutable; use with_* for
    copy-on-write.
    """

    cost_thresholds: list[float]
    turn_thresholds: list[int]
    token_thresholds: list[int]

    def with_cost_thresholds(self, v: list[float]) -> Budget:
        return replace(self, cost_thresholds=v)

    def with_turn_thresholds(self, v: list[int]) -> Budget:
        return replace(self, turn_thresholds=v)

    def with_token_thresholds(self, v: list[int]) -> Budget:
        return replace(self, token_thresholds=v)


def _bind_Budget(v: Value) -> Budget | None:
    if v.kind() != strictspec.Kind.RECORD:
        return None
    f_cost_thresholds = v.field("cost_thresholds")
    f_turn_thresholds = v.field("turn_thresholds")
    f_token_thresholds = v.field("token_thresholds")
    return Budget(
        cost_thresholds=([e.number()[0] for e in f_cost_thresholds[0].items()] if f_cost_thresholds[1] else []),
        turn_thresholds=([e.int()[0] for e in f_turn_thresholds[0].items()] if f_turn_thresholds[1] else []),
        token_thresholds=([e.int()[0] for e in f_token_thresholds[0].items()] if f_token_thresholds[1] else []),
    )


@dataclass(frozen=True, kw_only=True)
class Sandbox:
    """Frozen typed binding of the "Sandbox" record. Immutable; use with_* for
    copy-on-write.
    """

    tools: list[str]
    bare: bool
    write_paths: list[str]
    log_violations: bool
    skip_permissions: bool

    def with_tools(self, v: list[str]) -> Sandbox:
        return replace(self, tools=v)

    def with_bare(self, v: bool) -> Sandbox:
        return replace(self, bare=v)

    def with_write_paths(self, v: list[str]) -> Sandbox:
        return replace(self, write_paths=v)

    def with_log_violations(self, v: bool) -> Sandbox:
        return replace(self, log_violations=v)

    def with_skip_permissions(self, v: bool) -> Sandbox:
        return replace(self, skip_permissions=v)


def _bind_Sandbox(v: Value) -> Sandbox | None:
    if v.kind() != strictspec.Kind.RECORD:
        return None
    f_tools = v.field("tools")
    f_bare = v.field("bare")
    f_write_paths = v.field("write_paths")
    f_log_violations = v.field("log_violations")
    f_skip_permissions = v.field("skip_permissions")
    return Sandbox(
        tools=([e.string()[0] for e in f_tools[0].items()] if f_tools[1] else []),
        bare=(f_bare[0].bool()[0] if f_bare[1] else False),
        write_paths=([e.string()[0] for e in f_write_paths[0].items()] if f_write_paths[1] else []),
        log_violations=(f_log_violations[0].bool()[0] if f_log_violations[1] else False),
        skip_permissions=(f_skip_permissions[0].bool()[0] if f_skip_permissions[1] else False),
    )


@dataclass(frozen=True, kw_only=True)
class McpOptions:
    """Frozen typed binding of the "McpOptions" record. Immutable; use with_* for
    copy-on-write.
    """

    config_files: list[str]
    strict: bool

    def with_config_files(self, v: list[str]) -> McpOptions:
        return replace(self, config_files=v)

    def with_strict(self, v: bool) -> McpOptions:
        return replace(self, strict=v)


def _bind_McpOptions(v: Value) -> McpOptions | None:
    if v.kind() != strictspec.Kind.RECORD:
        return None
    f_config_files = v.field("config_files")
    f_strict = v.field("strict")
    return McpOptions(
        config_files=([e.string()[0] for e in f_config_files[0].items()] if f_config_files[1] else []),
        strict=(f_strict[0].bool()[0] if f_strict[1] else False),
    )


@dataclass(frozen=True, kw_only=True)
class StreamOptions:
    """Frozen typed binding of the "StreamOptions" record. Immutable; use with_* for
    copy-on-write.
    """

    verbose: bool
    include_partial_messages: bool
    include_hook_events: bool
    replay_user_messages: bool
    exclude_dynamic_prompt_sections: bool

    def with_verbose(self, v: bool) -> StreamOptions:
        return replace(self, verbose=v)

    def with_include_partial_messages(self, v: bool) -> StreamOptions:
        return replace(self, include_partial_messages=v)

    def with_include_hook_events(self, v: bool) -> StreamOptions:
        return replace(self, include_hook_events=v)

    def with_replay_user_messages(self, v: bool) -> StreamOptions:
        return replace(self, replay_user_messages=v)

    def with_exclude_dynamic_prompt_sections(self, v: bool) -> StreamOptions:
        return replace(self, exclude_dynamic_prompt_sections=v)


def _bind_StreamOptions(v: Value) -> StreamOptions | None:
    if v.kind() != strictspec.Kind.RECORD:
        return None
    f_verbose = v.field("verbose")
    f_include_partial_messages = v.field("include_partial_messages")
    f_include_hook_events = v.field("include_hook_events")
    f_replay_user_messages = v.field("replay_user_messages")
    f_exclude_dynamic_prompt_sections = v.field("exclude_dynamic_prompt_sections")
    return StreamOptions(
        verbose=(f_verbose[0].bool()[0] if f_verbose[1] else False),
        include_partial_messages=(f_include_partial_messages[0].bool()[0] if f_include_partial_messages[1] else False),
        include_hook_events=(f_include_hook_events[0].bool()[0] if f_include_hook_events[1] else False),
        replay_user_messages=(f_replay_user_messages[0].bool()[0] if f_replay_user_messages[1] else False),
        exclude_dynamic_prompt_sections=(f_exclude_dynamic_prompt_sections[0].bool()[0] if f_exclude_dynamic_prompt_sections[1] else False),
    )


