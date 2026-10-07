package claudestream

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// stderrRingLines is how many stderr lines a session keeps.
const stderrRingLines = 200

// stderrLineLimit caps one kept stderr line, in bytes; the rest of a longer
// line is dropped and the kept part marked.
const stderrLineLimit = 64 << 10

// process is a running Claude Code subprocess. Its three standard streams
// are os.Pipe pairs rather than exec's pipe helpers, so the waiter can reap
// the process while the reader is still draining stdout, and so writes to
// stdin honor deadlines.
type process struct {
	cmd    *exec.Cmd
	stdin  *os.File
	stdout *os.File
	stderr *os.File

	stderrLines *stderrRing
	stderrDone  chan struct{}

	// exited is closed once the process has been reaped; state is set
	// before.
	exited chan struct{}
	state  *os.ProcessState
	// waitErr is a failure of the wait other than a non-zero exit; set
	// before exited is closed.
	waitErr error

	shutdownTimeout time.Duration
}

// startProcess spawns argv with env merged over this process's environment,
// in dir (empty inherits this process's working directory), and starts
// draining stderr and waiting for the exit.
func startProcess(argv []string, env map[string]string, dir string, shutdownTimeout time.Duration) (*process, error) {
	if len(argv) == 0 {
		return nil, errors.New("starting Claude Code: empty command line")
	}
	var parentEnds, childEnds []*os.File
	closeAll := func(files []*os.File) error {
		var errs []error
		for _, f := range files {
			if err := f.Close(); err != nil {
				errs = append(errs, fmt.Errorf("closing a pipe of Claude Code's standard streams: %w", err))
			}
		}
		return errors.Join(errs...)
	}
	newPipe := func() (r, w *os.File, err error) {
		r, w, err = os.Pipe()
		if err != nil {
			return nil, nil, errors.Join(fmt.Errorf("starting Claude Code: creating a pipe: %w", err),
				closeAll(parentEnds), closeAll(childEnds))
		}
		return r, w, nil
	}
	stdinR, stdinW, err := newPipe()
	if err != nil {
		return nil, err
	}
	parentEnds, childEnds = append(parentEnds, stdinW), append(childEnds, stdinR)
	stdoutR, stdoutW, err := newPipe()
	if err != nil {
		return nil, err
	}
	parentEnds, childEnds = append(parentEnds, stdoutR), append(childEnds, stdoutW)
	stderrR, stderrW, err := newPipe()
	if err != nil {
		return nil, err
	}
	parentEnds, childEnds = append(parentEnds, stderrR), append(childEnds, stderrW)

	// Deadlines on stdin are how a write gives up when its context ends;
	// refuse to start if the pipe does not support them.
	if err := stdinW.SetWriteDeadline(time.Time{}); err != nil {
		return nil, errors.Join(fmt.Errorf("starting Claude Code: the stdin pipe does not support write deadlines: %w", err),
			closeAll(parentEnds), closeAll(childEnds))
	}

	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(os.Environ(), env)
	cmd.Stdin = stdinR
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	startErr := cmd.Start()
	// The child holds its own copies of its ends; the parent's must close so
	// that EOF arrives when the child exits.
	closeChildErr := closeAll(childEnds)
	if startErr != nil {
		return nil, errors.Join(fmt.Errorf("starting Claude Code (%s): %w", argv[0], startErr),
			closeChildErr, closeAll(parentEnds))
	}
	if closeChildErr != nil {
		// A child end left open in this process would keep EOF from ever
		// arriving on stdout, so the process cannot be used.
		killErr := cmd.Process.Kill()
		if errors.Is(killErr, os.ErrProcessDone) {
			killErr = nil
		}
		waitErr := cmd.Wait()
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			waitErr = nil
		}
		return nil, errors.Join(fmt.Errorf("starting Claude Code (%s): %w", argv[0], closeChildErr),
			killErr, waitErr, closeAll(parentEnds))
	}

	p := &process{
		cmd:             cmd,
		stdin:           stdinW,
		stdout:          stdoutR,
		stderr:          stderrR,
		stderrLines:     &stderrRing{},
		stderrDone:      make(chan struct{}),
		exited:          make(chan struct{}),
		shutdownTimeout: shutdownTimeout,
	}
	go func() {
		defer close(p.stderrDone)
		p.stderrLines.drain(p.stderr)
	}()
	go func() {
		// A non-zero exit is reported through state; any other failure of
		// the wait is kept in waitErr.
		if err := cmd.Wait(); err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				p.waitErr = fmt.Errorf("waiting for Claude Code to exit: %w", err)
			}
		}
		p.state = cmd.ProcessState
		close(p.exited)
	}()
	return p, nil
}

// mergeEnv returns base with every key of overrides replaced or added, in
// sorted key order after the untouched base entries.
func mergeEnv(base []string, overrides map[string]string) []string {
	merged := make([]string, 0, len(base)+len(overrides))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[key]; overridden {
			continue
		}
		merged = append(merged, entry)
	}
	keys := make([]string, 0, len(overrides))
	for key := range overrides {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		merged = append(merged, key+"="+overrides[key])
	}
	return merged
}

func (p *process) pid() int {
	return p.cmd.Process.Pid
}

// exitCode is the process's exit code once it exited; -1 when a signal
// ended it or it has not exited.
func (p *process) exitCode() int {
	select {
	case <-p.exited:
		return p.state.ExitCode()
	default:
		return -1
	}
}

// waitError is a failure of the wait other than a non-zero exit; nil
// before the process exited.
func (p *process) waitError() error {
	select {
	case <-p.exited:
		return p.waitErr
	default:
		return nil
	}
}

func (p *process) waitExit(timeout time.Duration) bool {
	return waitClosed(p.exited, timeout)
}

// waitStderr gives the stderr drain up to timeout to read the last lines
// after the process exited, and reports whether it finished.
func (p *process) waitStderr(timeout time.Duration) bool {
	return waitClosed(p.stderrDone, timeout)
}

// waitClosed waits up to timeout for done to close and reports whether it
// did.
func waitClosed(done <-chan struct{}, timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

// shutdown stops the process: close stdin, wait shutdownTimeout, SIGTERM,
// wait shutdownTimeout, SIGKILL, and wait for the exit. It leaves stdout and
// stderr open for their readers to drain.
func (p *process) shutdown() error {
	var errs []error
	if err := p.stdin.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
		errs = append(errs, fmt.Errorf("closing Claude Code's stdin: %w", err))
	}
	if p.waitExit(p.shutdownTimeout) {
		return errors.Join(errs...)
	}
	if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		errs = append(errs, fmt.Errorf("sending SIGTERM to Claude Code: %w", err))
	}
	if p.waitExit(p.shutdownTimeout) {
		return errors.Join(errs...)
	}
	if err := p.cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		errs = append(errs, fmt.Errorf("sending SIGKILL to Claude Code: %w", err))
		return errors.Join(errs...)
	}
	<-p.exited
	return errors.Join(errs...)
}

// closeOutputs closes the parent's ends of stdout and stderr, which ends
// their readers even when a descendant of the process still holds the
// pipes open.
func (p *process) closeOutputs() error {
	var errs []error
	for _, f := range []*os.File{p.stdout, p.stderr} {
		if err := f.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			errs = append(errs, fmt.Errorf("closing Claude Code's output pipe: %w", err))
		}
	}
	return errors.Join(errs...)
}

// stderrRing keeps the last stderrRingLines non-blank stderr lines.
type stderrRing struct {
	mu    sync.Mutex
	lines []string
}

// drain reads src line by line until EOF or until the pipe is closed. A
// read error other than those is kept as a final line, so it is not lost.
func (r *stderrRing) drain(src io.Reader) {
	reader := bufio.NewReader(src)
	var line []byte
	truncated := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if room := stderrLineLimit - len(line); room > 0 {
			take := min(room, len(chunk))
			line = append(line, chunk[:take]...)
			truncated = truncated || take < len(chunk)
		} else if len(chunk) > 0 {
			truncated = true
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		r.add(line, truncated)
		line, truncated = line[:0], false
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				r.add([]byte("claudestream: reading Claude Code's stderr failed: "+err.Error()), false)
			}
			return
		}
	}
}

func (r *stderrRing) add(line []byte, truncated bool) {
	text := strings.TrimSpace(strings.ToValidUTF8(string(line), "\uFFFD"))
	if text == "" {
		return
	}
	if truncated {
		text += " [line truncated by claudestream]"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, text)
	if over := len(r.lines) - stderrRingLines; over > 0 {
		r.lines = slices.Delete(r.lines, 0, over)
	}
}

// snapshot returns a copy of the kept lines, oldest first.
func (r *stderrRing) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.lines)
}
