package printer

import (
	"os"

	"golang.org/x/term"
)

const (
	ansiReset  = "\033[0m"
	ansiRed    = "\033[31m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiDim    = "\033[2m"
	ansiBold   = "\033[1m"
)

// Colors wraps text in ANSI escape codes when enabled and returns it
// unchanged when not.
type Colors struct {
	enabled bool
}

// NewColors returns Colors that color text when enabled is true.
func NewColors(enabled bool) Colors {
	return Colors{enabled: enabled}
}

// Enabled reports whether c colors text.
func (c Colors) Enabled() bool { return c.enabled }

func (c Colors) wrap(code, text string) string {
	if !c.enabled || text == "" {
		return text
	}
	return code + text + ansiReset
}

// Red colors text red.
func (c Colors) Red(text string) string { return c.wrap(ansiRed, text) }

// Yellow colors text yellow.
func (c Colors) Yellow(text string) string { return c.wrap(ansiYellow, text) }

// Cyan colors text cyan.
func (c Colors) Cyan(text string) string { return c.wrap(ansiCyan, text) }

// Dim renders text dimmed.
func (c Colors) Dim(text string) string { return c.wrap(ansiDim, text) }

// Bold renders text bold.
func (c Colors) Bold(text string) string { return c.wrap(ansiBold, text) }

// ColorEnabled decides whether output written to stream is colored. An
// override (from --color or --no-color) decides alone; without one, color is
// on unless noColor (the value of the NO_COLOR environment variable) is
// non-empty or stream is not a terminal.
func ColorEnabled(override *bool, noColor string, stream *os.File) bool {
	if override != nil {
		return *override
	}
	if noColor != "" {
		return false
	}
	return term.IsTerminal(int(stream.Fd()))
}
