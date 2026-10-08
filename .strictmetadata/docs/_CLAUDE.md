+++
title = "CLAUDE.md"
+++
# claudestream

:-: var key="project.description"

## Layout

- The root package `claudestream` is the library: `config.go` (the config and its validation), `argv.go` (Claude Code's command line), `launch.go` (binary resolution, the version check, and the `claudewheel profile exec` argv), `process.go` (the subprocess and its pipes), `wire.go` and `events.go` (decoding stdout lines into events), `messages.go` (outgoing frames), `session.go` (sessions and turns), `control.go` (control requests, answers, and the stdin writer), `sandbox.go`, `mcp.go` (the SDK MCP servers), `budget.go`, and `errors.go`.
- `agent` loads `.agent.toml` agent definitions and starts sessions from them.
- `cmd/claudestream` is the binary; `internal/cli` its strictcli commands; `internal/printer` the renderings of the session commands.
- `internal/vcr` is the test suites' stand-in for the claude binary, imported only by tests.

## Commands

:-: table-commands

## Config

`Config` (in `config.go`) describes one session: `Model`, `Profile`, `ClaudeBinary`, `ClaudewheelBinary`, and `Session` are required, every other field is optional and its zero value adds nothing to the command line, and `Config.Validate` reports every problem at once, each wrapping `ErrInvalidConfig`. Its fields are documented in the source and on the API reference page.

## Code patterns

### Start a session and read a turn

```go
session, err := claudestream.Start(ctx, claudestream.Config{
	Model:             "sonnet",
	Profile:           "default",
	ClaudeBinary:      claude,      // absolute path; the library never searches PATH
	ClaudewheelBinary: claudewheel, // absolute path
	Session:           claudestream.NewSession{},
})
if err != nil {
	return err
}
defer session.Close()

turn, err := session.Send(ctx, "prompt")
if err != nil {
	return err
}
for {
	event, err := turn.Next(ctx)
	if errors.Is(err, io.EOF) {
		break
	}
	if err != nil {
		return err
	}
	switch e := event.(type) {
	case claudestream.AssistantText:
		fmt.Print(e.Text)
	case claudestream.ToolUse:
		fmt.Printf("[tool: %s]\n", e.Name)
	case claudestream.PermissionRequest:
		if err := session.RespondAllow(ctx, e.RequestID, e.ToolInput, nil); err != nil {
			return err
		}
	}
}
```

### Sandbox

```go
cfg.Sandbox = &claudestream.Sandbox{Tools: []string{"Read", "Bash"}, WritePaths: []string{"/project"}}
```

A sandbox answers every permission request itself, and the turn yields `PermissionDecided` instead of `PermissionRequest`.

### Run an agent definition

```go
def, err := agent.LoadByName(cwd, "reviewer")
if err != nil {
	return err
}
session, err := agent.Invoke(ctx, def, base, map[string]string{"repo": cwd}, handlers)
```

## Testing

`go test ./...` runs every test without Claude Code, credentials, or spend. The session tests start real subprocesses over real pipes: the test binary itself becomes the claude stand-in (`internal/vcr`) when `CLAUDESTREAM_VCR_MODE=replay`, replaying a cassette and checking the shape of each line the session sends. The cassettes under `tests/fixtures/transcripts/` were recorded from Claude Code 2.1.220; the Go tests also write synthetic cassettes, which may hold `raw` lines and an `exit` record. A claudewheel stand-in script runs what follows `--` in `profile exec`, and the command line tests run the app in a child process, since the session commands write to the process's stdout and stderr.

## Release workflow

This project uses [rlsbl](https://github.com/stricttools/rlsbl) for release orchestration.

- Add a changelog entry for every change with `rlsbl changelog add`.
- Run `rlsbl release init`, set the release file's bump and description, then `rlsbl release run --watch`.
- CI tests the release commit, and the publish workflow builds the Linux and macOS archives of the `claudestream` binary with GoReleaser and attaches them to the GitHub Release. Never publish by hand.
- The module is installed with `go install github.com/stricttools/claudestream/cmd/claudestream@v0`. Versions up to 0.15.3 were a Python package on PyPI and npm; nothing newer is published there.

## Conventions

- Every config value that matters is explicit: no implicit defaults for the model, the profile, the binaries, the session, or the permission posture.
- A frame the session cannot handle is never dropped: it becomes an event (`UnknownEvent`, `UndecodableEvent`, `NonJSONLine`, `UnknownControlRequest`), and a control request the session does not handle is answered with an error so Claude Code never waits for it.
- No tokens or secrets in command-line arguments: the profile's environment reaches Claude Code through `claudewheel profile exec`.
