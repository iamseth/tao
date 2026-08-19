// Package streamjson holds a reusable newline-delimited-JSON process pump for
// agent transports. These transports spawn a child process, deliver the prompt
// on stdin, and read a stream of one-JSON-object-per-line
// events until the process exits. The concurrency-sensitive machinery for that
// — reading stdout on a goroutine, draining stderr to the log, context-aware
// cancellation, killing the process exactly once on abort, and a deterministic
// Close — lives here once instead of being copied per transport. Each transport
// supplies only the parts that genuinely differ: how to decode one event into
// its result (the Handler) and the labels used in error messages.
package streamjson

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/iamseth/tao/internal/agent/logrecord"
	"github.com/iamseth/tao/internal/agent/process"
)

// Event is one decoded line of the agent's JSON stream.
type Event = map[string]any

// Handler folds one decoded event into the transport's accumulated result R.
// Returning an error aborts the stream and kills the process; the runner logs
// the error before returning it.
type Handler[R any] func(Event, *R) error

type readResult struct {
	event Event
	err   error
}

// Runner pumps an agent process's stdout stream and owns the process lifecycle.
// Name is the agent label used in error messages and streamKind names the wire
// format in parse errors.
type Runner[R any] struct {
	name         string
	streamKind   string
	proc         process.Process
	stdin        io.WriteCloser
	log          io.Writer
	handle       Handler[R]
	events       chan readResult
	done         chan error
	once         sync.Once
	mu           sync.Mutex
	doneConsumed bool
}

// SessionConfig describes one stream-json agent session. Executable is both
// the process name passed to Starter and the agent label used in diagnostics;
// streamKind names the wire format in parse errors.
type SessionConfig[R any] struct {
	Starter    process.ProcessStarter
	RepoRoot   string
	Executable string
	Args       []string
	Prompt     string
	StreamKind string
	Log        io.Writer
	Handle     Handler[R]
}

type preReadError struct {
	err error
}

func (e preReadError) Error() string { return e.err.Error() }
func (e preReadError) Unwrap() error { return e.err }

// IsPreReadError reports whether err happened before the stream read began.
// Callers use this to preserve legacy result shaping for process-start and
// prompt-write failures, where no stream result existed to decorate.
func IsPreReadError(err error) bool {
	var target preReadError
	return errors.As(err, &target)
}

// RunSession starts the configured process, sends the prompt, reads its event
// stream to completion, and waits for process exit. A nil Starter uses
// process.DefaultProcessStarter. On stream, handler, or context errors it
// preserves Runner's abort/kill-once/Close semantics and returns the partially
// accumulated result.
func RunSession[R any](ctx context.Context, config SessionConfig[R]) (R, error) {
	starter := config.Starter
	if starter == nil {
		starter = process.DefaultProcessStarter
	}
	proc, err := starter(ctx, config.RepoRoot, config.Executable, config.Args)
	if err != nil {
		var result R
		return result, preReadError{err: err}
	}
	runner := New(config.Executable, config.StreamKind, proc, config.Log, config.Handle)
	defer runner.Close()

	if err := runner.SendPrompt(ctx, config.Prompt); err != nil {
		var result R
		return result, preReadError{err: err}
	}
	result, err := runner.Read(ctx)
	if err != nil {
		return result, err
	}
	if err := runner.Wait(ctx); err != nil {
		return result, err
	}
	return result, nil
}

// New starts a Runner for proc: it begins reading stdout, waits on the process
// exit, and drains stderr to log (or discards it when log is nil). handle is
// invoked for each decoded event during Read.
func New[R any](name, streamKind string, proc process.Process, log io.Writer, handle Handler[R]) *Runner[R] {
	r := &Runner[R]{
		name:       name,
		streamKind: streamKind,
		proc:       proc,
		stdin:      proc.Stdin(),
		log:        log,
		handle:     handle,
		events:     make(chan readResult),
		done:       make(chan error, 1),
	}
	go r.readStdout(proc.Stdout())
	go func() { r.done <- proc.Wait() }()
	if stderr := proc.Stderr(); stderr != nil {
		go drainStderr(name, stderr, log)
	}
	return r
}

// SendPrompt writes the prompt to stdin and closes it, signalling end of input.
func (r *Runner[R]) SendPrompt(ctx context.Context, prompt string) error {
	select {
	case <-ctx.Done():
		return r.abort(ctx.Err())
	default:
	}
	if _, err := io.WriteString(r.stdin, prompt); err != nil {
		return fmt.Errorf("write %s prompt: %w", r.name, err)
	}
	if err := r.stdin.Close(); err != nil {
		return fmt.Errorf("close %s stdin: %w", r.name, err)
	}
	return nil
}

func (r *Runner[R]) readStdout(stdout io.Reader) {
	defer close(r.events)
	reader := bufio.NewReader(stdout)
	line := 0
	for {
		raw, err := reader.ReadBytes('\n')
		if len(raw) > 0 {
			line++
			raw = trimJSONLLineEnding(raw)
			if len(raw) != 0 {
				var ev Event
				if err := json.Unmarshal(raw, &ev); err != nil {
					r.events <- readResult{err: fmt.Errorf("parse %s %s line %d: %w", r.name, r.streamKind, line, err)}
					return
				}
				r.events <- readResult{event: ev}
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return
		}
		r.events <- readResult{err: fmt.Errorf("read %s stdout: %w", r.name, err)}
		return
	}
}

func trimJSONLLineEnding(raw []byte) []byte {
	if len(raw) > 0 && raw[len(raw)-1] == '\n' {
		raw = raw[:len(raw)-1]
	}
	if len(raw) > 0 && raw[len(raw)-1] == '\r' {
		raw = raw[:len(raw)-1]
	}
	return raw
}

// Read consumes events until the stream closes or a handler/parse error occurs.
// On error it kills the process and returns the partially accumulated result.
func (r *Runner[R]) Read(ctx context.Context) (R, error) {
	var result R
	for {
		select {
		case <-ctx.Done():
			return result, r.abort(ctx.Err())
		case item, ok := <-r.events:
			if !ok {
				return result, nil
			}
			if item.err != nil {
				return result, r.abort(item.err)
			}
			if err := r.handle(item.event, &result); err != nil {
				r.LogError(err)
				return result, r.abort(err)
			}
		}
	}
}

// Wait blocks for the process to exit after the stream is drained.
func (r *Runner[R]) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return r.abort(ctx.Err())
	case err := <-r.done:
		r.markDoneConsumed()
		if err != nil {
			return fmt.Errorf("%s exited: %w", r.name, err)
		}
		return nil
	}
}

func (r *Runner[R]) abort(cause error) error {
	r.once.Do(func() {
		_ = r.proc.Kill()
	})
	return cause
}

// Close releases the process. It closes stdin and, if the exit was not already
// consumed by Wait, kills the process and reaps it so no goroutine leaks.
func (r *Runner[R]) Close() {
	_ = r.stdin.Close()
	if r.isDoneConsumed() {
		return
	}
	select {
	case <-r.done:
		r.markDoneConsumed()
	default:
		_ = r.proc.Kill()
		<-r.done
		r.markDoneConsumed()
	}
}

func (r *Runner[R]) markDoneConsumed() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.doneConsumed = true
}

func (r *Runner[R]) isDoneConsumed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.doneConsumed
}

// LogError writes a "tao <name> error: ..." line to the runner log, matching the
// diagnostic the transports emit on a failed event.
func (r *Runner[R]) LogError(err error) {
	if r.log != nil && err != nil {
		_ = logrecord.Write(r.log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao %s error: %v", r.name, err)})
	}
}

func drainStderr(name string, stderr io.Reader, log io.Writer) {
	if log == nil {
		_, _ = io.Copy(io.Discard, stderr)
		return
	}
	scanner := bufio.NewScanner(stderr)
	for scanner.Scan() {
		_ = logrecord.Write(log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao %s stderr: %s", name, scanner.Text())})
	}
	if err := scanner.Err(); err != nil {
		_ = logrecord.Write(log, logrecord.Record{Type: logrecord.TypeDiagnostic, Content: fmt.Sprintf("tao %s stderr: %v", name, err)})
	}
}
