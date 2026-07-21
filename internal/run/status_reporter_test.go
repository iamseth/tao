package run

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/plan"
)

func TestServiceExecuteReportsStatus(t *testing.T) {
	planID := "20260710-0051-herdr-status"
	executorErr := errors.New("executor failed")

	t.Run("normal completion settles idle", func(t *testing.T) {
		reporter := &recordingStatusReporter{}
		err := executeServiceWithStatusReporter(t, context.Background(), reporter, &countingSliceExecutor{}, planID)
		if err != nil {
			t.Fatal(err)
		}
		reporter.requireCall(t, "idle")
	})

	t.Run("executor error settles blocked", func(t *testing.T) {
		reporter := &recordingStatusReporter{}
		err := executeServiceWithStatusReporter(t, context.Background(), reporter, sliceExecutorFunc(func(context.Context, SliceRun) error {
			return executorErr
		}), planID)
		if !errors.Is(err, executorErr) {
			t.Fatalf("expected executor error, got %v", err)
		}
		reporter.requireCall(t, "blocked")
	})

	t.Run("context cancellation settles blocked", func(t *testing.T) {
		reporter := &recordingStatusReporter{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		err := executeServiceWithStatusReporter(t, ctx, reporter, sliceExecutorFunc(func(ctx context.Context, run SliceRun) error {
			cancel()
			return ctx.Err()
		}), planID)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
		reporter.requireCall(t, "blocked")
	})
}

func TestServiceExecuteReportsStatusOnPanic(t *testing.T) {
	reporter := &recordingStatusReporter{}
	recovered := servicePanicValue(t, reporter)
	if recovered == nil {
		t.Fatal("expected service panic")
	}
	if recovered != "boom" {
		t.Fatalf("unexpected panic value %v", recovered)
	}
	reporter.requireCall(t, "blocked")
}

func TestRunStatusLabelCapsCustomStatus(t *testing.T) {
	if got := runStatusLabel("20260710-0051-herdr-status"); got != "run herdr-status" {
		t.Fatalf("expected slug label, got %q", got)
	}
	if got := runStatusLabel("plan-a"); got != "run plan-a" {
		t.Fatalf("expected plan ID fallback label, got %q", got)
	}

	longID := "20260710-0051-" + strings.Repeat("界", 40)
	got := runStatusLabel(longID)
	want := "run " + strings.Repeat("界", 28)
	if got != want {
		t.Fatalf("expected capped status %q, got %q", want, got)
	}
	if count := utf8.RuneCountInString(got); count != maxRunStatusLabelRunes {
		t.Fatalf("expected %d runes, got %d", maxRunStatusLabelRunes, count)
	}
}

func servicePanicValue(t *testing.T, reporter *recordingStatusReporter) (recovered any) {
	t.Helper()
	defer func() {
		recovered = recover()
	}()
	_ = executeServiceWithStatusReporter(t, context.Background(), reporter, sliceExecutorFunc(func(context.Context, SliceRun) error {
		panic("boom")
	}), "20260710-0051-herdr-status")
	return nil
}

func executeServiceWithStatusReporter(t *testing.T, ctx context.Context, reporter StatusReporter, executor SliceExecutor, planID string) error {
	t.Helper()
	detail, completedDetail := statusReporterPlanDetails(t, planID)
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail, completedDetail}}
	var calls []string
	service := NewService(repo, io.Discard, Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, ReviewEnabled: false}},
		RunDependencies: RunDependencies{
			CommandRunner:     runGitFake(&calls, nil),
			PlanRecordFactory: memoryPlanRecordFactory,
			SliceExecutor:     executor,
			StatusReporter:    reporter,
		},
	})
	return service.Execute(ctx, Request{Input: planID})
}

func statusReporterPlanDetails(t *testing.T, planID string) (*plan.PlanDetail, *plan.PlanDetail) {
	t.Helper()
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Plan.ID = planID
	detail.State.Repo.Root = repoRoot
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.Dir = planDir
	completedDetail.State.Plan.ID = planID
	completedDetail.State.Repo.Root = repoRoot
	return detail, completedDetail
}

type recordingStatusReporter struct {
	calls []recordedStatusCall
}

type recordedStatusCall struct {
	status     string
	settlement string
	events     []string
}

func (r *recordingStatusReporter) Track(status string, fn func() error) (err error) {
	r.calls = append(r.calls, recordedStatusCall{status: status, events: []string{"working"}})
	call := &r.calls[len(r.calls)-1]
	defer func() {
		if recovered := recover(); recovered != nil {
			call.settlement = "blocked"
			call.events = append(call.events, "blocked")
			panic(recovered)
		}
		if err != nil {
			call.settlement = "blocked"
			call.events = append(call.events, "blocked")
			return
		}
		call.settlement = "idle"
		call.events = append(call.events, "idle")
	}()
	return fn()
}

func (r *recordingStatusReporter) requireCall(t *testing.T, settlement string) {
	t.Helper()
	if len(r.calls) != 1 {
		t.Fatalf("expected one status report, got %#v", r.calls)
	}
	call := r.calls[0]
	if call.status != "run herdr-status" || call.settlement != settlement {
		t.Fatalf("expected status %q settled %q, got %#v", "run herdr-status", settlement, call)
	}
	if len(call.events) != 2 || call.events[0] != "working" || call.events[1] != settlement {
		t.Fatalf("unexpected report event order: %#v", call.events)
	}
}
