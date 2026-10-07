package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/stricttools/strictcli/go/strictcli"

	"github.com/stricttools/claudestream"
	"github.com/stricttools/claudestream/internal/printer"
)

func registerSessionCommands(app *strictcli.App) {
	flags := func() strictcli.CmdOption {
		return strictcli.WithFlags(append([]strictcli.Flag{requiredModelFlag()}, sessionFlags(true)...)...)
	}

	app.Command("ask",
		"Send one prompt to Claude Code and print only the reply's final text on stdout; with --json, the reply and its metadata are the document's payload. A turn that Claude Code ends with an error exits 1 with the result text on stderr",
		handleAsk,
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithDryRunUnsupported(sessionDryRunReason),
		strictcli.PayloadSchema(askResultSchema),
		flags(),
	)
	app.Command("stream",
		"Send one prompt to Claude Code and write the reply's text to stdout as it arrives, with a one-line marker on stderr for each thinking block, tool call, tool result, and notice",
		renderedHandler(printer.Live),
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithDryRunUnsupported(sessionDryRunReason),
		flags(),
	)
	app.Command("send",
		"Send one prompt to Claude Code and render the whole turn on stdout: the reply's text as it arrives, thinking, each tool call with its input as JSON, and each tool result; notices go to stderr",
		renderedHandler(printer.Detailed),
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithDryRunUnsupported(sessionDryRunReason),
		flags(),
	)
	app.Command("events",
		"Send one prompt to Claude Code and copy every line Claude Code writes to its stdout (stream-json) to stdout verbatim, for debugging the protocol",
		handleEvents,
		strictcli.WithEffect(strictcli.EffectMutating),
		strictcli.WithDryRunUnsupported(sessionDryRunReason),
		strictcli.OwnsStdout(),
		flags(),
	)
}

// renderedHandler runs one turn and renders it with a printer in mode.
func renderedHandler(mode printer.Mode) func(*strictcli.Context, map[string]interface{}) strictcli.Outcome {
	return func(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
		opts, err := readSessionOptions(kwargs, true)
		if err != nil {
			return fail(ctx, nil, err)
		}
		return runRendered(ctx, mode, opts, startConfig(opts.config()))
	}
}

// runRendered runs the turn start and opts describe, rendering it in mode.
func runRendered(ctx *strictcli.Context, mode printer.Mode, opts sessionOptions, start startFunc) strictcli.Outcome {
	out := newOutput(ctx, opts.colorOverride)
	p, err := printer.New(printer.Options{
		Mode:         mode,
		Stdout:       out.stdout,
		Stderr:       out.stderr,
		StdoutColors: out.stdoutColors,
		StderrColors: out.stderrColors,
		Footer:       opts.footer,
	})
	if err != nil {
		return fail(ctx, nil, err)
	}
	runCtx, cancel := runContext(ctx)
	defer cancel()

	_, err = runOneTurn(runCtx, start, opts.prompt, p.Render)
	if finishErr := p.Finish(); finishErr != nil {
		err = errors.Join(err, finishErr)
	}
	out.flush()
	if err != nil {
		return fail(ctx, runCtx, err)
	}
	return strictcli.Exit(0)
}

func handleEvents(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
	opts, err := readSessionOptions(kwargs, true)
	if err != nil {
		return fail(ctx, nil, err)
	}
	out := newOutput(ctx, opts.colorOverride)
	cfg := opts.config()
	cfg.RawOutput = ctx.Document()

	runCtx, cancel := runContext(ctx)
	defer cancel()
	result, err := runOneTurn(runCtx, startConfig(cfg), opts.prompt, func(claudestream.Event) error { return nil })
	if err != nil {
		return fail(ctx, runCtx, err)
	}
	// In machine mode stderr carries this command's --json document, so the
	// footer is not written there.
	if opts.footer && !ctx.JSON() {
		if err := out.footer(result); err != nil {
			return fail(ctx, runCtx, err)
		}
	}
	return strictcli.Exit(0)
}

// AskResult is the ask command's --json payload: the reply's text and the
// turn's Result.
type AskResult struct {
	// Text is the top-level assistant text of the turn, concatenated.
	Text      string `json:"text"`
	SessionID string `json:"session_id"`
	// Usage is nil when Claude Code reported none.
	Usage         *claudestream.Usage `json:"usage"`
	CostUSD       float64             `json:"cost_usd"`
	DurationMS    float64             `json:"duration_ms"`
	DurationAPIMS float64             `json:"duration_api_ms"`
	IsError       bool                `json:"is_error"`
	NumTurns      int                 `json:"num_turns"`
	StopReason    string              `json:"stop_reason"`
	Result        string              `json:"result"`
	// APIErrorStatus is nil unless the turn ended in an API error.
	APIErrorStatus *int   `json:"api_error_status"`
	Subtype        string `json:"subtype"`
}

var askResultSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"text":       map[string]interface{}{"type": "string"},
		"session_id": map[string]interface{}{"type": "string"},
		"usage": map[string]interface{}{
			"type": []interface{}{"object", "null"},
			"properties": map[string]interface{}{
				"input_tokens":                map[string]interface{}{"type": "integer"},
				"output_tokens":               map[string]interface{}{"type": "integer"},
				"cache_creation_input_tokens": map[string]interface{}{"type": "integer"},
				"cache_read_input_tokens":     map[string]interface{}{"type": "integer"},
			},
			"required":             []interface{}{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"},
			"additionalProperties": false,
		},
		"cost_usd":         map[string]interface{}{"type": "number"},
		"duration_ms":      map[string]interface{}{"type": "number"},
		"duration_api_ms":  map[string]interface{}{"type": "number"},
		"is_error":         map[string]interface{}{"type": "boolean"},
		"num_turns":        map[string]interface{}{"type": "integer"},
		"stop_reason":      map[string]interface{}{"type": "string"},
		"result":           map[string]interface{}{"type": "string"},
		"api_error_status": map[string]interface{}{"type": []interface{}{"integer", "null"}},
		"subtype":          map[string]interface{}{"type": "string"},
	},
	"required": []interface{}{
		"text", "session_id", "usage", "cost_usd", "duration_ms", "duration_api_ms",
		"is_error", "num_turns", "stop_reason", "result", "api_error_status", "subtype",
	},
	"additionalProperties": false,
}

func handleAsk(ctx *strictcli.Context, kwargs map[string]interface{}) strictcli.Outcome {
	opts, err := readSessionOptions(kwargs, true)
	if err != nil {
		return fail(ctx, nil, err)
	}
	out := newOutput(ctx, opts.colorOverride)
	runCtx, cancel := runContext(ctx)
	defer cancel()

	var text strings.Builder
	collect := func(event claudestream.Event) error {
		if t, ok := event.(claudestream.AssistantText); ok && t.ParentToolUseID == nil {
			text.WriteString(t.Text)
		}
		return nil
	}
	result, err := runOneTurn(runCtx, startConfig(opts.config()), opts.prompt, collect)
	if err != nil {
		return fail(ctx, runCtx, err)
	}
	ctx.Payload(AskResult{
		Text:           text.String(),
		SessionID:      result.SessionID,
		Usage:          result.Usage,
		CostUSD:        result.TotalCostUSD,
		DurationMS:     result.DurationMS,
		DurationAPIMS:  result.DurationAPIMS,
		IsError:        result.IsError,
		NumTurns:       result.NumTurns,
		StopReason:     result.StopReason,
		Result:         result.Result,
		APIErrorStatus: result.APIErrorStatus,
		Subtype:        result.Subtype,
	})
	if opts.footer {
		if err := out.footer(result); err != nil {
			return fail(ctx, runCtx, err)
		}
	}
	if result.IsError {
		message := result.Result
		if message == "" {
			message = fmt.Sprintf("Claude Code ended the turn with an error (subtype %q) and no result text", result.Subtype)
		}
		ctx.Error(message)
		return strictcli.Exit(1)
	}
	if !ctx.JSON() {
		ctx.Out(text.String())
	}
	return strictcli.Exit(0)
}
