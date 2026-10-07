package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/stricttools/strictcli/go/strictcli"

	"github.com/stricttools/claudestream"
	"github.com/stricttools/claudestream/agent"
	"github.com/stricttools/claudestream/internal/printer"
)

// The agent selector and its member flags.
const (
	agentSelector = "agent-definition"
	agentNameFlag = "agent-name"
	agentFileFlag = "agent-file"
)

func agentSelectorFlag() strictcli.Flag {
	return strictcli.MemberChoiceFlag(agentSelector,
		"Which agent definition to load: pass --agent-name or --agent-file",
		strictcli.Required(),
		strictcli.MemberChoice(
			strictcli.StringFlag(agentNameFlag, "Name of the agent defined in .claudestream/agents/<name>.agent.toml", strictcli.Required()),
			"load the agent by name from the agents directory"),
		strictcli.MemberChoice(
			strictcli.StringFlag(agentFileFlag, "Path of an agent file named <name>.agent.toml", strictcli.Required()),
			"load the agent from this file"),
	)
}

func registerAgentCommands(app *strictcli.App) {
	group := app.Group("agent",
		"Run and inspect agent definitions: strict TOML files at .claudestream/agents/<name>.agent.toml (format_version = 2) that declare a prompt template with {variable} placeholders and, optionally, a model, tools, a sandbox, budget thresholds, MCP servers, and stream options")

	runFlags := []strictcli.Flag{
		agentSelectorFlag(),
		strictcli.StringFlag("var",
			"Template variable as key=value, filling the {key} placeholder of the agent's prompt template; repeat it once per variable",
			strictcli.Repeatable(), strictcli.Unique(false), strictcli.Optional()),
		strictcli.StringFlag("model",
			"Claude model for the session (an alias such as sonnet or opus, or a full model ID); required when the agent declares no model, and refused when it declares one",
			strictcli.Short("m"), strictcli.Optional()),
	}
	group.Command("run",
		"Run one prompt through an agent definition and write the reply's text to stdout as it arrives, with markers on stderr as the stream command writes them. The agent's resolved prompt template becomes the system prompt, its name the session name, and its sandbox, budget, MCP, and stream tables replace the defaults. With --agent-name, the agents directory is searched under --cwd, or under the current directory when --cwd is omitted. Agents that declare tools are refused: their handlers exist only in Go programs that call the agent package. A turn that Claude Code ends with an error exits 1 with the result text on stderr",
		handleAgentRun,
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithDryRunUnsupported(sessionDryRunReason),
		strictcli.WithFlags(append(runFlags, sessionFlags(false)...)...),
	)
	group.Command("list",
		"List the agents in .claudestream/agents/ as a table of name, version, and description; any file there that fails to load is an error",
		handleAgentList,
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithFlags(
			strictcli.StringFlag("cwd",
				"Directory whose .claudestream/agents/ is listed; when omitted, the current directory",
				strictcli.Optional()),
		),
	)
	group.Command("info",
		"Load an agent definition and print every field it declares: name, version, file, description, model, prompt placeholders, tools, sandbox, budget, MCP servers, and stream options. --agent-name searches the agents directory under the current directory",
		handleAgentInfo,
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithFlags(agentSelectorFlag()),
	)
	group.Command("validate",
		"Load an agent definition, checking it as agent run would, and print its name and version; every problem the file has is reported and exits 1. --agent-name searches the agents directory under the current directory",
		handleAgentValidate,
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithFlags(agentSelectorFlag()),
	)
}

// loadAgent loads the definition the agent selector names, resolving a name
// in the agents directory under cwd.
func loadAgent(kwargs map[string]interface{}, cwd string) (*agent.Definition, error) {
	election := strictcli.GetElected(kwargs, paramName(agentSelector))
	value := strictcli.Get[string](election.Fields, memberValueField)
	if value == "" {
		return nil, fmt.Errorf("--%s is empty", election.Name())
	}
	switch election.Name() {
	case agentNameFlag:
		return agent.LoadByName(cwd, value)
	case agentFileFlag:
		return agent.Load(value)
	}
	return nil, fmt.Errorf("unknown %s election %q", agentSelector, election.Name())
}

// agentsCwd is the directory whose agents directory is searched: dir, or the
// current directory when dir is empty.
func agentsCwd(dir string) (string, error) {
	if dir != "" {
		return dir, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("finding the current directory: %w", err)
	}
	return wd, nil
}

// parseVars turns --var key=value values into a map, refusing a value
// without '=', an empty key, and a key given twice.
func parseVars(kwargs map[string]interface{}) (map[string]string, error) {
	raw, _ := strictcli.GetOpt[[]interface{}](kwargs, "var")
	vars := map[string]string{}
	var errs []error
	for _, item := range raw {
		s, ok := item.(string)
		if !ok {
			errs = append(errs, fmt.Errorf("--var value %v is not a string", item))
			continue
		}
		key, value, found := strings.Cut(s, "=")
		switch {
		case !found:
			errs = append(errs, fmt.Errorf("--var must be key=value, got %q", s))
		case key == "":
			errs = append(errs, fmt.Errorf("--var %q has an empty key", s))
		default:
			if _, dup := vars[key]; dup {
				errs = append(errs, fmt.Errorf("--var gives %q more than once", key))
				continue
			}
			vars[key] = value
		}
	}
	return vars, errors.Join(errs...)
}

func handleAgentRun(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
	opts, err := readSessionOptions(kwargs, false)
	if err != nil {
		return fail(ctx, nil, err)
	}
	vars, err := parseVars(kwargs)
	if err != nil {
		return fail(ctx, nil, err)
	}
	dir, err := agentsCwd(opts.cwd)
	if err != nil {
		return fail(ctx, nil, err)
	}
	def, err := loadAgent(kwargs, dir)
	if err != nil {
		return fail(ctx, nil, err)
	}

	var problems []error
	if len(def.Tools) > 0 {
		names := make([]string, 0, len(def.Tools))
		for _, t := range def.Tools {
			names = append(names, t.Server+"/"+t.Name)
		}
		problems = append(problems, fmt.Errorf("agent %s declares tools (%s); their handlers exist only in Go programs that call the agent package, so agent run cannot serve them", def.Name, strings.Join(names, ", ")))
	}
	switch {
	case def.Model != "" && opts.model != "":
		problems = append(problems, fmt.Errorf("agent %s declares model %q; drop --model (the definition's model is used)", def.Name, def.Model))
	case def.Model == "" && opts.model == "":
		problems = append(problems, fmt.Errorf("agent %s declares no model; pass --model", def.Name))
	}
	if def.Sandbox != nil && def.Sandbox.SkipPermissions != opts.skipPermissions {
		flag := "--no-skip-permissions"
		if def.Sandbox.SkipPermissions {
			flag = "--skip-permissions"
		}
		problems = append(problems, fmt.Errorf("agent %s's [sandbox] sets skip_permissions = %t, which replaces the command line's choice; pass %s to match it", def.Name, def.Sandbox.SkipPermissions, flag))
	}
	if len(problems) > 0 {
		return fail(ctx, nil, errors.Join(problems...))
	}

	base := opts.config()
	start := func(runCtx context.Context) (*claudestream.Session, error) {
		return agent.Invoke(runCtx, def, base, vars, nil)
	}
	return runRendered(ctx, printer.Live, opts, start)
}

func handleAgentList(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
	dirFlag, err := optionalString(kwargs, "cwd")
	if err != nil {
		return fail(ctx, nil, err)
	}
	dir, err := agentsCwd(dirFlag)
	if err != nil {
		return fail(ctx, nil, err)
	}
	defs, err := agent.List(dir)
	if err != nil {
		return fail(ctx, nil, err)
	}
	if len(defs) == 0 {
		ctx.Out("No agents in " + agent.Dir(dir))
		return strictcli.Exit(0)
	}
	rows := make([][]string, 0, len(defs))
	for _, d := range defs {
		rows = append(rows, []string{d.Name, d.Version, d.Description})
	}
	lines, err := printer.Table([]string{"NAME", "VERSION", "DESCRIPTION"}, rows)
	if err != nil {
		return fail(ctx, nil, err)
	}
	for _, line := range lines {
		ctx.Out(line)
	}
	return strictcli.Exit(0)
}

func handleAgentInfo(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
	dir, err := agentsCwd("")
	if err != nil {
		return fail(ctx, nil, err)
	}
	def, err := loadAgent(kwargs, dir)
	if err != nil {
		return fail(ctx, nil, err)
	}
	for _, line := range describeAgent(def) {
		ctx.Out(line)
	}
	return strictcli.Exit(0)
}

func handleAgentValidate(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
	dir, err := agentsCwd("")
	if err != nil {
		return fail(ctx, nil, err)
	}
	def, err := loadAgent(kwargs, dir)
	if err != nil {
		return fail(ctx, nil, err)
	}
	ctx.Out(fmt.Sprintf("Valid: %s, version %s (%s)", def.Name, def.Version, def.Path))
	return strictcli.Exit(0)
}

// describeAgent returns the agent info lines for def.
func describeAgent(def *agent.Definition) []string {
	lines := []string{
		"Name:         " + def.Name,
		"Version:      " + def.Version,
		"File:         " + def.Path,
	}
	if def.Description != "" {
		lines = append(lines, "Description:  "+def.Description)
	}
	model := def.Model
	if model == "" {
		model = "(none declared; agent run needs --model)"
	}
	lines = append(lines, "Model:        "+model)
	placeholders := agent.Placeholders(def.PromptTemplate)
	if len(placeholders) == 0 {
		lines = append(lines, "Placeholders: (none)")
	} else {
		lines = append(lines, "Placeholders: "+strings.Join(placeholders, ", "))
	}
	if len(def.Tools) > 0 {
		lines = append(lines, "Tools:")
		for _, t := range def.Tools {
			line := "  - " + t.Server + "/" + t.Name
			if t.Description != "" {
				line += ": " + t.Description
			}
			lines = append(lines, line)
		}
	}
	if s := def.Sandbox; s != nil {
		tools := "all"
		if s.Tools != nil {
			tools = listText(s.Tools)
		}
		writePaths := "unrestricted"
		if s.WritePaths != nil {
			writePaths = listText(s.WritePaths)
		}
		lines = append(lines, fmt.Sprintf("Sandbox:      tools=%s write_paths=%s skip_permissions=%t", tools, writePaths, s.SkipPermissions))
	}
	if b := def.Budget; b != nil {
		lines = append(lines, fmt.Sprintf("Budget:       cost_thresholds=%v turn_thresholds=%v token_thresholds=%v", b.CostThresholds, b.TurnThresholds, b.TokenThresholds))
	}
	if m := def.MCP; m != nil {
		lines = append(lines, fmt.Sprintf("MCP:          config_files=%s strict=%t", listText(m.ConfigFiles), m.Strict))
	}
	if s := def.Stream; s != nil {
		lines = append(lines, fmt.Sprintf("Stream:       verbose=%t include_partial_messages=%t include_hook_events=%t replay_user_messages=%t exclude_dynamic_prompt_sections=%t",
			s.Verbose, s.IncludePartialMessages, s.IncludeHookEvents, s.ReplayUserMessages, s.ExcludeDynamicPromptSections))
	}
	return lines
}

// listText renders values as [a, b, c].
func listText(values []string) string {
	return "[" + strings.Join(values, ", ") + "]"
}
