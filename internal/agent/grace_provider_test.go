package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent/process"
)

type observedSuspension struct {
	process.Process
	suspended chan struct{}
}

func (p observedSuspension) Suspend() error {
	err := p.Process.(interface{ Suspend() error }).Suspend()
	close(p.suspended)
	return err
}

func TestProvidersStopTurnsDuringCompletionGrace(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			write := func(name string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			defer write("allow-gate")
			waitFile := func(name string) {
				t.Helper()
				for {
					if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
						return
					}
					select {
					case <-ctx.Done():
						t.Fatalf("waiting for %s: %v", name, ctx.Err())
					case <-time.After(10 * time.Millisecond):
					}
				}
			}
			suspended := make(chan struct{})
			ended := make(chan struct{})
			starter := func(ctx context.Context, cwd, _ string, _ []string) (process.Process, error) {
				proc, err := process.DefaultProcessStarter(ctx, cwd, "sh", []string{"-c", `
 if [ "$1" = pi ]; then
   read -r request
   printf '%s\n' '{"id":"tao-readiness-state","type":"response","success":true,"data":{"model":{"provider":"test","id":"model"}}}'
   read -r request
   printf '%s\n' '{"id":"tao-readiness-models","type":"response","success":true,"data":{"models":[{"provider":"test","id":"model"}]}}'
   read -r request
   printf '%s\n' '{"id":"tao-prompt","type":"response","command":"prompt","success":true}'
 else
   cat >/dev/null
 fi
 # A background managed completion keeps working independently of agent turns.
 (while [ ! -f allow-gate ]; do sleep 0.01; done; echo done > gate-done) &
 echo ready > ready
 while [ ! -f attempt-turn ]; do sleep 0.01; done
 echo forbidden > new-turn
 wait
`, "sh", provider})
				if err != nil {
					return nil, err
				}
				return observedSuspension{proc, suspended}, nil
			}
			var inner Runtime = piRuntime{starter: starter}
			if provider == "claude" {
				inner = claudeRuntime{starter: starter}
			}
			ticks := make(chan time.Time, 1)
			var active atomic.Bool
			active.Store(true)
			runtime := timeoutRuntime{inner: inner, graceProbeInterval: time.Millisecond,
				newSoftTimer: func(time.Duration) (<-chan time.Time, func()) { return ticks, func() {} },
			}
			done := make(chan error, 1)
			go func() {
				_, err := runtime.RunSession(ctx, Session{
					RepoRoot: dir, Prompt: "work", Timeout: time.Minute,
					Grace: &SessionGrace{Max: time.Minute, Active: active.Load},
					BindLifetime: func(ctx context.Context) (context.Context, func() error, error) {
						return process.WithSessionLifetime(ctx, nil, func() { close(ended) }), func() error { return nil }, nil
					},
				})
				done <- err
			}()
			waitFile("ready")
			ticks <- time.Now()
			select {
			case <-suspended:
			case <-ctx.Done():
				t.Fatal("provider was not suspended at soft deadline")
			}
			write("attempt-turn")
			write("allow-gate")
			waitFile("gate-done")
			// Give an unsuspended provider ample opportunity to execute its turn.
			time.Sleep(100 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(dir, "new-turn")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("provider executed a turn during grace: %v", err)
			}
			select {
			case <-ended:
				t.Fatal("soft cutoff invalidated the completion lifetime")
			case err := <-done:
				t.Fatalf("session ended before completion settled: %v", err)
			default:
			}
			active.Store(false)
			select {
			case err := <-done:
				var timeout *SessionTimeoutError
				if !errors.As(err, &timeout) {
					t.Fatalf("session error = %v", err)
				}
			case <-ctx.Done():
				t.Fatal("session did not stop after completion settled")
			}
		})
	}
}
