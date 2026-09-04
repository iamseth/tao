package merge

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/durableintent/crashfixture"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

type defaultTargetCrashPoint struct {
	name         string
	build        func(*testing.T, *crashfixture.Fixture) crashfixture.State
	wantMutation bool
}

func defaultTargetCrashPoints() []defaultTargetCrashPoint {
	return []defaultTargetCrashPoint{
		{name: "before intent", build: func(_ *testing.T, f *crashfixture.Fixture) crashfixture.State { return f.BeforeIntent() }},
		{name: "after intent", build: func(_ *testing.T, f *crashfixture.Fixture) crashfixture.State { return f.AfterIntent() }},
		{name: "after git mutation", build: func(t *testing.T, f *crashfixture.Fixture) crashfixture.State {
			return f.AfterGitMutation(t, crashfixture.DefaultTarget)
		}, wantMutation: true},
		{name: "after settlement", build: func(t *testing.T, f *crashfixture.Fixture) crashfixture.State {
			return f.AfterSettlement(t, crashfixture.DefaultTarget)
		}, wantMutation: true},
	}
}

func TestInspectSingleMergeIntentCrashMatrix(t *testing.T) {
	for _, test := range defaultTargetCrashPoints() {
		t.Run(test.name, func(t *testing.T) {
			fixture := crashfixture.New(t)
			state := test.build(t, fixture)
			detail, intent := singleMergeCrashIntent(fixture)

			integrated, err := inspectSingleMergeIntent(context.Background(), fixture.Git, detail, intent)
			if err != nil {
				t.Fatalf("inspect at %s: %v", state.Point, err)
			}
			if integrated != test.wantMutation {
				t.Fatalf("integrated at %s = %t, want %t", state.Point, integrated, test.wantMutation)
			}
		})
	}
}

func TestRestartSingleMergeInterruptedRestartMatrix(t *testing.T) {
	for _, point := range []string{"after safety checks", "after clear before render"} {
		t.Run(point, func(t *testing.T) {
			fixture := newRealGitWorktree(t)
			parent := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
			runRealGit(t, fixture.worktreePath, "commit", "--allow-empty", "-m", "source work")
			sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
			runRealGit(t, fixture.repoRoot, "commit", "--allow-empty", "-m", "advance default")
			advanced := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
			planDir, intent := persistSingleMergeRestartPlan(t, fixture, parent, sourceHead)
			service := Service{Git: gitops.NewClient(fixture.repoRoot, nil), NewGit: func(dir string) GitClient { return gitops.NewClient(dir, nil) }}

			detail, err := plan.NewFileRepository(filepath.Dir(planDir)).ResolvePlan(context.Background(), planDir)
			if err != nil {
				t.Fatal(err)
			}
			if point == "after safety checks" {
				live, _, _, inspectErr := service.inspectSingleMergeRestartBoundary(context.Background(), service.Git, detail, intent)
				if inspectErr != nil {
					t.Fatal(inspectErr)
				}
				if recovery := ClassifySingleMergeIntentRecovery(intent, live); recovery.Verdict != SingleMergeIntentRecoveryRestartable {
					t.Fatalf("pre-interruption recovery = %#v", recovery)
				}
			} else {
				record, recordErr := plan.NewPlanRecord(planDir, detail)
				if recordErr != nil {
					t.Fatal(recordErr)
				}
				if clearErr := record.ClearSingleMergeCommitIntent(intent); clearErr != nil {
					t.Fatal(clearErr)
				}
			}

			result, err := service.RestartSingleMerge(context.Background(), planDir)
			if err != nil {
				t.Fatalf("resume restart: %v", err)
			}
			if point == "after safety checks" {
				if result.Discarded == nil || *result.Discarded != intent {
					t.Fatalf("resumed safety-check interruption = %#v", result)
				}
			} else if result.Discarded != nil || result.MergedDefaultSHA != "" || result.NextAction != "tao merge plan-a" {
				t.Fatalf("resumed post-clear interruption = %#v", result)
			}
			reloaded, reloadErr := plan.NewFileRepository(filepath.Dir(planDir)).ResolvePlan(context.Background(), planDir)
			if reloadErr != nil {
				t.Fatal(reloadErr)
			}
			if reloaded.State.Plan.MergeCommitIntent != nil {
				t.Fatalf("resumed restart retained intent: %#v", reloaded.State.Plan.MergeCommitIntent)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != advanced {
				t.Fatalf("restart moved default: got %s want %s", got, advanced)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceHead {
				t.Fatalf("restart moved source: got %s want %s", got, sourceHead)
			}
		})
	}
}

func TestInspectSingleMergeIntentRefusesLiveDefaultDrift(t *testing.T) {
	fixture := crashfixture.New(t)
	fixture.AfterGitMutation(t, crashfixture.DefaultTarget)
	detail, intent := singleMergeCrashIntent(fixture)
	intent.Message = "test: a different intended squash"

	integrated, err := inspectSingleMergeIntent(context.Background(), fixture.Git, detail, intent)
	if err == nil || !strings.Contains(err.Error(), "does not contain the exact intended squash") {
		t.Fatalf("mismatched live commit message returned integrated=%t, err=%v", integrated, err)
	}
	if integrated {
		t.Fatal("mismatched live commit message was accepted")
	}
}

func TestMergeRecoversInterruptedSingleResolutionRollbackSettlement(t *testing.T) {
	fixture := crashfixture.New(t)
	state := fixture.AfterGitMutation(t, crashfixture.DefaultTarget)
	message := "fix(merge): recover interrupted rollback\n\nWhat:\nSettle the exact restored integration boundary.\n\nWhy:\nAllow safe source rework without manual state edits.\n\nTao-Plan: plan-crash\nTao-Source-Head: " + fixture.SourceSHA
	amendCrashCommitMessage(t, fixture, message)
	integrationHead := crashRev(t, fixture, "HEAD")
	runCrashGit(t, fixture, "reset", "--hard", fixture.BaseSHA)

	createdAt := time.Date(2026, 9, 2, 18, 0, 0, 0, time.UTC)
	resolution := plan.SingleMergeResolution{
		Phase: plan.SingleMergeResolutionPhaseCommitted, ConflictFiles: []string{"source.txt"}, RequestedAt: createdAt.Add(time.Minute),
		Outcome: plan.SingleMergeResolutionOutcomeResolved, Summary: "Resolved the source conflict.", ChangedPaths: []string{"source.txt"},
		ContentFingerprint: strings.Repeat("a", 64), CommitMessage: message, ResolvedAt: createdAt.Add(2 * time.Minute),
		IntegrationHead: integrationHead, CommittedAt: createdAt.Add(3 * time.Minute),
	}
	intent := plan.SingleMergeCommitIntent{
		Message: message, PlanID: "plan-crash", SourceHead: fixture.SourceSHA,
		DefaultBranch: crashfixture.DefaultBranch, DefaultParent: fixture.BaseSHA,
		CreatedAt: createdAt, Resolution: &resolution,
	}
	detail := &plan.PlanDetail{
		Dir:   t.TempDir(),
		State: plan.State{Status: plan.StatusReviewed, Repo: plan.Repo{Root: fixture.RepoRoot}, Plan: plan.PlanState{ID: "plan-crash", MergeCommitIntent: &intent}, Workspace: &plan.Workspace{Branch: crashfixture.SourceBranch, BaseBranch: crashfixture.DefaultBranch}},
	}
	events := &fakeEventAppender{}
	service := Service{Git: fixture.Git, Events: events, Now: func() time.Time { return createdAt.Add(4 * time.Minute) }}

	err := service.Merge(context.Background(), detail, Options{NoVerify: true})
	if !errors.Is(err, ErrSingleResolutionRolledBack) {
		t.Fatalf("recover interrupted rollback at %s: %v", state.Point, err)
	}
	settled := detail.State.Plan.MergeCommitIntent
	if settled == nil || settled.Resolution == nil || settled.Resolution.Phase != plan.SingleMergeResolutionPhaseRolledBack || settled.Resolution.RollbackReason != plan.SingleMergeResolutionRollbackRecoveredInterruption {
		t.Fatalf("interrupted rollback settlement = %#v", settled)
	}
	event := events.requireSingle(t, plan.EventTypeSingleMergeRolledBack)
	if event.SingleMergeResolution == nil || event.SingleMergeResolution.IntegrationHead != integrationHead {
		t.Fatalf("interrupted rollback event lost exact commit evidence: %#v", event)
	}
	if head := crashRev(t, fixture, crashfixture.DefaultBranch); head != fixture.BaseSHA {
		t.Fatalf("recovery moved restored default: got %s want %s", head, fixture.BaseSHA)
	}
}

func TestMergeNoSquashRefusesActiveSingleResolutionBeforeRefMutation(t *testing.T) {
	tests := []struct {
		name        string
		phase       plan.SingleMergeResolutionPhase
		nonApproval bool
	}{
		{name: "committed evidence", phase: plan.SingleMergeResolutionPhaseCommitted},
		{name: "reviewed non-approval evidence", phase: plan.SingleMergeResolutionPhaseReviewed, nonApproval: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := crashfixture.New(t)
			state := fixture.AfterGitMutation(t, crashfixture.DefaultTarget)
			detail, intent := singleMergeCrashIntent(fixture)
			resolution := &plan.SingleMergeResolution{
				Phase: tt.phase, IntegrationHead: state.MutationSHA,
			}
			if tt.nonApproval {
				resolution.Review = &plan.SingleMergeResolutionReview{
					Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested,
					Base: fixture.BaseSHA, Head: state.MutationSHA,
				}
			}
			intent.Resolution = resolution
			detail.State.Status = plan.StatusReviewed
			detail.State.Repo.Root = fixture.RepoRoot
			detail.State.Plan.MergeCommitIntent = &intent
			detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
			detail.State.Workspace.Path = fixture.SourceWorktree

			defaultBefore := crashRev(t, fixture, crashfixture.DefaultBranch)
			sourceBefore := crashRev(t, fixture, crashfixture.SourceBranch)
			events := &fakeEventAppender{}
			service := Service{
				Git: fixture.Git,
				NewGit: func(dir string) GitClient {
					if dir != fixture.SourceWorktree {
						t.Fatalf("worktree Git root = %q, want %q", dir, fixture.SourceWorktree)
					}
					return fixture.SourceGit
				},
				Cleaner: successfulCleanup(), Events: events,
			}

			err := service.Merge(context.Background(), detail, Options{Force: true, NoSquash: true, NoVerify: true})
			if !errors.Is(err, ErrSingleResolutionRejected) || !strings.Contains(err.Error(), "--no-squash") {
				t.Fatalf("Merge() error = %v, want no-squash active-resolution refusal", err)
			}
			if got := crashRev(t, fixture, crashfixture.DefaultBranch); got != defaultBefore {
				t.Fatalf("default ref moved: got %s want %s", got, defaultBefore)
			}
			if got := crashRev(t, fixture, crashfixture.SourceBranch); got != sourceBefore {
				t.Fatalf("source ref moved: got %s want %s", got, sourceBefore)
			}
			if events.count(plan.EventTypePlanMerged) != 0 {
				t.Fatalf("refused no-squash merge recorded completion: %#v", events.events)
			}
		})
	}
}

func TestRecoverApplyingBatchIntegrationCrashMatrix(t *testing.T) {
	messageBranches := []struct {
		name          string
		commitMessage func(*crashfixture.Fixture) string
		override      bool
	}{
		{
			name: "tao squash trailers",
			commitMessage: func(f *crashfixture.Fixture) string {
				return batchCrashMessage("plan-crash", f.SourceSHA)
			},
		},
		{
			name:          "exact integration commit message override",
			commitMessage: func(*crashfixture.Fixture) string { return crashfixture.MutationMessage },
			override:      true,
		},
	}
	points := defaultTargetCrashPoints()

	for _, branch := range messageBranches {
		t.Run(branch.name, func(t *testing.T) {
			for _, point := range points {
				t.Run(point.name, func(t *testing.T) {
					fixture := crashfixture.New(t)
					state := point.build(t, fixture)
					message := branch.commitMessage(fixture)
					if state.MutationSHA != "" && message != crashfixture.MutationMessage {
						amendCrashCommitMessage(t, fixture, message)
					}
					candidate, integration := batchCrashIntent(fixture, message, branch.override)
					currentHead := crashRev(t, fixture, crashfixture.DefaultBranch)

					base, committed, err := recoverApplyingBatchIntegrationAtRevision(
						context.Background(), fixture.Git, candidate, integration, currentHead, "HEAD",
					)
					if err != nil {
						t.Fatalf("recover at %s: %v", state.Point, err)
					}
					if base != fixture.BaseSHA || committed != point.wantMutation {
						t.Fatalf("recover at %s = (%s, %t), want (%s, %t)", state.Point, base, committed, fixture.BaseSHA, point.wantMutation)
					}
				})
			}
		})
	}
}

func TestRecoverApplyingBatchIntegrationRefusesLiveGitDrift(t *testing.T) {
	tests := []struct {
		name     string
		override bool
		message  func(*crashfixture.Fixture) string
	}{
		{
			name: "tao squash trailers",
			message: func(f *crashfixture.Fixture) string {
				return batchCrashMessage("another-plan", f.SourceSHA)
			},
		},
		{
			name: "exact integration commit message override", override: true,
			message: func(*crashfixture.Fixture) string { return "test: wrong exact message" },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := crashfixture.New(t)
			fixture.AfterGitMutation(t, crashfixture.DefaultTarget)
			candidate, integration := batchCrashIntent(fixture, crashfixture.MutationMessage, test.override)
			if !test.override {
				integration.CommitMessage = ""
			}
			commitCrashDrift(t, fixture, test.message(fixture))
			currentHead := crashRev(t, fixture, crashfixture.DefaultBranch)

			base, committed, err := recoverApplyingBatchIntegrationAtRevision(
				context.Background(), fixture.Git, candidate, integration, currentHead, "HEAD",
			)
			if err == nil || !strings.Contains(err.Error(), "does not match the intended Tao-owned squash") {
				t.Fatalf("mismatched live Git evidence returned (%q, %t, %v)", base, committed, err)
			}
			if base != "" || committed {
				t.Fatalf("mismatched live Git evidence was accepted: base=%q committed=%t", base, committed)
			}
		})
	}
}

func singleMergeCrashIntent(fixture *crashfixture.Fixture) (*plan.PlanDetail, plan.SingleMergeCommitIntent) {
	const planID = "plan-crash"
	detail := &plan.PlanDetail{State: plan.State{
		Plan: plan.PlanState{ID: planID},
		Workspace: &plan.Workspace{
			Branch: crashfixture.SourceBranch, BaseBranch: crashfixture.DefaultBranch,
		},
	}}
	intent := plan.SingleMergeCommitIntent{
		PlanID: planID, SourceHead: fixture.SourceSHA,
		DefaultBranch: crashfixture.DefaultBranch, DefaultParent: fixture.BaseSHA,
		Message: crashfixture.MutationMessage,
	}
	return detail, intent
}

func batchCrashIntent(fixture *crashfixture.Fixture, message string, override bool) (BatchCandidate, BatchIntegration) {
	candidate := BatchCandidate{PlanID: "plan-crash", SourceTip: fixture.SourceSHA}
	integration := BatchIntegration{PlanID: candidate.PlanID, SourceHead: fixture.SourceSHA, IntegrationBaseSHA: fixture.BaseSHA}
	if override {
		integration.CommitMessage = message
	}
	return candidate, integration
}

func batchCrashMessage(planID, sourceHead string) string {
	return "test: integrate crash fixture\n\nTao-Plan: " + planID + "\nTao-Source-Head: " + sourceHead
}

func amendCrashCommitMessage(t *testing.T, fixture *crashfixture.Fixture, message string) {
	t.Helper()
	runCrashGit(t, fixture, "commit", "--amend", "-m", message)
}

func commitCrashDrift(t *testing.T, fixture *crashfixture.Fixture, message string) {
	t.Helper()
	runCrashGit(t, fixture, "commit", "--allow-empty", "-m", message)
}

func runCrashGit(t *testing.T, fixture *crashfixture.Fixture, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // Test invokes fixed git with test-owned arguments.
	cmd.Dir = fixture.RepoRoot
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func crashRev(t *testing.T, fixture *crashfixture.Fixture, revision string) string {
	t.Helper()
	sha, err := fixture.Git.RevParse(context.Background(), revision)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(sha)
}
