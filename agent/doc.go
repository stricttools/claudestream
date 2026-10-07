// Package agent loads agent definitions and starts claudestream sessions
// from them.
//
// An agent definition is a strict TOML file at
// .claudestream/agents/<name>.agent.toml under a working directory. It names
// the agent, carries a system prompt template with {variable} placeholders,
// and may declare a model, tool schemas, a sandbox, budget thresholds,
// external MCP servers, and stream options:
//
//	format_version = 2
//	name = "reviewer"
//	version = "1.0.0"
//	description = "Reviews a diff"
//	model = "sonnet"
//	prompt_template = "Review the changes in {repo} for {focus}."
//
//	[[tools]]
//	server = "review"
//	name = "post_comment"
//	description = "Post a review comment"
//	input_schema = { type = "object", properties = { body = { type = "string" } }, required = ["body"] }
//
//	[sandbox]
//	tools = ["Read", "Grep", "mcp__review__post_comment"]
//	write_paths = []
//	skip_permissions = false
//
//	[budget]
//	cost_thresholds = [0.5, 1.0]
//	turn_thresholds = [10]
//	token_thresholds = []
//
//	[mcp]
//	config_files = ["mcp.json"]
//	strict = true
//
//	[stream]
//	verbose = true
//	include_partial_messages = true
//	include_hook_events = false
//	replay_user_messages = false
//	exclude_dynamic_prompt_sections = false
//
// Unknown keys anywhere outside input_schema are refused, as is any
// format_version other than [FormatVersion], and a name that differs from
// the file name without its .agent.toml suffix. Within [sandbox],
// skip_permissions is required; within [mcp], both keys are required; within
// [stream], all five keys are required. Every problem a file has is reported
// at once, each wrapping [ErrInvalidDefinition].
//
// # Migrating from .agent.json
//
// The JSON format (.agent.json, validated by strictspec) is not read. To
// migrate a definition by hand:
//
//  1. Convert each JSON key to the TOML key of the same name, one to one:
//     top-level keys stay top-level, and the sandbox, budget, mcp, and stream
//     objects become [sandbox], [budget], [mcp], and [stream] tables. Each
//     entry of the tools list becomes a [[tools]] table, its input_schema an
//     inline table or a [tools.input_schema] table. A JSON null becomes an
//     absent key.
//  2. Set format_version = 2.
//  3. Delete bare and log_violations from the sandbox table; add
//     skip_permissions = false to it if the key was absent.
//  4. Give every tool a server.
//  5. Rename the file from <name>.agent.json to <name>.agent.toml.
//
// A file still named *.agent.json in the agents directory, or passed to
// [Load], is refused with a pointer to these steps.
package agent
