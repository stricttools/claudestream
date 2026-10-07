package claudestream

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidConfig is wrapped by every problem [Config.Validate] reports.
var ErrInvalidConfig = errors.New("invalid claudestream config")

// ErrTurnActive is returned by a send while the previous turn is still open.
var ErrTurnActive = errors.New("a turn is already active on this session")

// ErrSessionUnusable is returned by every send after a turn was cancelled
// through its context or ended in an authentication failure: the subprocess
// is in an unknown state and the session must be closed.
var ErrSessionUnusable = errors.New("session is unusable after a cancelled turn or an authentication failure; close it and start a new one")

// ErrAuthentication is returned when Claude Code reports that the profile is
// not logged in or its credentials are rejected.
var ErrAuthentication = errors.New("Claude Code reported an authentication failure; log the profile in again or check its credentials")

// ErrSessionClosed is returned by every operation on a session after Close.
var ErrSessionClosed = errors.New("session is closed")

// ErrUnknownRequest is returned when a response names a request ID the
// session is not waiting to answer: never received, already answered, or of
// another request type (a permission answer for a dialog, for example).
var ErrUnknownRequest = errors.New("no unanswered request with this ID")

// ErrLineTooLong is returned when a stdout line exceeds the configured line
// limit.
var ErrLineTooLong = errors.New("Claude Code wrote a stdout line longer than the line limit")

// ErrProcessExited reports that the Claude Code subprocess exited while
// something still waited on it. StderrTail holds the last stderr lines.
type ErrProcessExited struct {
	ExitCode   int
	StderrTail []string
}

func (e *ErrProcessExited) Error() string {
	msg := fmt.Sprintf("Claude Code exited with code %d", e.ExitCode)
	if len(e.StderrTail) == 0 {
		return msg + " and wrote nothing to stderr"
	}
	return msg + "; last stderr lines:\n" + strings.Join(e.StderrTail, "\n")
}

// ControlError is an error response Claude Code sent to one of the session's
// control requests.
type ControlError struct {
	RequestID string
	// Subtype is the response subtype ("error", or any value other than
	// "success").
	Subtype string
	Message string
}

func (e *ControlError) Error() string {
	return fmt.Sprintf("Claude Code answered control request %s with %q: %s", e.RequestID, e.Subtype, e.Message)
}
