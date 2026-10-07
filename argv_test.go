package claudestream

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
)

func baseConfig() Config {
	return Config{Model: "opus", Profile: "work", ClaudeBinary: "/bin/claude", ClaudewheelBinary: "/bin/claudewheel", Session: NewSession{}}
}

func TestArgvOfAMinimalConfig(t *testing.T) {
	got := buildArgv(baseConfig())
	want := []string{"/bin/claudewheel", "profile", "exec", "--name", "work", "--", "/bin/claude",
		"--output-format", "stream-json", "--input-format", "stream-json",
		"--model", "opus", "--verbose", "--include-partial-messages"}
	if !slices.Equal(got, want) {
		t.Fatalf("\n got: %q\nwant: %q", got, want)
	}
}

func TestArgvOfEveryField(t *testing.T) {
	c := baseConfig()
	c.SystemPrompt = "be brief"
	c.PermissionMode = "plan"
	c.Session = ResumeSession{ID: "abc", Fork: true}
	c.Effort = "high"
	c.JSONSchema = json.RawMessage(`{"type":"object"}`)
	c.FallbackModel = "sonnet"
	c.SessionName = "job"
	c.SettingSources = "user,project"
	c.Settings = "/s.json"
	c.Debug = &DebugOptions{Enabled: true, Filter: "api", File: "/d.log"}
	c.AgentName = "reviewer"
	c.AgentsJSON = `{"a":{}}`
	c.FromPR = "12"
	c.Sandbox = &Sandbox{Tools: []string{"Read", "Grep"}, SkipPermissions: true}
	c.Tools = []Tool{{Server: "s1", Name: "a"}, {Server: "s2", Name: "b"}, {Server: "s1", Name: "c"}}
	c.Betas = []string{"b1", "b2"}
	c.AddDirs = []string{"/x", "/y"}
	c.BuiltinTools = []string{"Read", "Bash"}
	c.Files = []string{"f1"}
	c.MCP = &MCPOptions{ConfigFiles: []string{"a.json", "b.json"}, Strict: true}
	c.Plugins = &PluginOptions{Dirs: []string{"/p"}, URLs: []string{"https://u"}}
	c.Brief = true
	c.NoPersistence = true
	c.Stream = &StreamOptions{IncludeHookEvents: true, ReplayUserMessages: true, ExcludeDynamicPromptSections: true}
	c.ExtraArgs = []string{"--extra", "1"}
	got := strings.Join(buildArgv(c), " ")
	want := "/bin/claudewheel profile exec --name work -- /bin/claude --output-format stream-json --input-format stream-json" +
		" --model opus --system-prompt be brief --permission-mode plan --permission-prompt-tool stdio --resume abc --effort high" +
		` --json-schema {"type":"object"} --fallback-model sonnet --name job --setting-sources user,project --settings /s.json` +
		` --debug-file /d.log --agent reviewer --agents {"a":{}} --from-pr 12` +
		" --allowedTools Read --allowedTools Grep --allowedTools mcp__s1__* --allowedTools mcp__s2__*" +
		" --betas b1 --betas b2 --add-dir /x --add-dir /y --tools Read,Bash --file f1 --mcp-config a.json --mcp-config b.json" +
		" --plugin-dir /p --plugin-url https://u --brief --fork-session --no-session-persistence --strict-mcp-config" +
		" --include-hook-events --replay-user-messages --exclude-dynamic-system-prompt-sections --dangerously-skip-permissions" +
		" --debug api --extra 1"
	if got != want {
		t.Fatalf("\n got: %s\nwant: %s", got, want)
	}
}

func TestArgvSessionChoices(t *testing.T) {
	c := baseConfig()
	c.Session = NewSession{ID: "id1"}
	if a := buildArgv(c); !slices.Contains(a, "--session-id") || slices.Contains(a, "--continue") {
		t.Errorf("new with id: %q", a)
	}
	c.Session = ContinueLastSession{}
	if a := buildArgv(c); !slices.Contains(a, "--continue") || slices.Contains(a, "--fork-session") {
		t.Errorf("continue: %q", a)
	}
}

func TestPermissionPromptRouting(t *testing.T) {
	cases := []struct {
		name       string
		change     func(*Config)
		routes     bool
		initialize bool
	}{
		{"plain", func(*Config) {}, false, false},
		{"skip permissions only", func(c *Config) { c.Sandbox = &Sandbox{SkipPermissions: true} }, false, false},
		{"sandbox tools", func(c *Config) { c.Sandbox = &Sandbox{Tools: []string{"Read"}} }, true, true},
		{"empty write paths", func(c *Config) { c.Sandbox = &Sandbox{WritePaths: []string{}} }, true, true},
		{"intercept", func(c *Config) { c.InterceptPermissions = true }, true, true},
		{"tools", func(c *Config) { c.Tools = []Tool{{Server: "s", Name: "t"}} }, true, true},
		{"dialogs", func(c *Config) { c.SupportedDialogs = []string{} }, false, true},
	}
	for _, tc := range cases {
		c := baseConfig()
		tc.change(&c)
		if c.routesPermissionPrompts() != tc.routes || c.sendsInitialize() != tc.initialize {
			t.Errorf("%s: routes %v initialize %v", tc.name, c.routesPermissionPrompts(), c.sendsInitialize())
		}
		if slices.Contains(buildArgv(c), "--permission-prompt-tool") != tc.routes {
			t.Errorf("%s: argv %q", tc.name, buildArgv(c))
		}
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	if err := baseConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	c := Config{
		ClaudeBinary:      "relative/claude",
		Session:           &NewSession{},
		Debug:             &DebugOptions{Filter: "x"},
		BuiltinTools:      []string{"a,b"},
		Env:               map[string]string{"A=B": "1", "": "2"},
		JSONSchema:        json.RawMessage(`[1]`),
		Limits:            &ProcessLimits{},
		Tools:             []Tool{{Server: "s", Name: "t"}, {Server: "s", Name: "t"}},
		AddDirs:           []string{""},
		Budget:            &Budget{CostThresholds: []float64{-1}},
		ClaudewheelBinary: "",
	}
	err := c.Validate()
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("%v", err)
	}
	for _, want := range []string{"Model", "Profile", "ClaudeBinary", "ClaudewheelBinary", "Session", "Debug", "BuiltinTools", "Env", "JSONSchema", "Limits", "AddDirs"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not name %s:\n%v", want, err)
		}
	}
	if c := (Config{Model: "m", Profile: "p", ClaudeBinary: "/c", ClaudewheelBinary: "/w"}); c.Validate() == nil {
		t.Error("a config without a session choice was accepted")
	}
}
