package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/agent/process"
)

// Provider exit must complete the session even with timeouts disabled and a
// surviving child holding both output pipes open.
func TestClaudeInheritedStdoutExit(t *testing.T) {
	for _, outcome := range []string{"success", "failure"} {
		t.Run(outcome, func(t *testing.T) {
			ready := filepath.Join(t.TempDir(), "ready")
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan struct{})
			t.Cleanup(func() { cleanupInheritedWriter(t, ready, cancel, finished) })
			starter := func(ctx context.Context, cwd, _ string, _ []string) (process.Process, error) {
				return process.DefaultProcessStarter(ctx, cwd, "sh", []string{"-c", `
 cat >/dev/null
 # No natural EOF: only test cleanup releases the inherited writer.
 (while [ ! -f "$1.release" ]; do sleep 0.02; done; echo done > "$1.finished") </dev/null &
 echo $! > "$1"
 printf '%s\n' '{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}'
 printf '%s\n' '{"type":"result","subtype":"success","session_id":"terminal"}'
 echo provider-diagnostic >&2
 if [ "$2" = failure ]; then exit 7; fi
`, "sh", ready, outcome})
			}
			type completion struct {
				result SessionResult
				err    error
			}
			done := make(chan completion, 1)
			go func() {
				defer close(finished)
				result, err := (claudeRuntime{starter: starter}).RunSession(ctx, Session{Prompt: "work", CollectMetrics: true})
				done <- completion{result, err}
			}()
			var pid int
			// External PID publication requires bounded polling, not a success sleep.
			for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
				data, _ := os.ReadFile(ready) // #nosec G304 -- test-owned PID file under t.TempDir.
				pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
				if pid > 0 {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if pid <= 0 {
				t.Fatal("provider did not publish child PID")
			}
			child, err := os.FindProcess(pid)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = child.Release() }()
			select {
			case got := <-done:
				if got.result.Output != "done" || got.result.FinalText != "done" || got.result.Metrics == nil || got.result.Metrics.SessionID != "terminal" {
					t.Fatalf("terminal output lost: %#v", got.result)
				}
				if outcome == "success" && got.err != nil {
					t.Fatalf("successful provider exit: %v", got.err)
				}
				if outcome == "failure" && (got.err == nil || !strings.Contains(got.err.Error(), "exit status 7") || !strings.Contains(got.err.Error(), "provider-diagnostic")) {
					t.Fatalf("exit status or diagnostic lost: %v", got.err)
				}
			case <-time.After(30 * time.Second):
				_ = os.WriteFile(ready+".release", nil, 0o600)
				select {
				case <-done:
				case <-time.After(30 * time.Second):
					t.Fatal("session did not stop even after child cleanup")
				}
				t.Fatal("session waited for surviving stdout writer without a context timeout")
			}
			if err := child.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("child must still be alive when session returns: %v", err)
			}
		})
	}
}

// The provider exits or is killed while its child retains stderr. Cleanup must
// not depend on that child exiting, and must preserve the provider diagnostic.
func TestAdaptersInheritedStderrShutdown(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, outcome := range []string{"cancel", "timeout", "exit"} {
			t.Run(provider+"/"+outcome, func(t *testing.T) {
				ready := filepath.Join(t.TempDir(), "ready")
				ctx, cancel := context.WithCancel(context.Background())
				finished := make(chan struct{})
				t.Cleanup(func() { cleanupInheritedWriter(t, ready, cancel, finished) })
				starter := func(ctx context.Context, cwd, _ string, _ []string) (process.Process, error) {
					return process.DefaultProcessStarter(ctx, cwd, "sh", []string{"-c", `
 # No natural EOF: only test cleanup releases the inherited writer.
 (while [ ! -f "$1.release" ]; do sleep 0.02; done; echo done > "$1.finished") </dev/null >/dev/null &
 echo provider-diagnostic >&2
 echo $! > "$1"
 if [ "$2" = exit ]; then exit 1; fi
 exec sleep 86400
`, "sh", ready, outcome})
				}
				var runtime Runtime = piRuntime{starter: starter}
				if provider == "claude" {
					runtime = claudeRuntime{starter: starter}
				}
				// Fire the behavioral timeout only after the writer is observable;
				// startup scheduling must not consume the timeout stimulus.
				ticks := make(chan time.Time, 1)
				bounded := timeoutRuntime{inner: runtime, newSoftTimer: func(time.Duration) (<-chan time.Time, func()) {
					return ticks, func() {}
				}}
				session := Session{Prompt: "work"}
				if outcome == "timeout" {
					session.Timeout = time.Second
				}
				done := make(chan error, 1)
				go func() {
					defer close(finished)
					_, err := bounded.RunSession(ctx, session)
					done <- err
				}()
				var pid int
				// External PID publication requires bounded polling, not a success sleep.
				for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); {
					data, _ := os.ReadFile(ready) // #nosec G304 -- test-owned PID file under t.TempDir.
					pid, _ = strconv.Atoi(strings.TrimSpace(string(data)))
					if pid > 0 {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				if pid <= 0 {
					t.Fatal("provider did not publish child PID")
				}
				child, err := os.FindProcess(pid)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = child.Release() }()
				switch outcome {
				case "cancel":
					cancel()
				case "timeout":
					ticks <- time.Now()
				}
				select {
				case err = <-done:
				case <-time.After(30 * time.Second):
					cancel()
					_ = os.WriteFile(ready+".release", nil, 0o600)
					select {
					case <-done:
					case <-time.After(30 * time.Second):
						t.Fatal("session did not stop even after child cleanup")
					}
					t.Fatal("session shutdown waited for the surviving stderr writer")
				}
				// SessionTimeoutError intentionally replaces the provider error;
				// direct cancellation and exit must retain stderr annotation.
				if err == nil || (outcome != "timeout" && !strings.Contains(err.Error(), "provider-diagnostic")) {
					t.Fatalf("provider diagnostic lost: %v", err)
				}
				if outcome == "cancel" && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
				if outcome == "timeout" {
					var timeout *SessionTimeoutError
					if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("session timeout lost: %v", err)
					}
				}
				if err := child.Signal(syscall.Signal(0)); err != nil {
					t.Fatalf("child must still be alive when session returns: %v", err)
				}
			})
		}
	}
}

// Register before starting the session so PID-publication failures also clean up.
// Providers are reaped by RunSession; orphaned writers are released here and
// reaped by the OS (they are not direct children of the test process).
func cleanupInheritedWriter(t *testing.T, ready string, cancel context.CancelFunc, finished <-chan struct{}) {
	t.Helper()
	// This also releases a writer if cancellation races its PID publication.
	if err := os.WriteFile(ready+".release", nil, 0o600); err != nil {
		t.Error(err)
	}
	defer cancel()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond) // External PID-file observation only.
	defer ticker.Stop()
	for {
		if _, err := os.Stat(ready + ".finished"); err == nil {
			break
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Error("writer did not acknowledge release during cleanup")
			return
		}
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(30 * time.Second):
		t.Error("provider was not reaped after writer cleanup")
	}
}

func TestAdaptersPassOpaqueModel(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, model := range []string{"", "provider/opaque-model"} {
			for _, effort := range []string{"", "opaque-effort"} {
				t.Run(provider+"/model="+model+"/effort="+effort, func(t *testing.T) {
					startErr := errors.New("stop after argv capture")
					starter := func(_ context.Context, cwd, name string, args []string) (process.Process, error) {
						want := []string{"--mode", "rpc", "--no-session"}
						if provider == "claude" {
							want = []string{"--print", "--output-format", "stream-json", "--verbose", "--no-session-persistence", "--permission-mode", "auto"}
						}
						if model != "" {
							want = append(want, "--model", model)
						}
						if effort != "" {
							flag := "--thinking"
							if provider == "claude" {
								flag = "--effort"
							}
							want = append(want, flag, effort)
						}
						if cwd != "/repo" || name != provider || !slices.Equal(args, want) {
							t.Fatalf("launch = %q %q %q, want %q", cwd, name, args, want)
						}
						return nil, startErr
					}
					var runtime Runtime = piRuntime{starter: starter}
					if provider == "claude" {
						runtime = claudeRuntime{starter: starter}
					}
					_, err := runtime.RunSession(context.Background(), Session{RepoRoot: "/repo", Model: model, Effort: effort})
					if !errors.Is(err, startErr) {
						t.Fatalf("error = %v, want starter error", err)
					}
				})
			}
		}
	}

}

func TestAdaptersOptionalWarningCompatibility(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		t.Run(provider, func(t *testing.T) {
			proc := newFakeProcess(t)
			warnings := make(chan string, 1)
			warnings <- "wrap up"
			calls := 0
			serverDone := make(chan struct{})
			starter := func(_ context.Context, _ string, name string, args []string) (process.Process, error) {
				calls++
				if name != provider {
					t.Fatalf("runtime %q", name)
				}
				return proc, nil
			}
			go func() {
				defer close(serverDone)
				defer proc.finish()
				if provider == "pi" {
					_ = proc.readCommand()
					var cmd map[string]any
					if err := proc.stdinDecoder.Decode(&cmd); err != nil {
						t.Error(err)
						return
					}
					if cmd["type"] != "steer" || cmd["message"] != "wrap up" {
						t.Errorf("warning %v", cmd)
						return
					}
					proc.writeEvent(fmt.Sprintf(`{"type":"response","command":"steer","id":%q,"success":false,"error":"unsupported"}`, cmd["id"]))
					proc.writeEvent(`{"type":"agent_end"}`)
					_ = proc.readCommand()
					proc.writeEvent(`{"id":"2","type":"response","command":"get_state","success":true,"data":{"sessionId":"final"}}`)
					_ = proc.readCommand()
					proc.writeEvent(`{"id":"3","type":"response","command":"get_session_stats","success":true,"data":{"tokens":{"total":42}}}`)
				} else {
					prompt, err := io.ReadAll(proc.stdinReader)
					if err != nil || string(prompt) != "work" {
						t.Errorf("Claude prompt %q, %v", prompt, err)
						return
					}
					proc.writeEvent(`{"type":"result","subtype":"success","session_id":"final"}`)
				}
			}()
			var runtime Runtime = piRuntime{starter: starter}
			if provider == "claude" {
				runtime = claudeRuntime{starter: starter}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			result, err := runtime.RunSession(ctx, Session{Prompt: "work", Warning: &SessionWarning{Percent: 80, Message: "policy"}, WarningMessages: warnings, CollectMetrics: true})
			<-serverDone
			if err != nil || calls != 1 {
				t.Fatalf("calls %d error %v", calls, err)
			}
			if provider == "pi" && (result.Metrics == nil || result.Metrics.TotalTokens != 42 || len(warnings) != 0) {
				t.Fatalf("Pi result %+v", result)
			}
			if provider == "claude" && len(warnings) != 1 {
				t.Fatal("Claude consumed unsupported notice")
			}
		})
	}
}

func TestAdaptersMeasurementPresence(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, tt := range []struct {
			name, stats, usage, cost      string
			availability                  agentmetrics.Availability
			input, output, total, hasCost bool
		}{
			{name: "complete", stats: `{"tokens":{"input":3,"output":2,"total":5},"cost":1}`, usage: `{"input_tokens":3,"output_tokens":2}`, cost: `,"total_cost_usd":1`, availability: agentmetrics.Reported, input: true, output: true, total: true, hasCost: true},
			{name: "zero", stats: `{"tokens":{"input":0,"output":0,"total":0},"cost":0}`, usage: `{"input_tokens":0,"output_tokens":0}`, cost: `,"total_cost_usd":0`, availability: agentmetrics.Reported, input: true, output: true, total: true, hasCost: true},
			{name: "partial", stats: `{"tokens":{"input":0}}`, usage: `{"input_tokens":0}`, availability: agentmetrics.Partial, input: true},
			{name: "absent", stats: `{}`, usage: `{}`, availability: agentmetrics.Unavailable},
		} {
			t.Run(provider+"/"+tt.name, func(t *testing.T) {
				proc := newFakeProcess(t)
				calls := 0
				starter := func(context.Context, string, string, []string) (process.Process, error) { calls++; return proc, nil }
				var runtime Runtime = claudeRuntime{starter: starter}
				if provider == "pi" {
					runtime = piRuntime{starter: starter}
				}
				go func() {
					defer proc.finish()
					if provider == "pi" {
						_ = proc.readCommand()
						if tt.name == "complete" || tt.name == "zero" {
							proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","usage":{"input":99,"output":99,"totalTokens":198,"cost":{"total":9}}}}`)
						}
						proc.writeEvent(`{"type":"agent_end"}`)
						_ = proc.readCommand()
						proc.writeEvent(`{"id":"2","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"provider","id":"model"}}}`)
						_ = proc.readCommand()
						proc.writeEvent(fmt.Sprintf(`{"id":"3","type":"response","command":"get_session_stats","success":true,"data":%s}`, tt.stats))
					} else {
						_ = proc.readPrompt()
						proc.writeEvent(fmt.Sprintf(`{"type":"result","subtype":"success","session_id":"session","model":"model","usage":%s%s}`, tt.usage, tt.cost))
					}
				}()
				result, err := runtime.RunSession(context.Background(), Session{Prompt: "work", CollectMetrics: true})
				if err != nil {
					t.Fatal(err)
				}
				m := result.Metrics
				if calls != 1 || m == nil || result.MetricsAvailability() != tt.availability || m.InputTokensPresent != tt.input || m.OutputTokensPresent != tt.output || m.TotalTokensPresent != tt.total || m.CostPresent != tt.hasCost {
					t.Fatalf("calls=%d metrics=%+v", calls, m)
				}
				if tt.name == "zero" && (m.InputTokens != 0 || m.OutputTokens != 0 || m.TotalTokens != 0 || m.Cost != 0) {
					t.Fatalf("explicit zero lost: %+v", m)
				}
				if tt.name == "complete" && (m.InputTokens != 3 || m.OutputTokens != 2 || m.TotalTokens != 5 || m.Cost != 1) {
					t.Fatalf("final stats lost or double counted: %+v", m)
				}
			})
		}
	}
}

func TestAdaptersRetainMeasurementsOnFailureAndTimeout(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, outcome := range []string{"failure", "timeout"} {
			t.Run(provider+"/"+outcome, func(t *testing.T) {
				proc := newFakeProcess(t)
				calls := 0
				serverDone := make(chan struct{})
				starter := func(ctx context.Context, _ string, _ string, _ []string) (process.Process, error) {
					calls++
					go func() {
						defer close(serverDone)
						defer proc.finish()
						if provider == "pi" {
							_ = proc.readCommand()
							proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","usage":{"input":3,"output":0,"totalTokens":3,"cost":{"total":0}}}}`)
						} else {
							_ = proc.readPrompt()
							proc.writeEvent(`{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":3,"output_tokens":0}},"total_cost_usd":0}`)
						}
						if outcome == "failure" {
							proc.writeEvent(`{"type":"error","message":"original provider failure"}`)
						} else {
							<-ctx.Done()
						}
					}()
					return proc, nil
				}
				var runtime Runtime = claudeRuntime{starter: starter}
				if provider == "pi" {
					runtime = piRuntime{starter: starter}
				}
				// One second is the timeout-under-test, not a synchronization budget.
				result, err := WithSessionTimeout(runtime).RunSession(context.Background(), Session{Prompt: "work", CollectMetrics: true, Timeout: time.Second})
				select {
				case <-serverDone:
				case <-time.After(30 * time.Second):
					t.Fatal("fake provider did not finish after session return")
				}
				if outcome == "timeout" {
					var timeout *SessionTimeoutError
					if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("timeout error = %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "original provider failure") {
					t.Fatalf("original error lost: %v", err)
				}
				want := agentmetrics.Reported
				if provider == "pi" {
					want = agentmetrics.Partial
				} // message usage is not final session stats
				m := result.Metrics
				if calls != 1 || m == nil || m.Availability != want || m.InputTokens != 3 || !m.OutputTokensPresent || m.OutputTokens != 0 || !m.CostPresent || m.Cost != 0 || !m.TotalTokensPresent || m.TotalTokens != 3 {
					t.Fatalf("calls=%d metrics=%+v", calls, m)
				}
			})
		}
	}
}

func TestAdaptersPreserveStartErrorWithoutInventingMeasurements(t *testing.T) {
	original := errors.New("original start failure")
	for _, provider := range []string{"pi", "claude"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			starter := func(context.Context, string, string, []string) (process.Process, error) {
				calls++
				return nil, original
			}
			var runtime Runtime = claudeRuntime{starter: starter}
			if provider == "pi" {
				runtime = piRuntime{starter: starter}
			}
			result, err := runtime.RunSession(context.Background(), Session{CollectMetrics: true})
			if calls != 1 || !errors.Is(err, original) || result.MetricsAvailability() != agentmetrics.Unavailable || result.Metrics == nil || result.Metrics.CostPresent {
				t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
			}
		})
	}
}
