+++
title = "README.md"
+++
# claudestream

:-: var key="project.description"

## Install

The command:

```
go install github.com/stricttools/claudestream/cmd/claudestream@v0
```

Each GitHub Release also carries `claudestream` archives for Linux and macOS (amd64 and arm64).

The library:

```
go get github.com/stricttools/claudestream@v0
```

A session needs Claude Code 2.0.0 or newer and [claudewheel](https://github.com/stricttools/claudewheel), whose `profile exec` command starts Claude Code with a profile's environment. Versions up to 0.15.3 were a Python package, published to PyPI and npm; those releases stay there, and nothing newer is published to either.

## Quick start

```go
ctx := context.Background()
claude, err := claudestream.ResolveOnPath(claudestream.ClaudeProgram)
if err != nil {
	log.Fatal(err)
}
claudewheel, err := claudestream.ResolveOnPath(claudestream.ClaudewheelProgram)
if err != nil {
	log.Fatal(err)
}
session, err := claudestream.Start(ctx, claudestream.Config{
	Model:             "sonnet",
	Profile:           "default",
	ClaudeBinary:      claude,
	ClaudewheelBinary: claudewheel,
	Session:           claudestream.NewSession{},
})
if err != nil {
	log.Fatal(err)
}
defer session.Close()

turn, err := session.Send(ctx, "What is 2 + 2?")
if err != nil {
	log.Fatal(err)
}
for {
	event, err := turn.Next(ctx)
	if errors.Is(err, io.EOF) {
		break
	}
	if err != nil {
		log.Fatal(err)
	}
	if text, ok := event.(claudestream.AssistantText); ok {
		fmt.Print(text.Text)
	}
}
```

From the command line:

```
claudestream ask --model sonnet --profile default --new-session --no-skip-permissions --prompt "What is 2 + 2?"
```

## Command line

:-: table-commands

## Documentation

The guide, the agent definition format, and the API reference (the session config, every event type, and the sandbox) are at https://smmh.dev/claudestream.

## License

MIT
