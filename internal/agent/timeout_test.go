package agent

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type fakeSlowRuntime struct {
	run func(context.Context, Session) (SessionResult, error)
}

func (r fakeSlowRuntime) RunSession(ctx context.Context, session Session) (SessionResult, error) {
	return r.run(ctx, session)
}

func TestTimeoutRuntimeWarningThreshold(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		runtime := WithSessionTimeout(fakeSlowRuntime{run: func(ctx context.Context, session Session) (SessionResult, error) {
			if session.WarningMessages == nil {
				t.Fatal("missing warning channel")
			}
			time.Sleep(5*time.Second - time.Nanosecond)
			synctest.Wait()
			select {
			case <-session.WarningMessages:
				t.Fatal("early warning")
			default:
			}
			time.Sleep(time.Nanosecond)
			synctest.Wait()
			select {
			case message := <-session.WarningMessages:
				if message != "wrap up" {
					t.Fatalf("message=%q", message)
				}
			default:
				t.Fatal("missing warning at threshold")
			}
			if time.Since(start) != 8*time.Second {
				t.Fatal("binding shifted warning origin")
			}
			<-ctx.Done()
			select {
			case <-session.WarningMessages:
				t.Fatal("duplicate warning")
			default:
			}
			return SessionResult{Output: "partial"}, ctx.Err()
		}})
		result, err := runtime.RunSession(context.Background(), Session{
			Timeout: 10 * time.Second, Warning: &SessionWarning{Percent: 80, Message: "wrap up"},
			BindLifetime: func(ctx context.Context) (context.Context, func() error, error) {
				time.Sleep(3 * time.Second)
				return ctx, func() error { return nil }, nil
			},
		})
		var timeout *SessionTimeoutError
		if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) || result.Output != "partial" {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

func TestTimeoutRuntimeWarningDisabled(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Second} {
		for _, policy := range []*SessionWarning{nil, {}, {Percent: -1}, {Percent: 100}, {Percent: 80}} {
			if timeout > 0 && policy != nil && policy.Percent == 80 {
				continue
			}
			runtime := timeoutRuntime{
				newWarningTimer: func(time.Duration) (<-chan time.Time, func()) { t.Fatal("disabled warning scheduled"); return nil, nil },
				inner: fakeSlowRuntime{run: func(ctx context.Context, session Session) (SessionResult, error) {
					if session.WarningMessages != nil {
						t.Fatal("disabled warning has channel")
					}
					_, deadline := ctx.Deadline()
					if deadline != (timeout > 0) {
						t.Fatal("policy changed hard timeout")
					}
					return SessionResult{}, nil
				}},
			}
			_, err := runtime.RunSession(context.Background(), Session{Timeout: timeout, Warning: policy, WarningMessages: make(chan string)})
			if err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestTimeoutRuntimeWarningCleanup(t *testing.T) {
	for _, mode := range []string{"success", "error", "parent cancellation", "parent deadline", "binding failure", "cancelled binding", "unconsumed"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "parent deadline" {
					var stop context.CancelFunc
					parent, stop = context.WithTimeout(parent, time.Second)
					defer stop()
				}
				created, stopped, calls := 0, 0, 0
				wantErr := errors.New("failure")
				var messages <-chan string
				runtime := timeoutRuntime{
					newWarningTimer: func(d time.Duration) (<-chan time.Time, func()) {
						created++
						timer := time.NewTimer(d)
						return timer.C, func() { stopped++; timer.Stop() }
					},
					inner: fakeSlowRuntime{run: func(ctx context.Context, session Session) (SessionResult, error) {
						calls++
						messages = session.WarningMessages
						switch mode {
						case "error":
							return SessionResult{}, wantErr
						case "parent cancellation":
							cancel()
							<-ctx.Done()
							return SessionResult{}, ctx.Err()
						case "parent deadline", "cancelled binding":
							<-ctx.Done()
							return SessionResult{}, ctx.Err()
						case "unconsumed":
							time.Sleep(9 * time.Second)
							synctest.Wait()
						}
						return SessionResult{Output: "done"}, nil
					}},
				}
				session := Session{Timeout: 10 * time.Second, Warning: &SessionWarning{Percent: 80, Message: "notice"}}
				if mode == "binding failure" || mode == "cancelled binding" {
					session.BindLifetime = func(ctx context.Context) (context.Context, func() error, error) {
						if mode == "binding failure" {
							return nil, nil, wantErr
						}
						bound, stop := context.WithCancel(ctx)
						stop()
						return bound, func() error { return nil }, nil
					}
				}
				_, err := runtime.RunSession(parent, session)
				switch mode {
				case "error", "binding failure":
					if !errors.Is(err, wantErr) {
						t.Fatalf("err=%v", err)
					}
				case "parent cancellation", "cancelled binding":
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("err=%v", err)
					}
				case "parent deadline":
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("err=%v", err)
					}
				default:
					if err != nil {
						t.Fatal(err)
					}
				}
				if mode == "binding failure" {
					if created != 0 || calls != 0 {
						t.Fatal("binding failure scheduled work")
					}
				} else if created != 1 || calls != 1 {
					t.Fatal("unexpected invocation count")
				}
				if stopped != created {
					t.Fatal("timer not stopped")
				}
				time.Sleep(20 * time.Second)
				synctest.Wait()
				if mode == "unconsumed" {
					select {
					case message := <-messages:
						if message != "notice" {
							t.Fatal(message)
						}
					default:
						t.Fatal("missing buffered notice")
					}
				}
				select {
				case <-messages:
					t.Fatal("unexpected notice after cleanup")
				default:
				}
			})
		})
	}
}

func TestTimeoutRuntimeWarningDurationBounds(t *testing.T) {
	for _, timeout := range []time.Duration{time.Nanosecond, 99 * time.Nanosecond, time.Duration(1<<63 - 1)} {
		t.Run(timeout.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				runtime := timeoutRuntime{
					newWarningTimer: func(d time.Duration) (<-chan time.Time, func()) {
						want := timeout/100*99 + timeout%100*99/100
						if d != want || d < 0 || d >= timeout {
							t.Fatalf("delay=%v want=%v timeout=%v", d, want, timeout)
						}
						// Do not advance the fake clock centuries beyond its timer range.
						if timeout == time.Duration(1<<63-1) {
							ticks := make(chan time.Time, 1)
							ticks <- time.Now()
							return ticks, func() {}
						}
						timer := time.NewTimer(d)
						return timer.C, func() { timer.Stop() }
					},
					inner: fakeSlowRuntime{run: func(ctx context.Context, session Session) (SessionResult, error) {
						if got := <-session.WarningMessages; got != "notice" {
							t.Fatal(got)
						}
						if ctx.Err() != nil {
							t.Fatal("warning extended to deadline")
						}
						return SessionResult{}, nil
					}},
				}
				if _, err := runtime.RunSession(context.Background(), Session{Timeout: timeout, Warning: &SessionWarning{Percent: 99, Message: "notice"}}); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestTimeoutRuntimeLifetimeUsesActualDeadlineAndCloses(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Minute} {
		t.Run(timeout.String(), func(t *testing.T) {
			var bound context.Context
			closed := 0
			wantErr := errors.New("provider failed")
			runtime := WithSessionTimeout(fakeSlowRuntime{run: func(ctx context.Context, _ Session) (SessionResult, error) {
				if ctx != bound {
					t.Fatal("provider did not receive lifetime context")
				}
				_, hasDeadline := ctx.Deadline()
				if hasDeadline != (timeout > 0) {
					t.Fatal("lifetime changed timeout-disabled semantics")
				}
				return SessionResult{Output: "partial"}, wantErr
			}})
			result, err := runtime.RunSession(context.Background(), Session{
				Timeout: timeout,
				BindLifetime: func(ctx context.Context) (context.Context, func() error, error) {
					var cancel context.CancelFunc
					bound, cancel = context.WithCancel(ctx)
					deadline, ok := ctx.Deadline()
					boundDeadline, boundOK := bound.Deadline()
					if ok != boundOK || !deadline.Equal(boundDeadline) {
						t.Fatal("binding restarted session budget")
					}
					return bound, func() error { closed++; cancel(); return nil }, nil
				},
			})
			if !errors.Is(err, wantErr) || result.Output != "partial" || closed != 1 || bound.Err() == nil {
				t.Fatalf("result=%+v err=%v closed=%d context=%v", result, err, closed, bound.Err())
			}
		})
	}
}

func TestTimeoutRuntimeLifetimeBindFailureStopsProvider(t *testing.T) {
	wantErr := errors.New("lifetime unavailable")
	runtime := WithSessionTimeout(fakeSlowRuntime{run: func(context.Context, Session) (SessionResult, error) {
		t.Fatal("provider invoked without lifetime")
		return SessionResult{}, nil
	}})
	_, err := runtime.RunSession(context.Background(), Session{
		BindLifetime: func(ctx context.Context) (context.Context, func() error, error) {
			return ctx, nil, wantErr
		},
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("bind failure = %v", err)
	}
}

func TestTimeoutRuntimeReturnsTypedTimeoutError(t *testing.T) {
	const timeout = time.Millisecond
	parentCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	runtime := WithSessionTimeout(fakeSlowRuntime{run: func(ctx context.Context, session Session) (SessionResult, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected timeout decorator to set a deadline")
		}
		<-ctx.Done()
		return SessionResult{Output: "partial"}, ctx.Err()
	}})

	result, err := runtime.RunSession(parentCtx, Session{Timeout: timeout})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if result.Output != "partial" {
		t.Fatalf("result output = %q, want partial", result.Output)
	}
	var timeoutErr *SessionTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("error = %T %v, want SessionTimeoutError", err, err)
	}
	if timeoutErr.Timeout != timeout {
		t.Fatalf("timeout error duration = %s, want %s", timeoutErr.Timeout, timeout)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error %v does not wrap context.DeadlineExceeded", err)
	}
}

func TestTimeoutRuntimeNoTimeoutPassThrough(t *testing.T) {
	type contextKey struct{}

	parentCtx := context.WithValue(context.Background(), contextKey{}, "value")
	wantResult := SessionResult{Output: "out", FinalText: "final"}
	wantErr := errors.New("pass through")
	gotCalls := 0
	runtime := WithSessionTimeout(fakeSlowRuntime{run: func(ctx context.Context, session Session) (SessionResult, error) {
		gotCalls++
		if ctx != parentCtx {
			t.Fatal("expected original context when timeout is zero")
		}
		if _, ok := ctx.Deadline(); ok {
			t.Fatal("did not expect a deadline when timeout is zero")
		}
		if session.Timeout != 0 {
			t.Fatalf("session timeout = %s, want zero", session.Timeout)
		}
		return wantResult, wantErr
	}})

	result, err := runtime.RunSession(parentCtx, Session{Prompt: "work"})
	if gotCalls != 1 {
		t.Fatalf("runtime calls = %d, want 1", gotCalls)
	}
	if result != wantResult {
		t.Fatalf("result = %#v, want %#v", result, wantResult)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestTimeoutRuntimeFastCallUnaffected(t *testing.T) {
	wantResult := SessionResult{Output: "done", FinalText: "done"}
	gotCalls := 0
	runtime := WithSessionTimeout(fakeSlowRuntime{run: func(ctx context.Context, session Session) (SessionResult, error) {
		gotCalls++
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected timeout decorator to set a deadline")
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("context error before fast runtime returned: %v", err)
		}
		return wantResult, nil
	}})

	result, err := runtime.RunSession(context.Background(), Session{Timeout: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if gotCalls != 1 {
		t.Fatalf("runtime calls = %d, want 1", gotCalls)
	}
	if result != wantResult {
		t.Fatalf("result = %#v, want %#v", result, wantResult)
	}
}

func TestTimeoutRuntimeGrace(t *testing.T) {
	for _, mode := range []string{"nil", "zero max", "nil probe", "inactive", "released", "hard bound", "fast", "disabled", "parent cancellation"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				var active atomic.Bool
				active.Store(mode != "inactive")
				var probes atomic.Int32
				grace := &SessionGrace{Max: 5 * time.Second, Active: func() bool { probes.Add(1); return active.Load() }}
				timeout := 10 * time.Second
				wantDeadline := 15 * time.Second
				switch mode {
				case "nil":
					grace = nil
					wantDeadline = timeout
				case "zero max":
					grace.Max = 0
					wantDeadline = timeout
				case "nil probe":
					grace.Active = nil
					wantDeadline = timeout
				case "disabled":
					timeout = 0
				}
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				runtime := timeoutRuntime{graceProbeInterval: time.Second, inner: fakeSlowRuntime{run: func(ctx context.Context, _ Session) (SessionResult, error) {
					switch mode {
					case "fast", "disabled":
						return SessionResult{}, nil
					case "parent cancellation":
						cancel()
					case "released":
						time.Sleep(12 * time.Second)
						synctest.Wait()
						if ctx.Err() != nil {
							t.Fatal("active grace cancelled early")
						}
						active.Store(false)
					}
					<-ctx.Done()
					return SessionResult{Output: "partial"}, ctx.Err()
				}}}
				result, err := runtime.RunSession(parent, Session{Timeout: timeout, Grace: grace, BindLifetime: func(ctx context.Context) (context.Context, func() error, error) {
					deadline, ok := ctx.Deadline()
					if mode == "disabled" {
						if ok {
							t.Fatal("disabled timeout has deadline")
						}
					} else if !ok || !deadline.Equal(start.Add(wantDeadline)) {
						t.Fatalf("deadline=%v want=%v", deadline, start.Add(wantDeadline))
					}
					return ctx, func() error { return nil }, nil
				}})
				switch mode {
				case "fast", "disabled":
					if err != nil {
						t.Fatal(err)
					}
					time.Sleep(20 * time.Second)
					synctest.Wait()
					if probes.Load() != 0 {
						t.Fatal("fast or disabled call probed grace")
					}
				case "parent cancellation":
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("err=%v", err)
					}
				default:
					var timeoutErr *SessionTimeoutError
					if !errors.As(err, &timeoutErr) || timeoutErr.Timeout != timeout || !errors.Is(err, context.DeadlineExceeded) || result.Output != "partial" {
						t.Fatalf("result=%+v err=%v", result, err)
					}
					elapsed := time.Since(start)
					switch mode {
					case "released":
						if elapsed < 12*time.Second || elapsed > 13*time.Second {
							t.Fatalf("elapsed=%v", elapsed)
						}
					case "hard bound":
						if elapsed != 15*time.Second {
							t.Fatalf("elapsed=%v", elapsed)
						}
					default:
						if elapsed != timeout {
							t.Fatalf("elapsed=%v", elapsed)
						}
					}
				}
			})
		})
	}
}
