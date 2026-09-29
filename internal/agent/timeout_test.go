package agent

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSlowRuntime struct {
	run func(context.Context, Session) (SessionResult, error)
}

func (r fakeSlowRuntime) RunSession(ctx context.Context, session Session) (SessionResult, error) {
	return r.run(ctx, session)
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
