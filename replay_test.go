package claudestream

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stricttools/claudestream/internal/vcr"
)

// replayConfig returns a config that starts the test binary as claude,
// replaying cassette, through a claudewheel stand-in that runs what follows
// "--" in `profile exec --name P -- ...`.
func replayConfig(t *testing.T, cassette string) Config {
	t.Helper()
	self, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	claudewheel := filepath.Join(dir, "claudewheel")
	script := "#!/bin/sh\n" +
		"[ \"$1 $2 $3\" = \"profile exec --name\" ] || { echo \"unexpected: $*\" >&2; exit 2; }\n" +
		"shift 3; profile=$1; shift\n" +
		"[ \"$1\" = \"--\" ] || { echo \"no -- after the profile\" >&2; exit 2; }\n" +
		"shift; echo \"$profile\" > \"" + filepath.Join(dir, "profile") + "\"\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(claudewheel, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	path, err := filepath.Abs(filepath.Join("tests", "fixtures", "transcripts", cassette))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(vcr.ModeEnv, "replay")
	t.Setenv(vcr.CassetteEnv, path)
	t.Setenv(vcr.TimeoutEnv, "10s")
	return Config{
		Model:             "haiku",
		Profile:           "work",
		ClaudeBinary:      self,
		ClaudewheelBinary: claudewheel,
		Session:           NewSession{},
	}
}

func timeoutCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestReplaySimpleSendEndsInAnAuthenticationError(t *testing.T) {
	cfg := replayConfig(t, "simple_send.jsonl")
	ctx := timeoutCtx(t)
	s, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()
	if v := s.ClaudeVersion(); !strings.HasPrefix(v, "2.1.220") {
		t.Errorf("version %q", v)
	}
	turn, err := s.Send(ctx, "respond with exactly the word 'pong'")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Send(ctx, "again"); !errors.Is(err, ErrTurnActive) {
		t.Errorf("a second send while the turn is open: %v", err)
	}
	var texts []string
	var sawInit bool
	var turnErr error
	for {
		ev, err := turn.Next(ctx)
		if err != nil {
			turnErr = err
			break
		}
		switch e := ev.(type) {
		case SystemInit:
			sawInit = true
		case AssistantText:
			texts = append(texts, e.Text)
		}
	}
	if !errors.Is(turnErr, ErrAuthentication) {
		t.Fatalf("turn ended with %v", turnErr)
	}
	if !sawInit || strings.Join(texts, "") != "Not logged in · Please run /login" {
		t.Errorf("init %v, texts %q", sawInit, texts)
	}
	if s.SessionID() != "cdd3df39-fdc3-44d1-9476-e8cc195afad3" || s.Model() != "claude-haiku-4-5-20251001" || s.PermissionMode() != "default" {
		t.Errorf("session %q model %q mode %q", s.SessionID(), s.Model(), s.PermissionMode())
	}
	if _, err := s.Send(ctx, "after"); !errors.Is(err, ErrSessionUnusable) {
		t.Errorf("send after an authentication error: %v", err)
	}
}

func TestReplayMCPHandshakeListsTheServersTools(t *testing.T) {
	cfg := replayConfig(t, "mcp_handshake.jsonl")
	cfg.Tools = []Tool{{
		Server:      "test_server",
		Name:        "echo",
		Description: "echo its input",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return string(args), nil
		},
	}}
	ctx := timeoutCtx(t)
	s, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v\nstderr: %q", err, s.Stderr())
	}
	if _, err := s.Send(ctx, "x"); !errors.Is(err, ErrSessionClosed) {
		t.Errorf("send after Close: %v", err)
	}
}

func TestReplayMismatchIsAnExitError(t *testing.T) {
	// The handshake cassette expects an initialize request first; a plain
	// session sends a user line, so the stand-in exits with its failure code.
	cfg := replayConfig(t, "mcp_handshake.jsonl")
	ctx := timeoutCtx(t)
	s, err := Start(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	turn, err := s.Send(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	for {
		_, err := turn.Next(ctx)
		if err == nil {
			continue
		}
		var exited *ErrProcessExited
		if !errors.As(err, &exited) || exited.ExitCode != vcr.ExitCode {
			t.Fatalf("turn ended with %v", err)
		}
		if !strings.Contains(strings.Join(s.Stderr(), "\n"), "cassette expects") {
			t.Errorf("stderr %q", s.Stderr())
		}
		return
	}
}

var _ = io.EOF
