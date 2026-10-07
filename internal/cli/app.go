// Package cli is the claudestream command line, built on strictcli. Every
// command that starts Claude Code does so through the claudestream library;
// no handler calls the effects handle.
package cli

import (
	"github.com/stricttools/strictcli/go/strictcli"
)

// noColorEnv is the environment variable that turns color off when no
// --color or --no-color is passed (https://no-color.org).
const noColorEnv = "NO_COLOR"

// sessionDryRunReason is why the commands that start Claude Code refuse
// --dry-run.
const sessionDryRunReason = "Claude Code runs as a subprocess whose file edits, commands, and API spend happen outside the effects handle, so a dry run could record none of them"

// NewApp builds the claudestream command line.
func NewApp(version string) *strictcli.App {
	app := strictcli.NewApp("claudestream", version,
		"Drive Claude Code from the command line: start a session under a claudewheel profile, send one prompt, and print the reply as final text, live text, a detailed rendering, or the raw stream-json lines; run agent definitions; and check the installation",
		strictcli.WithHandshakeEnv(noColorEnv,
			"When set to a non-empty value and neither --color nor --no-color is passed, output is not colored (https://no-color.org)"))
	registerSessionCommands(app)
	registerAgentCommands(app)
	registerDoctor(app)
	return app
}
