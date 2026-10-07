package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/stricttools/strictcli/go/strictcli"

	"github.com/stricttools/claudestream"
	"github.com/stricttools/claudestream/internal/printer"
)

// startFunc starts the session a command runs its turn in.
type startFunc func(ctx context.Context) (*claudestream.Session, error)

// startConfig starts a session from cfg.
func startConfig(cfg claudestream.Config) startFunc {
	return func(ctx context.Context) (*claudestream.Session, error) {
		return claudestream.Start(ctx, cfg)
	}
}

// runContext returns a context that ends when the handler's context does:
// on the first SIGINT or SIGTERM, and when the dispatch ends.
func runContext(ctx *strictcli.Context) (context.Context, context.CancelFunc) {
	runCtx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-runCtx.Done():
		}
	}()
	return runCtx, cancel
}

// runOneTurn starts a session, sends prompt, hands every event of the turn
// to handle, closes the session, and returns the turn's Result. A
// permission or dialog request ends the turn with an error after handle saw
// it: the command line has no way to answer one, and Claude Code would wait
// for the answer forever.
func runOneTurn(ctx context.Context, start startFunc, prompt string, handle func(claudestream.Event) error) (result claudestream.Result, err error) {
	session, err := start(ctx)
	if err != nil {
		return claudestream.Result{}, fmt.Errorf("starting Claude Code: %w", err)
	}
	defer func() {
		if closeErr := session.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("stopping Claude Code: %w", closeErr))
		}
	}()

	turn, err := session.Send(ctx, prompt)
	if err != nil {
		return claudestream.Result{}, fmt.Errorf("sending the prompt: %w", err)
	}
	for {
		event, nextErr := turn.Next(ctx)
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return claudestream.Result{}, nextErr
		}
		if handleErr := handle(event); handleErr != nil {
			return claudestream.Result{}, handleErr
		}
		switch e := event.(type) {
		case claudestream.PermissionRequest:
			return claudestream.Result{}, fmt.Errorf("Claude Code asked for permission to use %s, which the claudestream command line cannot answer", e.ToolName)
		case claudestream.UserDialogRequest:
			return claudestream.Result{}, fmt.Errorf("Claude Code asked for the %s dialog, which the claudestream command line cannot answer", e.Dialog)
		}
	}
	last := session.LastResult()
	if last == nil {
		return claudestream.Result{}, errors.New("the turn ended without a Result from Claude Code")
	}
	return *last, nil
}

// fail reports err on stderr (in machine mode, as a diagnostic) and returns
// exit code 1. An error caused by an interrupt is reported as one.
func fail(ctx *strictcli.Context, runCtx context.Context, err error) strictcli.Outcome {
	if runCtx != nil && runCtx.Err() != nil && errors.Is(err, context.Canceled) {
		ctx.Error("interrupted: " + err.Error())
	} else {
		ctx.Error(err.Error())
	}
	return strictcli.Exit(1)
}

// failErrorResult reports a Result that Claude Code ended with an error: its
// result text on stderr (in machine mode, as a diagnostic), and exit 1.
// Every session command calls it when result.IsError is true.
func failErrorResult(ctx *strictcli.Context, result claudestream.Result) strictcli.Outcome {
	message := result.Result
	if message == "" {
		message = fmt.Sprintf("Claude Code ended the turn with an error (subtype %q) and no result text", result.Subtype)
	}
	ctx.Error(message)
	return strictcli.Exit(1)
}

// output is where a session command writes its rendering.
type output struct {
	stdout       io.Writer
	stderr       io.Writer
	stdoutColors printer.Colors
	stderrColors printer.Colors
	// flush hands over anything stdout still buffers; it does nothing
	// outside machine mode.
	flush func()
}

// newOutput returns the streams a session command writes to. Outside
// machine mode they are the process's stdout and stderr. In machine mode
// (--json) the process's stdout is the framework's, so text meant for
// stdout goes into the --json document's output member through ctx.Out,
// one line at a time.
func newOutput(ctx *strictcli.Context, colorOverride *bool) output {
	noColor, _ := ctx.InfraValue(noColorEnv)
	o := output{
		stdout:       os.Stdout,
		stderr:       os.Stderr,
		stdoutColors: printer.NewColors(printer.ColorEnabled(colorOverride, noColor, os.Stdout)),
		stderrColors: printer.NewColors(printer.ColorEnabled(colorOverride, noColor, os.Stderr)),
		flush:        func() {},
	}
	if ctx.JSON() {
		lines := &machineStdout{ctx: ctx}
		o.stdout = lines
		o.flush = lines.flush
	}
	return o
}

// footer writes the footer line for result to stderr.
func (o output) footer(result claudestream.Result) error {
	if _, err := io.WriteString(o.stderr, printer.FooterLine(o.stderrColors, result)+"\n"); err != nil {
		return fmt.Errorf("writing the footer: %w", err)
	}
	return nil
}

// machineStdout passes what is written to it to ctx.Out one complete line
// at a time; flush passes an unterminated last line.
type machineStdout struct {
	ctx     *strictcli.Context
	pending string
}

func (w *machineStdout) Write(b []byte) (int, error) {
	w.pending += string(b)
	for {
		i := strings.IndexByte(w.pending, '\n')
		if i < 0 {
			break
		}
		w.ctx.Out(w.pending[:i])
		w.pending = w.pending[i+1:]
	}
	return len(b), nil
}

func (w *machineStdout) flush() {
	if w.pending != "" {
		w.ctx.Out(w.pending)
		w.pending = ""
	}
}
