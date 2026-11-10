package run

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/testenv"
)

func TestTimeoutContinuationSuccessfulHandoffAndLateCompletion(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			root := t.TempDir()
			detail := interruptedServiceRunDetail(t, root)
			detail.State.Repo.BaseCommit = "base"
			detail.Slices.Slices[0].ExpectedFiles = []string{"partial.go"}
			calls := 0
			status := testenv.StatusFixture("MM partial.go")
			var packets []string
			var events []plan.Event
			runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return status }, "tao/plan-a", "base")
			service := newTestService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
				RepositoryAdmission: testRepositoryAdmission,
				CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if name == "git" && runGitKey(args) == "diff --name-only --no-renames -z base..HEAD" {
						return nil
					}
					return runner(ctx, cwd, name, args, stdout, stderr)
				},
				EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error { events = append(events, event); return nil }),
				SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
					calls++
					packets = append(packets, run.RunPacket)
					if run.ResumeAttempt != calls {
						t.Fatalf("resume attempt = %d, want %d", run.ResumeAttempt, calls)
					}
					if calls == 1 && !late {
						return &agent.SessionTimeoutError{Timeout: time.Minute}
					}
					if status != testenv.StatusFixture("MM partial.go") {
						t.Fatal("partial work not preserved")
					}
					status = testenv.StatusFixture()
					detail.State.Status = plan.StatusCompleted
					detail.State.Plan.CurrentSlice = nil
					detail.State.Plan.PendingSlices = nil
					detail.State.Plan.CompletedSlices = []string{"001-a"}
					detail.Slices.Slices[0].Status = plan.StatusCompleted
					detail.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "base"}
					if late {
						return &agent.SessionTimeoutError{Timeout: time.Minute}
					}
					return nil
				}),
			}})
			err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice, MaxSlices: 1, SessionTimeout: time.Minute}})
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if late {
				want = 1
			}
			if calls != want {
				t.Fatalf("sessions=%d, want %d", calls, want)
			}
			if !late && packets[0] == packets[1] {
				t.Fatal("resume packet was not regenerated")
			}
			for _, e := range events {
				if e.Type == plan.EventTypeSliceResumeAttempted || e.Type == plan.EventTypeSliceResumeFailed {
					actor, trigger := "operator", "run_resume"
					if e.Attempts > 1 {
						actor, trigger = "automatic", "timeout_continue"
					}
					assertRecoveryAttribution(t, e, actor, trigger)
					if late && (e.Attempts > 1 || e.Type == plan.EventTypeSliceResumeFailed) {
						t.Fatalf("late completion claimed retry/failure: %#v", e)
					}
				}
				if e.Type == plan.EventTypeSliceStarted {
					t.Fatal("duplicate slice_started")
				}
			}
		})
	}
}

func TestTimeoutContinuationAdmission(t *testing.T) {
	cases := []struct {
		name, status, owned        string
		mutate                     func(*plan.PlanDetail)
		lookupError, cancel, allow bool
	}{
		{name: "empty", allow: true},
		{name: "staged and unstaged", status: testenv.StatusFixture("MM partial.go"), allow: true},
		{name: "untracked", status: testenv.StatusFixture("?? partial.go"), allow: true},
		{name: "plan owned", status: testenv.StatusFixture(" M earlier.go"), owned: "earlier.go\x00", allow: true},
		{name: "outside scope", status: testenv.StatusFixture(" M outside.go")},
		{name: "rename", status: "R  outside.go\x00partial.go\x00"},
		{name: "malformed", status: "XX partial.go\x00"},
		{name: "ambiguous path", status: " M partial.go \x00"},
		{name: "conflict", status: testenv.StatusFixture("UU partial.go")},
		{name: "lookup failure", lookupError: true},
		{name: "malformed ownership", owned: "../outside\x00"},
		{name: "truncated ownership", owned: "partial.go"},
		{name: "cancellation", cancel: true},
		{name: "missing base", mutate: func(d *plan.PlanDetail) { d.State.Repo.BaseCommit = "" }},
		{name: "blocked despite continue", mutate: func(d *plan.PlanDetail) {
			d.State.Status = plan.StatusBlocked
			d.Slices.Slices[0].Status = plan.StatusBlocked
		}},
		{name: "blocker note", mutate: func(d *plan.PlanDetail) { d.Slices.Slices[0].BlockerNote = "needs operator" }},
		{name: "intent", mutate: func(d *plan.PlanDetail) { d.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Policy: "slice"} }},
		{name: "declaration changed", mutate: func(d *plan.PlanDetail) { d.Slices.Slices[0].Verification.Commands = []string{"go test ./..."} }},
		{name: "approval changed", mutate: func(d *plan.PlanDetail) { d.Slices.Slices[0].Approval = &plan.Approval{Required: true} }},
		{name: "root changed", mutate: func(d *plan.PlanDetail) { d.Slices.Slices[0].ExecutionRoot += "/elsewhere" }},
		{name: "head changed", mutate: func(d *plan.PlanDetail) { d.Slices.Slices[0].ExecutionStart.Head = "other" }},
		{name: "selected changed", mutate: func(d *plan.PlanDetail) { id := "other"; d.State.Plan.CurrentSlice = &id }},
		{name: "manual ownership", mutate: func(d *plan.PlanDetail) { d.Slices.Slices[0].ExecutionStart.CommitPolicy = "none" }},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			detail := interruptedServiceRunDetail(t, root)
			detail.State.Repo.BaseCommit = "base"
			detail.Slices.Slices[0].ExpectedFiles = []string{"partial.go"}
			continueBlocked := tt.name == "blocked despite continue"
			if continueBlocked {
				detail.State.Status = plan.StatusBlocked
				detail.Slices.Slices[0].Status = plan.StatusBlocked
			}
			calls := 0
			status := ""
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			timeout := &agent.SessionTimeoutError{Timeout: time.Minute}
			runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return status }, "tao/plan-a", "base")
			service := newTestService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
				RepositoryAdmission: testRepositoryAdmission,
				CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if name == "git" && runGitKey(args) == "diff --name-only --no-renames -z base..HEAD" {
						if tt.lookupError {
							return errors.New("lookup failed")
						}
						_, err := io.WriteString(stdout, tt.owned)
						return err
					}
					return runner(ctx, cwd, name, args, stdout, stderr)
				},
				EventAppender: eventAppenderFunc(func(string, plan.Event) error { return nil }),
				SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
					calls++
					status = tt.status
					if tt.mutate != nil {
						tt.mutate(detail)
					}
					if tt.cancel {
						cancel()
					}
					return timeout
				}),
			}})
			err := service.Execute(ctx, Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{Continue: continueBlocked, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
			if err == nil {
				t.Fatal("expected timeout refusal")
			}
			want := 1
			if tt.allow {
				want = 2
			}
			if calls != want {
				t.Fatalf("sessions = %d, want %d; error: %v", calls, want, err)
			}
			if !tt.cancel && !errors.Is(err, timeout) {
				t.Fatalf("lost timeout: %v", err)
			}
		})
	}
}

func TestTimeoutContinuationInvocationAllowance(t *testing.T) {
	for _, runtime := range []runtimeconfig.AgentKind{"pi", "claude"} {
		t.Run(string(runtime), func(t *testing.T) {
			root := t.TempDir()
			detail := interruptedServiceRunDetail(t, root)
			detail.State.Repo.BaseCommit = "base"
			detail.Slices.Slices[0].ExpectedFiles = []string{"partial.go"}
			calls := 0
			timeout := &agent.SessionTimeoutError{Timeout: time.Minute}
			var events []plan.Event
			runner := interruptedServiceGitRunner(t, root, &[]string{}, func() string { return testenv.StatusFixture("MM partial.go") }, "tao/plan-a", "base")
			service := newTestService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
				RepositoryAdmission: testRepositoryAdmission,
				CommandRunner: CommandRunner(func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
					if name == "git" && runGitKey(args) == "diff --name-only --no-renames -z base..HEAD" {
						return nil
					}
					return runner(ctx, cwd, name, args, stdout, stderr)
				}),
				EventAppender: eventAppenderFunc(func(_ string, e plan.Event) error { events = append(events, e); return nil }),
				SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
					calls++
					if !run.Resuming || run.RunPacket == "" {
						t.Fatal("missing resume packet")
					}
					return timeout
				}),
			}})
			request := Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{Agent: runtime, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}}
			err := service.WithPlanRunLock(context.Background(), request, func(ctx context.Context) error {
				if err := service.Execute(ctx, request); !errors.Is(err, timeout) {
					t.Fatalf("timeout error = %v", err)
				}
				if calls != 2 {
					t.Fatalf("sessions = %d, want 2", calls)
				}
				// The coordinator selects another already-started slice without
				// creating a new invocation or a new timeout allowance.
				next := "002-b"
				detail.Slices.Slices[0].ID = next
				detail.State.Plan.CurrentSlice = &next
				detail.State.Plan.PendingSlices = []string{next}
				detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypeSliceStarted, SliceID: next})
				if err := service.Execute(ctx, request); !errors.Is(err, timeout) {
					t.Fatalf("second error = %v", err)
				}
				if calls != 3 {
					t.Fatalf("shared allowance sessions = %d, want 3", calls)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := service.Execute(context.Background(), request); !errors.Is(err, timeout) {
				t.Fatal(err)
			}
			if calls != 5 {
				t.Fatalf("independent invocation sessions = %d, want 5", calls)
			}
			for _, e := range events {
				if e.Type == plan.EventTypeSliceStarted {
					t.Fatal("duplicated slice_started")
				}
			}
		})
	}
}
