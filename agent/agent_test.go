package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stricttools/claudestream"
)

const reviewer = `format_version = 2
name = "reviewer"
version = "1.0"
description = "reviews code"
model = "opus"
prompt_template = "Review {target} for {focus_area}."

[[tools]]
server = "srv"
name = "lookup"
description = "looks things up"
input_schema = { type = "object" }

[sandbox]
tools = ["Read"]
write_paths = ["out"]
skip_permissions = false

[budget]
cost_thresholds = [1.0]

[mcp]
config_files = ["m.json"]
strict = true

[stream]
verbose = true
include_partial_messages = false
include_hook_events = false
replay_user_messages = false
exclude_dynamic_prompt_sections = true
`

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(Dir(dir), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAFullDefinition(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "reviewer.agent.toml", reviewer)
	def, err := LoadByName(dir, "reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if def.Model != "opus" || len(def.Tools) != 1 || def.Tools[0].InputSchema["type"] != "object" ||
		def.Sandbox == nil || def.Sandbox.SkipPermissions || def.Budget.CostThresholds[0] != 1 ||
		!def.MCP.Strict || def.Stream.IncludePartialMessages || !def.Stream.ExcludeDynamicPromptSections {
		t.Fatalf("%+v", def)
	}
	if got := strings.Join(Placeholders(def.PromptTemplate), ","); got != "focus_area,target" {
		t.Errorf("placeholders %s", got)
	}
}

func TestLoadRefusals(t *testing.T) {
	cases := map[string]string{
		"wrong format version": strings.Replace(reviewer, "format_version = 2", "format_version = 1", 1),
		"no format version":    strings.Replace(reviewer, "format_version = 2\n", "", 1),
		"unknown key":          reviewer + "\nsurprise = 1\n",
		"name mismatch":        strings.Replace(reviewer, `name = "reviewer"`, `name = "other"`, 1),
		"no prompt":            strings.Replace(reviewer, `prompt_template = "Review {target} for {focus_area}."`, "", 1),
		"no skip_permissions":  strings.Replace(reviewer, "skip_permissions = false\n", "", 1),
		"mcp without strict":   strings.Replace(reviewer, "strict = true\n", "", 1),
		"partial stream":       strings.Replace(reviewer, "verbose = true\n", "", 1),
		"negative budget":      strings.Replace(reviewer, "cost_thresholds = [1.0]", "cost_thresholds = [-1.0]", 1),
		"empty model":          strings.Replace(reviewer, `model = "opus"`, `model = ""`, 1),
		"tool without server":  strings.Replace(reviewer, "server = \"srv\"\n", "", 1),
		"duplicate tool": strings.Replace(reviewer, "[sandbox]", `[[tools]]
server = "srv"
name = "lookup"
input_schema = { type = "object" }

[sandbox]`, 1),
	}
	for name, body := range cases {
		dir := t.TempDir()
		path := write(t, dir, "reviewer.agent.toml", body)
		if _, err := Load(path); err == nil {
			t.Errorf("%s: loaded", name)
		}
	}
}

func TestLoadByNameAndList(t *testing.T) {
	dir := t.TempDir()
	if defs, err := List(dir); err != nil || len(defs) != 0 {
		t.Fatalf("no agents dir: %v %v", defs, err)
	}
	write(t, dir, "reviewer.agent.toml", reviewer)
	write(t, dir, "notes.txt", "ignored")
	defs, err := List(dir)
	if err != nil || len(defs) != 1 {
		t.Fatalf("%v %v", defs, err)
	}
	for _, bad := range []string{"", ".", "..", "a/b"} {
		if _, err := LoadByName(dir, bad); err == nil {
			t.Errorf("LoadByName(%q) succeeded", bad)
		}
	}
	_, err = LoadByName(dir, "missing")
	if !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "reviewer") {
		t.Errorf("missing agent: %v", err)
	}
	write(t, dir, "old.agent.json", "{}")
	if _, err := List(dir); err == nil || !strings.Contains(err.Error(), "old.agent.json") {
		t.Errorf("a legacy JSON agent was listed around: %v", err)
	}
	if _, err := LoadByName("", "reviewer"); err == nil {
		t.Error("an empty cwd was accepted")
	}
}

func TestResolvePrompt(t *testing.T) {
	got, err := ResolvePrompt("Hi {name}, see {other} and {{name}}.", map[string]string{"name": "{other}", "other": "x"})
	if err != nil || got != "Hi {other}, see x and {{other}}." {
		t.Fatalf("%q %v", got, err)
	}
	_, err = ResolvePrompt("{a} {b}", map[string]string{"a": "1", "c": "3"})
	if err == nil || !strings.Contains(err.Error(), "b") || !strings.Contains(err.Error(), "c") {
		t.Fatalf("unresolved and unused: %v", err)
	}
	if got, err := ResolvePrompt("{naïve_1}", map[string]string{"naïve_1": "ok"}); err != nil || got != "ok" {
		t.Fatalf("unicode name: %q %v", got, err)
	}
}

func TestBuildConfig(t *testing.T) {
	dir := t.TempDir()
	def, err := Load(write(t, dir, "reviewer.agent.toml", reviewer))
	if err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{"target": "x.go", "focus_area": "bugs"}
	handler := func(ctx context.Context, args json.RawMessage) (string, error) { return "", nil }
	base := claudestream.Config{Model: "sonnet", Profile: "p", Budget: &claudestream.Budget{}}
	cfg, err := BuildConfig(def, base, vars, map[string]Handler{"lookup": handler})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SystemPrompt != "Review x.go for bugs." || cfg.SessionName != "reviewer" || cfg.Model != "opus" ||
		len(cfg.Tools) != 1 || cfg.Tools[0].Server != "srv" || cfg.Budget != def.Budget || cfg.Profile != "p" {
		t.Fatalf("%+v", cfg)
	}
	if _, err := BuildConfig(def, base, vars, nil); err == nil {
		t.Error("a declared tool without a handler was accepted")
	}
	if _, err := BuildConfig(def, base, vars, map[string]Handler{"lookup": handler, "extra": handler}); err == nil {
		t.Error("a handler for an undeclared tool was accepted")
	}
	base.SystemPrompt = "mine"
	if _, err := BuildConfig(def, base, vars, map[string]Handler{"lookup": handler}); !errors.Is(err, ErrInvalidInvocation) {
		t.Errorf("a base system prompt: %v", err)
	}
}
