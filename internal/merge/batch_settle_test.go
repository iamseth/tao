package merge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

type fakeBatchSettlementWorkspace struct {
	calls     []string
	removeErr error
	clearErr  error
}

func (f *fakeBatchSettlementWorkspace) RemoveIntegration(_ context.Context, id string) error {
	f.calls = append(f.calls, "remove "+id)
	return f.removeErr
}

func (f *fakeBatchSettlementWorkspace) ClearActive(id string) error {
	f.calls = append(f.calls, "clear "+id)
	return f.clearErr
}

func TestBatchSettleRealGitRemovesSquashSourceOnlyAfterRecording(t *testing.T) {
	fixture := newRealGitWorktree(t)
	// Move the fixture into Tao's managed workspace namespace so the real
	// workspace manager can classify and remove it.
	runRealGit(t, fixture.repoRoot, "worktree", "remove", fixture.worktreePath)
	managedPath := filepath.Join(fixture.repoRoot, ".tao", "workspaces", "plan-a")
	runRealGit(t, fixture.repoRoot, "worktree", "add", managedPath, fixture.planBranch)
	if err := os.WriteFile(filepath.Join(managedPath, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, managedPath, "add", "feature.txt")
	runRealGit(t, managedPath, "commit", "-m", "source")
	source := strings.TrimSpace(batchReviewGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch))
	runRealGit(t, fixture.repoRoot, "merge", "--squash", fixture.planBranch)
	runRealGit(t, fixture.repoRoot, "commit", "-m", "source\n\nTao-Plan: plan-a\nTao-Source-Head: "+source)
	landed := strings.TrimSpace(batchReviewGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch))
	detail := mergeReadyDetail(strings.TrimSpace(batchReviewGitOutput(t, fixture.repoRoot, "rev-parse", landed+"^")))
	detail.Dir = "plan-a-dir"
	detail.State.Repo = plan.Repo{Root: fixture.repoRoot, Branch: fixture.defaultBranch}
	detail.State.Plan.Review.Head = source
	detail.State.Workspace.Path = managedPath
	detail.State.Workspace.Branch = fixture.planBranch
	events := &fakeEventAppender{}
	service := NewService(fixture.repoRoot, nil)
	service.Events = events
	state := BatchState{
		Schema: BatchStateSchema, ID: "real-settle", Status: BatchStatusLanded, RepoRoot: fixture.repoRoot,
		DefaultBranch: fixture.defaultBranch, LandedSHA: landed, IntegrationHead: landed,
		Candidates: []BatchCandidate{{PlanID: "plan-a", PlanDir: detail.Dir, Branch: fixture.planBranch, SourceTip: source}},
		Landing:    &BatchLanding{IntegrationHead: landed, LandedDefaultSHA: landed, Plans: []BatchLandingPlan{{PlanID: "plan-a", SquashSHA: landed}}},
	}
	result, err := (BatchSettler{Store: &recordingBatchTransitionStore{}, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Workspace: &fakeBatchSettlementWorkspace{}}).Settle(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
	if !result.State.Settlement[0].Completed {
		t.Fatalf("unexpected settlement state: %+v", result.State)
	}
	if _, err := os.Stat(managedPath); !os.IsNotExist(err) {
		t.Fatalf("managed source worktree remains: %v", err)
	}
	if out := batchReviewGitOutput(t, fixture.repoRoot, "branch", "--list", fixture.planBranch); strings.TrimSpace(out) != "" {
		t.Fatalf("managed source branch remains: %s", out)
	}
}

func TestBatchSettleRecordsBeforeCleanupAndRemovesBatchRefsLast(t *testing.T) {
	state, detail, service, cleaner, events := batchSettleFixture()
	store := &recordingBatchTransitionStore{}
	batchWorkspace := &fakeBatchSettlementWorkspace{}
	var order []string
	events.onCall = func(call string) { order = append(order, call) }
	cleaner.onCall = func(call string) { order = append(order, call) }
	settler := BatchSettler{Store: store, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Workspace: batchWorkspace}

	result, err := settler.Settle(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Status != BatchStatusCompleted || !result.State.Settlement[0].Completed || !result.State.Settlement[0].MergeEvidenceRecorded {
		t.Fatalf("unexpected settlement: %+v", result.State)
	}
	if result.State.Finalization == nil || !result.State.Finalization.IntegrationCleaned {
		t.Fatalf("integration cleanup not durable: %+v", result.State.Finalization)
	}
	event := events.requireSingle(t, plan.EventTypePlanMerged)
	if event.MergedDefaultSHA != "squash" {
		t.Fatalf("unexpected merge evidence: %#v", event)
	}
	if indexOf(order, "append-event") > indexOf(order, "plan-clean plan-a") {
		t.Fatalf("cleanup preceded recording: %#v", order)
	}
	if got := strings.Join(batchWorkspace.calls, ","); got != "remove batch-settle,clear batch-settle" {
		t.Fatalf("batch refs not removed last: %s", got)
	}
}

func TestBatchSettleRejectsStaleSquashEvidenceWithoutMutation(t *testing.T) {
	state, detail, service, cleaner, events := batchSettleFixture()
	service.Git.(*fakeGitClient).commitMessages["squash"] = "not a Tao squash"
	batchWorkspace := &fakeBatchSettlementWorkspace{}
	_, err := (BatchSettler{Store: &recordingBatchTransitionStore{}, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Workspace: batchWorkspace}).Settle(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "does not carry matching Tao") {
		t.Fatalf("expected stale evidence refusal, got %v", err)
	}
	if events.count(plan.EventTypePlanMerged) != 0 || len(cleaner.calls) != 0 || len(batchWorkspace.calls) != 0 {
		t.Fatalf("stale evidence mutated settlement: events=%v cleaner=%v batch=%v", events.events, cleaner.calls, batchWorkspace.calls)
	}
}

func TestBatchSettlePreservesSourcesWhenRecordingFails(t *testing.T) {
	state, detail, service, cleaner, events := batchSettleFixture()
	events.err = errors.New("store unavailable")
	batchWorkspace := &fakeBatchSettlementWorkspace{}
	result, err := (BatchSettler{Store: &recordingBatchTransitionStore{}, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Workspace: batchWorkspace}).Settle(context.Background(), state)
	if err == nil || !strings.Contains(err.Error(), "store unavailable") {
		t.Fatalf("expected recording failure, got %v", err)
	}
	if len(cleaner.calls) != 0 || len(batchWorkspace.calls) != 0 || result.State.Settlement[0].Completed {
		t.Fatalf("recording failure removed evidence: state=%+v cleaner=%v batch=%v", result.State, cleaner.calls, batchWorkspace.calls)
	}
}

func TestBatchSettleClassifiesDirtySourceAsRequiringAttention(t *testing.T) {
	state, detail, service, cleaner, _ := batchSettleFixture()
	cleaner.managed = []workspace.ManagedCleanup{{Branch: "tao/plan-a", WorktreePath: "/worktree", Status: workspace.ManagedStatusDirty, Reason: "worktree has uncommitted changes"}}
	batchWorkspace := &fakeBatchSettlementWorkspace{}
	result, err := (BatchSettler{Store: &recordingBatchTransitionStore{}, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Workspace: batchWorkspace}).Settle(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	got := result.State.Settlement[0]
	if !got.Completed || !got.RequiresAttention || got.WorkspaceCleaned || got.BranchCleaned || len(cleaner.cleaned) != 0 {
		t.Fatalf("dirty source was not safely retained: %+v cleaned=%#v", got, cleaner.cleaned)
	}
	if len(batchWorkspace.calls) != 2 {
		t.Fatalf("classified attention should permit batch cleanup: %v", batchWorkspace.calls)
	}
}

func TestBatchSettleBatchCleanupFailuresRetainRetryEvidence(t *testing.T) {
	for _, tt := range []struct {
		name      string
		removeErr error
		clearErr  error
		wantCalls string
	}{
		{name: "integration cleanup", removeErr: errors.New("worktree busy"), wantCalls: "remove batch-settle"},
		{name: "active identity", clearErr: errors.New("identity busy"), wantCalls: "remove batch-settle,clear batch-settle"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state, detail, service, _, events := batchSettleFixture()
			batchWorkspace := &fakeBatchSettlementWorkspace{removeErr: tt.removeErr, clearErr: tt.clearErr}
			settler := BatchSettler{Store: &recordingBatchTransitionStore{}, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Workspace: batchWorkspace}
			failed, err := settler.Settle(context.Background(), state)
			if err == nil || failed.State.Status != BatchStatusCompleted {
				t.Fatalf("expected completed settlement with retained finalization retry, state=%+v err=%v", failed.State, err)
			}
			events.requireSingle(t, plan.EventTypePlanMerged)
			if got := strings.Join(batchWorkspace.calls, ","); got != tt.wantCalls {
				t.Fatalf("calls=%q want %q", got, tt.wantCalls)
			}
			batchWorkspace.removeErr, batchWorkspace.clearErr = nil, nil
			if _, err := settler.Settle(context.Background(), failed.State); err != nil {
				t.Fatal(err)
			}
			events.requireSingle(t, plan.EventTypePlanMerged)
			calls := strings.Join(batchWorkspace.calls, ",")
			if tt.clearErr != nil && strings.Count(calls, "remove batch-settle") != 1 {
				t.Fatalf("active-clear retry repeated integration deletion: %s", calls)
			}
		})
	}
}

func TestBatchSettleCleanupFailureRetriesWithoutDuplicateEvent(t *testing.T) {
	state, detail, service, cleaner, events := batchSettleFixture()
	cleaner.cleanErr = errors.New("remove failed")
	store := &recordingBatchTransitionStore{}
	batchWorkspace := &fakeBatchSettlementWorkspace{}
	settler := BatchSettler{Store: store, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Workspace: batchWorkspace}

	failed, err := settler.Settle(context.Background(), state)
	if err == nil || len(batchWorkspace.calls) != 0 {
		t.Fatalf("expected retryable cleanup failure, got %v calls=%v", err, batchWorkspace.calls)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
	if !failed.State.Settlement[0].MergeEvidenceRecorded || failed.State.Settlement[0].Completed {
		t.Fatalf("unexpected interrupted state: %+v", failed.State)
	}
	cleaner.cleanErr = nil
	recovered, err := settler.Settle(context.Background(), failed.State)
	if err != nil {
		t.Fatal(err)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
	if recovered.State.Status != BatchStatusCompleted {
		t.Fatalf("retry did not complete: state=%+v", recovered.State)
	}
	// A retained active identity after ClearActive failed is a completed no-op:
	// only finalization is retried and no source event or cleanup is repeated.
	if _, err := settler.Settle(context.Background(), recovered.State); err != nil {
		t.Fatal(err)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
	if len(cleaner.cleaned) != 2 {
		t.Fatalf("completed rerun repeated cleanup: cleanup=%d", len(cleaner.cleaned))
	}
}

func batchSettleFixture() (BatchState, *plan.PlanDetail, Service, *fakeWorkspaceCleaner, *fakeEventAppender) {
	detail := mergeVerifyDetail()
	detail.Dir = "plan-a-dir"
	cleaner := successfulCleanup()
	cleaner.managed[0].Status = workspace.ManagedStatusUnmerged
	cleaner.managed[0].CanRemove = false
	events := &fakeEventAppender{}
	git := &fakeGitClient{
		root:           mergeVerifyRoot,
		revParse:       map[string]string{"main": "landed"},
		ancestors:      map[string]bool{"landed..landed": true, "squash..landed": true},
		commitMessages: map[string]string{"squash": "source\n\nTao-Plan: plan-a\nTao-Source-Head: source"},
	}
	service := Service{Git: git, Cleaner: cleaner, Events: events}
	state := BatchState{
		Schema: BatchStateSchema, ID: "batch-settle", Status: BatchStatusLanded, RepoRoot: mergeVerifyRoot,
		DefaultBranch: "main", DefaultStartSHA: "base", LandedSHA: "landed", IntegrationHead: "landed",
		Candidates: []BatchCandidate{{PlanID: "plan-a", PlanDir: detail.Dir, Branch: "tao/plan-a", SourceTip: "source"}},
		Landing:    &BatchLanding{DefaultParentSHA: "base", IntegrationHead: "landed", LandedDefaultSHA: "landed", Plans: []BatchLandingPlan{{PlanID: "plan-a", SquashSHA: "squash"}}},
	}
	return state, detail, service, cleaner, events
}
