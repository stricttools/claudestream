// Package vcr is the test suites' stand-in for the claude binary: it replays
// a cassette recorded from a real Claude Code (tests/fixtures/transcripts),
// writing the lines the CLI wrote and checking the shape of each line the
// host sends where the host spoke. Only tests use it: a test binary runs Main
// when the environment selects replay, so the session under test starts a
// real subprocess over real pipes.
//
// Cassette format (NDJSON): a meta record first, then the lines in order:
//
//	{"kind": "meta", "version": "2.1.220 (Claude Code)", ...}
//	{"kind": "cli",  "payload": {...}}   CLI to host
//	{"kind": "host", "payload": {...}}   host to CLI
//
// Cassettes written for the Go tests also use {"kind": "raw", "raw": "..."}
// (a line written as it is) and {"kind": "exit", "code": N, "stderr": "..."}
// (the CLI dies there).
package vcr

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// The environment the replay reads.
const (
	ModeEnv     = "CLAUDESTREAM_VCR_MODE"
	CassetteEnv = "CLAUDESTREAM_VCR_CASSETTE"
	TimeoutEnv  = "CLAUDESTREAM_VCR_READ_TIMEOUT"
	// HostLogEnv, when set, names a file each host line is appended to, so
	// a test can check what the host sent beyond its shape.
	HostLogEnv = "CLAUDESTREAM_VCR_HOST_LOG"
)

// ExitCode is the exit status of every replay failure.
const ExitCode = 96

// DefaultTimeout bounds each wait for a host line.
const DefaultTimeout = 15 * time.Second

type record struct {
	Kind    string          `json:"kind"`
	Payload json.RawMessage `json:"payload"`
	Version string          `json:"version"`
	// Raw is a "raw" record's line, written as it is (a line that is not
	// JSON, say).
	Raw string `json:"raw"`
	// Code and Stderr belong to an "exit" record: the stand-in writes
	// Stderr and exits with Code there, as a crashing CLI would.
	Code   int    `json:"code"`
	Stderr string `json:"stderr"`
}

// Active reports whether the environment selects the replay.
func Active() bool { return os.Getenv(ModeEnv) == "replay" }

// Main replays the cassette the environment names for the command line args
// and returns the exit status.
func Main(args []string) int {
	timeout := DefaultTimeout
	if raw := os.Getenv(TimeoutEnv); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fail(fmt.Sprintf("%s: %v", TimeoutEnv, err))
		}
		timeout = d
	}
	return Replay(os.Getenv(CassetteEnv), args, os.Stdin, os.Stdout, timeout)
}

func fail(msg string) int {
	fmt.Fprintln(os.Stderr, "vcr: "+msg)
	return ExitCode
}

func load(path string) (record, []record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return record{}, nil, err
	}
	var records []record
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var r record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return record{}, nil, err
		}
		records = append(records, r)
	}
	if len(records) == 0 || records[0].Kind != "meta" {
		return record{}, nil, errors.New("the first line must be a meta record")
	}
	return records[0], records[1:], nil
}

// signature is the part of a host line a replay may depend on: its type,
// and a control line's subtype. Ids and text differ between runs.
func signature(payload []byte) (string, error) {
	var head struct {
		Type     string                   `json:"type"`
		Request  struct{ Subtype string } `json:"request"`
		Response struct{ Subtype string } `json:"response"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		return "", err
	}
	switch head.Type {
	case "control_request":
		return "control_request:" + head.Request.Subtype, nil
	case "control_response":
		return "control_response:" + head.Response.Subtype, nil
	}
	return head.Type, nil
}

// Replay serves the cassette at path: -v or --version prints the recorded
// version; anything else writes the CLI's lines and checks each host line
// against the recording. After the last record it reads stdin to its end.
func Replay(path string, args []string, stdin io.Reader, stdout io.Writer, timeout time.Duration) int {
	meta, records, err := load(path)
	if err != nil {
		return fail(fmt.Sprintf("cannot load cassette %s: %v", path, err))
	}
	for _, a := range args {
		if a == "-v" || a == "--version" {
			fmt.Fprintf(stdout, "%s (vcr replay)\n", meta.Version)
			return 0
		}
	}
	lines := make(chan []byte)
	go func() {
		reader := bufio.NewReader(stdin)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				lines <- line
			}
			if err != nil {
				close(lines)
				return
			}
		}
	}()
	var hostLog *os.File
	if path := os.Getenv(HostLogEnv); path != "" {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
		if err != nil {
			return fail(err.Error())
		}
		defer f.Close()
		hostLog = f
	}
	for i, r := range records {
		switch r.Kind {
		case "raw":
			if _, err := io.WriteString(stdout, r.Raw+"\n"); err != nil {
				return fail(fmt.Sprintf("record %d: %v", i, err))
			}
			continue
		case "exit":
			fmt.Fprint(os.Stderr, r.Stderr)
			return r.Code
		}
		if r.Kind == "cli" {
			if _, err := stdout.Write(append(append([]byte(nil), r.Payload...), '\n')); err != nil {
				return fail(fmt.Sprintf("record %d: %v", i, err))
			}
			continue
		}
		want, err := signature(r.Payload)
		if err != nil {
			return fail(fmt.Sprintf("record %d: %v", i, err))
		}
		select {
		case line, ok := <-lines:
			if !ok {
				return fail(fmt.Sprintf("%s: record %d expected the host to send %q, but stdin ended", path, i, want))
			}
			if hostLog != nil {
				if _, err := hostLog.Write(line); err != nil {
					return fail(err.Error())
				}
			}
			got, err := signature(line)
			if err != nil {
				return fail(fmt.Sprintf("%s: record %d: the host sent non-JSON: %v", path, i, err))
			}
			if got != want {
				return fail(fmt.Sprintf("%s: record %d: the host sent %q, the cassette expects %q", path, i, got, want))
			}
		case <-time.After(timeout):
			return fail(fmt.Sprintf("%s: record %d expected the host to send %q within %v", path, i, want, timeout))
		}
	}
	for line := range lines {
		if hostLog != nil {
			if _, err := hostLog.Write(line); err != nil {
				return fail(err.Error())
			}
		}
	}
	return 0
}
