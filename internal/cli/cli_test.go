package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stricttools/claudestream/internal/vcr"
)

// childEnv makes the test binary run the claudestream command line on its
// arguments; the claude stand-in is the same binary in replay mode, started
// through a wrapper that clears it.
const childEnv = "CLAUDESTREAM_CLI_TEST_CHILD"

func TestMain(m *testing.M) {
	switch {
	case os.Getenv(childEnv) == "1":
		NewApp("0.0.0-test").Run()
		return
	case vcr.Active():
		os.Exit(vcr.Main(os.Args[1:]))
	}
	os.Exit(m.Run())
}

type harness struct {
	t           *testing.T
	dir         string
	claude      string
	claudewheel string
}

// newHarness writes the claude and claudewheel stand-ins replaying the
// cassette whose records are given.
func newHarness(t *testing.T, records ...map[string]any) *harness {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	lines := []string{`{"kind": "meta", "version": "2.1.281 (Claude Code)"}`}
	for _, r := range records {
		data, _ := json.Marshal(r)
		lines = append(lines, string(data))
	}
	cassette := filepath.Join(dir, "cassette.jsonl")
	if err := os.WriteFile(cassette, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: dir, claude: filepath.Join(dir, "claude"), claudewheel: filepath.Join(dir, "claudewheel")}
	claude := "#!/bin/sh\nunset " + childEnv + "\nexport " + vcr.ModeEnv + "=replay " + vcr.CassetteEnv + "=" + cassette + " " + vcr.TimeoutEnv + "=10s\nexec " + self + " \"$@\"\n"
	wheel := "#!/bin/sh\n[ \"$1 $2 $3\" = \"profile exec --name\" ] || exit 2\nshift 4\n[ \"$1\" = \"--\" ] || exit 2\nshift\nexec \"$@\"\n"
	for path, body := range map[string]string{h.claude: claude, h.claudewheel: wheel} {
		if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// run runs claudestream with args and the session flags, returning stdout,
// stderr, and the exit status.
func (h *harness) run(stdin string, args ...string) (string, string, int) {
	h.t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append(os.Environ(), childEnv+"=1")
	cmd.Dir = h.dir
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errOut
	err := cmd.Run()
	code := 0
	if exitErr, ok := err.(*exec.ExitError); ok {
		code = exitErr.ExitCode()
	} else if err != nil {
		h.t.Fatal(err)
	}
	return out.String(), errOut.String(), code
}

func (h *harness) session(command string, extra ...string) []string {
	return append([]string{command, "--model", "haiku", "--profile", "work", "--new-session", "--no-skip-permissions",
		"--claude-binary", h.claude, "--claudewheel-binary", h.claudewheel, "--no-color"}, extra...)
}

func cli(payload map[string]any) map[string]any {
	return map[string]any{"kind": "cli", "payload": payload}
}

func hostUser() map[string]any {
	return map[string]any{"kind": "host", "payload": map[string]any{"type": "user"}}
}

func successfulTurn() []map[string]any {
	return []map[string]any{
		hostUser(),
		cli(map[string]any{"type": "system", "subtype": "init", "cwd": "/w", "session_id": "s1", "model": "claude-test"}),
		cli(map[string]any{"type": "stream_event", "session_id": "s1", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "po"}}}),
		cli(map[string]any{"type": "stream_event", "session_id": "s1", "event": map[string]any{"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "ng"}}}),
		cli(map[string]any{"type": "assistant", "session_id": "s1", "message": map[string]any{"content": []any{map[string]any{"type": "text", "text": "pong"}}}}),
		cli(map[string]any{"type": "result", "subtype": "success", "is_error": false, "num_turns": 1, "result": "pong", "session_id": "s1",
			"total_cost_usd": 0.0012, "duration_ms": 1500, "usage": map[string]any{"input_tokens": 3, "output_tokens": 1}}),
	}
}

func TestAskPrintsTheFinalText(t *testing.T) {
	h := newHarness(t, successfulTurn()...)
	out, errOut, code := h.run("", h.session("ask", "--prompt", "ping", "--no-footer")...)
	if code != 0 || out != "pong\n" {
		t.Fatalf("exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}

func TestAskJSONPayload(t *testing.T) {
	h := newHarness(t, successfulTurn()...)
	out, errOut, code := h.run("", h.session("ask", "--prompt", "ping", "--json")...)
	if code != 0 {
		t.Fatalf("exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if !strings.Contains(out+errOut, `"session_id":"s1"`) && !strings.Contains(out+errOut, `"session_id": "s1"`) {
		t.Fatalf("no payload:\nstdout %q\nstderr %q", out, errOut)
	}
}

func TestStreamWritesTextOnceWithAFooter(t *testing.T) {
	h := newHarness(t, successfulTurn()...)
	out, errOut, code := h.run("", h.session("stream", "--prompt", "ping")...)
	if code != 0 || out != "pong\n" || !strings.Contains(errOut, "--- Done (1500ms, $0.0012) ---") {
		t.Fatalf("exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}

func TestEventsCopiesTheRawLines(t *testing.T) {
	h := newHarness(t, successfulTurn()...)
	out, errOut, code := h.run("", h.session("events", "--prompt", "ping", "--no-footer")...)
	if code != 0 || strings.Count(out, "\n") != 5 || !strings.Contains(out, `"subtype":"init"`) {
		t.Fatalf("exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}

func TestPromptFromStdin(t *testing.T) {
	h := newHarness(t, successfulTurn()...)
	args := append([]string{"ask", "--model", "haiku", "--profile", "work", "--new-session", "--no-skip-permissions",
		"--claude-binary", h.claude, "--claudewheel-binary", h.claudewheel, "--prompt-from-stdin", "--no-footer"})
	if out, errOut, code := h.run("  ping\n", args...); code != 0 || out != "pong\n" {
		t.Fatalf("exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if _, errOut, code := h.run("   \n", args...); code == 0 || !strings.Contains(errOut, "held no prompt") {
		t.Fatalf("an empty stdin prompt: exit %d, %q", code, errOut)
	}
}

func TestAnErrorResultExitsOneWithItsText(t *testing.T) {
	records := successfulTurn()
	records[len(records)-1] = cli(map[string]any{"type": "result", "subtype": "error_during_execution", "is_error": true, "result": "it failed", "session_id": "s1"})
	for _, command := range []string{"ask", "stream", "send", "events"} {
		h := newHarness(t, records...)
		_, errOut, code := h.run("", h.session(command, "--prompt", "ping")...)
		if code != 1 || !strings.Contains(errOut, "it failed") {
			t.Errorf("%s: exit %d, stderr %q", command, code, errOut)
		}
	}
}

func TestSessionFlagRefusals(t *testing.T) {
	h := newHarness(t)
	cases := [][]string{
		{"ask", "--profile", "work", "--prompt", "x", "--new-session", "--no-skip-permissions"},
		{"ask", "--model", "m", "--profile", "work", "--prompt", "x", "--new-session"},
		{"ask", "--model", "m", "--profile", "work", "--prompt", "x", "--no-skip-permissions"},
		{"ask", "--model", "m", "--profile", "work", "--prompt", "  ", "--new-session", "--no-skip-permissions", "--claude-binary", h.claude, "--claudewheel-binary", h.claudewheel},
		{"ask", "--model", "m", "--profile", "work", "--prompt", "x", "--new-session", "--no-skip-permissions", "--claude-binary", "relative/claude", "--claudewheel-binary", h.claudewheel},
		{"ask", "--model", "m", "--profile", "work", "--prompt", "x", "--new-session", "--no-skip-permissions", "--dry-run"},
	}
	for _, args := range cases {
		if _, errOut, code := h.run("", args...); code == 0 {
			t.Errorf("%v accepted: %s", args, errOut)
		}
	}
}

func TestDoctor(t *testing.T) {
	h := newHarness(t)
	out, errOut, code := h.run("", "doctor", "--claude-binary", h.claude, "--claudewheel-binary", h.claudewheel)
	if code != 0 || !strings.Contains(out, "[ok]") || !strings.Contains(out, "2.1.281") {
		t.Fatalf("exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	out, _, code = h.run("", "doctor", "--claude-binary", h.claude, "--claudewheel-binary", h.claudewheel, "--profile", "work")
	if code != 0 || strings.Count(out, "[ok]") < 3 {
		t.Fatalf("with a profile: exit %d\n%s", code, out)
	}
	out, _, code = h.run("", "doctor", "--claude-binary", filepath.Join(h.dir, "missing"), "--claudewheel-binary", h.claudewheel)
	if code != 1 || !strings.Contains(out, "[FAIL]") {
		t.Fatalf("a missing claude: exit %d\n%s", code, out)
	}
}

func TestAgentCommands(t *testing.T) {
	h := newHarness(t, successfulTurn()...)
	agents := filepath.Join(h.dir, ".claudestream", "agents")
	if err := os.MkdirAll(agents, 0o755); err != nil {
		t.Fatal(err)
	}
	def := "format_version = 2\nname = \"pinger\"\nversion = \"1\"\ndescription = \"pings\"\nprompt_template = \"Answer {word}.\"\n"
	if err := os.WriteFile(filepath.Join(agents, "pinger.agent.toml"), []byte(def), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, errOut, code := h.run("", "agent", "list"); code != 0 || !strings.Contains(out, "pinger") {
		t.Fatalf("list: exit %d\n%s\n%s", code, out, errOut)
	}
	if out, _, code := h.run("", "agent", "validate", "--agent-name", "pinger"); code != 0 || !strings.Contains(out, "Valid: pinger") {
		t.Fatalf("validate: exit %d\n%s", code, out)
	}
	if out, _, code := h.run("", "agent", "info", "--agent-name", "pinger"); code != 0 || !strings.Contains(out, "word") {
		t.Fatalf("info: exit %d\n%s", code, out)
	}
	run := []string{"agent", "run", "--agent-name", "pinger", "--model", "haiku", "--profile", "work", "--new-session",
		"--no-skip-permissions", "--claude-binary", h.claude, "--claudewheel-binary", h.claudewheel, "--prompt", "go", "--no-color", "--no-footer"}
	if _, errOut, code := h.run("", run...); code == 0 || !strings.Contains(errOut, "word") {
		t.Fatalf("run without its variable: exit %d %s", code, errOut)
	}
	if out, errOut, code := h.run("", append(run, "--var", "word=pong")...); code != 0 || out != "pong\n" {
		t.Fatalf("run: exit %d\nstdout %q\nstderr %q", code, out, errOut)
	}
}
