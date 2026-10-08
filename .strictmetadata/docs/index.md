+++
description = "claudestream drives Claude Code from Go: a library of typed stream-json events, sandboxed permissions, MCP tools, agent definitions, and a command line."
+++

# claudestream

:-: var key="project.description"

claudestream runs the `claude` command line in its stream-json mode (`--input-format stream-json --output-format stream-json`), started through `claudewheel profile exec`, and turns every line it writes into a typed Go event. The module holds a library (`github.com/stricttools/claudestream`), the `agent` package for agent definitions, and the `claudestream` command.

## Installation

The command:

```
go install github.com/stricttools/claudestream/cmd/claudestream@v0
```

Each GitHub Release also carries `claudestream` archives for Linux and macOS (amd64 and arm64).

The library:

```
go get github.com/stricttools/claudestream@v0
```

A session needs Claude Code 2.0.0 or newer and [claudewheel](https://github.com/stricttools/claudewheel), whose `profile exec` command starts Claude Code with a profile's environment.

## Quick start

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"

	"github.com/stricttools/claudestream"
)

func main() {
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
		switch e := event.(type) {
		case claudestream.AssistantText:
			fmt.Print(e.Text)
		case claudestream.Result:
			fmt.Printf("\ncost: $%.4f\n", e.TotalCostUSD)
		}
	}
}
```

A session holds one Claude Code process for any number of turns: call `Send` again after a turn's `Next` returned `io.EOF`.

## Command line

```
claudestream ask --model sonnet --profile default --new-session --no-skip-permissions --prompt "What is 2 + 2?"
claudestream stream -m opus --profile default --new-session --no-skip-permissions --prompt "Write a haiku"
claudestream events -m sonnet --profile default --continue-session --no-skip-permissions --prompt "Go on"
claudestream agent run --agent-name reviewer --profile default --new-session --no-skip-permissions --var repo=. --prompt "Review it"
claudestream doctor --profile default
```

Every session command requires the model, the profile, the session to use, and whether permissions are skipped; nothing is defaulted. The [command line reference](cli-index/) lists every flag.

## Where to go next

- [The guide](guide/): how a session runs, turns and events, permissions, tools, budgets, and the control methods.
- [Agent definitions](agents-guide/): the `.agent.toml` format and how to run one.
- [The library reference](claudestream/) and [the agent package reference](agent/).
