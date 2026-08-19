package run

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestNilHeaderReporterLeavesRunBehaviorUnchanged(t *testing.T) {
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer

	if err := executeDetail(context.Background(), detail, nil, &out, Options{RunDependencies: RunDependencies{HeaderReporter: nil}}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "Plan slices complete: plan-a\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestPanickingHeaderReporterDoesNotFailRun(t *testing.T) {
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer

	err := executeDetail(context.Background(), detail, nil, &out, Options{RunDependencies: RunDependencies{HeaderReporter: panickingHeaderReporter{}}})
	if err != nil {
		t.Fatalf("run failed because header reporter panicked: %v", err)
	}
	if got, want := out.String(), "Plan slices complete: plan-a\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestHeaderReporterPublishesSliceProgressionAndMetricTotals(t *testing.T) {
	startedAt := time.Date(2026, 8, 12, 19, 0, 0, 0, time.UTC)
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, &startedAt, nil)
	detail.State.Repo.Name = "tao"
	detail.State.Plan.Title = "Pinned run header"
	detail.Slices.Slices[0].Title = "Add observer seam"
	detail.Events = []plan.Event{{Type: plan.EventTypeReworkRound, Round: 2}}

	completedAt := startedAt.Add(time.Minute)
	completed := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, &startedAt, &completedAt)
	completed.State.Repo.Name = "tao"
	completed.State.Plan.Title = "Pinned run header"
	completed.Slices.Slices[0].Title = "Add observer seam"
	completed.Events = []plan.Event{
		{Type: plan.EventTypeReworkRound, Round: 2},
		{
			Type: plan.EventTypeAgentMetrics, PlanID: "plan-a", SliceID: "001-a",
			Metrics: &plan.AgentMetrics{Agent: "claude", SessionID: "session-1", Status: plan.StatusCompleted, TotalTokens: 77, Cost: 0.25},
		},
	}

	reporter := &recordingHeaderReporter{}
	var out bytes.Buffer
	var calls []string
	err := executeDetail(context.Background(), detail, func(context.Context, *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completed, nil
	}, &out, Options{
		ExecutionConfig: ExecutionConfig{
			ResolvedRunOptions: ResolvedRunOptions{
				CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, Agent: AgentClaude,
			},
			MaxReworkAttempts: 7,
		},
		RunDependencies: RunDependencies{
			HeaderReporter: reporter,
			SliceExecutor: sliceExecutorFunc(func(ctx context.Context, _ SliceRun) error {
				publishAgentMetrics(ctx, plan.AgentMetrics{Agent: "claude", SessionID: "session-1", TotalTokens: 77, Cost: 0.25})
				return nil
			}),
			PlanRecordFactory: memoryPlanRecordFactory,
			CommandRunner:     runGitFake(&calls, nil),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	initial := reporter.states[0]
	if initial.RepoName != "tao" || initial.PlanID != "plan-a" || initial.PlanTitle != "Pinned run header" {
		t.Fatalf("initial identity = %+v", initial)
	}
	if initial.Agent != "claude" || initial.ExecutionMode != "current" || initial.ReviewEnabled || !initial.CostReported || initial.ReworkRound != 2 || initial.MaxReworkAttempts != 7 {
		t.Fatalf("initial config = %+v", initial)
	}
	if initial.CompletedCount != 0 || initial.TotalCount != 1 || len(initial.Slices) != 1 || initial.Slices[0].Status != plan.StatusPending {
		t.Fatalf("initial progression = %+v", initial)
	}

	if !hasHeaderState(reporter.states, func(state HeaderState) bool {
		return state.Phase == PhaseRunningSlice && state.CurrentSliceID == "001-a" && state.CurrentSliceTitle == "Add observer seam" && state.Slices[0].Status == plan.StatusInProgress
	}) {
		t.Fatalf("running slice state not published: %+v", reporter.states)
	}
	if !hasHeaderState(reporter.states, func(state HeaderState) bool {
		return state.AgentSessionCount == 1 && state.TotalTokens == 77 && state.Cost == 0.25 && state.CompletedCount == 0
	}) {
		t.Fatalf("running metric totals not published: %+v", reporter.states)
	}
	if !hasHeaderState(reporter.states, func(state HeaderState) bool {
		return state.CompletedCount == 1 && state.TotalCount == 1 && state.Slices[0].Status == plan.StatusCompleted && state.AgentSessionCount == 1 && state.TotalTokens == 77 && state.Cost == 0.25 && state.ReworkRound == 2 && state.MaxReworkAttempts == 7
	}) {
		t.Fatalf("completed progression not published: %+v", reporter.states)
	}
}

type panickingHeaderReporter struct{}

func (panickingHeaderReporter) ReportHeader(HeaderState) { panic("presentation failed") }

type recordingHeaderReporter struct {
	states []HeaderState
}

func (r *recordingHeaderReporter) ReportHeader(state HeaderState) {
	r.states = append(r.states, state)
}

func hasHeaderState(states []HeaderState, match func(HeaderState) bool) bool {
	for _, state := range states {
		if match(state) {
			return true
		}
	}
	return false
}
