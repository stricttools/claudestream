package claudestream

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSandboxDecide(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(dir, "src")
	if err := os.MkdirAll(allowed, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "escape")
	if err := os.Symlink("/etc", link); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "afile")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s := Sandbox{Tools: []string{"Read", "Write", "Edit"}, WritePaths: []string{"src"}}
	cases := []struct {
		tool  string
		input map[string]any
		allow bool
		says  string
	}{
		{"Bash", map[string]any{"command": "ls"}, false, "not in sandbox allow-list"},
		{"Read", map[string]any{"file_path": "/etc/passwd"}, true, ""},
		{"Write", map[string]any{"file_path": filepath.Join(allowed, "new", "x.go")}, true, ""},
		{"Write", map[string]any{"file_path": "src/rel.go"}, true, ""},
		{"Write", map[string]any{"file_path": allowed}, true, ""},
		{"Write", map[string]any{"file_path": allowed + "bar/x"}, false, "outside allowed write scope"},
		{"Edit", map[string]any{"file_path": filepath.Join(allowed, "..", "x")}, false, "outside"},
		{"Write", map[string]any{"file_path": filepath.Join(link, "x")}, false, "outside"},
		{"Write", map[string]any{"file_path": filepath.Join(file, "under")}, false, "cannot be resolved"},
		{"Write", map[string]any{}, false, "Empty file_path"},
		{"Write", map[string]any{"file_path": 3}, false, "not a string"},
	}
	for _, c := range cases {
		d := s.Decide(c.tool, c.input, dir)
		if d.Allowed != c.allow || !strings.Contains(d.Message, c.says) {
			t.Errorf("%s %v: %+v", c.tool, c.input, d)
		}
	}
	if d := (Sandbox{}).Decide("Write", map[string]any{"file_path": "/anything"}, dir); !d.Allowed {
		t.Errorf("an unrestricted sandbox denied: %+v", d)
	}
	if err := (Sandbox{Tools: []string{""}, WritePaths: []string{""}}).Validate(); err == nil {
		t.Error("empty entries were accepted")
	}
}

func TestBudgetThresholdsFireOnceInOrder(t *testing.T) {
	tr := newBudgetTracker(&Budget{CostThresholds: []float64{1, 0.5}, TurnThresholds: []int64{2}, TokenThresholds: []int64{100}})
	var totals sessionTotals
	totals.add(Result{TotalCostUSD: 0.6, Usage: &Usage{InputTokens: 50, OutputTokens: 60}})
	got := tr.check(EventHeader{}, totals)
	if len(got) != 2 || got[0].Metric != BudgetMetricCost || got[0].Threshold != 0.5 || got[1].Metric != BudgetMetricTokens {
		t.Fatalf("first turn: %+v", got)
	}
	totals.add(Result{TotalCostUSD: 1.5, Usage: &Usage{InputTokens: 1, OutputTokens: 1}})
	got = tr.check(EventHeader{}, totals)
	if len(got) != 2 || got[0].Threshold != 1 || got[1].Metric != BudgetMetricTurns {
		t.Fatalf("second turn: %+v", got)
	}
	if got := tr.check(EventHeader{}, totals); len(got) != 0 {
		t.Fatalf("fired again: %+v", got)
	}
	if newBudgetTracker(nil).check(EventHeader{}, totals) != nil {
		t.Fatal("a nil budget fired")
	}
	if err := (Budget{TurnThresholds: []int64{-1}}).Validate(); err == nil {
		t.Fatal("a negative threshold was accepted")
	}
}

func parse(t *testing.T, line string) []Event {
	t.Helper()
	return parseLine([]byte(line)).events
}

func TestParseFrames(t *testing.T) {
	if evs := parse(t, "   "); len(evs) != 0 {
		t.Errorf("blank: %v", evs)
	}
	if evs := parse(t, "oops"); len(evs) != 1 || evs[0].(NonJSONLine).Line != "oops" {
		t.Errorf("non-JSON: %v", evs)
	}
	evs := parse(t, `{"type":"assistant","session_id":"s","uuid":"u","parent_tool_use_id":"p","message":{"content":[`+
		`{"type":"text","text":"hi"},{"type":"thinking","thinking":"hmm"},`+
		`{"type":"tool_use","id":"t","name":"Read","input":{"file_path":"/x"}},{"type":"weird"}]}}`)
	types := []string{}
	for _, e := range evs {
		types = append(types, reflect.TypeOf(e).Name())
	}
	if strings.Join(types, ",") != "AssistantText,Thinking,ToolUse,UnknownContentBlock" {
		t.Fatalf("assistant: %v", types)
	}
	if use := evs[2].(ToolUse); use.ParentToolUseID == nil || *use.ParentToolUseID != "p" || use.SessionID != "s" {
		t.Errorf("tool use %+v", use)
	}
	if evs := parse(t, `{"type":"assistant","message":{"content":"not a list"}}`); len(evs) != 1 || reflect.TypeOf(evs[0]).Name() != "UndecodableEvent" {
		t.Errorf("mistyped assistant: %v", evs)
	}
	for _, line := range []string{
		`{"type":"system","subtype":"rate_limit","status":"allowed_warning","resets_at":5,"rateLimitType":"five_hour","utilization":0.9}`,
		`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":5,"rate_limit_type":"five_hour","utilization":0.9}}`,
	} {
		evs := parse(t, line)
		rl, ok := evs[0].(RateLimit)
		if !ok || rl.ResetsAt == nil || *rl.ResetsAt != 5 || rl.RateLimitType != "five_hour" || rl.Utilization != 0.9 {
			t.Errorf("%s: %+v", line, evs)
		}
	}
	evs = parse(t, `{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"ab"}}}`)
	if d := evs[0].(StreamDelta); d.Text() != "ab" || d.DeltaType() != "text_delta" || d.StreamEventType() != "content_block_delta" || d.ThinkingText() != "" {
		t.Errorf("delta %+v", d)
	}
	evs = parse(t, `{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.25,"usage":{"input_tokens":3,"output_tokens":4},`+
		`"modelUsage":{"m":{"inputTokens":3,"outputTokens":4,"cacheReadInputTokens":1,"cacheCreationInputTokens":2,"contextWindow":200000,"costUSD":0.25}}}`)
	r := evs[0].(Result)
	if r.TotalCostUSD != 0.25 || r.ModelUsage["m"].ContextWindow != 200000 || r.Usage.OutputTokens != 4 {
		t.Errorf("result %+v", r)
	}
	evs = parse(t, `{"type":"system","subtype":"init","cwd":"/w","session_id":"s","model":"m","permissionMode":"plan","mcp_servers":[{"name":"x","status":"connected"}]}`)
	if in := evs[0].(SystemInit); in.PermissionMode != "plan" || len(in.MCPServers) != 1 || in.MCPServers[0].Status != "connected" {
		t.Errorf("init %+v", in)
	}
	if p := parseLine([]byte(`{"type":"control_request","request":{"subtype":"can_use_tool"}}`)); p.controlRequest != nil || len(p.events) != 1 {
		t.Errorf("a request without an id: %+v", p)
	}
	if p := parseLine([]byte(`{"type":"assistant","error":"authentication_failed","message":{"content":[]}}`)); !isAuthenticationError(p.assistantError) {
		t.Errorf("authentication error not recognized: %+v", p)
	}
	if isAuthenticationError("rate_limit") || !isAuthenticationError("Invalid Authentication token") {
		t.Error("authentication markers")
	}
}

func TestEventTrackerDerivesFileEventsAfterSuccessfulResults(t *testing.T) {
	tr := newEventTracker()
	tr.track([]Event{SystemInit{Cwd: "/w"}})
	tr.track([]Event{
		ToolUse{ToolUseID: "1", Name: "Write", Input: map[string]any{"file_path": "rel.txt", "content": "héllo"}},
		ToolUse{ToolUseID: "2", Name: "MultiEdit", Input: map[string]any{"file_path": "/a", "edits": []any{
			map[string]any{"old_string": "x"}, map[string]any{"file_path": "/b"}, map[string]any{"file_path": "/a"}}}},
		ToolUse{ToolUseID: "3", Name: "Edit", Input: map[string]any{"file_path": "/c"}},
	})
	out := tr.track([]Event{
		ToolResult{ToolUseID: "1"},
		ToolResult{ToolUseID: "2"},
		ToolResult{ToolUseID: "3", IsError: true},
		ToolResult{ToolUseID: "unknown"},
	})
	var got []string
	for _, e := range out {
		switch v := e.(type) {
		case FileWrite:
			got = append(got, "write "+v.Path+" "+string(rune('0'+v.ContentLength)))
		case FileEdit:
			got = append(got, "edit "+v.Path)
		case ToolResult:
			got = append(got, "result "+v.ToolName)
		}
	}
	want := "result Write|write /w/rel.txt 5|result MultiEdit|edit /a|edit /b|result Edit|result "
	if strings.Join(got, "|") != want {
		t.Fatalf("\n got: %s\nwant: %s", strings.Join(got, "|"), want)
	}
}

func TestToolResultContentText(t *testing.T) {
	cases := map[string]string{
		`"plain"`: "plain",
		`[{"type":"text","text":"a"},{"type":"image","source":{}}]`: "a\n" + `{"type":"image","source":{}}`,
	}
	for content, want := range cases {
		if got := (ToolResult{Content: []byte(content)}).ContentText(); got != want {
			t.Errorf("%s: %q", content, got)
		}
	}
}
