package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runqueue"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

func TestQueueCommandParsing(t *testing.T) {
	clearTaoEnv(t)
	plansRoot := t.TempDir()
	app := App{Out: io.Discard, Err: io.Discard, Repository: func(plansDir string) Repository { return plan.NewFileRepository(plansRoot) }}

	if got := normalizeCommand("q"); got != "queue" {
		t.Fatalf("normalizeCommand(q) = %q, want queue", got)
	}
	if commandByName("queue") == nil {
		t.Fatal("queue command is not registered")
	}

	assertQueueError(t, app.Run(context.Background(), []string{"queue"}), "usage: tao queue")
	assertQueueError(t, app.Run(context.Background(), []string{"queue", "start", "extra"}), "usage: tao queue start")
	assertQueueError(t, app.Run(context.Background(), []string{"queue", "start", "--max-parallel", "0"}), "--max-parallel must be at least 1")
	assertQueueError(t, app.Run(context.Background(), []string{"queue", "status", "extra"}), "usage: tao queue status [--all]")
	assertQueueError(t, app.Run(context.Background(), []string{"queue", "status", "--unknown"}), "flag provided but not defined")
	assertQueueError(t, app.Run(context.Background(), []string{"queue", "unknown"}), "unknown queue command")
}

func TestQueueAddEnqueuesPlansWithoutDraining(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planA := "20260628-0100-plan-a"
	planB := "20260628-0101-plan-b"
	writeQueuePlan(t, plansRoot, planA)
	writeQueuePlan(t, plansRoot, planB)

	var out bytes.Buffer
	var executed []string
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error {
		executed = append(executed, request.Input)
		return nil
	})
	app := queueTestApp(plansRoot, &out)

	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "add", planA, planB}); err != nil {
		t.Fatal(err)
	}
	if len(executed) != 0 {
		t.Fatalf("queue add should not drain, executed %v", executed)
	}
	assertContains(t, out.String(), "Queued "+planA)
	assertContains(t, out.String(), "Queued "+planB)

	snapshot := loadQueueSnapshotForTest(t)
	if got, want := queueSnapshotStatuses(snapshot), map[string]runqueue.QueueStatus{planA: runqueue.QueueStatusPending, planB: runqueue.QueueStatusPending}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queue statuses = %+v, want %+v", got, want)
	}
}

func TestQueueStartAutoReworkValidatesPersistedReviewOption(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planID := "20260628-0100-plan-a"
	writeQueuePlan(t, plansRoot, planID)
	app := queueTestApp(plansRoot, io.Discard)

	t.Setenv("TAO_REVIEW", "false")
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "add", planID}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_REVIEW", "true")
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start", "--auto-rework"}); err == nil || !strings.Contains(err.Error(), "requires automatic review") {
		t.Fatalf("persisted review option validation error = %v", err)
	}

	snapshot := loadQueueSnapshotForTest(t)
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != runqueue.QueueStatusPending || snapshot.Entries[0].RunOptions == nil || snapshot.Entries[0].RunOptions.ReviewEnabled == nil || *snapshot.Entries[0].RunOptions.ReviewEnabled || snapshot.Entries[0].AutoReworkPolicy != nil {
		t.Fatalf("invalid auto-rework start mutated queued request: %+v", snapshot)
	}
}

func TestQueueStartDrainsToCompletion(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planA := "20260628-0100-plan-a"
	planB := "20260628-0101-plan-b"
	writeQueuePlan(t, plansRoot, planA)
	writeQueuePlan(t, plansRoot, planB)

	var mu sync.Mutex
	executed := make([]string, 0, 2)
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error {
		mu.Lock()
		executed = append(executed, request.Input)
		mu.Unlock()
		return nil
	})
	var out bytes.Buffer
	app := queueTestApp(plansRoot, &out)

	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start"}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotExecuted := append([]string(nil), executed...)
	mu.Unlock()
	if got, want := stringSet(gotExecuted), map[string]bool{planA: true, planB: true}; !reflect.DeepEqual(got, want) {
		t.Fatalf("executed plans = %v, want set %+v", gotExecuted, want)
	}
	assertContains(t, out.String(), "Reconciled queue: 2 runnable, 2 enqueued, 0 already queued")
	assertContains(t, out.String(), "Queue drain completed.")
	assertContains(t, out.String(), "Final batch summary: 2 succeeded (0 reviewed), 0 failed, 0 running, 0 pending of 2")

	snapshot := loadQueueSnapshotForTest(t)
	if got, want := queueSnapshotStatuses(snapshot), map[string]runqueue.QueueStatus{planA: runqueue.QueueStatusSucceeded, planB: runqueue.QueueStatusSucceeded}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queue statuses = %+v, want %+v", got, want)
	}
}

func TestQueueStartRecoversInterruptedRunningEntry(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planID := "20260628-0100-plan-a"
	writeQueuePlan(t, plansRoot, planID)
	queuedAt := time.Date(2026, 6, 28, 3, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	if err := queueStoreForTest(t).SaveSnapshot(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{{
		PlanID:        planID,
		Status:        runqueue.QueueStatusRunning,
		QueuedAt:      queuedAt,
		StartedAt:     &startedAt,
		Mode:          run.ModeRun,
		CommitPolicy:  run.CommitPolicySlice,
		ExecutionMode: run.ExecutionModeIsolated,
		Agent:         run.AgentPi,
	}}}); err != nil {
		t.Fatal(err)
	}

	var executed []string
	withQueueExecutor(t, func(_ context.Context, request run.Request) error {
		executed = append(executed, request.Input)
		return nil
	})
	var out bytes.Buffer
	app := queueTestApp(plansRoot, &out)
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(executed, []string{planID}) {
		t.Fatalf("executed plans = %v, want [%s]", executed, planID)
	}
	assertContains(t, out.String(), "Reconciled queue: 1 runnable, 0 enqueued, 1 already queued")
	snapshot := loadQueueSnapshotForTest(t)
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != runqueue.QueueStatusSucceeded {
		t.Fatalf("recovered queue snapshot = %+v, want one succeeded entry", snapshot)
	}
}

func TestQueueStartAutoReworkRecoversCompletedChangesRequestedPlan(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planID := "20260628-0100-reviewed-plan"
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/queue.go", Line: 150, Message: "resume requested changes", Suggestion: "reopen before queue validation"}
	writeCLIReworkPlan(t, plansRoot, planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}))

	queuedAt := time.Date(2026, 6, 28, 3, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	persistedPolicy := runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: runtimeconfig.DefaultMaxReworkAttempts}
	if err := queueStoreForTest(t).SaveSnapshot(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{{
		PlanID:           planID,
		Status:           runqueue.QueueStatusRunning,
		QueuedAt:         queuedAt,
		StartedAt:        &startedAt,
		Mode:             run.ModeRun,
		CommitPolicy:     run.CommitPolicySlice,
		ExecutionMode:    run.ExecutionModeIsolated,
		Agent:            run.AgentPi,
		AutoReworkPolicy: &persistedPolicy,
	}}}); err != nil {
		t.Fatal(err)
	}

	executions := 0
	withQueueExecutor(t, func(_ context.Context, request run.Request) error {
		executions++
		detail, err := plan.NewFileRepository(plansRoot).ResolvePlan(context.Background(), request.Input)
		if err != nil {
			return err
		}
		if detail.State.Status != plan.StatusInProgress || len(detail.State.Plan.PendingSlices) != 1 {
			return errors.New("recovered plan was executed before requested changes were reopened")
		}
		return nil
	})
	var out bytes.Buffer
	app := queueTestApp(plansRoot, &out)
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start"}); err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want one rework run using the persisted policy", executions)
	}

	detail, err := plan.NewFileRepository(plansRoot).ResolvePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Status != plan.StatusInProgress || len(detail.State.Plan.PendingSlices) != 1 || !strings.HasPrefix(detail.State.Plan.PendingSlices[0], "r101-") {
		t.Fatalf("recovered plan was not reopened: status=%q pending=%v", detail.State.Status, detail.State.Plan.PendingSlices)
	}
	snapshot := loadQueueSnapshotForTest(t)
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != runqueue.QueueStatusSucceeded || snapshot.Entries[0].ReworkAttempts != 1 || snapshot.Entries[0].RecoveryPending {
		t.Fatalf("recovered automatic rework snapshot = %+v", snapshot)
	}
}

func TestQueueStartRendersAutoReworkRestartRefusal(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planID := "20260628-0101-stopped-rework"
	finding := plan.ReviewFinding{
		Severity:   "major",
		File:       "internal/cli/queue.go",
		Line:       161,
		Message:    "surface the persisted blocking finding",
		Suggestion: "render failed queue entry errors when the drain completes",
	}
	planDir := writeCLIReworkPlan(t, plansRoot, planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}))
	now := time.Date(2026, 6, 28, 4, 0, 0, 0, time.UTC)
	repo := plan.NewFileRepository(plansRoot)
	if err := repo.AppendEvent(planDir, plan.Event{
		Type:      plan.EventTypeReworkStopped,
		Timestamp: now.Add(-time.Minute),
		PlanID:    planID,
		Reason:    "automatic rework stalled on equivalent consecutive findings",
		Message:   "automatic rework stalled on equivalent consecutive findings",
	}); err != nil {
		t.Fatal(err)
	}

	persistedPolicy := runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: runtimeconfig.DefaultMaxReworkAttempts}
	if err := queueStoreForTest(t).SaveSnapshot(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{{
		PlanID:           planID,
		Status:           runqueue.QueueStatusRunning,
		QueuedAt:         now.Add(-2 * time.Minute),
		StartedAt:        new(now.Add(-time.Minute)),
		Mode:             run.ModeRun,
		CommitPolicy:     run.CommitPolicySlice,
		ExecutionMode:    run.ExecutionModeIsolated,
		Agent:            run.AgentPi,
		ReviewEnabled:    new(true),
		AutoReworkPolicy: &persistedPolicy,
	}}}); err != nil {
		t.Fatal(err)
	}

	executions := 0
	withQueueExecutor(t, func(context.Context, run.Request) error {
		executions++
		return nil
	})
	var out bytes.Buffer
	app := queueTestApp(plansRoot, &out)
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start"}); err != nil {
		t.Fatal(err)
	}
	if executions != 0 {
		t.Fatalf("executions = %d, want restart refusal before execution", executions)
	}
	for _, want := range []string{
		"Final batch summary: 0 succeeded (0 reviewed), 1 failed",
		"Failed " + planID,
		"THE LOOP IS GOING IN CIRCLES",
		finding.Message,
		finding.Suggestion,
		"A new automatic-rework budget was not started",
		"--rework-restart",
	} {
		assertContains(t, out.String(), want)
	}
}

func TestRunAllReworkRestartReplacesInterruptedRecoveryProgress(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planID := "20260718-0102-stopped-recovery"
	finding := plan.ReviewFinding{
		Severity:   "major",
		File:       "internal/cli/queue.go",
		Line:       421,
		Message:    "reset interrupted queue progress for an acknowledged restart",
		Suggestion: "replace the recovered entry before granting a fresh budget",
	}
	planDir := writeCLIReworkPlan(t, plansRoot, planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}))
	now := time.Date(2026, 7, 18, 5, 0, 0, 0, time.UTC)
	repo := plan.NewFileRepository(plansRoot)
	if err := repo.AppendEvent(planDir, plan.Event{
		Type:      plan.EventTypeReworkStopped,
		Timestamp: now.Add(-time.Minute),
		PlanID:    planID,
		Reason:    "automatic rework stalled on equivalent consecutive findings",
		Message:   "automatic rework stalled on equivalent consecutive findings",
	}); err != nil {
		t.Fatal(err)
	}

	baseline := 3
	failedAt := now.Add(-4 * time.Minute)
	startedAt := now.Add(-2 * time.Minute)
	persistedPolicy := runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: runtimeconfig.DefaultMaxReworkAttempts}
	if err := queueStoreForTest(t).SaveSnapshot(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{
			PlanID:     planID,
			Status:     runqueue.QueueStatusFailed,
			QueuedAt:   now.Add(-5 * time.Minute),
			FinishedAt: &failedAt,
			Error:      "older automatic rework stop",
		},
		{
			PlanID:                     planID,
			Status:                     runqueue.QueueStatusRunning,
			QueuedAt:                   now.Add(-3 * time.Minute),
			StartedAt:                  &startedAt,
			Mode:                       run.ModeRun,
			CommitPolicy:               run.CommitPolicySlice,
			ExecutionMode:              run.ExecutionModeIsolated,
			Agent:                      run.AgentPi,
			ReviewEnabled:              new(true),
			AutoReworkPolicy:           &persistedPolicy,
			ReworkBaselineRound:        &baseline,
			ReworkAttempts:             runtimeconfig.DefaultMaxReworkAttempts,
			PreviousFindingFingerprint: "stale-fingerprint",
		},
	}}); err != nil {
		t.Fatal(err)
	}

	executions := 0
	withQueueExecutor(t, func(_ context.Context, request run.Request) error {
		executions++
		detail, err := repo.ResolvePlan(context.Background(), request.Input)
		if err != nil {
			return err
		}
		if detail.State.Status != plan.StatusInProgress || len(detail.State.Plan.PendingSlices) != 1 {
			return errors.New("fresh automatic rework round was not created before execution")
		}
		return nil
	})
	var out bytes.Buffer
	app := queueTestApp(plansRoot, &out)
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "run", "--all", "--rework-restart"}); err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want one run with a fresh automatic-rework budget", executions)
	}
	assertContains(t, out.String(), "Reconciled queue: 1 runnable, 1 enqueued, 0 already queued")

	snapshot := loadQueueSnapshotForTest(t)
	if len(snapshot.Entries) != 2 {
		t.Fatalf("queue entries = %d, want historical failure and replacement entry: %+v", len(snapshot.Entries), snapshot)
	}
	if entry := snapshot.Entries[0]; entry.Status != runqueue.QueueStatusFailed || entry.Error != "older automatic rework stop" {
		t.Fatalf("historical queue entry = %+v, want preserved failure", entry)
	}
	entry := snapshot.Entries[1]
	if entry.Status != runqueue.QueueStatusSucceeded || entry.ReworkBaselineRound == nil || *entry.ReworkBaselineRound != 0 || entry.ReworkAttempts != 1 || entry.PreviousFindingFingerprint == "stale-fingerprint" || entry.RecoveryPending {
		t.Fatalf("replacement queue progress = %+v, want fresh baseline and attempt 1", entry)
	}
}

func TestQueueStartAutoReworkRecoversProgressFromCreatedRound(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planID := "20260628-0101-reopened-plan"
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/queue.go", Line: 226, Message: "preserve the prior finding", Suggestion: "recover it from the superseded review"}
	repo := plan.NewFileRepository(plansRoot)
	writeCLIReworkPlan(t, plansRoot, planID, plan.StatusChangesRequested, reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}))
	detail, err := repo.ResolvePlan(context.Background(), planID)
	if err != nil {
		t.Fatal(err)
	}
	record, err := repo.PlanRecord(detail)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rework.Reopen(record, time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	queuedAt := time.Date(2026, 6, 28, 12, 30, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	persistedPolicy := runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: runtimeconfig.DefaultMaxReworkAttempts}
	if err := queueStoreForTest(t).SaveSnapshot(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{{
		PlanID:           planID,
		Status:           runqueue.QueueStatusRunning,
		QueuedAt:         queuedAt,
		StartedAt:        &startedAt,
		Mode:             run.ModeRun,
		CommitPolicy:     run.CommitPolicySlice,
		ExecutionMode:    run.ExecutionModeIsolated,
		Agent:            run.AgentPi,
		AutoReworkPolicy: &persistedPolicy,
	}}}); err != nil {
		t.Fatal(err)
	}

	executions := 0
	withQueueExecutor(t, func(context.Context, run.Request) error {
		executions++
		return nil
	})
	var out bytes.Buffer
	app := queueTestApp(plansRoot, &out)
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start"}); err != nil {
		t.Fatal(err)
	}
	if executions != 1 {
		t.Fatalf("executions = %d, want the existing rework round to run once using the persisted policy", executions)
	}

	snapshot := loadQueueSnapshotForTest(t)
	wantFingerprint := rework.ReworkFindingsFingerprint([]plan.ReviewFinding{finding})
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != runqueue.QueueStatusSucceeded || snapshot.Entries[0].ReworkAttempts != 1 || snapshot.Entries[0].PreviousFindingFingerprint != wantFingerprint || snapshot.Entries[0].RecoveryPending {
		t.Fatalf("recovered created-round snapshot = %+v, want round 1 and fingerprint %q", snapshot, wantFingerprint)
	}
}

func TestQueueStartResumesInterruptedPendingReview(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planID := "20260628-1330-pending-review"
	writeRunPlan(t, plansRoot, planID, plan.StatusInReview, nil, []string{"001-work"}, "001-work", plan.StatusCompleted)

	queuedAt := time.Date(2026, 6, 28, 13, 30, 0, 0, time.UTC)
	if err := queueStoreForTest(t).SaveSnapshot(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{{
		PlanID: planID, Status: runqueue.QueueStatusRunning, QueuedAt: queuedAt, ReviewEnabled: new(true),
	}}}); err != nil {
		t.Fatal(err)
	}

	executions := 0
	reviews := 0
	withQueueExecutor(t, func(context.Context, run.Request) error {
		executions++
		return nil
	})
	withQueueRecoveryReviewer(t, func(ctx context.Context, request run.Request) error {
		reviews++
		repo := plan.NewFileRepository(plansRoot)
		detail, err := repo.ResolvePlan(ctx, request.Input)
		if err != nil {
			return err
		}
		record, err := repo.PlanRecord(detail)
		if err != nil {
			return err
		}
		return record.RecordReviewCompleted(plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, ReviewedAt: queuedAt.Add(time.Minute)}, "pi")
	})

	app := queueTestApp(plansRoot, io.Discard)
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start"}); err != nil {
		t.Fatal(err)
	}
	if executions != 0 || reviews != 1 {
		t.Fatalf("interrupted review calls = (%d executions, %d reviews), want (0, 1)", executions, reviews)
	}
	snapshot := loadQueueSnapshotForTest(t)
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != runqueue.QueueStatusSucceeded || snapshot.Entries[0].RecoveryPending {
		t.Fatalf("recovered pending review snapshot = %+v", snapshot)
	}
}

func TestQueueAutoReworkerTreatsUnsafeFindingsAsNonActionable(t *testing.T) {
	now := time.Date(2026, 6, 28, 13, 45, 0, 0, time.UTC)
	planID := "20260628-1345-unsafe-finding"
	finding := plan.ReviewFinding{Severity: "major", File: "../outside.go", Message: "unsafe path", Suggestion: "do not generate a slice"}
	detail := &plan.PlanDetail{
		State: plan.State{
			Status:    plan.StatusChangesRequested,
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now,
			Plan: plan.PlanState{
				ID:              planID,
				CompletedSlices: []string{"001-work"},
				Review:          reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding}),
			},
		},
		Slices: plan.SlicesFile{PlanID: planID, Slices: []plan.Slice{{ID: "001-work", Status: plan.StatusCompleted}}},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}

	result, err := planAutoReworker(repo, func() time.Time { return now })(context.Background(), planID, 0, 0, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, rework.Decision{}) {
		t.Fatalf("automatic rework result = %+v, want non-actionable success", result)
	}
	if detail.Dir != "" || detail.State.Status != plan.StatusChangesRequested || len(detail.State.Plan.PendingSlices) != 0 || len(detail.Slices.Slices) != 1 {
		t.Fatalf("non-actionable review was mutated: %+v", detail)
	}
}

func TestQueueStartAutoReworkKeepsPlanOwnedAcrossReopenAndRerun(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planID := "20260628-1350-owned-rework"
	writeQueuePlan(t, plansRoot, planID)

	now := time.Date(2026, 6, 28, 13, 50, 0, 0, time.UTC)
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/queue.go", Line: 271, Message: "keep ownership across automatic rework", Suggestion: "hold the plan run lock through the rerun handoff"}
	repo := plan.NewFileRepository(plansRoot)
	competingResult := make(chan error, 1)
	var mu sync.Mutex
	executions := 0
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error {
		mu.Lock()
		executions++
		call := executions
		mu.Unlock()

		detail, err := repo.ResolvePlan(ctx, request.Input)
		if err != nil {
			return err
		}
		if call == 1 {
			record, err := repo.PlanRecord(detail)
			if err != nil {
				return err
			}
			if err := record.StartSlice("001-work", now); err != nil {
				return err
			}
			if err := record.CompleteSlice("001-work", "done", nil, now.Add(time.Minute)); err != nil {
				return err
			}
			review := *reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding})
			review.ReviewedAt = now.Add(2 * time.Minute)
			return record.RecordReviewCompleted(review, "pi")
		}

		if detail.State.Status != plan.StatusInProgress || len(detail.State.Plan.PendingSlices) != 1 {
			return fmt.Errorf("automatic rework was not reopened before rerun: status=%q pending=%v", detail.State.Status, detail.State.Plan.PendingSlices)
		}
		competitor := run.NewService(repo, io.Discard, run.Options{})
		competingResult <- competitor.WithPlanRunLock(context.Background(), request, func(context.Context) error {
			return errors.New("competing runner acquired the plan")
		})
		return nil
	})

	app := queueTestApp(plansRoot, io.Discard)
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start", "--auto-rework", "--max-rework-attempts", "1"}); err != nil {
		t.Fatal(err)
	}
	competingErr := <-competingResult
	if !errors.Is(competingErr, run.ErrCannotStart) || !strings.Contains(competingErr.Error(), "already running") {
		t.Fatalf("competing runner at reopen boundary error = %v, want plan lock contention", competingErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if executions != 2 {
		t.Fatalf("queue executions = %d, want initial run and one owned rework run", executions)
	}
}

func TestQueueAutoReworkerExcludesExistingManualRoundsFromCap(t *testing.T) {
	now := time.Date(2026, 6, 28, 14, 0, 0, 0, time.UTC)
	planID := "20260628-1400-manual-rework"
	finding := plan.ReviewFinding{Severity: "major", File: "internal/cli/queue.go", Line: 225, Message: "count only automatic rounds", Suggestion: "subtract the queue baseline"}
	review := reworkReview(plan.ReviewVerdictChangesRequested, []plan.ReviewFinding{finding})
	detail := &plan.PlanDetail{
		State: plan.State{
			Status:    plan.StatusChangesRequested,
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now,
			Plan: plan.PlanState{
				ID:              planID,
				CompletedSlices: []string{"r201-existing-manual-rework"},
				Review:          review,
				Timing:          plan.PlanTiming{StartedAt: new(now), CompletedAt: new(now), LastActivityAt: new(now)},
			},
		},
		Slices: plan.SlicesFile{PlanID: planID, Slices: []plan.Slice{{
			ID:            "r201-existing-manual-rework",
			Status:        plan.StatusCompleted,
			Goal:          "Existing manual rework",
			ExpectedFiles: []string{"internal/cli/queue.go"},
			Verification:  plan.Verification{Commands: []string{"go test ./internal/cli"}},
		}}},
	}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{planID: detail}}

	result, err := planAutoReworker(repo, func() time.Time { return now })(context.Background(), planID, 2, 0, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Reworked || result.Round != 3 || result.StopReason != "" {
		t.Fatalf("automatic rework result = %+v, want first automatic cycle after baseline round 2", result)
	}
}

func TestQueueWiresStatusReporter(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "queue start", args: []string{"queue", "start"}},
		{name: "run all", args: []string{"run", "--all"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			clearTaoEnv(t)
			configureQueueDataHome(t)
			plansRoot := t.TempDir()
			writeQueuePlan(t, plansRoot, "20260628-0100-plan-a")
			reporter := newRecordingCLIStatusReporter()
			var got run.StatusReporter
			old := newQueueExecutor
			newQueueExecutor = func(repo run.Repository, out io.Writer, options run.Options) runqueue.Executor {
				got = options.StatusReporter
				return func(context.Context, run.Request) error { return nil }
			}
			t.Cleanup(func() { newQueueExecutor = old })
			var out bytes.Buffer
			app := queueTestApp(plansRoot, &out)
			app.StatusReporter = reporter

			args := append([]string{"--plans-dir", plansRoot}, test.args...)
			if err := app.Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			if got != reporter {
				t.Fatalf("queue run status reporter = %T, want injected reporter", got)
			}
		})
	}
}

func TestQueueNotifyCommandReceivesSummaryEnv(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	t.Setenv(runtimeconfig.EnvNotifyCommand, "notify --message tao")
	plansRoot := t.TempDir()
	planA := "20260628-0100-plan-a"
	writeQueuePlan(t, plansRoot, planA)
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error { return nil })

	var out bytes.Buffer
	var errOut bytes.Buffer
	type notifyCall struct {
		cwd  string
		name string
		args []string
		env  map[string]string
	}
	var calls []notifyCall
	app := queueTestApp(plansRoot, &out)
	app.Err = &errOut
	app.CommandRunner = func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("notify command context has no deadline")
		}
		calls = append(calls, notifyCall{
			cwd:  cwd,
			name: name,
			args: append([]string(nil), args...),
			env: map[string]string{
				"TAO_BATCH_TOTAL":     os.Getenv("TAO_BATCH_TOTAL"),
				"TAO_BATCH_SUCCEEDED": os.Getenv("TAO_BATCH_SUCCEEDED"),
				"TAO_BATCH_REVIEWED":  os.Getenv("TAO_BATCH_REVIEWED"),
				"TAO_BATCH_FAILED":    os.Getenv("TAO_BATCH_FAILED"),
				"TAO_BATCH_PENDING":   os.Getenv("TAO_BATCH_PENDING"),
			},
		})
		return nil
	}

	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("notify calls = %d, want 1", len(calls))
	}
	call := calls[0]
	if call.cwd != "" || call.name != "sh" || !reflect.DeepEqual(call.args, []string{"-c", "notify --message tao"}) {
		t.Fatalf("notify command = cwd %q name %q args %v", call.cwd, call.name, call.args)
	}
	wantEnv := map[string]string{
		"TAO_BATCH_TOTAL":     "1",
		"TAO_BATCH_SUCCEEDED": "1",
		"TAO_BATCH_REVIEWED":  "0",
		"TAO_BATCH_FAILED":    "0",
		"TAO_BATCH_PENDING":   "0",
	}
	if !reflect.DeepEqual(call.env, wantEnv) {
		t.Fatalf("notify env = %+v, want %+v", call.env, wantEnv)
	}
	if errOut.String() != "" {
		t.Fatalf("unexpected notify stderr: %s", errOut.String())
	}
	assertContains(t, out.String(), "Final batch summary: 1 succeeded (0 reviewed), 0 failed, 0 running, 0 pending of 1")
}

func TestQueueNotifyCommandErrorWarnsAndDoesNotFail(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	t.Setenv(runtimeconfig.EnvNotifyCommand, "notify tao")
	plansRoot := t.TempDir()
	planA := "20260628-0100-plan-a"
	writeQueuePlan(t, plansRoot, planA)
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error { return nil })

	var out bytes.Buffer
	var errOut bytes.Buffer
	app := queueTestApp(plansRoot, &out)
	app.Err = &errOut
	app.CommandRunner = func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		return errors.New("notify boom")
	}

	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start"}); err != nil {
		t.Fatal(err)
	}
	assertContains(t, out.String(), "Final batch summary: 1 succeeded (0 reviewed), 0 failed, 0 running, 0 pending of 1")
	assertContains(t, errOut.String(), "warning: TAO_NOTIFY_COMMAND failed: notify boom")
}

func TestQueueNotifyCommandUnsetIsNoop(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planA := "20260628-0100-plan-a"
	writeQueuePlan(t, plansRoot, planA)
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error { return nil })

	var out bytes.Buffer
	called := false
	app := queueTestApp(plansRoot, &out)
	app.CommandRunner = func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		called = true
		return nil
	}

	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "start"}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("notify command ran with TAO_NOTIFY_COMMAND unset")
	}
	assertContains(t, out.String(), "Final batch summary: 1 succeeded (0 reviewed), 0 failed, 0 running, 0 pending of 1")
}

func TestQueueStatusOutputGroupsDetailsAndHiddenHistory(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	runningStarted := now.Add(-5 * time.Minute)
	failedStarted := now.Add(-23 * time.Minute)
	failedFinished := now.Add(-20 * time.Minute)
	succeededStarted := now.Add(-40 * time.Minute)
	succeededFinished := now.Add(-30 * time.Minute)
	oldFinished := now.Add(-25 * time.Hour)
	snapshot := runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{PlanID: "20260711-2000-success", Status: runqueue.QueueStatusSucceeded, StartedAt: &succeededStarted, FinishedAt: &succeededFinished},
		{PlanID: "20260711-2001-queued", Status: runqueue.QueueStatusPending, QueuedAt: now.Add(-10 * time.Minute), WaitReason: "waiting for workspace"},
		{PlanID: "20260711-2002-old-failure", Status: runqueue.QueueStatusFailed, FinishedAt: &oldFinished, Error: "old boom"},
		{PlanID: "20260711-2003-running", Status: runqueue.QueueStatusRunning, StartedAt: &runningStarted},
		{PlanID: "20260711-2004-failure", Status: runqueue.QueueStatusFailed, StartedAt: &failedStarted, FinishedAt: &failedFinished, Error: "tests failed"},
	}}
	if err := queueStoreForTest(t).SaveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := queueTestApp(t.TempDir(), &out)
	app.Now = func() time.Time { return now }
	if err := app.Run(context.Background(), []string{"queue", "status"}); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{
		"Summary: 4 visible (1 running, 1 queued, 1 failed, 1 succeeded (0 reviewed))",
		"1 older result hidden; use `tao queue status --all` to show complete history.",
		"started 5m ago, 5m00s elapsed",
		"queued 10m ago  waiting: waiting for workspace",
		"finished 20m ago, 3m00s elapsed  error: tests failed",
		"finished 30m ago, 10m00s elapsed",
	} {
		assertContains(t, body, want)
	}
	assertQueueSectionOrder(t, body, "Running (1)", "Queued (1)", "Failed (1)", "Recently Succeeded (1)")
	if strings.Contains(body, "old-failure") || strings.Contains(body, "QUEUED AT") || strings.Contains(body, "2026-07-12T") {
		t.Fatalf("status output restored hidden or absolute-time table content:\n%s", body)
	}
}

func TestQueueStatusGroupedASCIIOutput(t *testing.T) {
	view := queueStatusView{
		Groups: []queueStatusGroup{
			{Name: queueStatusRunningGroup, Rows: []queueStatusRow{{
				Entry: runqueue.QueueEntry{Status: runqueue.QueueStatusRunning}, Label: "api", Age: "5m", Elapsed: "5m00s", Details: "-",
			}}},
			{Name: queueStatusQueuedGroup, Rows: []queueStatusRow{{
				Entry: runqueue.QueueEntry{Status: runqueue.QueueStatusPending, WaitReason: "workspace"}, Label: "worker", Age: "10m", Elapsed: "-", Details: "workspace",
			}}},
		},
		Summary: runqueue.BatchSummary{Statuses: runqueue.QueueStatusCounts{Running: 1, Pending: 1}},
		Visible: 2,
	}

	var out bytes.Buffer
	if err := renderQueueStatus(&out, view, false); err != nil {
		t.Fatal(err)
	}
	want := "Summary: 2 visible (1 running, 1 queued)\n" +
		"\nRunning (1)\n" +
		"  running  api     started 5m ago, 5m00s elapsed\n" +
		"\nQueued (1)\n" +
		"  pending  worker  queued 10m ago  waiting: workspace\n"
	if got := out.String(); got != want {
		t.Fatalf("grouped queue output:\n%s\nwant:\n%s", got, want)
	}
}

func TestQueueStatusUnicodeLabelAlignmentAndColorEquivalence(t *testing.T) {
	view := queueStatusView{
		Groups: []queueStatusGroup{
			{Name: queueStatusRunningGroup, Rows: []queueStatusRow{{
				Entry: runqueue.QueueEntry{Status: runqueue.QueueStatusRunning}, Label: "界", Age: "now", Elapsed: "-", Details: "-",
			}}},
			{Name: queueStatusQueuedGroup, Rows: []queueStatusRow{{
				Entry: runqueue.QueueEntry{Status: runqueue.QueueStatusPending}, Label: "alpha", Age: "now", Elapsed: "-", Details: "-",
			}}},
		},
		Summary: runqueue.BatchSummary{Statuses: runqueue.QueueStatusCounts{Running: 1, Pending: 1}},
		Visible: 2,
	}

	var plain bytes.Buffer
	if err := renderQueueStatus(&plain, view, false); err != nil {
		t.Fatal(err)
	}
	want := "Summary: 2 visible (1 running, 1 queued)\n" +
		"\nRunning (1)\n" +
		"  running  界      started now\n" +
		"\nQueued (1)\n" +
		"  pending  alpha  queued now\n"
	if got := plain.String(); got != want {
		t.Fatalf("Unicode queue output:\n%s\nwant:\n%s", got, want)
	}

	var colored bytes.Buffer
	if err := renderQueueStatus(&colored, view, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(colored.String(), "\x1b[") {
		t.Fatalf("colored queue output did not contain ANSI escapes: %q", colored.String())
	}
	if got := stripANSI(colored.String()); got != plain.String() {
		t.Fatalf("ANSI changed Unicode queue alignment\ncolored stripped:\n%s\nplain:\n%s", got, plain.String())
	}
}

func TestQueueStatusAllShowsCompleteHistoryWithoutMutatingSnapshot(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	recentSuccess := now.Add(-time.Minute)
	oldSuccess := now.Add(-2 * time.Hour)
	oldFailure := now.Add(-25 * time.Hour)
	snapshot := runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{PlanID: "20260711-2000-recent-success", Status: runqueue.QueueStatusSucceeded, FinishedAt: &recentSuccess},
		{PlanID: "20260711-2001-old-success", Status: runqueue.QueueStatusSucceeded, FinishedAt: &oldSuccess},
		{PlanID: "20260711-2002-old-failure", Status: runqueue.QueueStatusFailed, FinishedAt: &oldFailure},
	}}
	if err := queueStoreForTest(t).SaveSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := queueTestApp(t.TempDir(), &out)
	app.Now = func() time.Time { return now }
	if err := app.Run(context.Background(), []string{"queue", "status", "--all"}); err != nil {
		t.Fatal(err)
	}
	body := out.String()
	for _, want := range []string{"Summary: 3 visible", "old-success", "old-failure", "Failed (1)", "Recently Succeeded (2)"} {
		assertContains(t, body, want)
	}
	if strings.Contains(body, " hidden;") {
		t.Fatalf("--all output reported hidden history:\n%s", body)
	}
	if got := loadQueueSnapshotForTest(t); !reflect.DeepEqual(got, snapshot) {
		t.Fatalf("queue status --all mutated snapshot: got %+v, want %+v", got, snapshot)
	}
}

func TestQueueStatusEmpty(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	var out bytes.Buffer
	app := queueTestApp(t.TempDir(), &out)
	if err := app.Run(context.Background(), []string{"queue", "status"}); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "Summary: 0 visible\nNo queued runs.\n"; got != want {
		t.Fatalf("empty queue output = %q, want %q", got, want)
	}
}

func TestQueueStatusOmitsEmptySections(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	view := buildQueueStatusView(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{PlanID: "20260711-2000-queued", Status: runqueue.QueueStatusPending, QueuedAt: now},
	}}, nil, now, false)
	var out bytes.Buffer
	if err := renderQueueStatus(&out, view, false); err != nil {
		t.Fatal(err)
	}
	assertContains(t, out.String(), "Queued (1)")
	for _, heading := range []string{"Running (", "Failed (", "Recently Succeeded ("} {
		if strings.Contains(out.String(), heading) {
			t.Fatalf("queue output included empty section %q:\n%s", heading, out.String())
		}
	}
}

func TestQueueStatusColorIsTerminalOnlyAndANSISafe(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-time.Minute)
	view := buildQueueStatusView(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{PlanID: "20260711-2000-running", Status: runqueue.QueueStatusRunning},
		{PlanID: "20260711-2001-queued", Status: runqueue.QueueStatusPending},
		{PlanID: "20260711-2002-failed", Status: runqueue.QueueStatusFailed, FinishedAt: &finished},
		{PlanID: "20260711-2003-succeeded", Status: runqueue.QueueStatusSucceeded, FinishedAt: &finished},
	}}, nil, now, false)

	var plain bytes.Buffer
	if err := renderQueueStatus(&plain, view, outputSupportsColor(&plain)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("injected non-terminal writer received ANSI escapes: %q", plain.String())
	}

	redirected, err := os.CreateTemp(t.TempDir(), "queue-status-*.txt")
	if err != nil {
		t.Fatal(err)
	}
	if err := renderQueueStatus(redirected, view, outputSupportsColor(redirected)); err != nil {
		t.Fatal(err)
	}
	if _, err := redirected.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	redirectedBody, err := io.ReadAll(redirected)
	if err != nil {
		t.Fatal(err)
	}
	if err := redirected.Close(); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(redirectedBody, []byte("\x1b[")) {
		t.Fatalf("redirected output contained ANSI escapes: %q", redirectedBody)
	}

	terminal := &testTerminalBuffer{}
	if err := renderQueueStatus(terminal, view, outputSupportsColor(terminal)); err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"\x1b[36m", "\x1b[33m", "\x1b[31m", "\x1b[32m"} {
		assertContains(t, terminal.String(), code)
	}
	if got := stripANSI(terminal.String()); got != plain.String() {
		t.Fatalf("ANSI changed queue alignment\ncolored stripped:\n%s\nplain:\n%s", got, plain.String())
	}

	t.Setenv("NO_COLOR", "1")
	var noColor testTerminalBuffer
	if err := renderQueueStatus(&noColor, view, outputSupportsColor(&noColor)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(noColor.String(), "\x1b[") {
		t.Fatalf("NO_COLOR output contained ANSI escapes: %q", noColor.String())
	}
}

func TestQueueStatusViewGroupsAndFiltersWithoutMutatingSnapshot(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	failureBoundary := now.Add(-24 * time.Hour)
	oldFailure := failureBoundary.Add(-time.Second)
	successBoundary := now.Add(-time.Hour)
	oldSuccess := successBoundary.Add(-time.Second)
	futureSuccess := now.Add(time.Minute)
	zeroFinish := time.Time{}
	snapshot := runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{PlanID: "success-boundary", Status: runqueue.QueueStatusSucceeded, FinishedAt: &successBoundary},
		{PlanID: "queued", Status: runqueue.QueueStatusPending},
		{PlanID: "old-failure", Status: runqueue.QueueStatusFailed, FinishedAt: &oldFailure},
		{PlanID: "running", Status: runqueue.QueueStatusRunning},
		{PlanID: "failure-boundary", Status: runqueue.QueueStatusFailed, FinishedAt: &failureBoundary},
		{PlanID: "old-success", Status: runqueue.QueueStatusSucceeded, FinishedAt: &oldSuccess},
		{PlanID: "failure-missing-finish", Status: runqueue.QueueStatusFailed},
		{PlanID: "success-missing-finish", Status: runqueue.QueueStatusSucceeded},
		{PlanID: "success-zero-finish", Status: runqueue.QueueStatusSucceeded, FinishedAt: &zeroFinish},
		{PlanID: "success-future-finish", Status: runqueue.QueueStatusSucceeded, FinishedAt: &futureSuccess},
	}}
	before := runqueue.QueueSnapshot{Entries: append([]runqueue.QueueEntry(nil), snapshot.Entries...)}

	view := buildQueueStatusView(snapshot, nil, now, false)
	if got, want := queueStatusGroupNames(view.Groups), []string{"Running", "Queued", "Failed", "Recently Succeeded"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group names = %v, want %v", got, want)
	}
	wantIDs := [][]string{
		{"running"},
		{"queued"},
		{"failure-boundary", "failure-missing-finish"},
		{"success-boundary", "success-missing-finish", "success-zero-finish", "success-future-finish"},
	}
	for i, group := range view.Groups {
		if got := queueStatusRowIDs(group.Rows); !reflect.DeepEqual(got, wantIDs[i]) {
			t.Fatalf("group %q IDs = %v, want %v", group.Name, got, wantIDs[i])
		}
	}
	if view.Visible != 8 || view.Hidden != 2 {
		t.Fatalf("visibility = %d visible, %d hidden, want 8 visible, 2 hidden", view.Visible, view.Hidden)
	}
	if !reflect.DeepEqual(snapshot, before) {
		t.Fatalf("buildQueueStatusView mutated snapshot: got %+v, want %+v", snapshot, before)
	}

	all := buildQueueStatusView(snapshot, nil, now, true)
	if all.Visible != len(snapshot.Entries) || all.Hidden != 0 {
		t.Fatalf("unfiltered visibility = %d visible, %d hidden, want %d visible, 0 hidden", all.Visible, all.Hidden, len(snapshot.Entries))
	}

	queuedOnly := buildQueueStatusView(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{{PlanID: "queued", Status: runqueue.QueueStatusPending}}}, nil, now, false)
	if got, want := queueStatusGroupNames(queuedOnly.Groups), []string{"Queued"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nonempty group names = %v, want %v", got, want)
	}
}

func TestQueueStatusViewSummarizesOnlyVisibleEntries(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	recentSuccess := now.Add(-30 * time.Minute)
	oldSuccess := now.Add(-2 * time.Hour)
	recentFailure := now.Add(-23 * time.Hour)
	oldFailure := now.Add(-25 * time.Hour)
	snapshot := runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{PlanID: "reviewed-visible", Status: runqueue.QueueStatusSucceeded, FinishedAt: &recentSuccess},
		{PlanID: "reviewed-hidden", Status: runqueue.QueueStatusSucceeded, FinishedAt: &oldSuccess},
		{PlanID: "failed-visible", Status: runqueue.QueueStatusFailed, FinishedAt: &recentFailure},
		{PlanID: "failed-hidden", Status: runqueue.QueueStatusFailed, FinishedAt: &oldFailure},
		{PlanID: "failed-missing", Status: runqueue.QueueStatusFailed},
		{PlanID: "running", Status: runqueue.QueueStatusRunning},
		{PlanID: "queued", Status: runqueue.QueueStatusPending},
	}}
	summaries := []plan.PlanSummary{
		{ID: "reviewed-visible", Reviewed: true},
		{ID: "reviewed-hidden", Reviewed: true},
	}

	view := buildQueueStatusView(snapshot, summaries, now, false)
	want := runqueue.BatchSummary{
		Total:             5,
		Statuses:          runqueue.QueueStatusCounts{Pending: 1, Running: 1, Succeeded: 1, Failed: 2},
		SucceededReviewed: 1,
	}
	if !reflect.DeepEqual(view.Summary, want) {
		t.Fatalf("summary = %+v, want %+v", view.Summary, want)
	}
	if view.Hidden != 2 {
		t.Fatalf("hidden = %d, want 2", view.Hidden)
	}
}

func TestQueueStatusTimingValues(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	ageTests := []struct {
		name  string
		value time.Time
		want  string
	}{
		{name: "zero", want: "-"},
		{name: "future", value: now.Add(time.Minute), want: "now"},
		{name: "exact now", value: now, want: "now"},
		{name: "under one minute", value: now.Add(-59 * time.Second), want: "<1m"},
		{name: "one minute", value: now.Add(-time.Minute), want: "1m"},
		{name: "one hour", value: now.Add(-time.Hour), want: "1h"},
		{name: "twenty four hours", value: now.Add(-24 * time.Hour), want: "1d"},
	}
	for _, test := range ageTests {
		t.Run("age "+test.name, func(t *testing.T) {
			if got := formatQueueStatusAge(test.value, now); got != test.want {
				t.Fatalf("age = %q, want %q", got, test.want)
			}
		})
	}

	started := now.Add(-time.Hour - 2*time.Minute - 3*time.Second)
	finished := started.Add(2*time.Minute + 3*time.Second)
	futureStart := now.Add(time.Minute)
	zeroStart := time.Time{}
	elapsedTests := []struct {
		name  string
		entry runqueue.QueueEntry
		want  string
	}{
		{name: "running", entry: runqueue.QueueEntry{Status: runqueue.QueueStatusRunning, StartedAt: &started}, want: "1h02m"},
		{name: "completed", entry: runqueue.QueueEntry{Status: runqueue.QueueStatusSucceeded, StartedAt: &started, FinishedAt: &finished}, want: "2m03s"},
		{name: "missing start", entry: runqueue.QueueEntry{Status: runqueue.QueueStatusSucceeded, FinishedAt: &finished}, want: "-"},
		{name: "zero start", entry: runqueue.QueueEntry{Status: runqueue.QueueStatusRunning, StartedAt: &zeroStart}, want: "-"},
		{name: "missing finish", entry: runqueue.QueueEntry{Status: runqueue.QueueStatusFailed, StartedAt: &started}, want: "-"},
		{name: "future start", entry: runqueue.QueueEntry{Status: runqueue.QueueStatusRunning, StartedAt: &futureStart}, want: "0s"},
		{name: "zero elapsed", entry: runqueue.QueueEntry{Status: runqueue.QueueStatusSucceeded, StartedAt: &finished, FinishedAt: &finished}, want: "0s"},
	}
	for _, test := range elapsedTests {
		t.Run("elapsed "+test.name, func(t *testing.T) {
			if got := queueStatusEntryElapsed(test.entry, now); got != test.want {
				t.Fatalf("elapsed = %q, want %q", got, test.want)
			}
		})
	}
}

func TestQueueStatusRowsPreserveDetailsAndDisambiguateLabels(t *testing.T) {
	now := time.Date(2026, 7, 12, 12, 0, 0, 0, time.UTC)
	finished := now.Add(-time.Minute)
	first := "20260711-2015-repeated"
	second := "20260711-2016-repeated"
	unique := "20260711-2017-unique"
	snapshot := runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{PlanID: first, Status: runqueue.QueueStatusPending, WaitReason: "waiting for files"},
		{PlanID: second, Status: runqueue.QueueStatusFailed, FinishedAt: &finished, Error: "boom"},
		{PlanID: unique, Status: runqueue.QueueStatusSucceeded, FinishedAt: &finished},
	}}

	view := buildQueueStatusView(snapshot, nil, now, false)
	rows := make(map[string]queueStatusRow)
	for _, group := range view.Groups {
		for _, row := range group.Rows {
			rows[row.Entry.PlanID] = row
		}
	}
	if rows[first].Label != first || rows[second].Label != second {
		t.Fatalf("colliding labels = %q and %q, want full plan IDs", rows[first].Label, rows[second].Label)
	}
	if rows[unique].Label != "unique" {
		t.Fatalf("unique label = %q, want unique", rows[unique].Label)
	}
	if rows[first].Details != "waiting for files" || rows[second].Details != "boom" || rows[unique].Details != "-" {
		t.Fatalf("row details = %q, %q, %q", rows[first].Details, rows[second].Details, rows[unique].Details)
	}
}

func queueStatusGroupNames(groups []queueStatusGroup) []string {
	names := make([]string, len(groups))
	for i, group := range groups {
		names[i] = group.Name
	}
	return names
}

func queueStatusRowIDs(rows []queueStatusRow) []string {
	ids := make([]string, len(rows))
	for i, row := range rows {
		ids[i] = row.Entry.PlanID
	}
	return ids
}

func TestQueueDequeueRemovesPendingEntry(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	plansRoot := t.TempDir()
	planA := "20260628-0100-plan-a"
	planB := "20260628-0101-plan-b"
	writeQueuePlan(t, plansRoot, planA)
	writeQueuePlan(t, plansRoot, planB)
	withQueueExecutor(t, func(ctx context.Context, request run.Request) error { return nil })
	var out bytes.Buffer
	app := queueTestApp(plansRoot, &out)

	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "add", planA, planB}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"--plans-dir", plansRoot, "queue", "stop", planA}); err != nil {
		t.Fatal(err)
	}
	assertContains(t, out.String(), "Dequeued "+planA)

	snapshot := loadQueueSnapshotForTest(t)
	if got, want := queueSnapshotStatuses(snapshot), map[string]runqueue.QueueStatus{planB: runqueue.QueueStatusPending}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queue statuses after dequeue = %+v, want %+v", got, want)
	}
}

func queueTestApp(plansRoot string, out io.Writer) App {
	return App{Out: out, Err: io.Discard, Repository: func(plansDir string) Repository {
		if plansDir == "" {
			plansDir = plansRoot
		}
		return plan.NewFileRepository(plansDir)
	}}
}

func writeQueuePlan(t *testing.T, plansRoot string, planID string) {
	t.Helper()
	writeRunPlan(t, plansRoot, planID, plan.StatusPlanned, []string{"001-work"}, nil, "001-work", plan.StatusPending)
}

func configureQueueDataHome(t *testing.T) {
	t.Helper()
	t.Setenv("TAO_DATA_HOME", t.TempDir())
}

func withQueueExecutor(t *testing.T, execute func(context.Context, run.Request) error) {
	t.Helper()
	old := newQueueExecutor
	newQueueExecutor = func(repo run.Repository, out io.Writer, options run.Options) runqueue.Executor {
		return execute
	}
	t.Cleanup(func() { newQueueExecutor = old })
}

func withQueueRecoveryReviewer(t *testing.T, review runqueue.RecoveryReviewer) {
	t.Helper()
	old := newQueueRecoveryReviewer
	newQueueRecoveryReviewer = func(repo run.Repository, out io.Writer, options run.Options) runqueue.RecoveryReviewer {
		return review
	}
	t.Cleanup(func() { newQueueRecoveryReviewer = old })
}

func loadQueueSnapshotForTest(t *testing.T) runqueue.QueueSnapshot {
	t.Helper()
	snapshot, err := queueStoreForTest(t).Load()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func queueStoreForTest(t *testing.T) runqueue.Store {
	t.Helper()
	registry := taodata.NewRegistry("")
	repo, err := registry.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return runqueue.NewFileStorePaths(registry.QueuePath(repo), registry.QueueLogPath(repo))
}

func queueSnapshotStatuses(snapshot runqueue.QueueSnapshot) map[string]runqueue.QueueStatus {
	statuses := make(map[string]runqueue.QueueStatus, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		statuses[entry.PlanID] = entry.Status
	}
	return statuses
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func assertQueueError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want containing %q", err, want)
	}
}

func assertContains(t *testing.T, body string, want string) {
	t.Helper()
	if !strings.Contains(body, want) {
		t.Fatalf("expected %q to contain %q", body, want)
	}
}

func assertQueueSectionOrder(t *testing.T, body string, headings ...string) {
	t.Helper()
	previous := -1
	for _, heading := range headings {
		position := strings.Index(body, heading)
		if position < 0 {
			t.Fatalf("queue output missing section %q:\n%s", heading, body)
		}
		if position <= previous {
			t.Fatalf("queue section %q is out of order:\n%s", heading, body)
		}
		previous = position
	}
}
