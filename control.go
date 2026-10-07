package claudestream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"sync"
	"time"
)

// pastDeadline is a write deadline already in the past, set to make a
// blocked stdin write return when its context ends.
var pastDeadline = time.Unix(1, 0)

// stdinWriter is the only writer of Claude Code's stdin. Its goroutine
// writes the submitted frames one at a time, in submission order. Submitting
// never waits for a write, so the stdout reader answers Claude Code's
// requests without ever blocking on stdin.
//
// A write that fails, or stops part-way and leaves a broken frame on stdin,
// makes the writer fail every queued and later frame with an error wrapping
// ErrSessionUnusable. After stop, queued and later frames fail with
// ErrSessionClosed.
type stdinWriter struct {
	file *os.File
	// broke is called once, on the writer goroutine, when a write failure
	// makes stdin unusable.
	broke func()

	mu       sync.Mutex
	queue    []*frameWrite
	stopping bool
	failure  error
	// wake holds at most one pending wake-up for the writer goroutine.
	wake chan struct{}
	// failed is closed when failure is set.
	failed chan struct{}
	// exited is closed when the writer goroutine returns.
	exited chan struct{}
}

// frameWrite is one frame waiting to be written.
type frameWrite struct {
	// ctx bounds the write: a frame whose ctx ended before its turn is not
	// written, and a write in progress is interrupted through a past
	// deadline.
	ctx  context.Context
	data []byte
	// outcome receives the result once; buffered so the writer never waits.
	outcome chan error
	// written, when set, is run once with the result: on the writer
	// goroutine, or on the goroutine of stop when the frame was still queued.
	// It must not block.
	written func(error)
}

// newStdinWriter returns a writer of file; start its goroutine with run.
func newStdinWriter(file *os.File, broke func()) *stdinWriter {
	return &stdinWriter{
		file:   file,
		broke:  broke,
		wake:   make(chan struct{}, 1),
		failed: make(chan struct{}),
		exited: make(chan struct{}),
	}
}

func (w *stdinWriter) signal() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// submit queues data for writing under ctx; written, when set, receives the
// result (see frameWrite.written).
func (w *stdinWriter) submit(ctx context.Context, data []byte, written func(error)) (*frameWrite, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.stopping:
		return nil, ErrSessionClosed
	case w.failure != nil:
		return nil, w.failure
	}
	fw := &frameWrite{ctx: ctx, data: data, outcome: make(chan error, 1), written: written}
	w.queue = append(w.queue, fw)
	w.signal()
	return fw, nil
}

// write queues data and waits for it to be written. When ctx ends while the
// frame still waits its turn, the frame is withdrawn; when it ends during
// the write, the write is interrupted and its result returned.
func (w *stdinWriter) write(ctx context.Context, data []byte) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("writing to Claude Code's stdin: %w", err)
	}
	fw, err := w.submit(ctx, data, nil)
	if err != nil {
		return err
	}
	select {
	case err := <-fw.outcome:
		return err
	case <-ctx.Done():
		if w.withdraw(fw) {
			return fmt.Errorf("waiting to write to Claude Code's stdin: %w", ctx.Err())
		}
		return <-fw.outcome
	}
}

// enqueue queues data without waiting; written receives the result (see
// frameWrite.written), or receives the refusal at once on this goroutine.
func (w *stdinWriter) enqueue(ctx context.Context, data []byte, written func(error)) {
	if _, err := w.submit(ctx, data, written); err != nil {
		written(err)
	}
}

// withdraw removes fw from the queue and reports whether it was still there.
func (w *stdinWriter) withdraw(fw *frameWrite) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	i := slices.Index(w.queue, fw)
	if i < 0 {
		return false
	}
	w.queue = slices.Delete(w.queue, i, i+1)
	return true
}

// failureErr reports why stdin became unusable; nil while it is usable.
func (w *stdinWriter) failureErr() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.failure
}

// stop makes the writer goroutine return once the write in progress, if
// any, has ended, and fails every queued and later frame with
// ErrSessionClosed. Closing the file afterwards interrupts a write still in
// progress.
func (w *stdinWriter) stop() {
	w.mu.Lock()
	queued := w.queue
	w.queue = nil
	w.stopping = true
	w.signal()
	w.mu.Unlock()
	for _, fw := range queued {
		fw.resolve(ErrSessionClosed)
	}
}

func (fw *frameWrite) resolve(err error) {
	fw.outcome <- err
	if fw.written != nil {
		fw.written(err)
	}
}

// run is the writer goroutine.
func (w *stdinWriter) run() {
	defer close(w.exited)
	for {
		fw, ok := w.next()
		if !ok {
			return
		}
		frameErr, breakErr := w.writeOne(fw)
		var failedFrames []*frameWrite
		var failure error
		if breakErr != nil {
			failedFrames, failure = w.fail(breakErr)
		}
		fw.resolve(frameErr)
		for _, queued := range failedFrames {
			queued.resolve(failure)
		}
	}
}

// next waits for the next frame; false once the writer is stopping.
func (w *stdinWriter) next() (*frameWrite, bool) {
	for {
		w.mu.Lock()
		if w.stopping {
			w.mu.Unlock()
			return nil, false
		}
		if len(w.queue) > 0 {
			fw := w.queue[0]
			w.queue[0] = nil
			w.queue = w.queue[1:]
			if len(w.queue) == 0 {
				w.queue = nil
			}
			w.mu.Unlock()
			return fw, true
		}
		w.mu.Unlock()
		<-w.wake
	}
}

// fail records why stdin became unusable, takes every queued frame to fail
// with it, and tells the session once.
func (w *stdinWriter) fail(cause error) ([]*frameWrite, error) {
	w.mu.Lock()
	first := w.failure == nil
	if first {
		w.failure = fmt.Errorf("%w: %w", ErrSessionUnusable, cause)
		close(w.failed)
	}
	failure := w.failure
	queued := w.queue
	w.queue = nil
	w.mu.Unlock()
	if first {
		w.broke()
	}
	return queued, failure
}

func (w *stdinWriter) isStopping() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stopping
}

// writeOne writes one frame. It returns the frame's result and, when stdin
// can no longer be used, why.
func (w *stdinWriter) writeOne(fw *frameWrite) (frameErr, breakErr error) {
	if err := fw.ctx.Err(); err != nil {
		return fmt.Errorf("writing to Claude Code's stdin: %w", err), nil
	}
	var deadlineErr error
	deadlineSet := make(chan struct{})
	stopDeadline := context.AfterFunc(fw.ctx, func() {
		deadlineErr = w.file.SetWriteDeadline(pastDeadline)
		close(deadlineSet)
	})
	written, writeErr := w.file.Write(fw.data)
	if !stopDeadline() {
		<-deadlineSet
	}
	var resetErr error
	if err := w.file.SetWriteDeadline(time.Time{}); err != nil {
		resetErr = fmt.Errorf("clearing the write deadline on Claude Code's stdin: %w", err)
	}

	switch {
	case writeErr == nil:
		// The frame is out; a deadline that cannot be cleared would fail the
		// next write.
		return nil, resetErr
	case w.isStopping():
		// Close closed stdin under the write; the writer is ending anyway.
		return fmt.Errorf("%w: writing to Claude Code's stdin: %w", ErrSessionClosed, writeErr), nil
	case fw.ctx.Err() != nil && errors.Is(writeErr, os.ErrDeadlineExceeded):
		frameErr = fmt.Errorf("writing to Claude Code's stdin: %w", fw.ctx.Err())
		var causes []error
		if written > 0 {
			causes = append(causes, errors.New("a write to Claude Code's stdin stopped part-way, leaving a broken frame"))
		}
		if deadlineErr != nil {
			causes = append(causes, fmt.Errorf("setting the write deadline on Claude Code's stdin: %w", deadlineErr))
		}
		if resetErr != nil {
			causes = append(causes, resetErr)
		}
		return frameErr, errors.Join(causes...)
	default:
		frameErr = fmt.Errorf("writing to Claude Code's stdin: %w", errors.Join(writeErr, deadlineErr, resetErr))
		return frameErr, frameErr
	}
}

// encodeFrame renders frame as one JSON line.
func encodeFrame(frame any) ([]byte, error) {
	data, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("encoding a frame for Claude Code's stdin: %w", err)
	}
	return append(data, '\n'), nil
}

// writeFrame writes one frame to Claude Code's stdin through the writer
// goroutine and waits for the result; ctx bounds both the wait for the
// frame's turn and the write itself.
func (s *Session) writeFrame(ctx context.Context, frame any) error {
	data, err := encodeFrame(frame)
	if err != nil {
		return err
	}
	return s.writeError(s.stdin.write(ctx, data))
}

// enqueueAnswer queues an automatic answer to req without waiting for it to
// be written. A failure to write it is queued for the turn; written, when
// set, runs on the writer goroutine once the answer is out.
func (s *Session) enqueueAnswer(req controlRequest, frame any, written func()) {
	data, err := encodeFrame(frame)
	if err != nil {
		s.pushAnswerFailure(req, err)
		return
	}
	s.stdin.enqueue(s.lifeCtx, data, func(err error) {
		if err != nil {
			s.pushAnswerFailure(req, s.writeError(err))
			return
		}
		if written != nil {
			written()
		}
	})
}

// writeError reports a failed write as the process's exit error once the
// process has exited, unless a context or Close caused it.
func (s *Session) writeError(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrSessionClosed) {
		return err
	}
	if exitErr := s.exitError(); exitErr != nil {
		return exitErr
	}
	return err
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
