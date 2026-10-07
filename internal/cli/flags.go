package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/stricttools/strictcli/go/strictcli"

	"github.com/stricttools/claudestream"
)

// Selector names: the keys their elections arrive under, and the nouns
// help and errors use.
const (
	promptSelector  = "prompt-source"
	sessionSelector = "session"
)

// Member flag names of the selectors.
const (
	promptFlag          = "prompt"
	promptFromStdinFlag = "prompt-from-stdin"
	newSessionFlag      = "new-session"
	resumeFlag          = "resume"
	continueSessionFlag = "continue-session"
)

// memberValueField is the field a member flag's value arrives under in its
// election.
const memberValueField = "value"

const requiredModelHelp = "Claude model for the session, passed to Claude Code as --model (an alias such as sonnet or opus, or a full model ID)"

// requiredModelFlag is --model for every session command but agent run.
func requiredModelFlag() strictcli.Flag {
	return strictcli.StringFlag("model", requiredModelHelp, strictcli.Short("m"), strictcli.Required())
}

// sessionFlags are the flags every command that starts Claude Code takes,
// except --model; withSystemPrompt adds --system-prompt.
func sessionFlags(withSystemPrompt bool) []strictcli.Flag {
	flags := []strictcli.Flag{
		strictcli.StringFlag("profile",
			"claudewheel profile the session runs under: Claude Code is started as `claudewheel profile exec --name <profile> -- <claude> ...`, which gives it the profile's environment",
			strictcli.Required()),
		strictcli.MemberChoiceFlag(promptSelector,
			"Where the prompt comes from: pass --prompt or --prompt-from-stdin",
			strictcli.Required(),
			strictcli.MemberChoice(
				strictcli.StringFlag(promptFlag, "Prompt text to send", strictcli.Required()),
				"send this flag's value as the prompt"),
			strictcli.MemberChoice(
				strictcli.BoolFlag(promptFromStdinFlag, "Read the prompt from standard input until end of file, trimming surrounding whitespace", strictcli.Required()),
				"read the prompt from standard input"),
		),
		strictcli.MemberChoiceFlag(sessionSelector,
			"Which Claude Code session to use: pass --new-session, --resume, or --continue-session",
			strictcli.Required(),
			strictcli.MemberChoice(
				strictcli.BoolFlag(newSessionFlag, "Start a new session, whose ID Claude Code chooses", strictcli.Required()),
				"start a new session"),
			strictcli.MemberChoice(
				strictcli.StringFlag(resumeFlag, "ID of the session to resume", strictcli.Required()),
				"resume the session with this ID"),
			strictcli.MemberChoice(
				strictcli.BoolFlag(continueSessionFlag, "Continue the most recent session in the working directory", strictcli.Required()),
				"continue the most recent session in the working directory"),
		),
		strictcli.BoolFlag("skip-permissions",
			"Required either way: --skip-permissions runs Claude Code with --dangerously-skip-permissions, so no tool use is checked; --no-skip-permissions keeps Claude Code's permission checks, so tools that need permission are refused",
			strictcli.Required()),
		strictcli.StringFlag("cwd",
			"Working directory for Claude Code; when omitted, Claude Code runs in this process's working directory",
			strictcli.Optional()),
	}
	if withSystemPrompt {
		flags = append(flags, strictcli.StringFlag("system-prompt",
			"System prompt for the session, passed to Claude Code as --system-prompt; when omitted, Claude Code uses its own",
			strictcli.Short("s"), strictcli.Optional()))
	}
	return append(flags,
		strictcli.StringFlag("from-pr",
			"GitHub pull request to load into the session, passed to Claude Code as --from-pr; when omitted, none is loaded",
			strictcli.Optional()),
		strictcli.StringFlag("claude-binary",
			"Absolute path of the Claude Code executable; when omitted, `claude` is looked up on PATH",
			strictcli.Optional()),
		strictcli.StringFlag("claudewheel-binary",
			"Absolute path of the claudewheel executable; when omitted, `claudewheel` is looked up on PATH",
			strictcli.Optional()),
		strictcli.BoolFlag("color",
			"--color colors the output and --no-color does not; when neither is passed, stdout and stderr are each colored only when they are a terminal and NO_COLOR is unset or empty",
			strictcli.Optional()),
		strictcli.BoolFlag("footer",
			"--footer writes a line with the turn's duration and cost to stderr after the reply and --no-footer does not; when neither is passed, the line is written (events with --json never writes it: its stderr carries the --json document)",
			strictcli.Optional()),
	)
}

// sessionOptions is what the session flags say.
type sessionOptions struct {
	// model is empty when --model was not passed (agent run only).
	model             string
	profile           string
	prompt            string
	session           claudestream.SessionResolution
	skipPermissions   bool
	cwd               string
	systemPrompt      string
	fromPR            string
	claudeBinary      string
	claudewheelBinary string
	// colorOverride is nil when neither --color nor --no-color was passed.
	colorOverride *bool
	footer        bool
}

// readSessionOptions reads the session flags, reads the prompt from
// standard input when asked to, and resolves the binaries. It reports every
// problem at once.
func readSessionOptions(kwargs map[string]interface{}, withSystemPrompt bool) (sessionOptions, error) {
	var o sessionOptions
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}
	var err error

	o.model, err = optionalString(kwargs, "model")
	collect(err)
	o.profile, err = requiredString(kwargs, "profile")
	collect(err)
	o.skipPermissions = strictcli.Get[bool](kwargs, "skip_permissions")
	o.cwd, err = optionalString(kwargs, "cwd")
	collect(err)
	if withSystemPrompt {
		o.systemPrompt, err = optionalString(kwargs, "system-prompt")
		collect(err)
	}
	o.fromPR, err = optionalString(kwargs, "from-pr")
	collect(err)
	if color, given := strictcli.GetOpt[bool](kwargs, "color"); given {
		o.colorOverride = &color
	}
	footer, given := strictcli.GetOpt[bool](kwargs, "footer")
	o.footer = !given || footer

	o.session, err = readSessionResolution(kwargs)
	collect(err)
	o.claudeBinary, err = resolveBinary(kwargs, "claude-binary", claudestream.ClaudeProgram)
	collect(err)
	o.claudewheelBinary, err = resolveBinary(kwargs, "claudewheel-binary", claudestream.ClaudewheelProgram)
	collect(err)
	if len(errs) == 0 {
		// Standard input is read only once every other flag is usable.
		o.prompt, err = readPrompt(kwargs)
		collect(err)
	}
	return o, errors.Join(errs...)
}

// config returns the session config the options describe.
func (o sessionOptions) config() claudestream.Config {
	cfg := claudestream.Config{
		Model:             o.model,
		Profile:           o.profile,
		ClaudeBinary:      o.claudeBinary,
		ClaudewheelBinary: o.claudewheelBinary,
		Cwd:               o.cwd,
		SystemPrompt:      o.systemPrompt,
		Session:           o.session,
		FromPR:            o.fromPR,
	}
	if o.skipPermissions {
		cfg.Sandbox = &claudestream.Sandbox{SkipPermissions: true}
	}
	return cfg
}

func readPrompt(kwargs map[string]interface{}) (string, error) {
	election := strictcli.GetElected(kwargs, paramName(promptSelector))
	switch election.Name() {
	case promptFlag:
		prompt := strictcli.Get[string](election.Fields, memberValueField)
		if strings.TrimSpace(prompt) == "" {
			return "", fmt.Errorf("--%s is empty", promptFlag)
		}
		return prompt, nil
	case promptFromStdinFlag:
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", fmt.Errorf("reading the prompt from standard input: %w", err)
		}
		prompt := strings.TrimSpace(string(data))
		if prompt == "" {
			return "", fmt.Errorf("--%s was passed but standard input held no prompt", promptFromStdinFlag)
		}
		return prompt, nil
	}
	return "", fmt.Errorf("unknown %s election %q", promptSelector, election.Name())
}

func readSessionResolution(kwargs map[string]interface{}) (claudestream.SessionResolution, error) {
	election := strictcli.GetElected(kwargs, paramName(sessionSelector))
	switch election.Name() {
	case newSessionFlag:
		return claudestream.NewSession{}, nil
	case resumeFlag:
		id := strictcli.Get[string](election.Fields, memberValueField)
		if id == "" {
			return nil, fmt.Errorf("--%s is empty", resumeFlag)
		}
		return claudestream.ResumeSession{ID: id}, nil
	case continueSessionFlag:
		return claudestream.ContinueLastSession{}, nil
	}
	return nil, fmt.Errorf("unknown %s election %q", sessionSelector, election.Name())
}

// resolveBinary returns the absolute path the flag names, or the program
// found on PATH when the flag was not passed.
func resolveBinary(kwargs map[string]interface{}, flag, program string) (string, error) {
	path, err := optionalString(kwargs, flag)
	if err != nil {
		return "", err
	}
	if path == "" {
		return claudestream.ResolveOnPath(program)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("--%s must be an absolute path, got %q", flag, path)
	}
	return path, nil
}

// requiredString returns a required string flag's value, refusing an empty
// one.
func requiredString(kwargs map[string]interface{}, flag string) (string, error) {
	value := strictcli.Get[string](kwargs, paramName(flag))
	if value == "" {
		return "", fmt.Errorf("--%s is empty", flag)
	}
	return value, nil
}

// optionalString returns an optional string flag's value, empty when the
// flag was not passed, refusing an empty value that was passed.
func optionalString(kwargs map[string]interface{}, flag string) (string, error) {
	value, given := strictcli.GetOpt[string](kwargs, paramName(flag))
	if given && value == "" {
		return "", fmt.Errorf("--%s is empty", flag)
	}
	return value, nil
}

// paramName is the key a flag's value arrives under in kwargs.
func paramName(flag string) string {
	return strings.ReplaceAll(flag, "-", "_")
}
