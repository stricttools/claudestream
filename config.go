package claudestream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"
)

// Defaults applied when Config.Limits is nil.
const (
	DefaultLineLimit           = 16 << 20
	DefaultShutdownTimeout     = 5 * time.Second
	DefaultVersionCheckTimeout = 2 * time.Second
)

// Config describes one Claude Code session. Model, Profile, ClaudeBinary,
// ClaudewheelBinary, and Session are required; every other field is
// optional and its zero value adds nothing to the command line.
type Config struct {
	// Model is passed as --model.
	Model string
	// Profile is the claudewheel profile the subprocess runs under.
	Profile string
	// ClaudeBinary and ClaudewheelBinary are absolute paths; the library
	// never searches PATH.
	ClaudeBinary      string
	ClaudewheelBinary string

	// Cwd is the subprocess's working directory; empty inherits this
	// process's.
	Cwd string
	// PermissionMode is passed as --permission-mode unvalidated; Claude Code
	// refuses unknown modes.
	PermissionMode string
	// InterceptPermissions routes permission prompts to the consumer as
	// PermissionRequest events even without a Sandbox or Tools.
	InterceptPermissions bool
	// SupportedDialogs lists the dialogs the consumer can render, sent in the
	// initialize handshake; nil omits the key,
	// and a non-nil value (even empty) makes the session send the handshake.
	SupportedDialogs []string

	SystemPrompt string
	// Tools are served to Claude Code over the SDK MCP channel, one MCP
	// server per distinct Tool.Server.
	Tools []Tool
	// Sandbox, when set, answers every permission request itself.
	Sandbox *Sandbox
	// ExtraArgs are appended to the claude command line after every
	// generated flag.
	ExtraArgs []string
	// Env is merged over this process's environment for the subprocess.
	Env map[string]string

	// Session chooses between a new session, resuming one, and continuing
	// the most recent one.
	Session SessionResolution
	// SessionName is passed as --name.
	SessionName string

	Debug   *DebugOptions
	MCP     *MCPOptions
	Plugins *PluginOptions
	// Stream nil means verbose output and partial messages on, the other
	// stream options off.
	Stream *StreamOptions
	// Limits nil means DefaultLineLimit, DefaultShutdownTimeout, and
	// DefaultVersionCheckTimeout.
	Limits *ProcessLimits
	Budget *Budget

	Effort string
	// JSONSchema, when set, is a JSON object passed as --json-schema.
	JSONSchema     json.RawMessage
	FallbackModel  string
	Betas          []string
	AddDirs        []string
	BuiltinTools   []string
	Brief          bool
	Settings       string
	SettingSources string
	Files          []string
	AgentName      string
	AgentsJSON     string
	NoPersistence  bool
	FromPR         string

	// RawOutput, when set, receives every stdout line verbatim, newline
	// included, before it is decoded.
	RawOutput io.Writer
}

// SessionResolution chooses which Claude Code session a Config starts:
// NewSession, ResumeSession, or ContinueLastSession.
type SessionResolution interface {
	sessionResolution()
}

// NewSession starts a new session. A non-empty ID is passed as
// --session-id; empty lets Claude Code choose.
type NewSession struct {
	ID string
}

// ResumeSession resumes the session with ID (--resume). Fork resumes into a
// new session ID (--fork-session).
type ResumeSession struct {
	ID   string
	Fork bool
}

// ContinueLastSession continues the most recent session in the working
// directory (--continue). Fork continues into a new session ID
// (--fork-session).
type ContinueLastSession struct {
	Fork bool
}

func (NewSession) sessionResolution()          {}
func (ResumeSession) sessionResolution()       {}
func (ContinueLastSession) sessionResolution() {}

// DebugOptions turns on Claude Code's debug output.
type DebugOptions struct {
	// Enabled passes --debug; Filter, when set, becomes its argument.
	// Filter requires Enabled.
	Enabled bool
	Filter  string
	// File is passed as --debug-file.
	File string
}

// MCPOptions loads external MCP servers.
type MCPOptions struct {
	// ConfigFiles are passed as one --mcp-config each.
	ConfigFiles []string
	// Strict passes --strict-mcp-config: only servers from ConfigFiles are
	// used.
	Strict bool
}

// PluginOptions loads plugins.
type PluginOptions struct {
	// Dirs are passed as one --plugin-dir each.
	Dirs []string
	// URLs are passed as one --plugin-url each.
	URLs []string
}

// StreamOptions chooses what Claude Code writes to the stream.
type StreamOptions struct {
	Verbose                      bool
	IncludePartialMessages       bool
	IncludeHookEvents            bool
	ReplayUserMessages           bool
	ExcludeDynamicPromptSections bool
}

// defaultStreamOptions is what a nil Config.Stream means.
var defaultStreamOptions = StreamOptions{Verbose: true, IncludePartialMessages: true}

// ProcessLimits bounds the subprocess. Every field must be positive.
type ProcessLimits struct {
	// LineLimit is the longest stdout line accepted, in bytes.
	LineLimit int
	// ShutdownTimeout is how long Close waits at each step (after closing
	// stdin, after SIGTERM).
	ShutdownTimeout time.Duration
	// VersionCheckTimeout bounds `claude -v`.
	VersionCheckTimeout time.Duration
}

// Tool is a tool the session serves to Claude Code through an SDK MCP
// server named Server; the model sees it as mcp__<Server>__<Name>.
type Tool struct {
	Server      string
	Name        string
	Description string
	// InputSchema is the JSON Schema of the tool's arguments.
	InputSchema map[string]any
	// Handler runs the tool. Its text becomes the tool's result; an error
	// becomes an error result carrying the error's message.
	Handler func(ctx context.Context, arguments json.RawMessage) (string, error)
}

func (c Config) streamOptions() StreamOptions {
	if c.Stream == nil {
		return defaultStreamOptions
	}
	return *c.Stream
}

func (c Config) limits() ProcessLimits {
	if c.Limits == nil {
		return ProcessLimits{
			LineLimit:           DefaultLineLimit,
			ShutdownTimeout:     DefaultShutdownTimeout,
			VersionCheckTimeout: DefaultVersionCheckTimeout,
		}
	}
	return *c.Limits
}

// toolServers returns the distinct Tool.Server names in order of first
// appearance.
func (c Config) toolServers() []string {
	var servers []string
	seen := map[string]bool{}
	for _, t := range c.Tools {
		if !seen[t.Server] {
			seen[t.Server] = true
			servers = append(servers, t.Server)
		}
	}
	return servers
}

// Validate reports every problem with the config, each wrapping
// ErrInvalidConfig; nil when the config is usable.
func (c Config) Validate() error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if c.Model == "" {
		add("Model is required")
	}
	if c.Profile == "" {
		add("Profile is required")
	}
	for _, b := range []struct{ field, path string }{
		{"ClaudeBinary", c.ClaudeBinary},
		{"ClaudewheelBinary", c.ClaudewheelBinary},
	} {
		switch {
		case b.path == "":
			add("%s is required", b.field)
		case !filepath.IsAbs(b.path):
			add("%s must be an absolute path, got %q", b.field, b.path)
		}
	}

	switch s := c.Session.(type) {
	case nil:
		add("Session is required: NewSession, ResumeSession, or ContinueLastSession")
	case ResumeSession:
		if s.ID == "" {
			add("Session is ResumeSession with an empty ID")
		}
	case *NewSession, *ResumeSession, *ContinueLastSession:
		add("Session must be a NewSession, ResumeSession, or ContinueLastSession value, not a pointer")
	}

	addEmptyEntries := func(field string, values []string) {
		for i, v := range values {
			if v == "" {
				add("%s[%d] is empty", field, i)
			}
		}
	}
	addEmptyEntries("SupportedDialogs", c.SupportedDialogs)
	addEmptyEntries("Betas", c.Betas)
	addEmptyEntries("AddDirs", c.AddDirs)
	addEmptyEntries("Files", c.Files)
	addEmptyEntries("BuiltinTools", c.BuiltinTools)
	for i, t := range c.BuiltinTools {
		if strings.Contains(t, ",") {
			add("BuiltinTools[%d] %q contains a comma; give one tool per entry", i, t)
		}
	}

	for key := range c.Env {
		if key == "" || strings.Contains(key, "=") {
			add("Env key %q is empty or contains '='", key)
		}
	}

	if len(c.JSONSchema) > 0 {
		var schema map[string]any
		if err := json.Unmarshal(c.JSONSchema, &schema); err != nil || schema == nil {
			add("JSONSchema must be a JSON object")
		}
	}

	if c.Debug != nil && c.Debug.Filter != "" && !c.Debug.Enabled {
		add("Debug.Filter is set but Debug.Enabled is false")
	}
	if c.MCP != nil {
		addEmptyEntries("MCP.ConfigFiles", c.MCP.ConfigFiles)
	}
	if c.Plugins != nil {
		addEmptyEntries("Plugins.Dirs", c.Plugins.Dirs)
		addEmptyEntries("Plugins.URLs", c.Plugins.URLs)
	}
	if c.Limits != nil {
		if c.Limits.LineLimit <= 0 {
			add("Limits.LineLimit must be positive, got %d", c.Limits.LineLimit)
		}
		if c.Limits.ShutdownTimeout <= 0 {
			add("Limits.ShutdownTimeout must be positive, got %s", c.Limits.ShutdownTimeout)
		}
		if c.Limits.VersionCheckTimeout <= 0 {
			add("Limits.VersionCheckTimeout must be positive, got %s", c.Limits.VersionCheckTimeout)
		}
	}

	errs := make([]error, 0, len(problems))
	for _, p := range problems {
		errs = append(errs, fmt.Errorf("%w: %s", ErrInvalidConfig, p))
	}
	if c.Sandbox != nil {
		if err := c.Sandbox.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("%w: Sandbox: %w", ErrInvalidConfig, err))
		}
	}
	if c.Budget != nil {
		if err := c.Budget.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("%w: Budget: %w", ErrInvalidConfig, err))
		}
	}
	if err := validateTools(c.Tools); err != nil {
		errs = append(errs, fmt.Errorf("%w: Tools: %w", ErrInvalidConfig, err))
	}
	return errors.Join(errs...)
}

// validateTools requires every tool to have a server, a name, a schema, and
// a handler, and every server and name pair to be unique.
func validateTools(tools []Tool) error {
	var errs []error
	seen := map[[2]string]bool{}
	for i, t := range tools {
		if t.Server == "" {
			errs = append(errs, fmt.Errorf("tool %d has an empty Server", i))
		}
		if t.Name == "" {
			errs = append(errs, fmt.Errorf("tool %d has an empty Name", i))
		}
		if t.InputSchema == nil {
			errs = append(errs, fmt.Errorf("tool %d (%s/%s) has no InputSchema", i, t.Server, t.Name))
		}
		if t.Handler == nil {
			errs = append(errs, fmt.Errorf("tool %d (%s/%s) has no Handler", i, t.Server, t.Name))
		}
		key := [2]string{t.Server, t.Name}
		if seen[key] {
			errs = append(errs, fmt.Errorf("tool %s/%s is declared more than once", t.Server, t.Name))
		}
		seen[key] = true
	}
	return errors.Join(errs...)
}
