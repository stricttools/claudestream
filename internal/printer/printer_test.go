package printer

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stricttools/claudestream"
)

func delta(text string) claudestream.StreamDelta {
	return claudestream.StreamDelta{Event: map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": text}}}
}

func render(t *testing.T, mode Mode, footer bool, events ...claudestream.Event) (string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	p, err := New(Options{Mode: mode, Stdout: &out, Stderr: &errOut, Footer: footer})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if err := p.Render(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Finish(); err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String()
}

func TestStreamedTextIsNotWrittenTwice(t *testing.T) {
	parent := "toolu_1"
	out, _ := render(t, Live, false,
		delta("Hel"), delta("lo"),
		claudestream.AssistantText{Text: "Hello"},
		claudestream.AssistantText{Text: " world"},
		claudestream.AssistantText{Text: "subagent text", ParentToolUseID: &parent},
		claudestream.StreamDelta{Event: map[string]any{"delta": map[string]any{"type": "text_delta", "text": "sub"}}, ParentToolUseID: &parent},
		claudestream.Result{},
	)
	if out != "Hello world\n" {
		t.Fatalf("%q", out)
	}
}

func TestLiveMarkersGoToStderr(t *testing.T) {
	parent := "p1"
	out, errOut := render(t, Live, true,
		claudestream.Thinking{Text: "hmm"},
		claudestream.ToolUse{Name: "Read", ToolUseID: "1", ParentToolUseID: &parent},
		claudestream.ToolResult{IsError: true},
		claudestream.AssistantText{Text: "done"},
		claudestream.Result{DurationMS: 1234, TotalCostUSD: 0.01234},
		claudestream.NonJSONLine{Line: "garbage"},
	)
	if out != "done\n" {
		t.Errorf("stdout %q", out)
	}
	want := "[thinking...]\n[tool: Read (subagent of p1)]\n[result: error]\n--- Done (1234ms, $0.0123) ---\n[non-JSON line from Claude Code: garbage]\n"
	if errOut != want {
		t.Errorf("stderr\n got: %q\nwant: %q", errOut, want)
	}
}

func TestDetailedRendering(t *testing.T) {
	out, _ := render(t, Detailed, false,
		claudestream.AssistantText{Text: "Looking"},
		claudestream.ToolUse{Name: "Read", Input: map[string]any{"file_path": "<a&b>"}},
		claudestream.ToolResult{Content: []byte(`"contents"`)},
		claudestream.Thinking{Text: "ok"},
		claudestream.Result{},
	)
	want := "Looking\n--- Tool: Read ---\n{\n  \"file_path\": \"<a&b>\"\n}\n--- Result ---\ncontents\n[thinking: ok]\n"
	if out != want {
		t.Fatalf("\n got: %q\nwant: %q", out, want)
	}
}

type unknownEvent struct{ claudestream.EventHeader }

func TestUnknownEventTypeIsAnError(t *testing.T) {
	p, _ := New(Options{Mode: Live, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}})
	if err := p.Render(unknownEvent{}); err == nil {
		t.Fatal("an unknown event type rendered")
	}
	if _, err := New(Options{Mode: 9, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{}}); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

func TestColors(t *testing.T) {
	if NewColors(false).Red("x") != "x" || !strings.Contains(NewColors(true).Red("x"), "\x1b[") {
		t.Fatal("colors")
	}
	on, off := true, false
	if !ColorEnabled(&on, "1", nil) || ColorEnabled(&off, "", nil) {
		t.Fatal("an explicit choice was not taken")
	}
	if ColorEnabled(nil, "1", nil) {
		t.Fatal("NO_COLOR was not honored")
	}
}

func TestTable(t *testing.T) {
	lines, err := Table([]string{"NAME", "VERSION", "DESCRIPTION"}, [][]string{{"日本", "1", "wide"}, {"a", "10.0", ""}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"NAME  VERSION  DESCRIPTION", "日本  1        wide", "a     10.0"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("%q", lines)
	}
	if _, err := Table([]string{"A"}, [][]string{{"1", "2"}}); err == nil {
		t.Fatal("a ragged row was accepted")
	}
}
