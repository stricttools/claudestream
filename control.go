package claudestream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"
)

// pastDeadline is a write deadline already in the past, set to make a
// blocked stdin write return when its context ends.
var pastDeadline = time.Unix(1, 0)

// writeFrame writes one frame to Claude Code's stdin as a JSON line. Writes
// are serialized; waiting for the turn to write and the write itself end
// when ctx does. A write that stopped part-way leaves a broken frame on
// stdin, so it makes the session unusable and refuses every later write.
func (s *Session) writeFrame(ctx context.Context, frame any) error {
	data, err := json.Marshal(frame)
	if err != nil {
		return fmt.Errorf("encoding a frame for Claude Code's stdin: %w", err)
	}
	data = append(data, '\n')

	select {
	case s.stdinLock <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-s.stdinLock }()
	if err := s.writable(); err != nil {
		return err
	}

	stdin := s.proc.stdin
	var deadlineErr error
	deadlineSet := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		deadlineErr = stdin.SetWriteDeadline(pastDeadline)
		close(deadlineSet)
	})
	written, writeErr := stdin.Write(data)
	if !stop() {
		<-deadlineSet
	}
	resetErr := stdin.SetWriteDeadline(time.Time{})

	if writeErr == nil {
		// The frame is out; a deadline that failed to interrupt it no longer
		// matters, but one that cannot be cleared would fail the next write.
		if resetErr != nil {
			return fmt.Errorf("clearing the write deadline on Claude Code's stdin: %w", resetErr)
		}
		return nil
	}
	if written > 0 {
		s.mu.Lock()
		s.stdinBroken = true
		s.unusable = true
		s.mu.Unlock()
	}
	if ctx.Err() != nil && errors.Is(writeErr, os.ErrDeadlineExceeded) {
		return fmt.Errorf("writing to Claude Code's stdin: %w", ctx.Err())
	}
	if exitErr := s.exitError(); exitErr != nil {
		return exitErr
	}
	return fmt.Errorf("writing to Claude Code's stdin: %w", errors.Join(writeErr, deadlineErr))
}

func (s *Session) writable() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return ErrSessionClosed
	case s.stdinBroken:
		return fmt.Errorf("%w: an earlier write to Claude Code's stdin stopped part-way", ErrSessionUnusable)
	}
	return nil
}

// controlOutcome is the answer to one of the session's control requests:
// the success payload, or an error.
type controlOutcome struct {
	response json.RawMessage
	err      error
}

// pendingControls holds the session's control requests still waiting for a
// response, keyed by request ID.
type pendingControls struct {
	mu      sync.Mutex
	counter int
	waiting map[string]chan controlOutcome
	// failure, once set, is the outcome of every later registration.
	failure error
}

func newPendingControls() *pendingControls {
	return &pendingControls{waiting: map[string]chan controlOutcome{}}
}

// register starts waiting for the response to requestID.
func (p *pendingControls) register(requestID string) (<-chan controlOutcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.registerLocked(requestID)
}

// registerNext allocates the next ctrl_<n> request ID and starts waiting for
// its response.
func (p *pendingControls) registerNext() (string, <-chan controlOutcome, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.counter++
	requestID := controlRequestIDPrefix + strconv.Itoa(p.counter)
	outcome, err := p.registerLocked(requestID)
	return requestID, outcome, err
}

func (p *pendingControls) registerLocked(requestID string) (<-chan controlOutcome, error) {
	if p.failure != nil {
		return nil, p.failure
	}
	if _, taken := p.waiting[requestID]; taken {
		return nil, fmt.Errorf("control request %s is already waiting for a response", requestID)
	}
	outcome := make(chan controlOutcome, 1)
	p.waiting[requestID] = outcome
	return outcome, nil
}

// drop stops waiting for requestID; a response arriving later becomes an
// UnknownEvent.
func (p *pendingControls) drop(requestID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.waiting, requestID)
}

// resolve delivers resp to the request waiting for it and reports whether
// one was. Claude Code answers the start-up handshake requests without a
// request ID; such a response goes to whichever handshake request is
// waiting (Start sends them one at a time).
func (p *pendingControls) resolve(resp controlResponse) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	requestID := resp.requestID
	if requestID == "" {
		requestID = p.waitingHandshakeLocked()
	}
	outcome, found := p.waiting[requestID]
	if !found {
		return false
	}
	delete(p.waiting, requestID)
	resp.requestID = requestID
	outcome <- controlOutcome{response: resp.response, err: resp.err()}
	return true
}

// waitingHandshakeLocked returns the ID of the one handshake request
// waiting; empty when none or both are.
func (p *pendingControls) waitingHandshakeLocked() string {
	var found []string
	for _, id := range []string{initializeRequestID, mcpSetServersRequestID} {
		if _, waiting := p.waiting[id]; waiting {
			found = append(found, id)
		}
	}
	if len(found) != 1 {
		return ""
	}
	return found[0]
}

// failAll fails every waiting request with err, and every later
// registration; the first failure recorded is kept.
func (p *pendingControls) failAll(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.failure == nil {
		p.failure = err
	}
	for requestID, outcome := range p.waiting {
		outcome <- controlOutcome{err: err}
		delete(p.waiting, requestID)
	}
}

// awaitControl waits for the response to requestID until ctx ends.
func (s *Session) awaitControl(ctx context.Context, requestID string, outcome <-chan controlOutcome) (json.RawMessage, error) {
	select {
	case o := <-outcome:
		return o.response, o.err
	case <-ctx.Done():
		s.pending.drop(requestID)
		return nil, fmt.Errorf("waiting for the response to control request %s: %w", requestID, ctx.Err())
	}
}

// control sends a generic control request and returns its success payload.
func (s *Session) control(ctx context.Context, subtype string, payload map[string]any) (json.RawMessage, error) {
	requestID, outcome, err := s.pending.registerNext()
	if err != nil {
		return nil, err
	}
	if err := s.writeFrame(ctx, newControlRequest(requestID, subtype, payload)); err != nil {
		s.pending.drop(requestID)
		return nil, err
	}
	response, err := s.awaitControl(ctx, requestID, outcome)
	if err != nil {
		return nil, fmt.Errorf("control request %s: %w", subtype, err)
	}
	return response, nil
}

// decodeControlPayload decodes a success payload into v; an absent payload
// leaves v as it is.
func decodeControlPayload(subtype string, payload json.RawMessage, v any) error {
	if len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, v); err != nil {
		return fmt.Errorf("decoding the response to control request %s: %w", subtype, err)
	}
	return nil
}

// Interrupt stops the running turn and returns the user messages Claude
// Code still had queued (empty when it reports none).
func (s *Session) Interrupt(ctx context.Context) ([]string, error) {
	const subtype = "interrupt"
	payload, err := s.control(ctx, subtype, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		StillQueued []string `json:"still_queued"`
	}
	if err := decodeControlPayload(subtype, payload, &response); err != nil {
		return nil, err
	}
	if response.StillQueued == nil {
		return []string{}, nil
	}
	return response.StillQueued, nil
}

// SetPermissionMode changes the permission mode. The mode is passed
// unvalidated; Claude Code refuses unknown modes with an error response
// (a *ControlError).
func (s *Session) SetPermissionMode(ctx context.Context, mode string) error {
	if _, err := s.control(ctx, "set_permission_mode", map[string]any{"mode": mode}); err != nil {
		return err
	}
	s.mu.Lock()
	s.permissionMode = mode
	s.mu.Unlock()
	return nil
}

// SetModel switches the model; nil resets Claude Code to its default model,
// after which Model is empty until the next SystemInit reports it.
func (s *Session) SetModel(ctx context.Context, model *string) error {
	payload := map[string]any{}
	if model != nil {
		payload["model"] = *model
	}
	if _, err := s.control(ctx, "set_model", payload); err != nil {
		return err
	}
	s.mu.Lock()
	s.model = ""
	if model != nil {
		s.model = *model
	}
	s.mu.Unlock()
	return nil
}

// ContextCategory is one category of the context window's usage.
type ContextCategory struct {
	Name   string
	Tokens int64
}

// ContextUsage is a snapshot of the model's context-window usage.
type ContextUsage struct {
	TotalTokens int64
	MaxTokens   int64
	// Percentage is as Claude Code reports it.
	Percentage         float64
	Categories         []ContextCategory
	AutoCompactEnabled bool
	// Raw is the whole response payload.
	Raw json.RawMessage
}

// GetContextUsage asks Claude Code for the context window's usage. A
// response without totalTokens or maxTokens is an error.
func (s *Session) GetContextUsage(ctx context.Context) (ContextUsage, error) {
	const subtype = "get_context_usage"
	payload, err := s.control(ctx, subtype, nil)
	if err != nil {
		return ContextUsage{}, err
	}
	var response struct {
		TotalTokens *int64  `json:"totalTokens"`
		MaxTokens   *int64  `json:"maxTokens"`
		Percentage  float64 `json:"percentage"`
		Categories  []struct {
			Name   string `json:"name"`
			Tokens int64  `json:"tokens"`
		} `json:"categories"`
		IsAutoCompactEnabled bool `json:"isAutoCompactEnabled"`
	}
	if err := decodeControlPayload(subtype, payload, &response); err != nil {
		return ContextUsage{}, err
	}
	if response.TotalTokens == nil || response.MaxTokens == nil {
		return ContextUsage{}, fmt.Errorf("the response to control request %s lacks totalTokens or maxTokens: %s", subtype, payload)
	}
	categories := make([]ContextCategory, 0, len(response.Categories))
	for _, c := range response.Categories {
		categories = append(categories, ContextCategory{Name: c.Name, Tokens: c.Tokens})
	}
	return ContextUsage{
		TotalTokens:        *response.TotalTokens,
		MaxTokens:          *response.MaxTokens,
		Percentage:         response.Percentage,
		Categories:         categories,
		AutoCompactEnabled: response.IsAutoCompactEnabled,
		Raw:                payload,
	}, nil
}

// RespondAllow allows the PermissionRequest with requestID. input becomes
// the tool's input (pass the request's ToolInput to run it unchanged);
// permissions, when not nil, are rule updates to apply (for example from
// PermissionSuggestions).
func (s *Session) RespondAllow(ctx context.Context, requestID string, input map[string]any, permissions []map[string]any) error {
	return s.respond(ctx, requestID, controlSubtypeCanUseTool, newAllowResponse(requestID, input, permissions))
}

// RespondDeny denies the PermissionRequest with requestID; message is the
// reason the model sees.
func (s *Session) RespondDeny(ctx context.Context, requestID, message string) error {
	return s.respond(ctx, requestID, controlSubtypeCanUseTool, newDenyResponse(requestID, message))
}

// RespondDialog completes the UserDialogRequest with requestID; result is
// sent as is, in the shape the dialog defines.
func (s *Session) RespondDialog(ctx context.Context, requestID string, result any) error {
	return s.respond(ctx, requestID, controlSubtypeRequestUserDialog, newDialogCompletedResponse(requestID, result))
}

// RespondDialogCancelled cancels the UserDialogRequest with requestID;
// Claude Code applies the dialog's default behavior.
func (s *Session) RespondDialogCancelled(ctx context.Context, requestID string) error {
	return s.respond(ctx, requestID, controlSubtypeRequestUserDialog, newDialogCancelledResponse(requestID))
}

// respond answers an outstanding consumer request of the given subtype. An
// ID that is not outstanding for that subtype is ErrUnknownRequest; a
// failed write leaves the request outstanding.
func (s *Session) respond(ctx context.Context, requestID, subtype string, answer controlResponseFrame) error {
	s.mu.Lock()
	if got, found := s.outstanding[requestID]; !found || got != subtype {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s request %q", ErrUnknownRequest, subtype, requestID)
	}
	delete(s.outstanding, requestID)
	s.mu.Unlock()

	if err := s.writeFrame(ctx, answer); err != nil {
		s.mu.Lock()
		s.outstanding[requestID] = subtype
		s.mu.Unlock()
		return err
	}
	return nil
}
