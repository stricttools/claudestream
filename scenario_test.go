package claudestream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stricttools/claudestream/internal/vcr"
)

// cassette writes a cassette for the stand-in from records given as Go
// values: cli(...), host(...), raw(...), and exit(...).
func cassette(t *testing.T, records ...map[string]any) string {
	t.Helper()
	lines := []string{`{"kind": "meta", "version": "2.1.281 (Claude Code)"}`}
	for _, r := range records {
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(data))
	}
	path := filepath.Join(t.TempDir(), "cassette.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func cli(payload map[string]any) map[string]any {
	return map[string]any{"kind": "cli", "payload": payload}
}

// host records the shape of a line the host sends: its type and, for a
// control line, its subtype.
func host(kind, subtype string) map[string]any {
	p := map[string]any{"type": kind}
	switch kind {
	case "control_request":
		p["request"] = map[string]any{"subtype": subtype}
	case "control_response":
		p["response"] = map[string]any{"subtype": subtype}
	}
	return map[string]any{"kind": "host", "payload": p}
}

func raw(line string) map[string]any { return map[string]any{"kind": "raw", "raw": line} }

func exit(code int, stderr string) map[string]any {
	return map[string]any{"kind": "exit", "code": code, "stderr": stderr}
}

func initFrame(cwd string) map[string]any {
	return map[string]any{"type": "system", "subtype": "init", "cwd": cwd, "session_id": "s1",
		"tools": []string{"Read", "Write"}, "mcp_servers": []any{}, "model": "claude-test", "permissionMode": "default",
		"claude_code_version": "2.1.281", "uuid": "u0"}
}

func resultFrame(cost float64) map[string]any {
	return map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": 1, "result": "done",
		"session_id": "s1", "total_cost_usd": cost, "usage": map[string]any{"input_tokens": 10, "output_tokens": 5}}
}

func cliRequest(id, subtype string, body map[string]any) map[string]any {
	request := map[string]any{"subtype": subtype}
	for k, v := range body {
		request[k] = v
	}
	return map[string]any{"type": "control_request", "request_id": id, "request": request}
}

// scenarioConfig is replayConfig for a cassette at any path, also logging
// what the host sends.
func scenarioConfig(t *testing.T, path string) (Config, string) {
	t.Helper()
	cfg := replayConfig(t, "simple_send.jsonl")
	t.Setenv(vcr.CassetteEnv, path)
	log := filepath.Join(t.TempDir(), "host.jsonl")
	t.Setenv(vcr.HostLogEnv, log)
	return cfg, log
}

func hostLines(t *testing.T, log string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("host line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func drain(t *testing.T, ctx context.Context, turn *Turn) ([]Event, error) {
	t.Helper()
	var events []Event
	for {
		ev, err := turn.Next(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return events, nil
			}
			return events, err
		}
		events = append(events, ev)
	}
}

func eventTypes(events []Event) []string {
	var out []string
	for _, ev := range events {
		out = append(out, reflect.TypeOf(ev).Name())
	}
	return out
}

func TestSandboxAnswersPermissionsAndTheTurnYieldsEveryEvent(t *testing.T) {
	allowed := t.TempDir()
	outside := filepath.Join(t.TempDir(), "x.txt")
	inside := filepath.Join(allowed, "notes.txt")
	path := cassette(t,
		host("control_request", "initialize"),
		cli(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "response": map[string]any{}}}),
		host("user", ""),
		cli(initFrame("/w")),
		cli(cliRequest("r1", "can_use_tool", map[string]any{"tool_name": "Write", "input": map[string]any{"file_path": outside, "content": "x"}, "tool_use_id": "t1"})),
		host("control_response", "success"),
		cli(cliRequest("r2", "can_use_tool", map[string]any{"tool_name": "Write", "input": map[string]any{"file_path": inside, "content": "hello"}, "tool_use_id": "t2"})),
		host("control_response", "success"),
		cli(cliRequest("r3", "can_use_tool", map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "ls"}, "tool_use_id": "t3"})),
		host("control_response", "success"),
		cli(map[string]any{"type": "assistant", "session_id": "s1", "uuid": "u1", "message": map[string]any{"content": []any{
			map[string]any{"type": "text", "text": "writing"},
			map[string]any{"type": "tool_use", "id": "t2", "name": "Write", "input": map[string]any{"file_path": inside, "content": "hello"}},
			map[string]any{"type": "redacted_thinking", "data": "zz"},
		}}}),
		cli(map[string]any{"type": "user", "session_id": "s1", "message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t2", "content": "ok", "is_error": false},
		}}}),
		raw("this is not json"),
		cli(map[string]any{"type": "mystery", "session_id": "s1"}),
		cli(cliRequest("r4", "hook_callback", map[string]any{"callback_id": "c"})),
		host("control_response", "error"),
		cli(resultFrame(0.5)),
	)
	cfg, log := scenarioConfig(t, path)
	cfg.Sandbox = &Sandbox{Tools: []string{"Read", "Write"}, WritePaths: []string{allowed}}
	cfg.Budget = &Budget{CostThresholds: []float64{0.25, 1}, TurnThresholds: []int64{1}}
	ctx := timeoutCtx(t)
	s, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := s.Send(ctx, "write the file")
	if err != nil {
		t.Fatal(err)
	}
	events, err := drain(t, ctx, turn)
	if err != nil {
		t.Fatalf("turn: %v after %v", err, eventTypes(events))
	}
	want := []string{"SystemInit", "PermissionDecided", "PermissionDecided", "PermissionDecided", "AssistantText", "ToolUse",
		"UnknownContentBlock", "ToolResult", "FileWrite", "NonJSONLine", "UnknownEvent", "UnknownControlRequest", "Result",
		"BudgetThreshold", "BudgetThreshold"}
	if got := eventTypes(events); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("events\n got: %v\nwant: %v", got, want)
	}
	decisions := []PermissionDecided{events[1].(PermissionDecided), events[2].(PermissionDecided), events[3].(PermissionDecided)}
	if decisions[0].Allowed || !decisions[1].Allowed || decisions[2].Allowed {
		t.Errorf("decisions %+v", decisions)
	}
	if fw := events[8].(FileWrite); fw.Path != inside || fw.ContentLength != 5 {
		t.Errorf("file write %+v", fw)
	}
	if tr := events[7].(ToolResult); tr.ToolName != "Write" || tr.ContentText() != "ok" {
		t.Errorf("tool result %+v", tr)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := s.FilesModified(); len(got) != 1 || got[0] != inside {
		t.Errorf("files modified %v", got)
	}
	if s.TurnCount() != 1 || s.TotalCostUSD() != 0.5 || s.TotalTokens() != 15 {
		t.Errorf("totals: turns %d cost %v tokens %d", s.TurnCount(), s.TotalCostUSD(), s.TotalTokens())
	}
	lines := hostLines(t, log)
	answers := map[string]map[string]any{}
	for _, l := range lines {
		if l["type"] == "control_response" {
			resp := l["response"].(map[string]any)
			answers[fmt.Sprint(resp["request_id"])] = resp
		}
	}
	deny := answers["r1"]["response"].(map[string]any)
	if deny["behavior"] != "deny" || !strings.Contains(fmt.Sprint(deny["message"]), outside) {
		t.Errorf("r1 answer %v", answers["r1"])
	}
	allow := answers["r2"]["response"].(map[string]any)
	if allow["behavior"] != "allow" || allow["updatedInput"].(map[string]any)["file_path"] != inside {
		t.Errorf("r2 answer %v", answers["r2"])
	}
	if answers["r3"]["response"].(map[string]any)["behavior"] != "deny" {
		t.Errorf("r3 answer %v", answers["r3"])
	}
	if answers["r4"]["subtype"] != "error" || !strings.Contains(fmt.Sprint(answers["r4"]["error"]), "hook_callback") {
		t.Errorf("r4 answer %v", answers["r4"])
	}
	init := lines[0]["request"].(map[string]any)
	if init["subtype"] != "initialize" || init["request_id"] != "init_1" {
		t.Errorf("first host line %v", lines[0])
	}
}

func TestSDKToolsAreServedOverMCP(t *testing.T) {
	mcp := func(id string, method string, msgID any, params map[string]any) map[string]any {
		message := map[string]any{"jsonrpc": "2.0", "method": method}
		if msgID != nil {
			message["id"] = msgID
		}
		if params != nil {
			message["params"] = params
		}
		return cli(cliRequest(id, "mcp_message", map[string]any{"server_name": "srv", "message": message}))
	}
	path := cassette(t,
		host("control_request", "initialize"),
		cli(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "response": map[string]any{}}}),
		host("control_request", "mcp_set_servers"),
		cli(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "response": map[string]any{"added": []string{"srv"}}}}),
		mcp("m1", "initialize", 0, map[string]any{"protocolVersion": "2025-06-18"}),
		host("control_response", "success"),
		mcp("m2", "notifications/initialized", nil, nil),
		host("control_response", "success"),
		mcp("m3", "tools/list", 1, nil),
		host("control_response", "success"),
		host("user", ""),
		cli(initFrame("/w")),
		mcp("m4", "tools/call", 2, map[string]any{"name": "upper", "arguments": map[string]any{"text": "abc"}}),
		host("control_response", "success"),
		mcp("m5", "tools/call", 3, map[string]any{"name": "fails", "arguments": map[string]any{}}),
		host("control_response", "success"),
		mcp("m6", "tools/call", 4, map[string]any{"name": "nope", "arguments": map[string]any{}}),
		host("control_response", "success"),
		mcp("m7", "resources/list", 5, nil),
		host("control_response", "success"),
		cli(cliRequest("m8", "mcp_message", map[string]any{"server_name": "other", "message": map[string]any{"jsonrpc": "2.0", "method": "tools/list", "id": 6}})),
		host("control_response", "success"),
		mcp("m9", "initialize", 7, map[string]any{"protocolVersion": "1999-01-01"}),
		host("control_response", "success"),
		cli(resultFrame(0)),
	)
	cfg, log := scenarioConfig(t, path)
	cfg.Tools = []Tool{
		{Server: "srv", Name: "upper", Description: "upper-case", InputSchema: map[string]any{"type": "object"},
			Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
				var in struct{ Text string }
				if err := json.Unmarshal(args, &in); err != nil {
					return "", err
				}
				return strings.ToUpper(in.Text), nil
			}},
		{Server: "srv", Name: "fails", Description: "always fails", InputSchema: map[string]any{"type": "object"},
			Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return "", errors.New("it broke") }},
	}
	ctx := timeoutCtx(t)
	s, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := s.Send(ctx, "use the tools")
	if err != nil {
		t.Fatal(err)
	}
	events, err := drain(t, ctx, turn)
	if err != nil {
		t.Fatalf("turn: %v after %v", err, eventTypes(events))
	}
	var calls []SDKToolCall
	unknown := 0
	for _, ev := range events {
		switch e := ev.(type) {
		case SDKToolCall:
			calls = append(calls, e)
		case UnknownControlRequest:
			unknown++
		}
	}
	if len(calls) != 2 || calls[0].Output != "ABC" || calls[0].IsError || !calls[1].IsError || calls[1].Output != "it broke" {
		t.Errorf("tool calls %+v", calls)
	}
	if unknown != 2 { // the unknown method and the unknown server
		t.Errorf("%d unknown control requests in %v", unknown, eventTypes(events))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	byID := map[string]map[string]any{}
	for _, l := range hostLines(t, log) {
		if l["type"] != "control_response" {
			continue
		}
		resp := l["response"].(map[string]any)
		if inner, ok := resp["response"].(map[string]any); ok {
			if m, ok := inner["mcp_response"].(map[string]any); ok {
				byID[fmt.Sprint(resp["request_id"])] = m
			}
		}
	}
	if v := byID["m1"]["result"].(map[string]any)["protocolVersion"]; v != "2025-06-18" {
		t.Errorf("initialize echoed %v", v)
	}
	tools := byID["m3"]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 2 || tools[0].(map[string]any)["name"] != "upper" {
		t.Errorf("tools/list %v", tools)
	}
	if text := byID["m4"]["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"]; text != "ABC" {
		t.Errorf("tools/call %v", byID["m4"])
	}
	if byID["m5"]["result"].(map[string]any)["isError"] != true {
		t.Errorf("failing tool %v", byID["m5"])
	}
	codes := map[string]float64{"m6": -32602, "m7": -32601, "m8": -32601, "m9": -32602}
	for id, code := range codes {
		e, ok := byID[id]["error"].(map[string]any)
		if !ok || e["code"] != code {
			t.Errorf("%s answered %v, want error %v", id, byID[id], code)
		}
	}
}

func TestAProcessThatDiesMidTurnEndsItWithItsExitError(t *testing.T) {
	path := cassette(t,
		host("user", ""),
		cli(initFrame("/w")),
		exit(3, "boom: the CLI crashed\n"),
	)
	cfg, _ := scenarioConfig(t, path)
	ctx := timeoutCtx(t)
	s, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	turn, err := s.Send(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	_, err = drain(t, ctx, turn)
	var exited *ErrProcessExited
	if !errors.As(err, &exited) || exited.ExitCode != 3 || !strings.Contains(strings.Join(exited.StderrTail, "\n"), "boom") {
		t.Fatalf("turn ended with %v", err)
	}
	if _, err := s.Send(ctx, "again"); !errors.As(err, &exited) {
		t.Errorf("a send after the exit: %v", err)
	}
}

func TestPermissionRequestsWithoutASandboxAreForwardedAndAnswered(t *testing.T) {
	path := cassette(t,
		host("control_request", "initialize"),
		cli(map[string]any{"type": "control_response", "response": map[string]any{"subtype": "success", "response": map[string]any{}}}),
		host("user", ""),
		cli(initFrame("/w")),
		cli(cliRequest("r1", "can_use_tool", map[string]any{"tool_name": "Bash", "input": map[string]any{"command": "ls"}, "tool_use_id": "t1"})),
		host("control_response", "success"),
		cli(cliRequest("d1", "request_user_dialog", map[string]any{"dialog_kind": "ask_user_question", "payload": map[string]any{"q": "?"}})),
		host("control_response", "success"),
		cli(resultFrame(0)),
	)
	cfg, log := scenarioConfig(t, path)
	cfg.InterceptPermissions = true
	cfg.SupportedDialogs = []string{"ask_user_question"}
	ctx := timeoutCtx(t)
	s, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := s.Send(ctx, "x")
	if err != nil {
		t.Fatal(err)
	}
	for {
		ev, err := turn.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		switch e := ev.(type) {
		case PermissionRequest:
			if err := s.RespondDeny(ctx, "nope", "no"); !errors.Is(err, ErrUnknownRequest) {
				t.Errorf("deny of an unknown id: %v", err)
			}
			if err := s.RespondDialog(ctx, e.RequestID, "x"); !errors.Is(err, ErrUnknownRequest) {
				t.Errorf("a dialog answer to a permission request: %v", err)
			}
			if err := s.RespondAllow(ctx, e.RequestID, e.ToolInput, nil); err != nil {
				t.Fatal(err)
			}
			if err := s.RespondAllow(ctx, e.RequestID, e.ToolInput, nil); !errors.Is(err, ErrUnknownRequest) {
				t.Errorf("a second answer: %v", err)
			}
		case UserDialogRequest:
			if e.Dialog != "ask_user_question" {
				t.Errorf("dialog %+v", e)
			}
			if err := s.RespondDialogCancelled(ctx, e.RequestID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	lines := hostLines(t, log)
	init := lines[0]["request"].(map[string]any)
	if dialogs, ok := init["supportedDialogKinds"].([]any); !ok || len(dialogs) != 1 {
		t.Errorf("initialize %v", init)
	}
	var behaviors []string
	for _, l := range lines {
		if l["type"] == "control_response" {
			behaviors = append(behaviors, fmt.Sprint(l["response"].(map[string]any)["response"].(map[string]any)["behavior"]))
		}
	}
	if strings.Join(behaviors, ",") != "allow,cancelled" {
		t.Errorf("answers %v", behaviors)
	}
}
