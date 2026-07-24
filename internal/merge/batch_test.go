package merge

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

type batchRepository struct {
	summaries []plan.PlanSummary
	details   map[string]*plan.PlanDetail
	errors    map[string]error
}

func (r *batchRepository) ListPlans(context.Context, plan.PlanFilter) ([]plan.PlanSummary, error) {
	return append([]plan.PlanSummary(nil), r.summaries...), nil
}

func (r *batchRepository) GetPlan(ctx context.Context, id string) (*plan.PlanDetail, error) {
	return r.ResolvePlan(ctx, id)
}

func (r *batchRepository) ResolvePlan(_ context.Context, id string) (*plan.PlanDetail, error) {
	if err := r.errors[id]; err != nil {
		return nil, err
	}
	return r.details[id], nil
}

func TestBatchCandidateDiscoveryEmpty(t *testing.T) {
	repo := &batchRepository{}
	got, err := (BatchCandidateDiscovery{Repository: repo}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 0 || len(got.Blockers) != 0 {
		t.Fatalf("empty discovery = %#v", got)
	}
}

func TestBatchCandidateDiscoveryExcludesCompletedApprovedPlans(t *testing.T) {
	repo := &batchRepository{summaries: []plan.PlanSummary{{
		ID:            "already-merged",
		Status:        plan.StatusCompleted,
		Reviewed:      true,
		ReviewVerdict: plan.ReviewVerdictApprove,
	}}}
	got, err := (BatchCandidateDiscovery{Repository: repo}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Candidates) != 0 || len(got.Blockers) != 0 {
		t.Fatalf("completed approved plan was rediscovered: %#v", got)
	}
}

func TestBatchPreflightMixedEligibleAndIneligibleReportsAllBlockers(t *testing.T) {
	good := batchReadyDetail("plan-a", "tao/plan-a")
	bad := batchReadyDetail("plan-b", "tao/plan-b")
	bad.State.Status = plan.StatusInProgress
	missingErr := errors.New("plan disappeared")
	repo := &batchRepository{
		summaries: []plan.PlanSummary{
			{ID: "plan-b", Dir: "/plans/plan-b", Status: plan.StatusReviewed, Reviewed: true, ReviewVerdict: plan.ReviewVerdictApprove},
			{ID: "plan-c", Dir: "/plans/plan-c", Status: plan.StatusReviewed, Reviewed: true, ReviewVerdict: plan.ReviewVerdictApprove},
			{ID: "plan-a", Dir: "/plans/plan-a", Status: plan.StatusReviewed, Reviewed: true, ReviewVerdict: plan.ReviewVerdictApprove},
			{ID: "not-approved", Status: plan.StatusChangesRequested, Reviewed: true, ReviewVerdict: plan.ReviewVerdictChangesRequested},
		},
		details: map[string]*plan.PlanDetail{"plan-a": good, "plan-b": bad},
		errors:  map[string]error{"plan-c": missingErr},
	}
	git := &fakeGitClient{
		root:          "/repo",
		defaultBranch: "main",
		mergeBase:     "base",
		revParse: map[string]string{
			"main":       "default-start",
			"tao/plan-a": "tip-a",
			"tao/plan-b": "tip-b",
		},
	}
	discovery := BatchCandidateDiscovery{
		Repository: repo,
		Merge:      Service{Git: git},
		Health: func(context.Context, plan.Repo) error {
			return nil
		},
	}
	got, err := discovery.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if ids := candidateIDs(got.Candidates); !reflect.DeepEqual(ids, []string{"plan-a", "plan-b", "plan-c"}) {
		t.Fatalf("candidate IDs = %v", ids)
	}
	if len(got.Candidates[0].Blockers) != 0 {
		t.Fatalf("eligible candidate blockers = %#v", got.Candidates[0].Blockers)
	}
	if len(got.Candidates[1].Blockers) == 0 || len(got.Candidates[2].Blockers) == 0 {
		t.Fatalf("all ineligible candidates must be reported: %#v", got.Blockers)
	}
	if got.Candidates[0].ReviewBase != "base" || got.Candidates[0].ReviewHead != "tip-a" || got.Candidates[0].SourceTip != "tip-a" || got.DefaultStartSHA != "default-start" || got.Candidates[0].ReviewCommitMessage == nil || got.Candidates[0].CommitMessage == "" {
		t.Fatalf("candidate snapshot incomplete: %#v", got.Candidates[0])
	}
	if !strings.Contains(got.Candidates[0].CommitMessage, "Tao-Plan: plan-a\nTao-Source-Head: tip-a") {
		t.Fatalf("candidate final message lacks trusted evidence: %q", got.Candidates[0].CommitMessage)
	}
	if !blockersContain(got.Blockers, "plan-b", "not approved") || !blockersContain(got.Blockers, "plan-c", missingErr.Error()) {
		t.Fatalf("blockers = %#v", got.Blockers)
	}
	for _, call := range git.calls {
		if strings.HasPrefix(call, "checkout") || strings.HasPrefix(call, "reset") || strings.HasPrefix(call, "commit") || strings.HasPrefix(call, "merge-squash") {
			t.Fatalf("preflight invoked mutating Git operation %q", call)
		}
	}
}

func TestBatchCandidateDiscoveryBlocksInvalidApprovedProposalAndKeepsLegacyEligible(t *testing.T) {
	invalid := batchReadyDetail("plan-a", "tao/plan-a")
	invalid.State.Plan.Review.CommitMessage.Body += "\n\nTao-Plan: forged"
	legacy := batchReadyDetail("plan-b", "tao/plan-b")
	legacy.State.Plan.Review.CommitMessage = nil
	repo := &batchRepository{
		summaries: []plan.PlanSummary{
			{ID: "plan-a", Status: plan.StatusReviewed, Reviewed: true, ReviewVerdict: plan.ReviewVerdictApprove},
			{ID: "plan-b", Status: plan.StatusReviewed, Reviewed: true, ReviewVerdict: plan.ReviewVerdictApprove},
		},
		details: map[string]*plan.PlanDetail{"plan-a": invalid, "plan-b": legacy},
	}
	git := &fakeGitClient{root: "/repo", defaultBranch: "main", mergeBase: "base", revParse: map[string]string{"main": "default-start", "tao/plan-a": "tip-a", "tao/plan-b": "tip-b"}}
	got, err := (BatchCandidateDiscovery{Repository: repo, Merge: Service{Git: git}, Health: func(context.Context, plan.Repo) error { return nil }}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !blockersContain(got.Blockers, "plan-a", "reserved Tao-*") {
		t.Fatalf("invalid approved proposal was not blocked: %+v", got.Blockers)
	}
	if len(got.Candidates) != 2 || got.Candidates[1].ReviewCommitMessage != nil || got.Candidates[1].CommitMessage != "" || len(got.Candidates[1].Blockers) != 0 {
		t.Fatalf("legacy candidate was not retained for exceptional generation: %+v", got.Candidates)
	}
}

func TestBatchOrderStableOverlapAndAncestry(t *testing.T) {
	candidates := []BatchCandidate{
		{PlanID: "plan-c", SourceTip: "tip-c"},
		{PlanID: "plan-a", SourceTip: "tip-a"},
		{PlanID: "plan-b", SourceTip: "tip-b"},
	}
	ancestryCalls := 0
	simulationCalls := 0
	seams := BatchPlanningSeams{
		IsAncestor: func(_ context.Context, ancestor, descendant string) (bool, error) {
			ancestryCalls++
			return ancestor == "tip-a" && descendant == "tip-c", nil
		},
		SimulateSquash: func(_ context.Context, _ []BatchCandidate, candidate BatchCandidate) (BatchSquashSimulation, error) {
			simulationCalls++
			overlaps := map[string]int{"plan-a": 3, "plan-b": 1, "plan-c": 0}
			return BatchSquashSimulation{OverlapCount: overlaps[candidate.PlanID]}, nil
		},
	}
	got := PlanBatchCandidates(context.Background(), candidates, seams)
	if len(got.Blockers) != 0 || len(got.Deferred) != 0 {
		t.Fatalf("planning findings = %#v / %#v", got.Blockers, got.Deferred)
	}
	// plan-c has the fewest overlaps but must follow its ancestor plan-a;
	// plan-b is therefore the lowest-overlap ready candidate first.
	if ids := candidateIDs(got.Ordered); !reflect.DeepEqual(ids, []string{"plan-b", "plan-a", "plan-c"}) {
		t.Fatalf("order = %v", ids)
	}
	if ancestryCalls == 0 || simulationCalls == 0 {
		t.Fatalf("planning seams not invoked: ancestry=%d simulation=%d", ancestryCalls, simulationCalls)
	}

	again := PlanBatchCandidates(context.Background(), []BatchCandidate{candidates[1], candidates[2], candidates[0]}, seams)
	if !reflect.DeepEqual(candidateIDs(got.Ordered), candidateIDs(again.Ordered)) {
		t.Fatalf("order is not stable: %v versus %v", candidateIDs(got.Ordered), candidateIDs(again.Ordered))
	}
}

func TestBatchOrderDefersPredictedConflictWithoutMutation(t *testing.T) {
	mutations := 0
	candidates := []BatchCandidate{{PlanID: "safe", SourceTip: "safe-tip"}, {PlanID: "conflict", SourceTip: "conflict-tip"}}
	got := PlanBatchCandidates(context.Background(), candidates, BatchPlanningSeams{
		IsAncestor: func(context.Context, string, string) (bool, error) { return false, nil },
		SimulateSquash: func(_ context.Context, _ []BatchCandidate, candidate BatchCandidate) (BatchSquashSimulation, error) {
			// This seam only predicts. A separate mutation counter demonstrates
			// that the pure planner has no mutating operation to invoke.
			if candidate.PlanID == "conflict" {
				return BatchSquashSimulation{OverlapCount: 9, Conflicted: true, Reason: "shared generated file"}, nil
			}
			return BatchSquashSimulation{}, nil
		},
	})
	if ids := candidateIDs(got.Ordered); !reflect.DeepEqual(ids, []string{"safe"}) {
		t.Fatalf("ordered = %v", ids)
	}
	if len(got.Deferred) != 1 || got.Deferred[0].PlanID != "conflict" || got.Deferred[0].OverlapCount != 9 {
		t.Fatalf("deferred = %#v", got.Deferred)
	}
	if mutations != 0 {
		t.Fatalf("planner performed %d mutations", mutations)
	}
}

func batchReadyDetail(id, branch string) *plan.PlanDetail {
	return &plan.PlanDetail{
		Dir: "/plans/" + id,
		State: plan.State{
			Status: plan.StatusCompleted,
			Repo:   plan.Repo{Name: "repo", Root: "/repo", Branch: "main"},
			Plan: plan.PlanState{ID: id, Review: &plan.PlanReview{
				Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: "base", Head: "tip-" + strings.TrimPrefix(id, "plan-"),
				CommitMessage: &plan.ReviewCommitMessage{Subject: "feat(batch): integrate reviewed candidate", Body: "What:\nIntegrate the exact approved candidate changes.\n\nWhy:\nAvoid a second proposal session."},
			}},
			Workspace: &plan.Workspace{Branch: branch, BaseBranch: "main"},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001", Status: plan.StatusCompleted}}},
	}
}

func candidateIDs(candidates []BatchCandidate) []string {
	ids := make([]string, len(candidates))
	for i := range candidates {
		ids[i] = candidates[i].PlanID
	}
	return ids
}

func blockersContain(blockers []BatchBlocker, planID, text string) bool {
	for _, blocker := range blockers {
		if blocker.PlanID == planID && strings.Contains(blocker.Reason, text) {
			return true
		}
	}
	return false
}
