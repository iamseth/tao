package merge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type batchLandResolver struct {
	details map[string]*plan.PlanDetail
	err     error
}

func (r *batchLandResolver) ResolvePlan(_ context.Context, input string) (*plan.PlanDetail, error) {
	if r.err != nil {
		return nil, r.err
	}
	if detail := r.details[input]; detail != nil {
		return detail, nil
	}
	return nil, errors.New("plan not found")
}

func TestBatchLandPersistsIntentMovesDefaultOnceAndRecordsSquashEvidence(t *testing.T) {
	fixture, state, integrationRoot, detail := batchLandFixture(t)
	store := &recordingBatchTransitionStore{}
	events := &fakeEventAppender{}
	service := NewService(fixture.repoRoot, nil)
	service.Events = events
	lander := BatchLander{Store: store, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Health: func(context.Context, plan.Repo) error { return nil }}

	result, err := lander.Land(context.Background(), state, integrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Status != BatchStatusLanded || result.State.LandedSHA != state.IntegrationHead {
		t.Fatalf("unexpected landed state: %+v", result.State)
	}
	if len(store.states) < 3 || store.states[0].Landing == nil || store.states[0].LandedSHA != "" || store.states[1].LandedSHA != state.IntegrationHead {
		t.Fatalf("intent must precede landed settlement: %#v", store.states)
	}
	if got := strings.TrimSpace(batchReviewGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)); got != state.IntegrationHead {
		t.Fatalf("default = %s, want %s", got, state.IntegrationHead)
	}
	event := events.requireSingle(t, plan.EventTypePlanMerged)
	if event.MergedDefaultSHA != state.Integrations[0].IntegrationSHA {
		t.Fatalf("merge evidence must name the plan squash commit: %#v", event)
	}
	if !result.State.Settlement[0].MergeEvidenceRecorded || result.State.Settlement[0].WorkspaceCleaned || result.State.Settlement[0].BranchCleaned {
		t.Fatalf("landing must record evidence without cleanup: %#v", result.State.Settlement)
	}

	// A landed retry settles from intent and never moves default or duplicates evidence.
	retried, err := lander.Land(context.Background(), result.State, integrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
	if retried.State.LandedSHA != result.State.LandedSHA {
		t.Fatalf("retry changed landed SHA: got %q want %q", retried.State.LandedSHA, result.State.LandedSHA)
	}
}

func TestBatchLandAcceptsVerifiedReducedSetAfterEject(t *testing.T) {
	fixture, state, integrationRoot, detail := batchLandFixture(t)
	reason := "aggregate review not converging on deferred.txt (plan plan-b)"
	state.Candidates = append(state.Candidates, BatchCandidate{PlanID: "plan-b", Deferred: &BatchDeferral{PlanID: "plan-b", Reason: reason}})
	state.Ejection = &BatchEjection{PlanID: "plan-b", Reason: reason, Status: batchEjectionCompleted}

	service := NewService(fixture.repoRoot, nil)
	events := &fakeEventAppender{}
	service.Events = events
	result, err := (BatchLander{Store: &recordingBatchTransitionStore{}, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Health: func(context.Context, plan.Repo) error { return nil }}).Land(context.Background(), state, integrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.Status != BatchStatusLanded || len(result.State.Landing.Plans) != 1 || result.State.Landing.Plans[0].PlanID != "plan-a" {
		t.Fatalf("reduced landing = %+v", result.State)
	}
	if len(events.events) != 1 || result.State.Candidates[1].Deferred == nil || result.State.Candidates[1].Deferred.Reason != reason {
		t.Fatalf("ejected plan was settled or lost attribution: events=%+v candidate=%+v", events.events, result.State.Candidates[1])
	}
}

func TestBatchLandRejectsIncompleteEjectRebuild(t *testing.T) {
	fixture, state, integrationRoot, detail := batchLandFixture(t)
	reason := "aggregate review not converging on deferred.txt (plan plan-b)"
	state.Candidates = append(state.Candidates, BatchCandidate{PlanID: "plan-b", Deferred: &BatchDeferral{PlanID: "plan-b", Reason: reason}})
	state.Ejection = &BatchEjection{PlanID: "plan-b", Reason: reason, Status: batchEjectionPending}

	_, err := (BatchLander{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil), Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Health: func(context.Context, plan.Repo) error { return nil }}).Land(context.Background(), state, integrationRoot)
	if err == nil || !strings.Contains(err.Error(), "ejection rebuild is incomplete") {
		t.Fatalf("expected incomplete eject gate, got %v", err)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != state.DefaultStartSHA {
		t.Fatalf("incomplete eject moved default to %s", got)
	}
}

func TestBatchLandAcceptsVerifiedAggregateResolutionHead(t *testing.T) {
	fixture, state, integrationRoot, detail := batchLandFixture(t)
	if err := os.WriteFile(filepath.Join(integrationRoot, "aggregate-fix.txt"), []byte("fix\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, integrationRoot, "add", "aggregate-fix.txt")
	runRealGit(t, integrationRoot, "commit", "-m", "fix: aggregate review")
	resolutionHead := strings.TrimSpace(batchReviewGitOutput(t, integrationRoot, "rev-parse", "HEAD"))
	state.IntegrationHead = resolutionHead
	state.Verification.HeadSHA = resolutionHead
	state.Review.HeadSHA = resolutionHead
	state.Review.ResolutionSHAs = []string{resolutionHead}
	service := NewService(fixture.repoRoot, nil)
	service.Events = &fakeEventAppender{}
	result, err := (BatchLander{Store: &recordingBatchTransitionStore{}, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Health: func(context.Context, plan.Repo) error { return nil }}).Land(context.Background(), state, integrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	if result.State.LandedSHA != resolutionHead || result.State.Landing.Plans[0].SquashSHA != state.Integrations[0].IntegrationSHA {
		t.Fatalf("aggregate resolution lost squash evidence: %+v", result.State)
	}
}

func TestBatchLandGateAndIntentFailuresLeaveRefsExact(t *testing.T) {
	t.Run("drift gate", func(t *testing.T) {
		fixture, state, integrationRoot, detail := batchLandFixture(t)
		beforeDefault := state.DefaultStartSHA
		beforeSource := state.Candidates[0].SourceTip
		state.Review.HeadSHA = "stale"
		store := &recordingBatchTransitionStore{}
		lander := BatchLander{Store: store, Service: NewService(fixture.repoRoot, nil), Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Health: func(context.Context, plan.Repo) error { return nil }}
		result, err := lander.Land(context.Background(), state, integrationRoot)
		if err == nil || result.State.Status != BatchStatusBlocked {
			t.Fatalf("expected blocked aggregate drift, got %+v %v", result.State, err)
		}
		assertRef(t, fixture.repoRoot, fixture.defaultBranch, beforeDefault)
		assertRef(t, fixture.repoRoot, fixture.planBranch, beforeSource)
	})

	t.Run("intent write", func(t *testing.T) {
		fixture, state, integrationRoot, detail := batchLandFixture(t)
		store := &recordingBatchTransitionStore{failAt: 1}
		lander := BatchLander{Store: store, Service: NewService(fixture.repoRoot, nil), Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Health: func(context.Context, plan.Repo) error { return nil }}
		_, err := lander.Land(context.Background(), state, integrationRoot)
		if err == nil || !strings.Contains(err.Error(), "landing intent") {
			t.Fatalf("expected intent failure, got %v", err)
		}
		assertRef(t, fixture.repoRoot, fixture.defaultBranch, state.DefaultStartSHA)
		assertRef(t, fixture.repoRoot, fixture.planBranch, state.Candidates[0].SourceTip)
	})
}

func TestBatchLandRecoversAfterFastForwardAndPartialPlanRecording(t *testing.T) {
	fixture, state, integrationRoot, detail := batchLandFixture(t)
	store := &recordingBatchTransitionStore{failAt: 3}
	events := &fakeEventAppender{}
	service := NewService(fixture.repoRoot, nil)
	service.Events = events
	lander := BatchLander{Store: store, Service: service, Repository: &batchLandResolver{details: map[string]*plan.PlanDetail{detail.Dir: detail}}, Health: func(context.Context, plan.Repo) error { return nil }}

	failed, err := lander.Land(context.Background(), state, integrationRoot)
	if err == nil || !strings.Contains(err.Error(), "merge settlement") {
		t.Fatalf("expected settlement persistence failure, got %+v %v", failed.State, err)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
	store.failAt = 0
	recovered, err := lander.Land(context.Background(), failed.State, integrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
	if !recovered.State.Settlement[0].MergeEvidenceRecorded {
		t.Fatalf("recovery did not record merge evidence: settlement=%#v", recovered.State.Settlement)
	}
	assertRef(t, fixture.repoRoot, fixture.defaultBranch, state.IntegrationHead)
}

func batchLandFixture(t *testing.T) (realGitWorktree, BatchState, string, *plan.PlanDetail) {
	t.Helper()
	fixture := newRealGitWorktree(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source")
	base := strings.TrimSpace(batchReviewGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch))
	source := strings.TrimSpace(batchReviewGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch))
	integrationRoot := filepath.Join(filepath.Dir(fixture.repoRoot), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/batch-land", integrationRoot, base)
	runRealGit(t, integrationRoot, "merge", "--squash", source)
	runRealGit(t, integrationRoot, "commit", "-m", "source\n\nTao-Plan: plan-a\nTao-Source-Head: "+source)
	head := strings.TrimSpace(batchReviewGitOutput(t, integrationRoot, "rev-parse", "HEAD"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	detail := mergeReadyDetail(base)
	detail.Dir = "plan-a-dir"
	detail.State.Repo = plan.Repo{Root: fixture.repoRoot, Branch: fixture.defaultBranch}
	detail.State.Plan.Review.Head = source
	state := BatchState{
		Schema: BatchStateSchema, ID: "batch-land", Status: BatchStatusReadyToLand, RepoRoot: fixture.repoRoot,
		DefaultBranch: fixture.defaultBranch, DefaultStartSHA: base, IntegrationHead: head, ChosenOrder: []string{"plan-a"},
		Candidates:   []BatchCandidate{{PlanID: "plan-a", PlanDir: detail.Dir, RepoRoot: fixture.repoRoot, Branch: fixture.planBranch, ReviewBase: base, ReviewHead: source, SourceTip: source, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: base}},
		Integrations: []BatchIntegration{{PlanID: "plan-a", SourceHead: source, IntegrationBaseSHA: base, IntegrationSHA: head, Status: batchIntegrationApplied}},
		Verification: &BatchVerification{Command: "true", HeadSHA: head, Passed: true, StartedAt: now, CompletedAt: now},
		Review:       &BatchReview{Status: "completed", Verdict: plan.ReviewVerdictApprove, BaseSHA: base, HeadSHA: head, CompletedAt: now},
	}
	return fixture, state, integrationRoot, detail
}
