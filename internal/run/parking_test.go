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

func TestParkingRefusesEveryRunRecoveryMode(t *testing.T) {
	for _, request := range []Request{{}, {ResolvedRunOptions: ResolvedRunOptions{Continue: true}}, {RecoveryMode: RecoveryMode{RestartBlocked: true}}, {RecoveryMode: RecoveryMode{Reverify: true}}, {RecoveryMode: RecoveryMode{RepairVerification: true}}} {
		detail := runPlanDetail(plan.StatusBlocked, []string{"001-a"}, nil, "001-a", plan.StatusBlocked, nil, nil)
		detail.State.Plan.Parking = &plan.Parking{Reason: "owner decision", ParkedAt: time.Now().UTC()}
		if err := CheckRequestCanStart(detail, request); !errors.Is(err, ErrCannotStart) || !strings.Contains(err.Error(), "tao unpark") {
			t.Fatalf("request %+v: %v", request, err)
		}
		service := newTestService(&memoryRunRepository{}, io.Discard, testOptions(RunDependencies{}))
		if _, err := service.prepareRequestRunExecution(context.Background(), detail, request, ExecutionConfig{}); !errors.Is(err, ErrCannotStart) || !strings.Contains(err.Error(), "tao unpark") {
			t.Fatalf("prepare request %+v: %v", request, err)
		}
		detail.State.Plan.Parking = nil
		if err := CheckRequestCanStart(detail, Request{}); err == nil {
			t.Fatal("unpark bypassed blocked admission")
		}
	}
}

func TestParkingReloadRefusesDirectAndCoordinatedRun(t *testing.T) {
	for _, outer := range []bool{false, true} {
		initial := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
		initial.Dir = t.TempDir()
		refreshed := *initial
		refreshed.State = initial.State
		refreshed.State.Plan.Parking = &plan.Parking{Reason: "owner decision", ParkedAt: time.Now().UTC()}
		repo := &memoryRunRepository{details: []*plan.PlanDetail{initial}}
		service := newTestService(repo, io.Discard, testOptions(RunDependencies{StatusReporter: callbackStatusReporter(func() { repo.details = []*plan.PlanDetail{&refreshed} })}))
		var err error
		if outer {
			err = service.WithPlanRunLock(context.Background(), Request{Input: initial.Dir}, func(context.Context) error { t.Error("parked callback invoked"); return nil })
		} else {
			err = service.Execute(context.Background(), Request{Input: initial.Dir})
		}
		if !errors.Is(err, ErrCannotStart) || !strings.Contains(err.Error(), "tao unpark") {
			t.Fatalf("outer=%v: %v", outer, err)
		}
	}
}
