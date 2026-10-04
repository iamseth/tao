package process

import (
	"context"
	"sync"
	"syscall"
)

// LimitTurns stops provider execution at cutoff without signalling its children.
// A suspended provider cannot start another turn or tool, but an already-running
// managed completion retains its pipes and lifetime until the session context
// ends. It is never resumed: ordinary session cancellation kills it after grace.
// Starters without suspension support fail closed by killing the provider.
func LimitTurns(starter ProcessStarter, cutoff <-chan struct{}) ProcessStarter {
	if cutoff == nil {
		return starter
	}
	if starter == nil {
		starter = DefaultProcessStarter
	}
	return func(ctx context.Context, cwd, name string, args []string) (Process, error) {
		select {
		case <-cutoff:
			return nil, context.DeadlineExceeded
		default:
		}
		proc, err := starter(ctx, cwd, name, args)
		if err != nil {
			return nil, err
		}
		p := &turnLimitedProcess{Process: proc, stop: make(chan struct{}), done: make(chan struct{})}
		go func() {
			defer close(p.done)
			select {
			case <-ctx.Done():
			case <-p.stop:
			case <-cutoff:
				if suspender, ok := proc.(interface{ Suspend() error }); ok {
					if suspender.Suspend() == nil {
						return
					}
				}
				_ = proc.Kill()
			}
		}()
		return p, nil
	}
}

type turnLimitedProcess struct {
	Process
	stop, done chan struct{}
	once       sync.Once
}

func (p *turnLimitedProcess) stopWatcher() {
	p.once.Do(func() { close(p.stop) })
	<-p.done
}

func (p *turnLimitedProcess) Wait() error {
	err := p.Process.Wait()
	p.stopWatcher()
	return err
}

func (p *turnLimitedProcess) Kill() error {
	p.stopWatcher()
	return p.Process.Kill()
}

func (p *execProcess) Suspend() error {
	// Signal the provider PID, never its process group: completion and gate
	// subprocesses must remain runnable. SIGSTOP cannot be ignored by the agent.
	return p.cmd.Process.Signal(syscall.SIGSTOP)
}
