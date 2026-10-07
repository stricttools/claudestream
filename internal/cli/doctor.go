package cli

import (
	"fmt"
	"time"

	"github.com/stricttools/strictcli/go/strictcli"

	"github.com/stricttools/claudestream"
)

// profileCheckTimeout bounds `claudewheel profile exec --name P -- claude
// -v`, which starts claudewheel before claude.
const profileCheckTimeout = 10 * time.Second

func registerDoctor(app *strictcli.App) {
	app.Command("doctor",
		fmt.Sprintf("Check what a session needs: the claude path and its version against the minimum %s (`claude -v`, within %s), and the claudewheel path; with --profile, also run `claudewheel profile exec --name <profile> -- <claude> -v` (within %s). Prints one [ok] or [FAIL] line per check and exits 1 when any check fails",
			claudestream.MinimumClaudeVersion, claudestream.DefaultVersionCheckTimeout, profileCheckTimeout),
		handleDoctor,
		strictcli.WithEffect(strictcli.EffectReadOnly),
		strictcli.WithFlags(
			strictcli.StringFlag("profile",
				"claudewheel profile to check by starting Claude Code under it; when omitted, no profile is checked",
				strictcli.Optional()),
			strictcli.StringFlag("claude-binary",
				"Absolute path of the Claude Code executable to check; when omitted, `claude` is looked up on PATH",
				strictcli.Optional()),
			strictcli.StringFlag("claudewheel-binary",
				"Absolute path of the claudewheel executable to check; when omitted, `claudewheel` is looked up on PATH",
				strictcli.Optional()),
		),
	)
}

func handleDoctor(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
	runCtx, cancel := runContext(ctx)
	defer cancel()

	failed := false
	report := func(err error, okMessage string) {
		if err != nil {
			failed = true
			ctx.Out("[FAIL] " + err.Error())
			return
		}
		ctx.Out("[ok] " + okMessage)
	}

	claude, claudeErr := resolveBinary(kwargs, "claude-binary", claudestream.ClaudeProgram)
	report(wrapCheck("claude path", claudeErr), "claude path: "+claude)
	if claudeErr == nil {
		version, err := claudestream.CheckClaudeVersion(runCtx, claude, claudestream.DefaultVersionCheckTimeout)
		report(wrapCheck("Claude Code version", err),
			fmt.Sprintf("Claude Code version: %s (minimum %s)", version, claudestream.MinimumClaudeVersion))
	}

	claudewheel, claudewheelErr := resolveBinary(kwargs, "claudewheel-binary", claudestream.ClaudewheelProgram)
	report(wrapCheck("claudewheel path", claudewheelErr), "claudewheel path: "+claudewheel)

	profile, err := optionalString(kwargs, "profile")
	switch {
	case err != nil:
		report(wrapCheck("profile", err), "")
	case profile == "":
		// No profile to check.
	case claudeErr != nil || claudewheelErr != nil:
		report(fmt.Errorf("profile %s: not checked, because the claude or claudewheel path did not resolve", profile), "")
	default:
		version, err := claudestream.CheckProfileVersion(runCtx, claudewheel, claude, profile, profileCheckTimeout)
		report(wrapCheck("profile "+profile, err),
			fmt.Sprintf("profile %s: Claude Code %s started under it", profile, version))
	}

	if failed {
		return strictcli.Exit(1)
	}
	return strictcli.Exit(0)
}

// wrapCheck prefixes a failed check's error with what was checked; nil stays
// nil.
func wrapCheck(what string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", what, err)
}
