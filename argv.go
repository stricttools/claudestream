package claudestream

import "strings"

// buildArgv returns the full command line for a validated config:
// claudewheel's `profile exec`, then claude in stream-json mode, then the
// flags the config sets, then ExtraArgs. Value flags appear when non-empty
// and boolean flags when true; list flags repeat the flag once per value,
// except --tools, which takes one comma-joined value.
func buildArgv(c Config) []string {
	argv := []string{
		c.ClaudewheelBinary, "profile", "exec", "--name", c.Profile, "--",
		c.ClaudeBinary, "--output-format", "stream-json", "--input-format", "stream-json",
	}
	value := func(flag, v string) {
		if v != "" {
			argv = append(argv, flag, v)
		}
	}
	repeated := func(flag string, values []string) {
		for _, v := range values {
			argv = append(argv, flag, v)
		}
	}
	boolean := func(flag string, on bool) {
		if on {
			argv = append(argv, flag)
		}
	}

	var sessionID, resumeID string
	var continueLast, fork bool
	switch s := c.Session.(type) {
	case NewSession:
		sessionID = s.ID
	case ResumeSession:
		resumeID, fork = s.ID, s.Fork
	case ContinueLastSession:
		continueLast, fork = true, s.Fork
	}

	sandbox := Sandbox{}
	if c.Sandbox != nil {
		sandbox = *c.Sandbox
	}
	allowedTools := append([]string(nil), sandbox.Tools...)
	for _, server := range c.toolServers() {
		allowedTools = append(allowedTools, "mcp__"+server+"__*")
	}
	permissionPromptTool := ""
	if len(c.Tools) > 0 || c.InterceptPermissions || (c.Sandbox != nil && sandbox.restricts()) {
		permissionPromptTool = "stdio"
	}

	var debug DebugOptions
	if c.Debug != nil {
		debug = *c.Debug
	}
	var mcp MCPOptions
	if c.MCP != nil {
		mcp = *c.MCP
	}
	var plugins PluginOptions
	if c.Plugins != nil {
		plugins = *c.Plugins
	}
	stream := c.streamOptions()

	value("--model", c.Model)
	value("--system-prompt", c.SystemPrompt)
	value("--permission-mode", c.PermissionMode)
	value("--permission-prompt-tool", permissionPromptTool)
	value("--resume", resumeID)
	value("--effort", c.Effort)
	value("--json-schema", string(c.JSONSchema))
	value("--fallback-model", c.FallbackModel)
	value("--name", c.SessionName)
	value("--setting-sources", c.SettingSources)
	value("--settings", c.Settings)
	value("--debug-file", debug.File)
	value("--agent", c.AgentName)
	value("--agents", c.AgentsJSON)
	value("--from-pr", c.FromPR)
	value("--session-id", sessionID)

	repeated("--allowedTools", allowedTools)
	repeated("--betas", c.Betas)
	repeated("--add-dir", c.AddDirs)
	if len(c.BuiltinTools) > 0 {
		argv = append(argv, "--tools", strings.Join(c.BuiltinTools, ","))
	}
	repeated("--file", c.Files)
	repeated("--mcp-config", mcp.ConfigFiles)
	repeated("--plugin-dir", plugins.Dirs)
	repeated("--plugin-url", plugins.URLs)

	boolean("--brief", c.Brief)
	boolean("--continue", continueLast)
	boolean("--fork-session", fork)
	boolean("--no-session-persistence", c.NoPersistence)
	boolean("--strict-mcp-config", mcp.Strict)
	boolean("--include-hook-events", stream.IncludeHookEvents)
	boolean("--replay-user-messages", stream.ReplayUserMessages)
	boolean("--exclude-dynamic-system-prompt-sections", stream.ExcludeDynamicPromptSections)
	boolean("--verbose", stream.Verbose)
	boolean("--include-partial-messages", stream.IncludePartialMessages)
	boolean("--dangerously-skip-permissions", sandbox.SkipPermissions)

	if debug.Enabled {
		argv = append(argv, "--debug")
		if debug.Filter != "" {
			argv = append(argv, debug.Filter)
		}
	}

	return append(argv, c.ExtraArgs...)
}
