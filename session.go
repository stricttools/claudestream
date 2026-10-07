package claudestream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"sync"
)

// stdoutReadBuffer is the read buffer of the stdout reader; lines longer
// than it are assembled from several reads, up to the line limit.
const stdoutReadBuffer = 64 << 10

// Session is a running Claude Code subprocess. Start one with [Start], send
// prompts with [Session.Send] or [Session.SendBlocks], read each turn's
// events with [Turn.Next], and always Close it.
//
// One goroutine reads the subprocess's stdout for the whole life of the
// session. It answers what it can itself (sandbox decisions, the SDK MCP
// servers, control requests the session does not handle), resolves the
// session's own control requests, and queues every event without limit, so
// it never waits for the consumer. Events that arrive between turns are
// delivered at the start of the next turn.
//
// The methods are safe for concurrent use, except that a Turn's Next must
// not be called from more than one goroutine at a time.
type Session struct {
	cfg     Config
	proc    *process
	queue   *eventQueue
	pending *pendingControls
	mcp     *mcpResponder
	// tracker is used by the reader goroutine only.
	tracker *eventTracker

	// lifeCtx ends when Close starts. The reader's own stdin writes and the
	// tool handlers run under it.
	lifeCtx    context.Context
	endLife    context.CancelFunc
	readerDone chan struct{}
	handlers   sync.WaitGroup
	// stdinLock serializes stdin writes; a channel so waiting for it can end
	// with a context.
	stdinLock chan struct{}

	closeOnce sync.Once
	closeErr  error

	mu            sync.Mutex
	closed        bool
	unusable      bool
	stdinBroken   bool
	exitErr       error
	turnActive    bool
	turnEndQueued bool
	// outstanding maps the IDs of requests waiting for the consumer's answer
	// to their subtype.
	outstanding    map[string]string
	budget         *budgetTracker
	totals         sessionTotals
	lastResult     *Result
	filesModified  map[string]bool
	claudeVersion  string
	sessionID      string
	model          string
	cwd            string
	permissionMode string
	tools          []string
	mcpServers     []MCPServerStatus
}

// Start validates cfg, checks the Claude Code version, spawns Claude Code
// under the configured profile, and completes the start-up handshake: the
// initialize request when there are Tools, SupportedDialogs, or
// InterceptPermissions, and with Tools the SDK MCP server registration and
// every server's tool listing. ctx bounds the start only; the session lives
// until Close.
func Start(ctx context.Context, cfg Config) (*Session, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	limits := cfg.limits()
	version, err := CheckClaudeVersion(ctx, cfg.ClaudeBinary, limits.VersionCheckTimeout)
	if err != nil {
		return nil, err
	}
	proc, err := startProcess(buildArgv(cfg), cfg.Env, cfg.Cwd, limits.ShutdownTimeout)
	if err != nil {
		return nil, err
	}

	lifeCtx, endLife := context.WithCancel(context.Background())
	s := &Session{
		cfg:           cfg,
		proc:          proc,
		queue:         newEventQueue(),
		pending:       newPendingControls(),
		mcp:           newMCPResponder(cfg.Tools),
		tracker:       newEventTracker(),
		lifeCtx:       lifeCtx,
		endLife:       endLife,
		readerDone:    make(chan struct{}),
		stdinLock:     make(chan struct{}, 1),
		outstanding:   map[string]string{},
		budget:        newBudgetTracker(cfg.Budget),
		filesModified: map[string]bool{},
		claudeVersion: version,
	}
	go s.readLoop(limits.LineLimit)

	if err := s.handshake(ctx); err != nil {
		if closeErr := s.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
		return nil, err
	}
	return s, nil
}

func (s *Session) handshake(ctx context.Context) error {
	c := s.cfg
	if len(c.Tools) == 0 && c.SupportedDialogs == nil && !c.InterceptPermissions {
		return nil
	}
	servers := c.toolServers()
	if err := s.handshakeRequest(ctx, initializeRequestID, newInitializeRequest(servers, c.SupportedDialogs)); err != nil {
		return fmt.Errorf("the initialize handshake failed: %w", err)
	}
	if len(c.Tools) == 0 {
		return nil
	}
	if err := s.handshakeRequest(ctx, mcpSetServersRequestID, newMCPSetServersRequest(servers)); err != nil {
		return fmt.Errorf("registering the SDK MCP servers failed: %w", err)
	}
	select {
	case <-s.mcp.allListed:
		return nil
	case <-s.readerDone:
		return fmt.Errorf("waiting for Claude Code to list the SDK MCP servers' tools: %w", s.exitError())
	case <-ctx.Done():
		return fmt.Errorf("waiting for Claude Code to list the SDK MCP servers' tools: %w", ctx.Err())
	}
}

// handshakeRequest sends one of the fixed-ID start-up requests and waits
// for its response.
func (s *Session) handshakeRequest(ctx context.Context, requestID string, frame controlRequestFrame) error {
	outcome, err := s.pending.register(requestID)
	if err != nil {
		return err
	}
	if err := s.writeFrame(ctx, frame); err != nil {
		s.pending.drop(requestID)
		return err
	}
	_, err = s.awaitControl(ctx, requestID, outcome)
	return err
}

// Turn is one prompt's exchange. Read it with Next until io.EOF.
type Turn struct {
	s      *Session
	done   bool
	failed bool
}

// Send sends text as a user message and opens a turn. It returns
// ErrTurnActive while the previous turn has not been read to io.EOF,
// ErrSessionUnusable after a failed turn, the process's exit error after it
// exited, and ErrSessionClosed after Close.
func (s *Session) Send(ctx context.Context, text string) (*Turn, error) {
	return s.send(ctx, func(sessionID string) userFrame { return newUserTextFrame(text, sessionID) })
}

// SendBlocks sends a user message made of content blocks (text, image, and
// so on, each a JSON object) and opens a turn, as Send does.
func (s *Session) SendBlocks(ctx context.Context, blocks []json.RawMessage) (*Turn, error) {
	return s.send(ctx, func(sessionID string) userFrame { return newUserBlocksFrame(blocks, sessionID) })
}

func (s *Session) send(ctx context.Context, build func(sessionID string) userFrame) (*Turn, error) {
	s.mu.Lock()
	if err := s.sendableLocked(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.turnActive, s.turnEndQueued, s.lastResult = true, false, nil
	frame := build(s.sessionID)
	s.mu.Unlock()

	if err := s.writeFrame(ctx, frame); err != nil {
		s.mu.Lock()
		s.turnActive = false
		s.mu.Unlock()
		return nil, err
	}
	return &Turn{s: s}, nil
}

func (s *Session) sendableLocked() error {
	switch {
	case s.closed:
		return ErrSessionClosed
	case s.exitErr != nil:
		return s.exitErr
	case s.unusable:
		return ErrSessionUnusable
	case s.turnActive:
		return ErrTurnActive
	}
	return nil
}

// Next returns the turn's next event. After the turn's Result and the
// BudgetThreshold events it caused, it returns io.EOF, and keeps returning
// it. It returns ErrAuthentication when Claude Code reports the profile
// cannot authenticate, the process's exit error when it exits, and ctx's
// error when ctx ends first; each of these makes the session unusable, and
// later calls return ErrSessionUnusable.
func (t *Turn) Next(ctx context.Context) (Event, error) {
	if t.done {
		return nil, io.EOF
	}
	if t.failed {
		return nil, ErrSessionUnusable
	}
	item, err := t.s.queue.next(ctx)
	if err != nil {
		t.fail()
		return nil, err
	}
	switch {
	case item.turnEnd:
		t.done = true
		t.s.endTurn(false)
		return nil, io.EOF
	case item.err != nil:
		t.fail()
		return nil, item.err
	default:
		return item.event, nil
	}
}

func (t *Turn) fail() {
	t.failed = true
	t.s.endTurn(true)
}

func (s *Session) endTurn(unusable bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.turnActive = false
	if unusable {
		s.unusable = true
	}
}

// Close stops Claude Code (close stdin, wait the shutdown timeout, SIGTERM,
// wait again, SIGKILL), ends the reader and the tool handlers' context, and
// fails every pending control request with ErrSessionClosed. It returns
// what went wrong while stopping, and the same on every later call.
func (s *Session) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.endLife()

		var errs []error
		if err := s.proc.shutdown(); err != nil {
			errs = append(errs, err)
		}
		// The reader drains what the process wrote before it exited; a
		// descendant holding stdout open must not keep Close waiting.
		waitClosed(s.readerDone, s.proc.shutdownTimeout)
		if err := s.proc.closeOutputs(); err != nil {
			errs = append(errs, err)
		}
		<-s.readerDone
		<-s.proc.stderrDone

		handlersDone := make(chan struct{})
		go func() {
			s.handlers.Wait()
			close(handlersDone)
		}()
		if !waitClosed(handlersDone, s.proc.shutdownTimeout) {
			errs = append(errs, fmt.Errorf("a tool handler had not returned %s after its context ended", s.proc.shutdownTimeout))
		}
		s.pending.failAll(ErrSessionClosed)
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}

// readLoop is the only reader of stdout.
func (s *Session) readLoop(lineLimit int) {
	defer close(s.readerDone)
	reader := bufio.NewReaderSize(s.proc.stdout, stdoutReadBuffer)
	for {
		line, readErr := readStdoutLine(reader, lineLimit)
		if len(line) > 0 {
			if s.cfg.RawOutput != nil {
				if _, err := s.cfg.RawOutput.Write(line); err != nil {
					s.finish(fmt.Errorf("copying a stdout line to RawOutput: %w", err))
					return
				}
			}
			s.handleLine(line)
		}
		if readErr != nil {
			s.finish(readErr)
			return
		}
	}
}

// readStdoutLine returns the next line with its newline (none on a last
// line without one). A line whose content exceeds limit bytes is
// ErrLineTooLong.
func readStdoutLine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		chunk, err := reader.ReadSlice('\n')
		line = append(line, chunk...)
		if len(bytes.TrimSuffix(line, []byte("\n"))) > limit {
			return nil, fmt.Errorf("%w (%d bytes)", ErrLineTooLong, limit)
		}
		switch {
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err == nil, errors.Is(err, io.EOF), errors.Is(err, os.ErrClosed):
			return line, err
		default:
			return line, fmt.Errorf("reading Claude Code's stdout: %w", err)
		}
	}
}

// finish records why reading ended, fails every pending control request
// with it, and queues it as the terminal error.
func (s *Session) finish(readErr error) {
	var err error
	switch {
	case errors.Is(readErr, io.EOF):
		<-s.proc.exited
		s.proc.waitStderr(s.proc.shutdownTimeout)
		err = &ErrProcessExited{ExitCode: s.proc.exitCode(), StderrTail: s.proc.stderrLines.snapshot()}
	case errors.Is(readErr, os.ErrClosed):
		err = ErrSessionClosed
	default:
		err = readErr
	}
	s.mu.Lock()
	s.exitErr = err
	s.mu.Unlock()
	s.pending.failAll(err)
	s.queue.push(queueItem{err: err, terminal: true})
}

func (s *Session) exitError() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exitErr
}

func (s *Session) handleLine(line []byte) {
	parsed := parseLine(line)
	switch {
	case parsed.controlResponse != nil:
		if !s.pending.resolve(*parsed.controlResponse) {
			s.queue.push(queueItem{event: parsed.controlResponse.unknownEvent()})
		}
	case parsed.controlRequest != nil:
		s.handleControlRequest(*parsed.controlRequest)
	default:
		s.handleEvents(s.tracker.track(parsed.events))
		if isAuthenticationError(parsed.assistantError) {
			s.queue.push(queueItem{err: fmt.Errorf("%w (Claude Code reported %q)", ErrAuthentication, parsed.assistantError)})
		}
	}
}

// handleEvents updates the session's state from events and queues them;
// after a Result it also queues the budget thresholds it crossed and, when
// it ends the open turn, the turn's end.
func (s *Session) handleEvents(events []Event) {
	if len(events) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]queueItem, 0, len(events)+1)
	for _, ev := range events {
		items = append(items, queueItem{event: ev})
		switch e := ev.(type) {
		case SystemInit:
			s.sessionID = e.SessionID
			s.model = e.Model
			s.cwd = e.Cwd
			s.permissionMode = e.PermissionMode
			s.tools = slices.Clone(e.Tools)
			s.mcpServers = slices.Clone(e.MCPServers)
		case FileWrite:
			if e.Path != "" {
				s.filesModified[e.Path] = true
			}
		case FileEdit:
			if e.Path != "" {
				s.filesModified[e.Path] = true
			}
		case Result:
			result := cloneResult(e)
			s.lastResult = &result
			s.totals.add(e)
			for _, threshold := range s.budget.check(e.EventHeader, s.totals) {
				items = append(items, queueItem{event: threshold})
			}
			if s.turnActive && !s.turnEndQueued {
				items = append(items, queueItem{turnEnd: true})
				s.turnEndQueued = true
			}
		}
	}
	// Queued under mu so the turn bookkeeping and the queue order agree.
	s.queue.push(items...)
}

// handleControlRequest answers or forwards one control request from Claude
// Code. Every request gets an answer: Claude Code waits for one.
func (s *Session) handleControlRequest(req controlRequest) {
	switch req.subtype {
	case controlSubtypeCanUseTool:
		request, err := req.permissionRequest()
		if err != nil {
			s.refuseUndecodable(req, err)
			return
		}
		if s.cfg.Sandbox == nil {
			s.forwardRequest(req, request)
			return
		}
		decision := s.cfg.Sandbox.Decide(request.ToolName, request.ToolInput, s.cfg.Cwd)
		answer := newDenyResponse(req.requestID, decision.Message)
		if decision.Allowed {
			answer = newAllowResponse(req.requestID, request.ToolInput, nil)
		}
		if err := s.writeFrame(s.lifeCtx, answer); err != nil {
			s.pushAnswerFailure(req, err)
			return
		}
		s.queue.push(queueItem{event: PermissionDecided{
			EventHeader: request.EventHeader,
			Request:     request,
			Allowed:     decision.Allowed,
			Message:     decision.Message,
		}})
	case controlSubtypeRequestUserDialog:
		request, err := req.userDialogRequest()
		if err != nil {
			s.refuseUndecodable(req, err)
			return
		}
		s.forwardRequest(req, request)
	case controlSubtypeMCPMessage:
		s.handleMCPMessage(req)
	default:
		s.refuseUnhandled(req, req.unhandledMessage())
	}
}

// forwardRequest queues a request for the consumer to answer and records
// it as outstanding.
func (s *Session) forwardRequest(req controlRequest, ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.outstanding[req.requestID] = req.subtype
	s.queue.push(queueItem{event: ev})
}

// refuseUnhandled answers a request with an error response and queues an
// UnknownControlRequest.
func (s *Session) refuseUnhandled(req controlRequest, message string) {
	if err := s.writeFrame(s.lifeCtx, newErrorResponse(req.requestID, message)); err != nil {
		s.pushAnswerFailure(req, err)
		return
	}
	s.queue.push(queueItem{event: req.unknownControlRequest()})
}

// refuseUndecodable answers a request the session could not decode with an
// error response and queues an UndecodableEvent.
func (s *Session) refuseUndecodable(req controlRequest, decodeErr error) {
	message := fmt.Sprintf("claudestream could not decode the %s request: %v", req.subtype, decodeErr)
	if err := s.writeFrame(s.lifeCtx, newErrorResponse(req.requestID, message)); err != nil {
		s.pushAnswerFailure(req, err)
		return
	}
	s.queue.push(queueItem{event: UndecodableEvent{EventHeader: req.header, Raw: req.raw, Err: decodeErr}})
}

// pushAnswerFailure queues the failure to answer a request as an error the
// turn returns: Claude Code waits for that answer, so the turn cannot end.
func (s *Session) pushAnswerFailure(req controlRequest, err error) {
	s.queue.push(queueItem{err: fmt.Errorf("answering Claude Code's %s request %s: %w", req.subtype, req.requestID, err)})
}

func cloneResult(r Result) Result {
	if r.Usage != nil {
		usage := *r.Usage
		r.Usage = &usage
	}
	if r.APIErrorStatus != nil {
		status := *r.APIErrorStatus
		r.APIErrorStatus = &status
	}
	r.ModelUsage = maps.Clone(r.ModelUsage)
	return r
}

// Accessors. The session fields come from the latest SystemInit and change
// with SetModel and SetPermissionMode; the totals from each Result.

// SessionID is Claude Code's session ID; empty before the first SystemInit.
func (s *Session) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// Model is the model the latest SystemInit reported, or the one SetModel
// chose; empty when unknown.
func (s *Session) Model() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

// Tools lists the tools the latest SystemInit reported.
func (s *Session) Tools() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.tools)
}

// ClaudeVersion is the version `claude -v` reported at Start.
func (s *Session) ClaudeVersion() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claudeVersion
}

// Cwd is the working directory the latest SystemInit reported.
func (s *Session) Cwd() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cwd
}

// MCPServers lists the MCP servers the latest SystemInit reported.
func (s *Session) MCPServers() []MCPServerStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.mcpServers)
}

// PermissionMode is the mode the latest SystemInit reported, or the one
// SetPermissionMode set.
func (s *Session) PermissionMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permissionMode
}

// TurnCount counts the Results received.
func (s *Session) TurnCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totals.Turns
}

// TotalTokens is the input plus output tokens of the latest Result's usage.
func (s *Session) TotalTokens() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totals.Tokens
}

// TotalCostUSD is the latest Result's session cost.
func (s *Session) TotalCostUSD() float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.totals.CostUSD
}

// FilesModified lists, sorted, the paths of every FileWrite and FileEdit so
// far. Files changed by other tools (Bash, for example) are not tracked.
func (s *Session) FilesModified() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Sorted(maps.Keys(s.filesModified))
}

// LastResult is a copy of the current turn's Result; nil before it arrives.
func (s *Session) LastResult() *Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastResult == nil {
		return nil
	}
	result := cloneResult(*s.lastResult)
	return &result
}

// PID is the subprocess's process ID.
func (s *Session) PID() int {
	return s.proc.pid()
}

// Stderr returns the last lines Claude Code wrote to stderr, oldest first.
func (s *Session) Stderr() []string {
	return s.proc.stderrLines.snapshot()
}

// queueItem is one entry of the event queue: an event, an error the turn
// returns, the end of a turn, or the terminal error that stays at the head
// once reading ended.
type queueItem struct {
	event    Event
	err      error
	turnEnd  bool
	terminal bool
}

// eventQueue is unbounded: push never blocks. It has one consumer at a
// time, the open turn.
type eventQueue struct {
	mu    sync.Mutex
	items []queueItem
	// wake holds at most one pending wake-up for the consumer.
	wake chan struct{}
}

func newEventQueue() *eventQueue {
	return &eventQueue{wake: make(chan struct{}, 1)}
}

func (q *eventQueue) push(items ...queueItem) {
	if len(items) == 0 {
		return
	}
	q.mu.Lock()
	q.items = append(q.items, items...)
	q.mu.Unlock()
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

// next removes and returns the head item, waiting for one until ctx ends; a
// terminal item is returned without being removed.
func (q *eventQueue) next(ctx context.Context) (queueItem, error) {
	for {
		q.mu.Lock()
		if len(q.items) > 0 {
			item := q.items[0]
			if !item.terminal {
				q.items[0] = queueItem{}
				q.items = q.items[1:]
				if len(q.items) == 0 {
					q.items = nil
				}
			}
			q.mu.Unlock()
			return item, nil
		}
		q.mu.Unlock()
		select {
		case <-q.wake:
		case <-ctx.Done():
			return queueItem{}, ctx.Err()
		}
	}
}
