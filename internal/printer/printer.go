// Package printer renders claudestream events for the claudestream command
// line: the detailed rendering of the send command, the live rendering of
// the stream command, the footer line, and plain-text tables.
package printer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/stricttools/claudestream"
)

// Mode chooses how a Printer renders a turn.
type Mode int

const (
	// Detailed writes the assistant's text, each tool call's input as JSON,
	// each tool result's content, and thinking to stdout, and the footer
	// and notices to stderr.
	Detailed Mode = iota + 1
	// Live writes only the assistant's text to stdout, and a one-line
	// marker for every thinking block, tool call, tool result, and notice to
	// stderr, with the footer.
	Live
)

// Options configures a Printer.
type Options struct {
	Mode   Mode
	Stdout io.Writer
	Stderr io.Writer
	// StdoutColors and StderrColors color what is written to each stream.
	StdoutColors Colors
	StderrColors Colors
	// Footer writes a duration and cost line to stderr after each Result.
	Footer bool
}

// Printer renders the events of one session's turns. Text arrives twice
// when partial messages are on: first as text deltas, then as the finished
// AssistantText block. A Printer writes the deltas as they arrive and
// drops an AssistantText when text deltas were written since the last
// AssistantText; an AssistantText that no delta preceded (an explanation
// Claude Code sends when a turn fails, for example) is written whole.
// Subagent text (ParentToolUseID set) is not written.
type Printer struct {
	opts Options
	// textStreamed reports whether top-level text deltas were written since
	// the last top-level AssistantText.
	textStreamed bool
	// atLineStart reports whether the last byte written to stdout ended a
	// line (or nothing was written yet).
	atLineStart bool
	err         error
}

// New returns a Printer; it refuses an unknown mode and missing writers.
func New(opts Options) (*Printer, error) {
	if opts.Mode != Detailed && opts.Mode != Live {
		return nil, fmt.Errorf("printer: unknown mode %d", opts.Mode)
	}
	if opts.Stdout == nil || opts.Stderr == nil {
		return nil, errors.New("printer: both Stdout and Stderr are required")
	}
	return &Printer{opts: opts, atLineStart: true}, nil
}

// Render writes one event. It returns the first write error, on this call
// or an earlier one, and an error for an event type it does not know.
func (p *Printer) Render(event claudestream.Event) error {
	if p.err != nil {
		return p.err
	}
	oc, ec := p.opts.StdoutColors, p.opts.StderrColors
	detailed := p.opts.Mode == Detailed

	switch e := event.(type) {
	case claudestream.StreamDelta:
		if e.ParentToolUseID == nil && e.DeltaType() == "text_delta" {
			p.textStreamed = true
			p.out(e.Text())
		}

	case claudestream.AssistantText:
		if e.ParentToolUseID != nil {
			break
		}
		if p.textStreamed {
			p.textStreamed = false
		} else {
			p.out(e.Text)
		}

	case claudestream.Thinking:
		if e.ParentToolUseID != nil {
			break
		}
		if detailed {
			p.outLine(oc.Dim("[thinking: " + e.Text + "]"))
		} else {
			p.errLine(ec.Dim("[thinking...]"))
		}

	case claudestream.ToolUse:
		if detailed {
			p.outLine("--- Tool: " + oc.Bold(e.Name) + subagentSuffix(e.ParentToolUseID) + " ---")
			input, err := indentedJSON(e.Input)
			if err != nil {
				p.err = fmt.Errorf("rendering the input of tool call %s: %w", e.ToolUseID, err)
				break
			}
			p.out(input)
		} else {
			p.errLine("[tool: " + ec.Bold(e.Name) + subagentSuffix(e.ParentToolUseID) + "]")
		}

	case claudestream.ToolResult:
		if detailed {
			header := "--- Result ---"
			if e.IsError {
				header = "--- Result (error) ---"
			}
			p.outLine(header)
			if text := e.ContentText(); text != "" {
				p.outLine(text)
			}
		} else if e.IsError {
			p.errLine(ec.Yellow("[result: error]"))
		} else {
			p.errLine("[result]")
		}

	case claudestream.Result:
		p.endLine()
		if p.opts.Footer {
			p.errLine(FooterLine(ec, e))
		}
		p.textStreamed = false

	case claudestream.APIRetry:
		p.errLine(ec.Yellow(fmt.Sprintf("[retry %d/%d: %s]", e.Attempt, e.MaxRetries, e.Error)))

	case claudestream.RateLimit:
		p.errLine(ec.Yellow("[rate limit: " + e.Status + "]"))

	case claudestream.PermissionRequest:
		p.errLine(ec.Yellow("[permission needed: " + e.ToolName + "]"))

	case claudestream.PermissionDecided:
		if e.Allowed {
			p.errLine("[sandbox allowed: " + e.Request.ToolName + "]")
		} else {
			p.errLine(ec.Yellow("[sandbox denied: " + e.Request.ToolName + ": " + e.Message + "]"))
		}

	case claudestream.UserDialogRequest:
		p.errLine(ec.Yellow("[dialog needed: " + e.Dialog + "]"))

	case claudestream.BudgetThreshold:
		p.errLine(fmt.Sprintf("[threshold: %s %g crossed at %g]", e.Metric, e.Threshold, e.CurrentValue))

	case claudestream.SDKToolCall:
		marker := "[sdk tool: " + e.Server + "/" + e.Tool + "]"
		if e.IsError {
			marker = ec.Yellow("[sdk tool: " + e.Server + "/" + e.Tool + ": error]")
		}
		p.errLine(marker)

	case claudestream.UnknownControlRequest:
		p.errLine(ec.Dim("[unhandled control request: " + e.Subtype + "]"))

	case claudestream.NonJSONLine:
		p.errLine(ec.Yellow("[non-JSON line from Claude Code: " + e.Line + "]"))

	case claudestream.UndecodableEvent:
		p.errLine(ec.Yellow(fmt.Sprintf("[undecodable event from Claude Code: %v]", e.Err)))

	case claudestream.SystemInit, claudestream.CompactBoundary, claudestream.FileWrite,
		claudestream.FileEdit, claudestream.UnknownEvent, claudestream.UnknownContentBlock:
		// Nothing to show: the session start, compaction, and file changes
		// (already covered by the tool call) carry nothing for a reader, and
		// unknown frames and blocks carry nothing a reader could use.

	default:
		return fmt.Errorf("printer: no rendering for event type %T", event)
	}
	return p.err
}

// Finish ends the current stdout line if text left it open, and returns the
// first write error.
func (p *Printer) Finish() error {
	p.endLine()
	return p.err
}

// FooterLine is the line written to stderr after a turn's Result.
func FooterLine(c Colors, r claudestream.Result) string {
	return c.Cyan(fmt.Sprintf("--- Done (%.0fms, $%.4f) ---", r.DurationMS, r.TotalCostUSD))
}

func subagentSuffix(parent *string) string {
	if parent == nil {
		return ""
	}
	return " (subagent of " + *parent + ")"
}

// indentedJSON renders v as indented JSON ending in a newline, without
// escaping HTML characters.
func indentedJSON(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (p *Printer) write(w io.Writer, s string) bool {
	if p.err != nil || s == "" {
		return false
	}
	if _, err := io.WriteString(w, s); err != nil {
		p.err = fmt.Errorf("writing output: %w", err)
		return false
	}
	return true
}

func (p *Printer) out(s string) {
	if p.write(p.opts.Stdout, s) {
		p.atLineStart = strings.HasSuffix(s, "\n")
	}
}

// outLine writes s as a line of its own on stdout.
func (p *Printer) outLine(s string) {
	p.endLine()
	p.out(s + "\n")
}

func (p *Printer) endLine() {
	if !p.atLineStart {
		p.out("\n")
	}
}

func (p *Printer) errLine(s string) {
	p.write(p.opts.Stderr, s+"\n")
}
