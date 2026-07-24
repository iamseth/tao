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

func TestBatchWorkspaceCreatesAndReusesIsolatedIntegrationWorktree(t *testing.T) {
	fixture := newRealGitWorktree(t)
	state := batchWorkspaceState(t, fixture)
	batchesDir := filepath.Join(t.TempDir(), "merge-batches")
	owner, err := NewBatchWorkspace(fixture.repoRoot, batchesDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defaultBefore := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	sourceBefore := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)

	created, err := owner.Start(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if created.Branch != "tao/integration/"+state.ID || created.Path != filepath.Join(fixture.repoRoot, ".tao", "integrations", state.ID) {
		t.Fatalf("unexpected integration namespace: %#v", created)
	}
	reused, err := owner.Start(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if !reused.Reused || reused.HeadSHA != state.DefaultStartSHA {
		t.Fatalf("interrupted workspace was not exactly reused: %#v", reused)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != defaultBefore {
		t.Fatalf("default changed during integration start: %s -> %s", defaultBefore, got)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceBefore {
		t.Fatalf("source changed during integration start: %s -> %s", sourceBefore, got)
	}
}

func TestBatchWorkspaceResumeAcceptsRecoverableDirtyResolutionPhases(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    string
		completed string
	}{
		{name: "requested agent session", status: batchIntegrationDeferred},
		{name: "applying before commit", status: batchIntegrationApplying, completed: "2026-07-16T00:01:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newRealGitWorktree(t)
			state := batchWorkspaceState(t, fixture)
			owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
			if err != nil {
				t.Fatal(err)
			}
			integration, err := owner.Start(context.Background(), state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(integration.Path, "agent-edit.txt"), []byte("interrupted\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			state.Status = BatchStatusResolving
			state.IntegrationHead = state.DefaultStartSHA
			state.Integrations = []BatchIntegration{{
				PlanID: state.Candidates[0].PlanID, SourceHead: state.Candidates[0].SourceTip,
				IntegrationBaseSHA: state.DefaultStartSHA, Status: tc.status,
				Resolutions: []BatchResolution{{Attempt: 1, Kind: "conflict", BaseSHA: state.DefaultStartSHA, RequestedAt: "2026-07-16T00:00:00Z", CompletedAt: tc.completed}},
			}}

			if err := owner.ValidateResume(context.Background(), state); err != nil {
				t.Fatalf("recoverable dirty resolution phase was rejected: %v", err)
			}
		})
	}
}

func TestBatchWorkspaceResumeAcceptsRecoverableDirtyAggregateReworkPhases(t *testing.T) {
	for _, reviewStatus := range []string{"reworking", "applying"} {
		t.Run(reviewStatus, func(t *testing.T) {
			fixture := newRealGitWorktree(t)
			state := batchWorkspaceState(t, fixture)
			owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
			if err != nil {
				t.Fatal(err)
			}
			integration, err := owner.Start(context.Background(), state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(integration.Path, "aggregate-edit.txt"), []byte("interrupted\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			state.Status = BatchStatusReviewing
			state.IntegrationHead = state.DefaultStartSHA
			state.Integrations = []BatchIntegration{{
				PlanID: state.Candidates[0].PlanID, SourceHead: state.Candidates[0].SourceTip,
				IntegrationBaseSHA: state.DefaultStartSHA, IntegrationSHA: state.DefaultStartSHA, Status: batchIntegrationApplied,
			}}
			state.Attempts.AggregateRework = 1
			state.Review = &BatchReview{Status: reviewStatus, BaseSHA: state.DefaultStartSHA, HeadSHA: state.DefaultStartSHA, Attempts: 1}

			if err := owner.ValidateResume(context.Background(), state); err != nil {
				t.Fatalf("recoverable dirty aggregate %s phase was rejected: %v", reviewStatus, err)
			}
		})
	}
}

func TestBatchWorkspaceResumeAcceptsExactApplyingTaoCommit(t *testing.T) {
	fixture := newRealGitWorktree(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source")
	state := batchWorkspaceState(t, fixture)
	owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := owner.Start(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	candidate := state.Candidates[0]
	runRealGit(t, integration.Path, "merge", "--squash", candidate.SourceTip)
	runRealGit(t, integration.Path, "commit", "-m", batchSquashCommitMessage(candidate))
	state.Status = BatchStatusResolving
	state.IntegrationHead = state.DefaultStartSHA
	state.Integrations = []BatchIntegration{{PlanID: candidate.PlanID, SourceHead: candidate.SourceTip, IntegrationBaseSHA: state.DefaultStartSHA, Status: batchIntegrationApplying}}

	if err := owner.ValidateResume(context.Background(), state); err != nil {
		t.Fatalf("exact interrupted Tao commit was not resumable: %v", err)
	}
}

func TestBatchWorkspaceResumeRequiresExactPersistedApplyingMessage(t *testing.T) {
	fixture := newRealGitWorktree(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source")
	state := batchWorkspaceState(t, fixture)
	owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := owner.Start(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	candidate := &state.Candidates[0]
	candidate.CommitMessage = testBatchCommitMessage(candidate.PlanID, candidate.SourceTip)
	runRealGit(t, integration.Path, "merge", "--squash", candidate.SourceTip)
	runRealGit(t, integration.Path, "commit", "-m", candidate.CommitMessage)
	state.Status = BatchStatusResolving
	state.IntegrationHead = state.DefaultStartSHA
	state.Integrations = []BatchIntegration{{PlanID: candidate.PlanID, SourceHead: candidate.SourceTip, IntegrationBaseSHA: state.DefaultStartSHA, CommitMessage: candidate.CommitMessage, Status: batchIntegrationApplying}}

	if err := owner.ValidateResume(context.Background(), state); err != nil {
		t.Fatalf("exact interrupted message was not resumable: %v", err)
	}
	state.Integrations[0].CommitMessage = strings.Replace(state.Integrations[0].CommitMessage, "exact approved candidate", "different approved candidate", 1)
	if err := owner.ValidateResume(context.Background(), state); err == nil || !strings.Contains(err.Error(), "commit message") {
		t.Fatalf("workspace accepted drifted exact message intent: %v", err)
	}
}

func TestBatchWorkspaceResumeAcceptsExactApplyingAggregateReworkCommit(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		name := "proposed"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newRealGitWorktree(t)
			state := batchWorkspaceState(t, fixture)
			owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
			if err != nil {
				t.Fatal(err)
			}
			integration, err := owner.Start(context.Background(), state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(integration.Path, "combined.txt"), []byte("combined\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, integration.Path, "add", ".")
			runRealGit(t, integration.Path, "commit", "-m", "feat: combined")
			parent := strings.TrimSpace(realGitOutput(t, integration.Path, "rev-parse", "HEAD"))
			if err := os.WriteFile(filepath.Join(integration.Path, "reworked.txt"), []byte("fixed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, integration.Path, "add", ".")
			message := aggregateResolutionCommitMessage(state.ID, 1)
			if !legacy {
				message, err = aggregateProposedResolutionCommitMessage(plan.ReviewCommitMessage{Subject: "fix(batch): resolve aggregate findings", Body: "What:\nResolve aggregate findings.\n\nWhy:\nKeep the batch correct."}, state.ID, 1)
				if err != nil {
					t.Fatal(err)
				}
			}
			runRealGit(t, integration.Path, "commit", "-m", message)
			state.Status = BatchStatusReviewing
			state.Integrations = []BatchIntegration{{PlanID: "plan-a", SourceHead: state.Candidates[0].SourceTip, IntegrationBaseSHA: state.DefaultStartSHA, IntegrationSHA: parent, Status: batchIntegrationApplied}}
			state.IntegrationHead = parent
			state.Attempts.AggregateRework = 1
			state.Review = &BatchReview{Status: "applying", BaseSHA: state.DefaultStartSHA, HeadSHA: parent, Attempts: 1}
			if !legacy {
				state.Review.CommitMessage = message
			}

			if err := owner.ValidateResume(context.Background(), state); err != nil {
				t.Fatalf("exact interrupted aggregate rework commit was not resumable: %v", err)
			}
		})
	}
}

func TestBatchWorkspaceOwnershipContendsAcrossInstances(t *testing.T) {
	fixture := newRealGitWorktree(t)
	state := batchWorkspaceState(t, fixture)
	batchesDir := filepath.Join(t.TempDir(), "merge-batches")
	first, _ := NewBatchWorkspace(fixture.repoRoot, batchesDir, nil)
	second, _ := NewBatchWorkspace(fixture.repoRoot, batchesDir, nil)
	ownership, err := first.AcquireOwnership(state, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ownership.Release() })
	if _, err := second.AcquireOwnership(state, time.Now()); err == nil || !strings.Contains(err.Error(), "ownership") {
		t.Fatalf("expected repository batch lock contention, got %v", err)
	}
}

func TestBatchWorkspaceResumeReportsAllDefaultSourceAndCleanlinessDrift(t *testing.T) {
	fixture := newRealGitWorktree(t)
	state := batchWorkspaceState(t, fixture)
	owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := owner.Start(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, "default.txt"), []byte("default\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "add", "default.txt")
	runRealGit(t, fixture.repoRoot, "commit", "-m", "default drift")
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "source.txt"), []byte("source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "source.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source drift")
	if err := os.WriteFile(filepath.Join(integration.Path, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultDrifted := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	sourceDrifted := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)

	err = owner.ValidateResume(context.Background(), state)
	resumeErr, ok := errors.AsType[*BatchResumeError](err)
	if !ok {
		t.Fatalf("expected aggregate resume error, got %v", err)
	}
	text := resumeErr.Error()
	for _, want := range []string{"default tip drifted", "source tip drifted", "dirty"} {
		if !strings.Contains(text, want) {
			t.Fatalf("resume error missing %q: %v", want, err)
		}
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != defaultDrifted {
		t.Fatalf("resume validation mutated default: %s -> %s", defaultDrifted, got)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceDrifted {
		t.Fatalf("resume validation mutated source: %s -> %s", sourceDrifted, got)
	}
}

func TestBatchWorkspaceRestartRemovesOnlyBatchResources(t *testing.T) {
	fixture := newRealGitWorktree(t)
	state := batchWorkspaceState(t, fixture)
	batchesDir := filepath.Join(t.TempDir(), "merge-batches")
	owner, err := NewBatchWorkspace(fixture.repoRoot, batchesDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := owner.Start(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if err := owner.store.SetActive(state.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.store.Transition(state, "now"); err != nil {
		t.Fatal(err)
	}
	defaultBefore := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	sourceBefore := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)

	preview, err := owner.Restart(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Branch != integration.Branch || !preview.RemoveBranch || !preview.RemoveWorktree || !preview.RemoveRecovery {
		t.Fatalf("unexpected restart preview: %#v", preview)
	}
	if _, err := os.Stat(integration.Path); !os.IsNotExist(err) {
		t.Fatalf("integration worktree remains: %v", err)
	}
	if realGitOutput(t, fixture.repoRoot, "branch", "--list", integration.Branch) != "" {
		t.Fatal("integration branch remains")
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != defaultBefore {
		t.Fatalf("restart changed default: %s -> %s", defaultBefore, got)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceBefore {
		t.Fatalf("restart changed source: %s -> %s", sourceBefore, got)
	}
	if active, err := owner.store.ActiveID(); err != nil || active != "" {
		t.Fatalf("active recovery remains: active=%q err=%v", active, err)
	}
}

func TestBatchWorkspaceRestartRefusesAfterLanding(t *testing.T) {
	fixture := newRealGitWorktree(t)
	state := batchWorkspaceState(t, fixture)
	state.Status = BatchStatusLanded
	owner, _ := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
	if _, err := owner.PlanRestart(context.Background(), state); err == nil {
		t.Fatal("expected landed batch restart refusal")
	}
}

func TestBatchWorkspaceRestartRefusesWhenDefaultReachedLandingIntentBeforeLandedState(t *testing.T) {
	fixture := newRealGitWorktree(t)
	state := batchWorkspaceState(t, fixture)
	owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	integration, err := owner.Start(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(integration.Path, "integrated.txt"), []byte("integrated\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, integration.Path, "add", "integrated.txt")
	runRealGit(t, integration.Path, "commit", "-m", "integrated")
	integrationHead := realGitOutput(t, integration.Path, "rev-parse", "HEAD")
	state.Status = BatchStatusReadyToLand
	state.IntegrationHead = integrationHead
	state.Landing = &BatchLanding{DefaultParentSHA: state.DefaultStartSHA, IntegrationHead: integrationHead, ExpectedFastForward: true}
	runRealGit(t, fixture.repoRoot, "merge", "--ff-only", integrationHead)

	reached, err := owner.DefaultReachedLandingIntent(context.Background(), state)
	if err != nil || !reached {
		t.Fatalf("landing intent reached = %t, err = %v", reached, err)
	}
	if _, err := owner.Restart(context.Background(), state); err == nil || !strings.Contains(err.Error(), "durable landing intent") {
		t.Fatalf("expected durable landing intent restart refusal, got %v", err)
	}
	status, err := owner.Status(context.Background(), state.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status.Missing {
		t.Fatal("restart removed integration workspace after default reached landing intent")
	}
}

func batchWorkspaceState(t *testing.T, fixture realGitWorktree) BatchState {
	t.Helper()
	start := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	source := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	return BatchState{
		Schema: BatchStateSchema, ID: "batch-a", Status: BatchStatusPlanned,
		RepoRoot: fixture.repoRoot, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: start,
		Candidates: []BatchCandidate{{
			PlanID: "plan-a", PlanDir: t.TempDir(), RepoRoot: fixture.repoRoot, Branch: fixture.planBranch,
			ReviewBase: start, ReviewHead: source, SourceTip: source, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: start,
		}},
		ChosenOrder: []string{"plan-a"},
	}
}
