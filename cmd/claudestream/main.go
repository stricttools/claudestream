// Command claudestream drives Claude Code from the command line: it starts a
// session under a claudewheel profile, sends one prompt, and prints the reply
// as final text, live text, a detailed rendering, or the raw stream-json
// lines; it also runs agent definitions and checks the installation.
package main

import (
	"github.com/stricttools/claudestream/internal/cli"
)

func main() {
	cli.NewApp(Version).Run()
}
