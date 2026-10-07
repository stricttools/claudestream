package claudestream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Program names the CLIs resolve on PATH with [ResolveOnPath].
const (
	ClaudeProgram      = "claude"
	ClaudewheelProgram = "claudewheel"
)

// MinimumClaudeVersion is the oldest Claude Code release a session starts
// against.
const MinimumClaudeVersion = "2.0.0"

// ErrVersionCheck is wrapped by every failure of [CheckClaudeVersion] and
// [CheckProfileVersion].
var ErrVersionCheck = errors.New("Claude Code version check failed")

// ErrNotOnPath is wrapped by [ResolveOnPath] when the program is absent.
var ErrNotOnPath = errors.New("program not found on PATH")

// versionPattern finds the version in `claude -v` output such as
// "2.1.220 (Claude Code)".
var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

// ResolveOnPath looks name up on PATH and returns its absolute path. The
// library itself never searches PATH; this is for command-line front ends
// that fill in Config.ClaudeBinary and Config.ClaudewheelBinary when no
// explicit path was given. An absent program, or one PATH finds only through
// a relative entry, is an error.
func ResolveOnPath(name string) (string, error) {
	found, err := exec.LookPath(name)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%w: %s (install it, or pass its absolute path explicitly)", ErrNotOnPath, name)
		}
		return "", fmt.Errorf("looking up %s on PATH: %w", name, err)
	}
	abs, err := filepath.Abs(found)
	if err != nil {
		return "", fmt.Errorf("making the path of %s absolute: %w", name, err)
	}
	return abs, nil
}

// ProfileExecArgv returns the command line that runs command (an absolute
// executable followed by its arguments) under a claudewheel profile:
// `<claudewheel> profile exec --name <profile> -- <command...>`. claudewheel
// replaces itself with command after applying the profile's environment.
func ProfileExecArgv(claudewheelBinary, profile string, command ...string) []string {
	argv := []string{claudewheelBinary, "profile", "exec", "--name", profile, "--"}
	return append(argv, command...)
}

// CheckClaudeVersion runs `<claudeBinary> -v` directly (not through a
// profile) and returns the version it reports. Output without a version, a
// version below [MinimumClaudeVersion], a failed run, and a run longer than
// timeout are errors wrapping [ErrVersionCheck]; a version below the minimum
// is returned along with its error.
func CheckClaudeVersion(ctx context.Context, claudeBinary string, timeout time.Duration) (string, error) {
	return runVersionCommand(ctx, timeout, claudeBinary, "-v")
}

// CheckProfileVersion runs `claude -v` through `claudewheel profile exec`
// for profile, which proves the profile resolves and claude starts under
// it, and returns the version reported. Failures are as for
// [CheckClaudeVersion].
func CheckProfileVersion(ctx context.Context, claudewheelBinary, claudeBinary, profile string, timeout time.Duration) (string, error) {
	argv := ProfileExecArgv(claudewheelBinary, profile, claudeBinary, "-v")
	return runVersionCommand(ctx, timeout, argv[0], argv[1:]...)
}

func runVersionCommand(ctx context.Context, timeout time.Duration, program string, args ...string) (string, error) {
	if timeout <= 0 {
		return "", fmt.Errorf("%w: the timeout must be positive, got %s", ErrVersionCheck, timeout)
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	commandLine := strings.Join(append([]string{program}, args...), " ")
	cmd := exec.CommandContext(runCtx, program, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// A descendant holding the output pipes open must not stretch the wait
	// past the timeout.
	cmd.WaitDelay = timeout

	runErr := cmd.Run()
	if ctxErr := runCtx.Err(); ctxErr != nil {
		if errors.Is(ctxErr, context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("%w: `%s` did not finish within %s", ErrVersionCheck, commandLine, timeout)
		}
		return "", fmt.Errorf("%w: `%s`: %w", ErrVersionCheck, commandLine, ctxErr)
	}
	if runErr != nil {
		return "", fmt.Errorf("%w: `%s`: %w%s", ErrVersionCheck, commandLine, runErr, stderrSuffix(stderr.String()))
	}

	output := strings.TrimSpace(stdout.String())
	version := versionPattern.FindString(output)
	if version == "" {
		return "", fmt.Errorf("%w: `%s` printed no version (want MAJOR.MINOR.PATCH), got %q", ErrVersionCheck, commandLine, output)
	}
	older, err := versionLess(version, MinimumClaudeVersion)
	if err != nil {
		return "", fmt.Errorf("%w: `%s`: %w", ErrVersionCheck, commandLine, err)
	}
	if older {
		return version, fmt.Errorf("%w: Claude Code %s is older than the minimum %s; update it", ErrVersionCheck, version, MinimumClaudeVersion)
	}
	return version, nil
}

func stderrSuffix(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return "; stderr: " + stderr
}

// versionLess compares two MAJOR.MINOR.PATCH versions numerically.
func versionLess(a, b string) (bool, error) {
	pa, err := versionParts(a)
	if err != nil {
		return false, err
	}
	pb, err := versionParts(b)
	if err != nil {
		return false, err
	}
	for i := range pa {
		if pa[i] != pb[i] {
			return pa[i] < pb[i], nil
		}
	}
	return false, nil
}

func versionParts(v string) ([3]int, error) {
	var parts [3]int
	fields := strings.Split(v, ".")
	if len(fields) != len(parts) {
		return parts, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH", v)
	}
	for i, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return parts, fmt.Errorf("version %q is not MAJOR.MINOR.PATCH: %w", v, err)
		}
		parts[i] = n
	}
	return parts, nil
}
