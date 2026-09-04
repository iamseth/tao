package merge

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

func TestClassifySingleMergeIntentRecoveryDecisionTable(t *testing.T) {
	baseIntent := plan.SingleMergeCommitIntent{
		PlanID: "plan-a", DefaultBranch: "main", DefaultParent: "parent", SourceHead: "source",
	}
	baseLive := SingleMergeIntentLiveState{
		PlanID: "plan-a", DefaultBranch: "main", LiveDefault: "advanced", SourceHead: "source",
		SourceBranchExists: true, DefaultBranchExists: true, DefaultAdvanced: true,
	}
	resolution := func(phase plan.SingleMergeResolutionPhase) *plan.SingleMergeResolution {
		return &plan.SingleMergeResolution{Phase: phase}
	}
	tests := []struct {
		name        string
		intent      plan.SingleMergeCommitIntent
		live        SingleMergeIntentLiveState
		wantPhase   SingleMergeIntentPhase
		wantVerdict SingleMergeIntentRecoveryVerdict
		wantReason  string
	}{
		{name: "no resolution unchanged source advanced default", intent: baseIntent, live: baseLive, wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryRestartable, wantReason: "advanced cleanly"},
		{name: "no resolution exact clean boundary", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) {
			l.LiveDefault = "parent"
			l.DefaultAdvanced = false
		}), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoverySettleExistingResolution, wantReason: "recorded source and default boundary"},
		{name: "requested pre usable provider", intent: withSingleResolution(baseIntent, resolution(plan.SingleMergeResolutionPhaseRequested)), live: baseLive, wantPhase: SingleMergeIntentPhaseRequestedPreUsableProvider, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "before a usable provider"},
		{name: "requested post prompt ambiguous", intent: withSingleResolution(baseIntent, resolution(plan.SingleMergeResolutionPhaseRequested)), live: withProviderUsable(baseLive), wantPhase: SingleMergeIntentPhaseRequestedAmbiguous, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "ambiguous"},
		{name: "resolved with unstaged exact edits", intent: withSingleResolution(baseIntent, resolution(plan.SingleMergeResolutionPhaseResolved)), live: withExactResolutionEdits(baseLive), wantPhase: SingleMergeIntentPhaseResolved, wantVerdict: SingleMergeIntentRecoverySettleExistingResolution, wantReason: "exact durable resolution edits"},
		{name: "committed", intent: withSingleResolution(baseIntent, resolution(plan.SingleMergeResolutionPhaseCommitted)), live: baseLive, wantPhase: SingleMergeIntentPhaseCommitted, wantVerdict: SingleMergeIntentRecoverySettleExistingResolution, wantReason: "authority"},
		{name: "reviewed", intent: withSingleResolution(baseIntent, resolution(plan.SingleMergeResolutionPhaseReviewed)), live: baseLive, wantPhase: SingleMergeIntentPhaseReviewed, wantVerdict: SingleMergeIntentRecoverySettleExistingResolution, wantReason: "authority"},
		{name: "rolled back", intent: withSingleResolution(baseIntent, resolution(plan.SingleMergeResolutionPhaseRolledBack)), live: baseLive, wantPhase: SingleMergeIntentPhaseRolledBack, wantVerdict: SingleMergeIntentRecoveryRebaseAndReviewRequired, wantReason: "rolled back"},
		{name: "moved source", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.SourceHead = "moved" }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "source branch moved"},
		{name: "rewound default", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.DefaultAdvanced = false; l.DefaultRewound = true }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "rewound or diverged"},
		{name: "divergent default", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.DefaultAdvanced = false }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "rewound or diverged"},
		{name: "dirty worktree", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.Dirty = true }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "dirty"},
		{name: "active merge", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.ActiveOperation = "merge" }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "merge operation"},
		{name: "active rebase", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.ActiveOperation = "rebase" }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "rebase operation"},
		{name: "missing source branch", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.SourceBranchExists = false }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "missing"},
		{name: "missing default branch", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.DefaultBranchExists = false }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "missing"},
		{name: "identity mismatch", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.PlanID = "other" }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "identity"},
		{name: "unhealthy ownership", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.OwnershipUnsafe = true }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "ownership"},
		{name: "resolved exact edits with unrelated plan worktree dirt", intent: withSingleResolution(baseIntent, resolution(plan.SingleMergeResolutionPhaseResolved)), live: alterSingleLive(withExactResolutionEdits(baseLive), func(l *SingleMergeIntentLiveState) {
			l.PlanWorktreeDirty = true
		}), wantPhase: SingleMergeIntentPhaseResolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "plan worktree has uncommitted changes"},
		{name: "plan worktree dirt alone", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) { l.PlanWorktreeDirty = true }), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoveryManualOnly, wantReason: "plan worktree has uncommitted changes"},
		{name: "exact intended squash already present", intent: baseIntent, live: alterSingleLive(baseLive, func(l *SingleMergeIntentLiveState) {
			l.ExactIntendedSquash = true
			l.Dirty = true
			l.PlanWorktreeDirty = true
			l.SourceHead = "moved"
		}), wantPhase: SingleMergeIntentPhaseUnresolved, wantVerdict: SingleMergeIntentRecoverySettleExistingResolution, wantReason: "already present"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifySingleMergeIntentRecovery(tt.intent, tt.live)
			if got.Phase != tt.wantPhase || got.Verdict != tt.wantVerdict || !strings.Contains(got.Reason, tt.wantReason) {
				t.Fatalf("classification = %#v, want phase %q verdict %q reason containing %q", got, tt.wantPhase, tt.wantVerdict, tt.wantReason)
			}
		})
	}
}

// resolvedExactEditsFixture builds a separate-worktree plan whose integration
// worktree holds exactly the durable resolution edits, so callers can perturb
// one worktree at a time.
func resolvedExactEditsFixture(t *testing.T) (realGitWorktree, *plan.PlanDetail, Service) {
	t.Helper()
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "README.md"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "commit", "-am", "source work")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte("resolved\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := resolutionContentFingerprint(fixture.repoRoot, []string{"README.md"})
	if err != nil {
		t.Fatal(err)
	}

	detail := mergeReadyDetail(parent)
	detail.State.Repo = plan.Repo{Root: fixture.repoRoot, Branch: fixture.defaultBranch}
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(fixture.worktreePath), Path: fixture.worktreePath,
		Branch: fixture.planBranch, BaseBranch: fixture.defaultBranch, LifecycleStatus: plan.WorkspaceStatusReady,
	}
	detail.State.Plan.Review.Head = sourceHead
	setSingleMergeIntent(t, detail, sourceHead, parent)
	createdAt := time.Date(2026, 9, 4, 20, 0, 0, 0, time.UTC)
	detail.State.Plan.MergeCommitIntent.CreatedAt = createdAt
	detail.State.Plan.MergeCommitIntent.Resolution = &plan.SingleMergeResolution{
		Phase: plan.SingleMergeResolutionPhaseResolved, ConflictFiles: []string{"README.md"}, RequestedAt: createdAt.Add(time.Minute),
		Outcome: plan.SingleMergeResolutionOutcomeResolved, Summary: "Resolved the source conflict.", ChangedPaths: []string{"README.md"},
		ContentFingerprint: fingerprint, CommitMessage: detail.State.Plan.MergeCommitIntent.Message, ResolvedAt: createdAt.Add(2 * time.Minute),
	}
	if err := detail.State.Plan.MergeCommitIntent.Validate(); err != nil {
		t.Fatalf("resolved intent fixture is invalid: %v", err)
	}
	return fixture, detail, Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }}
}

// TestInspectSingleMergeIntentRecoveryRefusesUnrelatedPlanWorktreeDirt guards
// the classifier against treating exact integration edits as safe settlement
// while the separate plan worktree carries changes the durable resolution
// never authorized.
func TestInspectSingleMergeIntentRecoveryRefusesUnrelatedPlanWorktreeDirt(t *testing.T) {
	fixture, detail, service := resolvedExactEditsFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "unrelated.txt"), []byte("scratch\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	recovery, err := service.InspectSingleMergeIntentRecovery(context.Background(), detail)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Verdict != string(SingleMergeIntentRecoveryManualOnly) || !strings.Contains(recovery.Reason, "plan worktree has uncommitted changes") {
		t.Fatalf("unrelated plan-worktree dirt recovery = %#v, want manual-only", recovery)
	}
	detail.SingleMergeIntentRecovery = &recovery
	if next := plan.DeriveNextAction(detail).Primary; next.Command == "tao merge plan-a" {
		t.Fatalf("unrelated plan-worktree dirt must not recommend settlement: %#v", next)
	}

	// Removing only the unrelated dirt restores exact-settlement authority.
	if err := os.Remove(filepath.Join(fixture.worktreePath, "unrelated.txt")); err != nil {
		t.Fatal(err)
	}
	settled, err := service.InspectSingleMergeIntentRecovery(context.Background(), detail)
	if err != nil {
		t.Fatal(err)
	}
	if settled.Verdict != string(SingleMergeIntentRecoverySettleExistingResolution) || !strings.Contains(settled.Reason, "exact durable resolution edits") {
		t.Fatalf("clean plan worktree recovery = %#v, want settlement", settled)
	}
}

func TestInspectSingleMergeIntentRecoveryRecognizesExactResolvedEdits(t *testing.T) {
	fixture, detail, service := resolvedExactEditsFixture(t)
	recovery, err := service.InspectSingleMergeIntentRecovery(context.Background(), detail)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Verdict != string(SingleMergeIntentRecoverySettleExistingResolution) || !strings.Contains(recovery.Reason, "exact durable resolution edits") {
		t.Fatalf("exact resolved edit recovery = %#v", recovery)
	}
	detail.SingleMergeIntentRecovery = &recovery
	next := plan.DeriveNextAction(detail).Primary
	if next.Command != "tao merge plan-a" {
		t.Fatalf("exact resolved edit next action = %#v, want settlement command", next)
	}

	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte("intervening work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	drifted, err := service.InspectSingleMergeIntentRecovery(context.Background(), detail)
	if err != nil {
		t.Fatal(err)
	}
	if drifted.Verdict != string(SingleMergeIntentRecoveryManualOnly) || !strings.Contains(drifted.Reason, "dirty") {
		t.Fatalf("content-drift recovery = %#v, want manual-only", drifted)
	}
}

func TestRestartSingleMergeClearsOnlyObservedStaleIntent(t *testing.T) {
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "source.txt"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "source.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source work")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "default.txt"), []byte("unrelated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "add", "default.txt")
	runRealGit(t, fixture.repoRoot, "commit", "-m", "advance default")
	advanced := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)

	planDir, intent := persistSingleMergeRestartPlan(t, fixture, parent, sourceHead)
	result, err := (Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }}).RestartSingleMerge(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Discarded == nil || *result.Discarded != intent || result.Recovery.Verdict != SingleMergeIntentRecoveryRestartable || !strings.Contains(result.NextAction, "tao review --run plan-a") {
		t.Fatalf("restart result = %#v", result)
	}
	reloaded, err := plan.NewFileRepository(filepath.Dir(planDir)).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.MergeCommitIntent != nil {
		t.Fatalf("stale intent remains: %#v", reloaded.State.Plan.MergeCommitIntent)
	}
	restarted := requireSingleMergeRestartEvent(t, reloaded.Events)
	if restarted.PriorHead != sourceHead || restarted.BaselineHead != advanced || restarted.Branch != fixture.planBranch || restarted.BaselineBranch != fixture.defaultBranch {
		t.Fatalf("restart settlement boundary = %#v", restarted)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != advanced {
		t.Fatalf("default moved: got %s want %s", got, advanced)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceHead {
		t.Fatalf("source moved: got %s want %s", got, sourceHead)
	}
	if _, err := os.Stat(filepath.Join(planDir, ".mutation.json")); !os.IsNotExist(err) {
		t.Fatalf("mutation journal was not settled: %v", err)
	}
}

func TestRestartSingleMergeIsIdempotentAfterSuccessfulClear(t *testing.T) {
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	runRealGit(t, fixture.worktreePath, "commit", "--allow-empty", "-m", "source work")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	runRealGit(t, fixture.repoRoot, "commit", "--allow-empty", "-m", "advance default")
	planDir, _ := persistSingleMergeRestartPlan(t, fixture, parent, sourceHead)
	service := Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }}

	if _, err := service.RestartSingleMerge(context.Background(), planDir); err != nil {
		t.Fatal(err)
	}
	result, err := service.RestartSingleMerge(context.Background(), planDir)
	if err != nil {
		t.Fatalf("idempotent restart: %v", err)
	}
	if result.Discarded != nil || result.MergedDefaultSHA != "" || !strings.Contains(result.NextAction, "tao review --run plan-a") || result.Recovery.Verdict != SingleMergeIntentRecoveryRebaseAndReviewRequired {
		t.Fatalf("second restart = %#v, want persisted rebase-and-review action", result)
	}
}

func TestRestartSingleMergeInterruptedSettlementCannotRecommendOrPerformOrdinaryMerge(t *testing.T) {
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	runRealGit(t, fixture.worktreePath, "commit", "--allow-empty", "-m", "source work")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	runRealGit(t, fixture.repoRoot, "commit", "--allow-empty", "-m", "advance default")
	advanced := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	planDir, intent := persistSingleMergeRestartPlan(t, fixture, parent, sourceHead)

	repo := plan.NewFileRepository(filepath.Dir(planDir))
	detail, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	settled := detail.State
	settled.Plan.MergeCommitIntent = nil
	restartedAt := time.Date(2026, 9, 4, 22, 0, 0, 0, time.UTC)
	event := plan.Event{
		Type: plan.EventTypeSingleMergeIntentRestarted, Timestamp: restartedAt, PlanID: intent.PlanID, MutationID: "restart-merge",
		Branch: fixture.planBranch, PriorHead: sourceHead, BaselineBranch: fixture.defaultBranch, BaselineHead: advanced,
		Reason: "the source is unchanged and the default branch advanced cleanly", Message: "Stale single-plan merge intent restarted",
	}
	writeMergeRestartJournal(t, planDir, settled, event)

	service := Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }}
	result, err := service.RestartSingleMerge(context.Background(), planDir)
	if err != nil {
		t.Fatalf("restart after interrupted settlement: %v", err)
	}
	if result.NextAction == "tao merge plan-a" || !strings.Contains(result.NextAction, "tao review --run plan-a") {
		t.Fatalf("interrupted retry next action = %q", result.NextAction)
	}
	reloaded, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Merge(context.Background(), reloaded, Options{NoVerify: true}); err == nil || !strings.Contains(err.Error(), "restart requires recovery") {
		t.Fatalf("ordinary merge after interrupted settlement error = %v", err)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != advanced {
		t.Fatalf("ordinary retry moved default: got %s want %s", got, advanced)
	}
}

func TestMergeAfterRestartRejectsChangedReviewedHeadStillBasedOnStaleParent(t *testing.T) {
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	runRealGit(t, fixture.worktreePath, "commit", "--allow-empty", "-m", "source work")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	runRealGit(t, fixture.repoRoot, "commit", "--allow-empty", "-m", "advance default")
	advanced := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	planDir, _ := persistSingleMergeRestartPlan(t, fixture, parent, sourceHead)
	restartedAt := time.Date(2026, 9, 4, 22, 0, 0, 0, time.UTC)
	service := Service{
		Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) },
		Now: func() time.Time { return restartedAt },
	}
	if _, err := service.RestartSingleMerge(context.Background(), planDir); err != nil {
		t.Fatal(err)
	}

	// Changing the old source tip and reviewing it does not prove the required
	// rebase: this commit is still descended from the stale parent, not advanced.
	runRealGit(t, fixture.worktreePath, "commit", "--allow-empty", "-m", "amend on stale base")
	changedHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	detail, err := plan.NewFileRepository(filepath.Dir(planDir)).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Status = plan.StatusReviewed
	detail.State.Plan.Review.Status = plan.ReviewStatusCompleted
	detail.State.Plan.Review.Verdict = plan.ReviewVerdictApprove
	detail.State.Plan.Review.Base = parent
	detail.State.Plan.Review.Head = changedHead
	detail.State.Plan.Review.ReviewedAt = restartedAt.Add(time.Minute)

	err = service.Merge(context.Background(), detail, Options{NoVerify: true})
	if err == nil || !strings.Contains(err.Error(), "restart requires recovery") {
		t.Fatalf("merge with changed reviewed head on stale parent error = %v", err)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != advanced {
		t.Fatalf("refused merge moved default: got %s want %s", got, advanced)
	}
}

func TestRestartSingleMergeRecoversLegacyIntentWithoutResolution(t *testing.T) {
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	runRealGit(t, fixture.worktreePath, "commit", "--allow-empty", "-m", "legacy source work")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	runRealGit(t, fixture.repoRoot, "commit", "--allow-empty", "-m", "advance default")
	planDir, intent := persistSingleMergeRestartPlan(t, fixture, parent, sourceHead)

	stateJSON, err := os.ReadFile(filepath.Join(planDir, "state.json")) //nolint:gosec // Test reads its temporary fixture.
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateJSON), `"resolution"`) {
		t.Fatalf("legacy intent fixture unexpectedly has a resolution object: %s", stateJSON)
	}
	repo := plan.NewFileRepository(filepath.Dir(planDir))
	detail, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatalf("decode legacy intent: %v", err)
	}
	if got := detail.State.Plan.MergeCommitIntent; got == nil || got.Resolution != nil || *got != intent {
		t.Fatalf("decoded legacy intent = %#v", got)
	}
	service := Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }}
	recovery, err := service.InspectSingleMergeIntentRecovery(context.Background(), detail)
	if err != nil {
		t.Fatalf("classify legacy intent: %v", err)
	}
	if recovery.Phase != string(SingleMergeIntentPhaseUnresolved) || recovery.Verdict != string(SingleMergeIntentRecoveryRestartable) {
		t.Fatalf("legacy recovery classification = %#v", recovery)
	}
	result, err := service.RestartSingleMerge(context.Background(), planDir)
	if err != nil {
		t.Fatalf("recover legacy intent: %v", err)
	}
	if result.Discarded == nil || result.Discarded.Resolution != nil || result.Recovery.Phase != SingleMergeIntentPhaseUnresolved {
		t.Fatalf("legacy restart result = %#v", result)
	}
}

func TestRestartSingleMergeConflictStillRequiresRebaseAndReview(t *testing.T) {
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "README.md"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "commit", "-am", "source conflict")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte("default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "commit", "-am", "default conflict")
	planDir, _ := persistSingleMergeRestartPlan(t, fixture, parent, sourceHead)

	result, err := (Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }}).RestartSingleMerge(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.NextAction, "rebase "+fixture.planBranch+" onto main") || !strings.Contains(result.NextAction, "tao review --run plan-a") {
		t.Fatalf("conflicting restart next action = %q", result.NextAction)
	}
	if got := realGitOutput(t, fixture.repoRoot, "status", "--porcelain"); got != "" {
		t.Fatalf("restart touched default worktree: %q", got)
	}
}

func TestRestartSingleMergeRecordsExactIntendedSquashInsteadOfClearing(t *testing.T) {
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "landed.txt"), []byte("landed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "landed.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source work")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	planDir, intent := persistSingleMergeRestartPlan(t, fixture, parent, sourceHead)
	runRealGit(t, fixture.repoRoot, "merge", "--squash", fixture.planBranch)
	messageFile := filepath.Join(t.TempDir(), "message")
	if err := os.WriteFile(messageFile, []byte(intent.Message), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "commit", "-F", messageFile)
	landed := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)

	result, err := (Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }}).RestartSingleMerge(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if result.Discarded != nil || result.MergedDefaultSHA != landed || result.Recovery.Verdict != SingleMergeIntentRecoverySettleExistingResolution {
		t.Fatalf("exact landing result = %#v", result)
	}
	reloaded, err := plan.NewFileRepository(filepath.Dir(planDir)).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.PlanIsMerged(reloaded.Events) || reloaded.State.Plan.MergeCommitIntent != nil {
		t.Fatalf("exact landing was not recorded: status=%s intent=%#v events=%#v", reloaded.State.Status, reloaded.State.Plan.MergeCommitIntent, reloaded.Events)
	}
}

func TestRestartSingleMergeExactLandingVerificationFailureDoesNotRecordMerge(t *testing.T) {
	fixture := newRealGitWorktree(t)
	parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "landed.txt"), []byte("landed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "landed.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source work")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	planDir, intent := persistSingleMergeRestartPlan(t, fixture, parent, sourceHead)
	runRealGit(t, fixture.repoRoot, "merge", "--squash", fixture.planBranch)
	messageFile := filepath.Join(t.TempDir(), "message")
	if err := os.WriteFile(messageFile, []byte(intent.Message), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "commit", "-F", messageFile)
	landed := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)

	t.Setenv("TAO_MERGE_VERIFY_COMMAND", "failing verification")
	verifyErr := errors.New("tests failed")
	verifyCalls := 0
	runner := func(_ context.Context, cwd, name string, args []string, stdout, _ io.Writer) error {
		verifyCalls++
		if cwd != fixture.repoRoot || name != "sh" || len(args) != 2 || args[0] != "-c" || args[1] != "failing verification" {
			t.Fatalf("unexpected verification invocation: cwd=%q name=%q args=%#v", cwd, name, args)
		}
		_, _ = io.WriteString(stdout, "verification failed\n")
		return verifyErr
	}
	events := &fakeEventAppender{}
	cleaner := successfulCleanup()
	service := Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }, Runner: runner, Events: events, Cleaner: cleaner}
	result, err := service.RestartSingleMerge(context.Background(), planDir)
	if !errors.Is(err, verifyErr) || !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("restart verification error = %v, want verification failure", err)
	}
	if result.MergedDefaultSHA != "" || verifyCalls != 1 || events.count(plan.EventTypePlanMerged) != 0 || len(cleaner.calls) != 0 {
		t.Fatalf("failed verification settled merge: result=%#v calls=%d events=%#v cleanup=%#v", result, verifyCalls, events.events, cleaner.calls)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != landed {
		t.Fatalf("restart verification failure moved default: got %s want %s", got, landed)
	}
	reloaded, loadErr := plan.NewFileRepository(filepath.Dir(planDir)).ResolvePlan(context.Background(), planDir)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if plan.PlanIsMerged(reloaded.Events) || reloaded.State.Plan.MergeCommitIntent == nil {
		t.Fatalf("failed verification recorded merge: status=%s intent=%#v events=%#v", reloaded.State.Status, reloaded.State.Plan.MergeCommitIntent, reloaded.Events)
	}
}

func persistSingleMergeRestartPlan(t *testing.T, fixture realGitWorktree, parent, sourceHead string) (string, plan.SingleMergeCommitIntent) {
	t.Helper()
	detail := mergeReadyDetail(parent)
	detail.State.Schema = "tao.plan.state.v1"
	detail.State.Status = plan.StatusReviewed
	detail.State.Repo = plan.Repo{Root: fixture.repoRoot, Branch: fixture.defaultBranch}
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(fixture.worktreePath), Path: fixture.worktreePath, Branch: fixture.planBranch, BaseBranch: fixture.defaultBranch, LifecycleStatus: plan.WorkspaceStatusReady}
	detail.State.Plan.Review.Head = sourceHead
	detail.Slices.Schema = "tao.plan.slices.v1"
	detail.Slices.PlanID = detail.State.Plan.ID
	setSingleMergeIntent(t, detail, sourceHead, parent)
	detail.State.Plan.MergeCommitIntent.CreatedAt = time.Date(2026, 9, 4, 20, 0, 0, 0, time.UTC)
	intent := *detail.State.Plan.MergeCommitIntent
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, detail.State.Plan.ID)
	if err := os.Mkdir(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeMergeRestartJSON(t, filepath.Join(planDir, "state.json"), detail.State)
	writeMergeRestartJSON(t, filepath.Join(planDir, "slices.json"), detail.Slices)
	return planDir, intent
}

func requireSingleMergeRestartEvent(t *testing.T, events []plan.Event) plan.Event {
	t.Helper()
	var matches []plan.Event
	for _, event := range events {
		if event.Type == plan.EventTypeSingleMergeIntentRestarted {
			matches = append(matches, event)
		}
	}
	if len(matches) != 1 {
		t.Fatalf("single-merge restart events = %#v, want exactly one", matches)
	}
	return matches[0]
}

func TestSingleMergeIntentDriftErrorSupportsIsAndAs(t *testing.T) {
	err := error(&SingleMergeIntentDriftError{
		PlanID: "plan-a", DefaultBranch: "main", DefaultParent: "parent", LiveDefault: "advanced",
		SourceHead: "source", Phase: SingleMergeIntentPhaseUnresolved,
	})
	if !errors.Is(err, ErrSingleMergeIntentDrift) {
		t.Fatalf("errors.Is(%v, ErrSingleMergeIntentDrift) = false", err)
	}
	var drift *SingleMergeIntentDriftError
	if !errors.As(err, &drift) || drift.PlanID != "plan-a" || drift.LiveDefault != "advanced" || drift.SourceHead != "source" || drift.Phase != SingleMergeIntentPhaseUnresolved {
		t.Fatalf("errors.As drift = %#v", drift)
	}
	const want = "default branch main drifted from single-merge intent parent parent and does not contain the exact intended squash"
	if err.Error() != want {
		t.Fatalf("error text = %q, want %q", err.Error(), want)
	}
}

func withSingleResolution(intent plan.SingleMergeCommitIntent, resolution *plan.SingleMergeResolution) plan.SingleMergeCommitIntent {
	intent.Resolution = resolution
	return intent
}

func alterSingleLive(live SingleMergeIntentLiveState, alter func(*SingleMergeIntentLiveState)) SingleMergeIntentLiveState {
	alter(&live)
	return live
}

func withProviderUsable(live SingleMergeIntentLiveState) SingleMergeIntentLiveState {
	live.ProviderWasUsable = true
	return live
}

func withExactResolutionEdits(live SingleMergeIntentLiveState) SingleMergeIntentLiveState {
	live.ExactResolutionEdits = true
	live.Dirty = true
	return live
}
