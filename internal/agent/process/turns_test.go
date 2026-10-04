package process

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
)

type turnProcess struct {
	Process
	killed bool
	exited chan struct{}
	once   sync.Once
}

func (p *turnProcess) Kill() error {
	p.killed = true
	p.once.Do(func() { close(p.exited) })
	return nil
}

func (p *turnProcess) Wait() error { <-p.exited; return nil }

type suspendableTurnProcess struct {
	*turnProcess
	suspended bool
	err       error
}

func (p *suspendableTurnProcess) Suspend() error { p.suspended = true; return p.err }

func TestLimitTurns(t *testing.T) {
	for _, mode := range []string{"suspend", "unsupported", "suspend error", "early exit", "early kill", "context cancelled"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				cutoff := make(chan struct{})
				base := &turnProcess{exited: make(chan struct{})}
				suspender := &suspendableTurnProcess{turnProcess: base}
				var child Process = suspender
				if mode == "unsupported" {
					child = base
				}
				if mode == "suspend error" {
					suspender.err = errors.New("signal failed")
				}
				proc, err := LimitTurns(func(context.Context, string, string, []string) (Process, error) {
					return child, nil
				}, cutoff)(ctx, "", "", nil)
				if err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "early exit":
					base.once.Do(func() { close(base.exited) })
					if err := proc.Wait(); err != nil {
						t.Fatal(err)
					}
				case "early kill":
					_ = proc.Kill()
				case "context cancelled":
					cancel()
					synctest.Wait()
				}
				close(cutoff)
				synctest.Wait()
				wantSuspended := mode == "suspend" || mode == "suspend error"
				wantKilled := mode == "unsupported" || mode == "suspend error" || mode == "early kill"
				if suspender.suspended != wantSuspended || base.killed != wantKilled {
					t.Fatalf("suspended=%v killed=%v", suspender.suspended, base.killed)
				}
				_ = proc.Kill()
				_ = proc.Wait()
			})
		})
	}
}

func TestLimitTurnsStartup(t *testing.T) {
	if LimitTurns(nil, nil) != nil {
		t.Fatal("disabled cutoff replaced default starter")
	}
	cutoff := make(chan struct{})
	closed := make(chan struct{})
	close(closed)
	if _, err := LimitTurns(nil, closed)(context.Background(), "", "unused", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired cutoff allowed startup: %v", err)
	}
	want := errors.New("startup failed")
	starter := func(context.Context, string, string, []string) (Process, error) { return nil, want }
	if _, err := LimitTurns(starter, cutoff)(context.Background(), "", "", nil); !errors.Is(err, want) {
		t.Fatalf("startup error = %v", err)
	}
}
