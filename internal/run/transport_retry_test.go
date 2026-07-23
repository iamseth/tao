package run

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type retryableTransportTestError struct{ error }

func (retryableTransportTestError) RetryableTransportFailure() {}

func TestServiceExecuteRetriesTransportHandoffTwiceWithFreshResumeEvidence(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	transportErr := retryableTransportTestError{error: errors.New("transport dropped")}
	var delays []time.Duration
	var events []plan.Event
	agentCalls := 0
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base"),
		TransportRetryDelay: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
		EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error {
			events = append(events, event)
			return nil
		}),
		SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
			agentCalls++
			if !run.Resuming || run.ResumeAttempt != agentCalls {
				t.Fatalf("handoff %d resume context = %t/%d", agentCalls, run.Resuming, run.ResumeAttempt)
			}
			return transportErr
		}),
	}})
	request := Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}}

	if err := service.Execute(context.Background(), request); !errors.Is(err, transportErr) {
		t.Fatalf("execute error = %v, want transport failure", err)
	}
	if agentCalls != 3 {
		t.Fatalf("agent sessions = %d, want three-session maximum", agentCalls)
	}
	if len(delays) != 2 || delays[0] != time.Second || delays[1] != 2*time.Second {
		t.Fatalf("retry delays = %v, want [1s 2s]", delays)
	}
	assertTransportResumeEvents(t, events, []int{1, 2, 3}, 3)

	// A second explicit invocation gets a fresh two-retry budget while durable
	// resume numbering continues as audit history.
	if err := service.Execute(context.Background(), request); !errors.Is(err, transportErr) {
		t.Fatalf("second execute error = %v, want transport failure", err)
	}
	if agentCalls != 6 {
		t.Fatalf("agent sessions across two invocations = %d, want 6", agentCalls)
	}
	if len(delays) != 4 || delays[2] != time.Second || delays[3] != 2*time.Second {
		t.Fatalf("invocation-local retry delays = %v", delays)
	}
	assertTransportResumeEvents(t, events, []int{1, 2, 3, 4, 5, 6}, 6)
}

func TestServiceExecuteTransportRetryEventuallySucceedsOnThirdSession(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	completed := cloneRunRestartDetail(t, detail)
	completed.State.Status = plan.StatusCompleted
	completed.State.Plan.CurrentSlice = nil
	completed.State.Plan.PendingSlices = nil
	completed.State.Plan.CompletedSlices = []string{"001-a"}
	completed.Slices.Slices[0].Status = plan.StatusCompleted
	completed.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "base"}

	transportErr := retryableTransportTestError{error: errors.New("transport dropped")}
	agentCalls := 0
	var delays []time.Duration
	var events []plan.Event
	runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string {
		if agentCalls < 3 {
			return " M partial.go\n"
		}
		return ""
	}, "tao/plan-a", "base")
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail, detail, detail, completed}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runner,
		TransportRetryDelay: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
		EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error {
			events = append(events, event)
			return nil
		}),
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			agentCalls++
			if agentCalls < 3 {
				return transportErr
			}
			return nil
		}),
	}})

	if err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice, MaxSlices: 1}}); err != nil {
		t.Fatalf("eventual transport retry success: %v", err)
	}
	if agentCalls != 3 || len(delays) != 2 || delays[0] != time.Second || delays[1] != 2*time.Second {
		t.Fatalf("eventual success sessions=%d delays=%v", agentCalls, delays)
	}
	assertTransportResumeEvents(t, events, []int{1, 2, 3}, 2)
}

func TestServiceExecuteTransportRetryAcceptsLateDurableCompletionWithoutRetryHandoff(t *testing.T) {
	root := t.TempDir()
	detail := interruptedServiceRunDetail(t, root)
	completed := cloneRunRestartDetail(t, detail)
	completed.State.Status = plan.StatusCompleted
	completed.State.Plan.CurrentSlice = nil
	completed.State.Plan.PendingSlices = nil
	completed.State.Plan.CompletedSlices = []string{"001-a"}
	completed.Slices.Slices[0].Status = plan.StatusCompleted
	completed.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "base"}

	transportErr := retryableTransportTestError{error: errors.New("transport dropped after child completion")}
	agentCalls := 0
	var events []plan.Event
	var delays []time.Duration
	runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string {
		if agentCalls == 0 {
			return " M partial.go\n"
		}
		return ""
	}, "tao/plan-a", "base")
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail, completed}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: runner,
		TransportRetryDelay: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
		EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error {
			events = append(events, event)
			return nil
		}),
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			agentCalls++
			return transportErr
		}),
	}})

	if err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice, MaxSlices: 1}}); err != nil {
		t.Fatalf("recover durable completion: %v", err)
	}
	if agentCalls != 1 || len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("late completion recovery sessions=%d delays=%v, want no retry handoff after the bounded recovery wait", agentCalls, delays)
	}
	assertTransportResumeEvents(t, events, []int{1}, 1)
}

func TestServiceExecuteTransportRetryCancellationAndNonRetryableErrors(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		root := t.TempDir()
		detail := interruptedServiceRunDetail(t, root)
		transportErr := retryableTransportTestError{error: errors.New("transport dropped")}
		ctx, cancel := context.WithCancel(context.Background())
		agentCalls := 0
		delayCalls := 0
		service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
			CommandRunner: interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base"),
			TransportRetryDelay: func(ctx context.Context, delay time.Duration) error {
				delayCalls++
				if delay != time.Second {
					t.Fatalf("first delay = %s", delay)
				}
				cancel()
				return ctx.Err()
			},
			EventAppender: eventAppenderFunc(func(string, plan.Event) error { return nil }),
			SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
				agentCalls++
				return transportErr
			}),
		}})

		err := service.Execute(ctx, Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), transportErr.Error()) {
			t.Fatalf("cancellation error = %v, want cancellation and transport context", err)
		}
		if agentCalls != 1 || delayCalls != 1 {
			t.Fatalf("canceled retry sessions=%d delays=%d", agentCalls, delayCalls)
		}
	})

	t.Run("generic error", func(t *testing.T) {
		root := t.TempDir()
		detail := interruptedServiceRunDetail(t, root)
		genericErr := errors.New("provider authentication failed")
		agentCalls := 0
		delayCalls := 0
		service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
			CommandRunner:       interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base"),
			TransportRetryDelay: func(context.Context, time.Duration) error { delayCalls++; return nil },
			EventAppender:       eventAppenderFunc(func(string, plan.Event) error { return nil }),
			SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
				agentCalls++
				return genericErr
			}),
		}})

		err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
		if !errors.Is(err, genericErr) || agentCalls != 1 || delayCalls != 0 {
			t.Fatalf("generic error=%v sessions=%d delays=%d", err, agentCalls, delayCalls)
		}
	})
}

func TestServiceExecuteTransportRetryStopsAtEveryUnsafeReloadedBoundary(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*plan.PlanDetail, string)
		liveBranch string
		liveHead   string
		liveStatus string
		want       string
	}{
		{name: "branch drift", liveBranch: "other", want: "live branch differs"},
		{name: "HEAD drift", liveHead: "advanced", want: "live HEAD advanced"},
		{name: "conflict", liveStatus: "UU conflict.go\n", want: "conflicted entries"},
		{name: "ambiguous status", liveStatus: "R  old.go -> new.go\n", want: "ambiguous entry"},
		{name: "active Git operation", mutate: func(_ *plan.PlanDetail, root string) { writeLinkedWorktreeSequencer(t, root) }, liveStatus: " M partial.go\n", want: "Git operation"},
		{name: "blocked", mutate: func(detail *plan.PlanDetail, _ string) {
			detail.State.Status = plan.StatusBlocked
			detail.Slices.Slices[0].Status = plan.StatusBlocked
		}, liveStatus: " M partial.go\n", want: "blocked"},
		{name: "manual ownership", mutate: func(detail *plan.PlanDetail, _ string) {
			detail.Slices.Slices[0].ExecutionStart.WorkspaceStrategy = plan.WorkspaceStrategyCurrent
		}, liveStatus: " M partial.go\n", want: "workspace strategy"},
		{name: "post-intent", mutate: func(detail *plan.PlanDetail, _ string) {
			detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Policy: CommitPolicySlice.String()}
		}, liveStatus: " M partial.go\n", want: "interrupted post-intent completion transaction"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			initial := interruptedServiceRunDetail(t, root)
			reloaded := cloneRunRestartDetail(t, initial)
			transportErr := retryableTransportTestError{error: errors.New("transport dropped")}
			agentCalls := 0
			baseRunner := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base")
			runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if agentCalls > 0 {
					switch runGitKey(args) {
					case "branch --show-current":
						if tt.liveBranch != "" {
							_, _ = io.WriteString(stdout, tt.liveBranch+"\n")
							return nil
						}
					case "rev-parse HEAD":
						if tt.liveHead != "" {
							_, _ = io.WriteString(stdout, tt.liveHead+"\n")
							return nil
						}
					case "status --porcelain":
						if tt.liveStatus != "" {
							_, _ = io.WriteString(stdout, tt.liveStatus)
							return nil
						}
					}
				}
				return baseRunner(ctx, cwd, name, args, stdout, stderr)
			}
			service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{initial, reloaded}}, io.Discard, Options{RunDependencies: RunDependencies{
				CommandRunner:       runner,
				TransportRetryDelay: func(context.Context, time.Duration) error { return nil },
				EventAppender:       eventAppenderFunc(func(string, plan.Event) error { return nil }),
				SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
					agentCalls++
					if agentCalls == 1 && tt.mutate != nil {
						tt.mutate(reloaded, root)
					}
					return transportErr
				}),
			}})

			err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
			if err == nil || !strings.Contains(err.Error(), "transport dropped") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("unsafe retry error = %v, want transport context and %q", err, tt.want)
			}
			if agentCalls != 1 {
				t.Fatalf("unsafe boundary started %d sessions, want 1", agentCalls)
			}
		})
	}
}

func assertTransportResumeEvents(t *testing.T, events []plan.Event, wantAttempts []int, wantFailures int) {
	t.Helper()
	var attempts []int
	failures := 0
	for _, event := range events {
		switch event.Type {
		case plan.EventTypeSliceResumeAttempted:
			attempts = append(attempts, event.Attempts)
		case plan.EventTypeSliceResumeFailed:
			failures++
		case plan.EventTypeSliceStarted, plan.EventTypeSliceCompleted:
			t.Fatalf("transport retry duplicated lifecycle event: %+v", event)
		}
	}
	if len(attempts) != len(wantAttempts) {
		t.Fatalf("resume attempts = %v, want %v", attempts, wantAttempts)
	}
	for i := range wantAttempts {
		if attempts[i] != wantAttempts[i] {
			t.Fatalf("resume attempts = %v, want %v", attempts, wantAttempts)
		}
	}
	if failures != wantFailures {
		t.Fatalf("resume failures = %d, want %d", failures, wantFailures)
	}
}
