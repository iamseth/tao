package run

import (
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

type eventAppenderFunc func(string, plan.Event) error

func (f eventAppenderFunc) AppendEvent(planDir string, event plan.Event) error {
	return f(planDir, event)
}

func TestCheckRequestCanStartHonorsRunAndContinueCapabilities(t *testing.T) {
	runnable := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	if err := CheckRequestCanStart(runnable, Request{}); err != nil {
		t.Fatalf("expected runnable plan to start: %v", err)
	}
	blocked := runPlanDetail(plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusBlocked, nil, nil)
	if err := CheckRequestCanStart(blocked, Request{}); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected blocked run error, got %v", err)
	}
	if err := CheckRequestCanStart(blocked, Request{ResolvedRunOptions: ResolvedRunOptions{Continue: true}}); err != nil {
		t.Fatalf("expected blocked plan to allow continue, got %v", err)
	}
	complete := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	if err := CheckRequestCanStart(complete, Request{}); err == nil || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("expected complete plan error, got %v", err)
	}
	if err := CheckRequestCanStart(complete, Request{ResolvedRunOptions: ResolvedRunOptions{PullRequest: true}}); err != nil {
		t.Fatalf("expected effective pull-request run to resume completed finalization: %v", err)
	}
	if err := CheckRequestCanStart(complete, Request{ResolvedRunOptions: ResolvedRunOptions{PullRequest: false}}); err == nil || !strings.Contains(err.Error(), "complete") {
		t.Fatalf("expected explicit pull-request opt-out to preserve completed-plan refusal, got %v", err)
	}
	if got := runDisabledError(plan.RunCapabilities{}); got == nil || got.Error() != "plan cannot run" {
		t.Fatalf("unexpected generic disabled error: %v", got)
	}
}
