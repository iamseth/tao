// Package process holds the subprocess primitive shared by every agent runtime.
// Each runtime (pi, claude, opencode, codex) spawns a child agent the same way, so the
// Process interface, ProcessStarter signature, and DefaultProcessStarter live
// here once instead of being copied per runtime. Runtime packages alias these
// types (see each runtime's process.go) to keep their public API stable.
package process

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"

	"github.com/iamseth/tao/internal/herdr"
)

// ProcessStarter spawns an agent subprocess. Runtimes inject a fake in tests and
// DefaultProcessStarter in production.
type ProcessStarter func(ctx context.Context, cwd string, name string, args []string) (Process, error)

// Process is the subset of a running subprocess the runtimes depend on.
type Process interface {
	Stdin() io.WriteCloser
	Stdout() io.Reader
	Stderr() io.Reader
	Wait() error
	Kill() error
}

type execProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader
}

// DefaultProcessStarter starts name with args under cwd, wiring stdin/stdout/
// stderr pipes. It honors a cancelled context before spawning and binds the
// subprocess lifetime to ctx after spawning.
func DefaultProcessStarter(ctx context.Context, cwd string, name string, args []string) (Process, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204,G702 -- command and arguments are controlled by Tao.
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = herdr.StripInjectedEnv(cmd.Environ())
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd, stdin: stdin, stdout: stdout, stderr: stderr}, nil
}

func (p *execProcess) Stdin() io.WriteCloser { return p.stdin }
func (p *execProcess) Stdout() io.Reader     { return p.stdout }
func (p *execProcess) Stderr() io.Reader     { return p.stderr }
func (p *execProcess) Wait() error           { return p.cmd.Wait() }
func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	err := p.cmd.Process.Kill()
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}
