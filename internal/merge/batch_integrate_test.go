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

type recordingBatchTransitionStore struct {
	states          []BatchState
	failAt          int
	afterTransition func(BatchState) error
}

func (s *recordingBatchTransitionStore) Transition(state BatchState, _ string) (BatchState, error) {
	if s.failAt > 0 && len(s.states)+1 == s.failAt {
		return BatchState{}, errors.New("transition failed")
	}
	state.LogSequence++
	s.states = append(s.states, state)
	if s.afterTransition != nil {
		if err := s.afterTransition(state); err != nil {
			return state, err
		}
	}
	return state, nil
}

func TestBatchIntegratorCreatesOneSquashWithTrailersWithoutMovingSourcesOrDefault(t *testing.T) {
	fixture := newRealGitWorktree(t)
	writeBatchTestFile(t, fixture.worktreePath)
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source checkpoint")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/test", integrationRoot, defaultHead)

	store := &recordingBatchTransitionStore{}
	generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
	service := NewService(fixture.repoRoot, nil)
	service.ProposalGenerator = generator
	state := batchIntegrateTestState(fixture, sourceHead, defaultHead)
	state.Candidates[0].ReviewCommitMessage = &plan.ReviewCommitMessage{
		Subject: "feat(batch): integrate reviewed candidate",
		Body:    "What:\nIntegrate the exact approved candidate changes.\n\nWhy:\nPreserve reviewed intent without another proposal session.",
	}
	result, err := (BatchIntegrator{Store: store, Service: service}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{VerifyCommand: "test -f feature.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 1 || result.Applied[0] != "plan-a" || len(result.Deferred) != 0 || generator.calls != 0 {
		t.Fatalf("unexpected result or proposal sessions: result=%#v calls=%d", result, generator.calls)
	}
	integrationHead := realGitOutput(t, integrationRoot, "rev-parse", "HEAD")
	if parent := realGitOutput(t, integrationRoot, "rev-parse", "HEAD^"); parent != defaultHead {
		t.Fatalf("integration parent = %s, want %s", parent, defaultHead)
	}
	message := realGitOutput(t, integrationRoot, "show", "-s", "--format=%B", "HEAD")
	for _, want := range []string{"feat(batch): integrate reviewed candidate", "Tao-Plan: plan-a", "Tao-Source-Head: " + sourceHead} {
		if !strings.Contains(message, want) {
			t.Fatalf("commit %s missing %q: %q", integrationHead, want, message)
		}
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != defaultHead {
		t.Fatalf("default moved: got %s want %s", got, defaultHead)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceHead {
		t.Fatalf("source moved: got %s want %s", got, sourceHead)
	}
}

func TestBatchIntegratorDefersVerificationFailureAndRestoresCleanHead(t *testing.T) {
	fixture := newRealGitWorktree(t)
	writeBatchTestFile(t, fixture.worktreePath)
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source checkpoint")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/verify", integrationRoot, defaultHead)
	runner := func(_ context.Context, _ string, _ string, _ []string, stdout io.Writer, _ io.Writer) error {
		_, _ = stdout.Write([]byte("verification-only conflict\n"))
		return errors.New("exit 1")
	}
	service := Service{
		Git: gitops.NewClient(fixture.repoRoot, nil),
		NewGit: func(root string) GitClient {
			return gitops.NewClient(root, nil)
		},
		Runner: runner,
	}
	result, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: service}).Integrate(context.Background(), batchIntegrateTestState(fixture, sourceHead, defaultHead), integrationRoot, BatchIntegrateOptions{VerifyCommand: "false"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deferred) != 1 || !strings.Contains(result.Deferred[0].Reason, "verification failed") {
		t.Fatalf("unexpected deferral: %#v", result.Deferred)
	}
	if got := realGitOutput(t, integrationRoot, "rev-parse", "HEAD"); got != defaultHead {
		t.Fatalf("failed candidate left HEAD at %s, want %s", got, defaultHead)
	}
	if got := realGitOutput(t, integrationRoot, "status", "--porcelain"); got != "" {
		t.Fatalf("failed candidate left dirty output: %q", got)
	}
	if output := result.State.Integrations[0].VerificationOutput; !strings.Contains(output, "verification-only conflict") {
		t.Fatalf("verification output not persisted: %q", output)
	}
}

func TestBatchIntegratorDryRunUsesDisposableHistoryWithoutDurableWrites(t *testing.T) {
	fixture := newRealGitWorktree(t)
	writeBatchTestFile(t, fixture.worktreePath)
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source checkpoint")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "simulation")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/dry", integrationRoot, defaultHead)
	state := batchIntegrateTestState(fixture, sourceHead, defaultHead)
	state.Candidates[0].CommitMessage = ""
	generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
	service := NewService(fixture.repoRoot, nil)
	service.ProposalGenerator = generator
	result, err := (BatchIntegrator{Service: service}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{DryRun: true, VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Applied) != 1 || generator.calls != 0 || result.State.Candidates[0].CommitMessage != "" {
		t.Fatalf("dry-run prediction retained or generated message state: result=%#v calls=%d", result, generator.calls)
	}
	if got := realGitOutput(t, integrationRoot, "rev-parse", "HEAD"); got != defaultHead {
		t.Fatalf("dry run retained simulated history: got %s want %s", got, defaultHead)
	}
}

func TestBatchIntegratorDefersNoChangeCandidate(t *testing.T) {
	fixture := newRealGitWorktree(t)
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/no-change", integrationRoot, defaultHead)
	service := NewService(fixture.repoRoot, nil)
	result, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: service}).Integrate(context.Background(), batchIntegrateTestState(fixture, sourceHead, defaultHead), integrationRoot, BatchIntegrateOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Deferred) != 1 || result.Deferred[0].Reason != "candidate produces no changes" {
		t.Fatalf("no-change result = %#v", result)
	}
}

func TestBatchIntegratorTurnsPlannedConflictIntoActionableDeferral(t *testing.T) {
	fixture, sourceHead, defaultHead, integrationRoot := batchAgentConflictFixture(t)
	candidate := BatchCandidate{
		PlanID: "plan-a", PlanTitle: "Planner-deferred candidate", RepoRoot: fixture.repoRoot,
		Branch: fixture.planBranch, SourceTip: sourceHead, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead,
		CommitMessage: testBatchCommitMessage("plan-a", sourceHead),
	}
	planning := PlanBatchCandidates(context.Background(), []BatchCandidate{candidate}, BatchPlanningSeams{
		IsAncestor: func(context.Context, string, string) (bool, error) { return false, nil },
		SimulateSquash: func(context.Context, []BatchCandidate, BatchCandidate) (BatchSquashSimulation, error) {
			return BatchSquashSimulation{Conflicted: true, Reason: "predicted README conflict", OverlapCount: 1}, nil
		},
	})
	if len(planning.Ordered) != 0 || len(planning.Deferred) != 1 {
		t.Fatalf("planning result = %+v", planning)
	}
	candidate.Deferred = &planning.Deferred[0]
	state := BatchState{
		Schema: BatchStateSchema, ID: "planned-conflict", Status: BatchStatusPlanned,
		RepoRoot: fixture.repoRoot, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead,
		Candidates: []BatchCandidate{candidate},
	}
	store := &recordingBatchTransitionStore{}
	service := NewService(fixture.repoRoot, nil)
	integrated, err := (BatchIntegrator{Store: store, Service: service}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{VerifyCommand: "grep -q combined README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if integrated.State.Status != BatchStatusResolving || len(integrated.State.Integrations) != 1 {
		t.Fatalf("planned deferral was not made actionable: %+v", integrated.State)
	}
	if len(integrated.Deferred) != 1 || integrated.Deferred[0].OverlapCount != planning.Deferred[0].OverlapCount {
		t.Fatalf("reported planned deferral = %+v", integrated.Deferred)
	}
	record := integrated.State.Integrations[0]
	if record.PlanID != candidate.PlanID || record.Status != batchIntegrationDeferred || record.DeferredReason != planning.Deferred[0].Reason {
		t.Fatalf("planned integration deferral = %+v", record)
	}

	agent := batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		return batchResolutionJSON("resolved predicted conflict"), os.WriteFile(filepath.Join(root, "README.md"), []byte("combined\n"), 0o600)
	})
	resolved, err := (BatchAgentResolver{Store: store, Service: service, Agent: agent}).Resolve(context.Background(), integrated.State, integrationRoot, BatchResolveOptions{VerifyCommand: "grep -q combined README.md"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State.Status != BatchStatusReviewing || len(resolved.Resolved) != 1 || resolved.Resolved[0] != candidate.PlanID || resolved.State.Integrations[0].Status != batchIntegrationApplied {
		t.Fatalf("planned deferral resolution = %+v", resolved)
	}
}

func TestBatchAgentSecondPlannedDeferralRequestRemainsResumable(t *testing.T) {
	fixture := newRealGitWorktree(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "plan-a.txt"), []byte("plan a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "plan-a.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "plan a")
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	sourceA := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)

	planBRoot := filepath.Join(filepath.Dir(fixture.repoRoot), "plan-b")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/plan-b", planBRoot, defaultHead)
	if err := os.WriteFile(filepath.Join(planBRoot, "plan-b.txt"), []byte("plan b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, planBRoot, "add", "plan-b.txt")
	runRealGit(t, planBRoot, "commit", "-m", "plan b")
	sourceB := realGitOutput(t, fixture.repoRoot, "rev-parse", "tao/plan-b")

	deferredA := BatchDeferral{PlanID: "plan-a", Reason: "planner deferred plan a"}
	deferredB := BatchDeferral{PlanID: "plan-b", Reason: "planner deferred plan b"}
	state := BatchState{
		Schema: BatchStateSchema, ID: "planned-deferral-interruption", Status: BatchStatusPlanned,
		RepoRoot: fixture.repoRoot, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead,
		Candidates: []BatchCandidate{
			{PlanID: "plan-a", PlanTitle: "plan a", RepoRoot: fixture.repoRoot, Branch: fixture.planBranch, SourceTip: sourceA, ReviewBase: defaultHead, ReviewHead: sourceA, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead, CommitMessage: testBatchCommitMessage("plan-a", sourceA), Deferred: &deferredA},
			{PlanID: "plan-b", PlanTitle: "plan b", RepoRoot: fixture.repoRoot, Branch: "tao/plan-b", SourceTip: sourceB, ReviewBase: defaultHead, ReviewHead: sourceB, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead, CommitMessage: testBatchCommitMessage("plan-b", sourceB), Deferred: &deferredB},
		},
	}
	owner, err := NewBatchWorkspace(fixture.repoRoot, filepath.Join(t.TempDir(), "merge-batches"), nil)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := owner.Start(context.Background(), state)
	if err != nil {
		t.Fatal(err)
	}
	store := &recordingBatchTransitionStore{}
	service := NewService(fixture.repoRoot, nil)
	integrated, err := (BatchIntegrator{Store: store, Service: service}).Integrate(context.Background(), state, workspace.Path, BatchIntegrateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(integrated.State.Integrations) != 2 || integrated.State.Status != BatchStatusResolving {
		t.Fatalf("planned deferrals were not made actionable: %+v", integrated.State)
	}

	errInterrupted := errors.New("interrupted after durable second resolution request")
	store.afterTransition = func(state BatchState) error {
		planA := batchIntegrationIndex(state, "plan-a")
		planB := batchIntegrationIndex(state, "plan-b")
		if planA >= 0 && planB >= 0 && state.Integrations[planA].Status == batchIntegrationApplied && activeBatchResolution(&state.Integrations[planB]) != nil {
			return errInterrupted
		}
		return nil
	}
	agentCalls := 0
	_, err = (BatchAgentResolver{Store: store, Service: service, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		agentCalls++
		return batchResolutionJSON("resolved planned deferral"), os.WriteFile(filepath.Join(root, "resolution.txt"), []byte("resolved\n"), 0o600)
	})}).Resolve(context.Background(), integrated.State, workspace.Path, BatchResolveOptions{})
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("resolution interruption = %v, want %v", err, errInterrupted)
	}
	if agentCalls != 1 {
		t.Fatalf("agent calls = %d, want only the first candidate resolved", agentCalls)
	}
	interrupted := store.states[len(store.states)-1]
	if got := []string{interrupted.Integrations[0].PlanID, interrupted.Integrations[1].PlanID}; strings.Join(got, ",") != "plan-a,plan-b" {
		t.Fatalf("interrupted integration order = %v, want commit-chain order", got)
	}
	if interrupted.Integrations[1].IntegrationBaseSHA != interrupted.Integrations[0].IntegrationSHA {
		t.Fatalf("interrupted integration chain is not contiguous: %+v", interrupted.Integrations)
	}
	if drifts := validatePersistedProgress(interrupted); len(drifts) != 0 {
		t.Fatalf("interrupted resolution request does not validate: %+v", drifts)
	}
	if err := owner.ValidateResume(context.Background(), interrupted); err != nil {
		t.Fatalf("interrupted resolution request is not resumable: %v", err)
	}
}

func TestBatchIntegratorResumesApplyingIntentBeforeGitMutation(t *testing.T) {
	fixture := newRealGitWorktree(t)
	writeBatchTestFile(t, fixture.worktreePath)
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source checkpoint")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/resume-intent", integrationRoot, defaultHead)

	state := interruptedBatchIntegrateTestState(fixture, sourceHead, defaultHead)
	result, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil)}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.State.Integrations) != 1 || result.State.Integrations[0].Status != batchIntegrationApplied {
		t.Fatalf("resumed integrations = %#v", result.State.Integrations)
	}
	if len(result.Applied) != 1 || result.Applied[0] != "plan-a" {
		t.Fatalf("resumed result = %#v", result)
	}
}

func TestBatchIntegratorResumesApplyingIntentAfterTaoCommit(t *testing.T) {
	fixture := newRealGitWorktree(t)
	writeBatchTestFile(t, fixture.worktreePath)
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source checkpoint")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/resume-commit", integrationRoot, defaultHead)
	candidate := batchIntegrateTestState(fixture, sourceHead, defaultHead).Candidates[0]
	runRealGit(t, integrationRoot, "merge", "--squash", sourceHead)
	runRealGit(t, integrationRoot, "commit", "-m", candidate.CommitMessage)
	interruptedHead := realGitOutput(t, integrationRoot, "rev-parse", "HEAD")

	state := interruptedBatchIntegrateTestState(fixture, sourceHead, defaultHead)
	state.Integrations[0].CommitMessage = candidate.CommitMessage
	result, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil)}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got := realGitOutput(t, integrationRoot, "rev-parse", "HEAD"); got != interruptedHead {
		t.Fatalf("resume created another commit: got %s want %s", got, interruptedHead)
	}
	if len(result.State.Integrations) != 1 || result.State.Integrations[0].IntegrationSHA != interruptedHead || result.State.Integrations[0].Status != batchIntegrationApplied {
		t.Fatalf("resumed integrations = %#v", result.State.Integrations)
	}
}

func TestBatchIntegratorGeneratesLegacyMessageOnceBeforeMutationAndRecoversIntent(t *testing.T) {
	fixture := newRealGitWorktree(t)
	writeBatchTestFile(t, fixture.worktreePath)
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source checkpoint")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/legacy-message", integrationRoot, defaultHead)

	state := batchIntegrateTestState(fixture, sourceHead, defaultHead)
	state.Candidates[0].CommitMessage = ""
	state.Candidates[0].ReviewBase = defaultHead
	state.Candidates[0].ReviewHead = sourceHead
	generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
	service := NewService(fixture.repoRoot, nil)
	service.ProposalGenerator = generator
	interrupted := errors.New("interrupted after durable candidate message")
	store := &recordingBatchTransitionStore{afterTransition: func(state BatchState) error {
		if state.Candidates[0].CommitMessage != "" && len(state.Integrations) == 0 {
			return interrupted
		}
		return nil
	}}

	_, err := (BatchIntegrator{Store: store, Service: service}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{VerifyCommand: "true"})
	if !errors.Is(err, interrupted) || generator.calls != 1 {
		t.Fatalf("legacy message interruption = %v, calls=%d", err, generator.calls)
	}
	persisted := store.states[len(store.states)-1]
	if persisted.Candidates[0].CommitMessage == "" || len(persisted.Integrations) != 0 {
		t.Fatalf("legacy message was not persisted before intent: %+v", persisted)
	}
	if got := realGitOutput(t, integrationRoot, "rev-parse", "HEAD"); got != defaultHead {
		t.Fatalf("message interruption moved integration: got %s want %s", got, defaultHead)
	}

	store.afterTransition = nil
	result, err := (BatchIntegrator{Store: store, Service: service}).Integrate(context.Background(), persisted, integrationRoot, BatchIntegrateOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 1 || len(result.State.Integrations) != 1 || result.State.Integrations[0].CommitMessage != persisted.Candidates[0].CommitMessage {
		t.Fatalf("legacy message was regenerated or lost: calls=%d state=%+v", generator.calls, result.State)
	}
}

func TestBatchIntegratorBlocksInvalidPreparedMessageWithoutMutation(t *testing.T) {
	fixture := newRealGitWorktree(t)
	writeBatchTestFile(t, fixture.worktreePath)
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source checkpoint")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/invalid-message", integrationRoot, defaultHead)
	state := batchIntegrateTestState(fixture, sourceHead, defaultHead)
	state.Candidates[0].CommitMessage = "invalid fallback"

	result, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil)}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{})
	if err == nil || result.State.Status != BatchStatusBlocked || result.State.BlockKind != BatchBlockKindResumable {
		t.Fatalf("invalid message result = %+v, err=%v", result.State, err)
	}
	if got := realGitOutput(t, integrationRoot, "rev-parse", "HEAD"); got != defaultHead {
		t.Fatalf("invalid message moved integration: got %s want %s", got, defaultHead)
	}
}

func TestBatchIntegratorExactRecoveryRejectsDifferentFullMessage(t *testing.T) {
	fixture := newRealGitWorktree(t)
	writeBatchTestFile(t, fixture.worktreePath)
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source checkpoint")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/exact-mismatch", integrationRoot, defaultHead)
	candidate := batchIntegrateTestState(fixture, sourceHead, defaultHead).Candidates[0]
	runRealGit(t, integrationRoot, "merge", "--squash", sourceHead)
	other := strings.Replace(candidate.CommitMessage, "exact approved candidate", "different approved candidate", 1)
	runRealGit(t, integrationRoot, "commit", "-m", other)
	state := interruptedBatchIntegrateTestState(fixture, sourceHead, defaultHead)
	state.Integrations[0].CommitMessage = candidate.CommitMessage

	_, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil)}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{})
	if err == nil || !strings.Contains(err.Error(), "does not match the intended") {
		t.Fatalf("exact recovery mismatch = %v", err)
	}
}

func TestBatchIntegratorIntentWriteFailureDoesNotMutateIntegration(t *testing.T) {
	fixture := newRealGitWorktree(t)
	writeBatchTestFile(t, fixture.worktreePath)
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "source checkpoint")
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/failure", integrationRoot, defaultHead)
	store := &recordingBatchTransitionStore{failAt: 2}
	service := NewService(fixture.repoRoot, nil)
	_, err := (BatchIntegrator{Store: store, Service: service}).Integrate(context.Background(), batchIntegrateTestState(fixture, sourceHead, defaultHead), integrationRoot, BatchIntegrateOptions{VerifyCommand: "true"})
	if err == nil || !strings.Contains(err.Error(), "persist integration intent") {
		t.Fatalf("expected intent failure, got %v", err)
	}
	if got := realGitOutput(t, integrationRoot, "rev-parse", "HEAD"); got != defaultHead {
		t.Fatalf("intent failure moved integration: got %s want %s", got, defaultHead)
	}

	commitFailureRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/commit-write-failure", commitFailureRoot, defaultHead)
	store = &recordingBatchTransitionStore{failAt: 3}
	_, err = (BatchIntegrator{Store: store, Service: service}).Integrate(context.Background(), batchIntegrateTestState(fixture, sourceHead, defaultHead), commitFailureRoot, BatchIntegrateOptions{VerifyCommand: "true"})
	if err == nil || !strings.Contains(err.Error(), "persist integration commit") {
		t.Fatalf("expected commit evidence failure, got %v", err)
	}
	if got := realGitOutput(t, commitFailureRoot, "rev-parse", "HEAD"); got != defaultHead {
		t.Fatalf("commit evidence failure was not rolled back: got %s want %s", got, defaultHead)
	}
}

func TestBatchIntegratorEjectRebuildsReducedOrderedSet(t *testing.T) {
	fixture, state, integrationRoot := batchEjectTestFixture(t)
	reason := "aggregate review not converging on plan-a.txt (plan plan-a)"
	state.AggregateReviewSequence = 2
	state.Attempts = BatchAttempts{
		AggregateRework:   1,
		ReviewFingerprint: "stale-review",
		ReviewHistory:     []BatchReviewRound{{FindingFiles: []string{"plan-a.txt"}, FindingCount: 1}},
	}
	state.Review = &BatchReview{Status: "completed", Verdict: plan.ReviewVerdictChangesRequested, Attempts: 2}
	state.NonConvergence = &BatchNonConvergence{Files: []string{"plan-a.txt"}, PlanID: "plan-a", Reason: reason}
	store := newTestBatchStore(t)
	state.ID = testBatchState().ID
	state, err := store.Transition(state, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
	service := NewService(fixture.repoRoot, nil)
	service.ProposalGenerator = generator
	result, err := (BatchIntegrator{Store: store, Service: service}).Eject(context.Background(), state, integrationRoot, BatchEjectOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 0 {
		t.Fatalf("ejection regenerated persisted candidate messages: calls=%d", generator.calls)
	}
	if result.State.Status != BatchStatusReviewing || result.State.Ejection == nil || result.State.Ejection.Status != batchEjectionCompleted {
		t.Fatalf("eject state = %+v", result.State)
	}
	if got := strings.Join(result.State.ChosenOrder, ","); got != "plan-b" {
		t.Fatalf("reduced order = %q", got)
	}
	if result.State.Candidates[0].Deferred == nil || result.State.Candidates[0].Deferred.Reason != reason {
		t.Fatalf("ejected candidate attribution = %+v", result.State.Candidates[0].Deferred)
	}
	if len(result.State.Integrations) != 1 || result.State.Integrations[0].PlanID != "plan-b" {
		t.Fatalf("rebuilt integrations = %+v", result.State.Integrations)
	}
	if parent := realGitOutput(t, integrationRoot, "rev-parse", "HEAD^"); parent != state.DefaultStartSHA {
		t.Fatalf("rebuilt integration parent = %s, want %s", parent, state.DefaultStartSHA)
	}
	if result.State.Verification != nil || result.State.Review != nil || result.State.NonConvergence != nil {
		t.Fatalf("rebuilt state retained stale aggregate evidence: %+v", result.State)
	}
	if result.State.AggregateReviewSequence != 2 || result.State.Attempts.AggregateRework != 0 || result.State.Attempts.ReviewFingerprint != "" || result.State.Attempts.ReviewHistory != nil {
		t.Fatalf("rebuilt state did not isolate artifact sequence from resettable review attempts: sequence=%d attempts=%+v", result.State.AggregateReviewSequence, result.State.Attempts)
	}
}

func TestBatchIntegratorEjectRebuildIncludesResolvedPlannerDeferral(t *testing.T) {
	fixture, state, integrationRoot := batchEjectTestFixture(t)
	planCBranch := "tao/plan-c"
	planCRoot := filepath.Join(filepath.Dir(fixture.repoRoot), "plan-c")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", planCBranch, planCRoot, state.DefaultStartSHA)
	if err := os.WriteFile(filepath.Join(planCRoot, "plan-c.txt"), []byte("source c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, planCRoot, "add", "plan-c.txt")
	runRealGit(t, planCRoot, "commit", "-m", "feat: plan c")
	planCHead := realGitOutput(t, fixture.repoRoot, "rev-parse", planCBranch)
	deferral := BatchDeferral{PlanID: "plan-c", Reason: "planner predicted a conflict"}
	state.Candidates = append(state.Candidates, BatchCandidate{
		PlanID: "plan-c", PlanTitle: "Plan C", RepoRoot: fixture.repoRoot, Branch: planCBranch,
		SourceTip: planCHead, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: state.DefaultStartSHA,
		CommitMessage: testBatchCommitMessage("plan-c", planCHead), Deferred: &deferral,
	})
	state.Status = BatchStatusResolving
	if appended := appendPlannedBatchDeferrals(&state); len(appended) != 1 || appended[0].PlanID != "plan-c" {
		t.Fatalf("planned deferral records = %+v", appended)
	}
	if got := strings.Join(state.ChosenOrder, ","); got != "plan-a,plan-b" {
		t.Fatalf("planner-deferred candidate unexpectedly entered initial order: %q", got)
	}

	store := &recordingBatchTransitionStore{}
	service := NewService(fixture.repoRoot, nil)
	resolved, err := (BatchAgentResolver{Store: store, Service: service, Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
		return batchResolutionJSON("resolved planner deferral"), os.WriteFile(filepath.Join(root, "plan-c.txt"), []byte("resolved c\n"), 0o600)
	})}).Resolve(context.Background(), state, integrationRoot, BatchResolveOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(resolved.State.ChosenOrder, ","); got != "plan-a,plan-b,plan-c" {
		t.Fatalf("resolved durable order = %q", got)
	}
	resolvedMessage := resolved.State.Candidates[2].CommitMessage
	if resolvedMessage == "" || !resolved.State.Candidates[2].CommitMessageResolved {
		t.Fatalf("planner deferral did not retain its resolved message: %+v", resolved.State.Candidates[2])
	}

	rebuilt, err := (BatchIntegrator{Store: store, Service: service}).Eject(context.Background(), resolved.State, integrationRoot, BatchEjectOptions{
		PlanID: "plan-a", Reason: "aggregate review not converging on plan-a.txt (plan plan-a)", VerifyCommand: "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.State.Status != BatchStatusReviewing || rebuilt.State.Ejection == nil || rebuilt.State.Ejection.Status != batchEjectionCompleted {
		t.Fatalf("rebuilt state = %+v", rebuilt.State)
	}
	if got := strings.Join(rebuilt.State.ChosenOrder, ","); got != "plan-b,plan-c" {
		t.Fatalf("reduced durable order = %q", got)
	}
	if len(rebuilt.State.Integrations) != 2 || rebuilt.State.Integrations[0].PlanID != "plan-b" || rebuilt.State.Integrations[1].PlanID != "plan-c" {
		t.Fatalf("rebuilt integrations = %+v", rebuilt.State.Integrations)
	}
	if rebuilt.State.Candidates[2].CommitMessage != resolvedMessage || rebuilt.State.Integrations[1].CommitMessage != resolvedMessage {
		t.Fatalf("ejection rebuild lost resolved candidate message: candidate=%q integration=%q want=%q", rebuilt.State.Candidates[2].CommitMessage, rebuilt.State.Integrations[1].CommitMessage, resolvedMessage)
	}
}

func TestBatchIntegratorEjectResumesPendingIntentBeforeGitReset(t *testing.T) {
	fixture, state, integrationRoot := batchEjectTestFixture(t)
	reason := "aggregate review not converging on plan-a.txt (plan plan-a)"
	markBatchCandidateDeferred(&state, "plan-a", BatchDeferral{PlanID: "plan-a", Reason: reason})
	state.ChosenOrder = slicesDeleteValue(state.ChosenOrder, "plan-a")
	state.Ejection = &BatchEjection{PlanID: "plan-a", Reason: reason, Status: batchEjectionPending}
	oldHead := state.IntegrationHead
	if got := realGitOutput(t, integrationRoot, "rev-parse", "HEAD"); got != oldHead {
		t.Fatalf("pre-reset head = %s, want %s", got, oldHead)
	}

	resumed, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil)}).Eject(context.Background(), state, integrationRoot, BatchEjectOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State.Status != BatchStatusReviewing || resumed.State.Ejection.Status != batchEjectionCompleted || len(resumed.State.Integrations) != 1 || resumed.State.Integrations[0].PlanID != "plan-b" {
		t.Fatalf("pre-reset eject resume = %+v", resumed.State)
	}
}

func TestBatchIntegratorEjectResumeKeepsPersistedDeferralResolving(t *testing.T) {
	fixture, state, integrationRoot := batchEjectTestFixture(t)
	reason := "aggregate review not converging on plan-a.txt (plan plan-a)"
	markBatchCandidateDeferred(&state, "plan-a", BatchDeferral{PlanID: "plan-a", Reason: reason})
	state.ChosenOrder = slicesDeleteValue(state.ChosenOrder, "plan-a")
	state.Ejection = &BatchEjection{PlanID: "plan-a", Reason: reason, Status: batchEjectionReintegrating}
	state.Status = BatchStatusIntegrating
	state.Integrations = nil
	state.IntegrationHead = state.DefaultStartSHA
	runRealGit(t, integrationRoot, "reset", "--hard", state.DefaultStartSHA)

	store := &recordingBatchTransitionStore{failAt: 3}
	integrator := BatchIntegrator{Store: store, Service: NewService(fixture.repoRoot, nil)}
	_, err := integrator.Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{VerifyCommand: "false"})
	if err == nil || !strings.Contains(err.Error(), "persist integration result") {
		t.Fatalf("expected interruption after durable deferral, got %v", err)
	}
	if len(store.states) != 2 || len(store.states[1].Integrations) != 1 || store.states[1].Integrations[0].Status != batchIntegrationDeferred {
		t.Fatalf("missing durable deferral before phase transition: %+v", store.states)
	}

	resumed, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil)}).Integrate(context.Background(), store.states[1], integrationRoot, BatchIntegrateOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State.Status != BatchStatusResolving {
		t.Fatalf("resumed status = %s, want %s", resumed.State.Status, BatchStatusResolving)
	}
	if resumed.State.Ejection == nil || resumed.State.Ejection.Status != batchEjectionReintegrating {
		t.Fatalf("resumed ejection was completed before deferred candidate resolution: %+v", resumed.State.Ejection)
	}
}

func TestBatchIntegratorEjectResumesApplyingResolverIntentThroughResolver(t *testing.T) {
	fixture, state, integrationRoot := batchEjectTestFixture(t)
	reason := "aggregate review not converging on plan-a.txt (plan plan-a)"
	store := &durableFailingBatchTransitionStore{}
	service := NewService(fixture.repoRoot, nil)
	integrator := BatchIntegrator{Store: store, Service: service}

	deferred, err := integrator.Eject(context.Background(), state, integrationRoot, BatchEjectOptions{
		PlanID: "plan-a", Reason: reason, VerifyCommand: "false",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deferred.State.Status != BatchStatusResolving || deferred.State.Ejection.Status != batchEjectionReintegrating {
		t.Fatalf("reduced set did not require resolution: %+v", deferred.State)
	}

	store.failAt = len(store.states) + 4 // resolution request, outcome, applying intent, then failed settlement
	agentCalls := 0
	resolver := BatchAgentResolver{
		Store: store, Service: service,
		Agent: batchResolutionAgentFunc(func(_ context.Context, root, _ string) (string, error) {
			agentCalls++
			return batchResolutionJSON("fixed reduced candidate"), os.WriteFile(filepath.Join(root, "reduced-fixed.txt"), []byte("fixed\n"), 0o600)
		}),
	}
	_, err = resolver.Resolve(context.Background(), deferred.State, integrationRoot, BatchResolveOptions{VerifyCommand: "true"})
	if err == nil || !strings.Contains(err.Error(), "persist resolved candidate commit") {
		t.Fatalf("expected interruption after resolver commit intent, got %v", err)
	}
	applying := store.states[len(store.states)-1]
	if applying.Status != BatchStatusResolving || len(applying.Integrations) != 1 || applying.Integrations[0].Status != batchIntegrationApplying || applying.Candidates[1].Deferred == nil {
		t.Fatalf("resolver applying intent was not durable: %+v", applying)
	}
	committedHead := realGitOutput(t, integrationRoot, "rev-parse", "HEAD")
	if committedHead == applying.IntegrationHead {
		t.Fatalf("resolver commit was not created before settlement interruption")
	}

	store.failAt = 0
	resumed, err := integrator.Eject(context.Background(), applying, integrationRoot, BatchEjectOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State.Status != BatchStatusResolving || resumed.State.Integrations[0].Status != batchIntegrationApplying {
		t.Fatalf("eject resume bypassed applying resolver intent: %+v", resumed.State)
	}
	if got := realGitOutput(t, integrationRoot, "rev-parse", "HEAD"); got != committedHead {
		t.Fatalf("eject resume moved resolver commit: got %s want %s", got, committedHead)
	}

	agentCalls = 0
	resolved, err := resolver.Resolve(context.Background(), resumed.State, integrationRoot, BatchResolveOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if agentCalls != 0 {
		t.Fatalf("resolver reran agent after recovering committed intent: calls=%d", agentCalls)
	}
	if resolved.State.Status != BatchStatusReviewing || resolved.State.Ejection.Status != batchEjectionCompleted || resolved.State.Integrations[0].Status != batchIntegrationApplied || resolved.State.Candidates[1].Deferred != nil {
		t.Fatalf("resolver did not settle reduced reintegration: %+v", resolved.State)
	}
}

func TestBatchIntegratorEjectResumesAfterResetBeforeRebuildPersistence(t *testing.T) {
	fixture, state, integrationRoot := batchEjectTestFixture(t)
	reason := "aggregate review not converging on plan-a.txt (plan plan-a)"
	store := &recordingBatchTransitionStore{failAt: 2}
	integrator := BatchIntegrator{Store: store, Service: NewService(fixture.repoRoot, nil)}
	_, err := integrator.Eject(context.Background(), state, integrationRoot, BatchEjectOptions{PlanID: "plan-a", Reason: reason, VerifyCommand: "true"})
	if err == nil || !strings.Contains(err.Error(), "persist batch eject rebuild") {
		t.Fatalf("expected interrupted eject, got %v", err)
	}
	if len(store.states) != 1 || store.states[0].Ejection == nil || store.states[0].Ejection.Status != batchEjectionPending {
		t.Fatalf("missing durable eject intent: %+v", store.states)
	}
	if head := realGitOutput(t, integrationRoot, "rev-parse", "HEAD"); head != state.DefaultStartSHA {
		t.Fatalf("interrupted eject head = %s, want %s", head, state.DefaultStartSHA)
	}

	resumed, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil)}).Eject(context.Background(), store.states[0], integrationRoot, BatchEjectOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State.Status != BatchStatusReviewing || resumed.State.Ejection.Status != batchEjectionCompleted || len(resumed.State.Integrations) != 1 || resumed.State.Integrations[0].PlanID != "plan-b" {
		t.Fatalf("resumed eject = %+v", resumed.State)
	}
}

func batchEjectTestFixture(t *testing.T) (realGitWorktree, BatchState, string) {
	t.Helper()
	fixture := newRealGitWorktree(t)
	base := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "plan-a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", ".")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "feat: plan a")
	planAHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)

	planBBranch := "tao/plan-b"
	planBRoot := filepath.Join(filepath.Dir(fixture.repoRoot), "plan-b")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", planBBranch, planBRoot, base)
	if err := os.WriteFile(filepath.Join(planBRoot, "plan-b.txt"), []byte("b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, planBRoot, "add", ".")
	runRealGit(t, planBRoot, "commit", "-m", "feat: plan b")
	planBHead := realGitOutput(t, fixture.repoRoot, "rev-parse", planBBranch)

	integrationRoot := filepath.Join(t.TempDir(), "integration")
	runRealGit(t, fixture.repoRoot, "worktree", "add", "-b", "tao/integration/eject-test", integrationRoot, base)
	state := BatchState{
		Schema: BatchStateSchema, ID: "eject-test", Status: BatchStatusPlanned, RepoRoot: fixture.repoRoot,
		DefaultBranch: fixture.defaultBranch, DefaultStartSHA: base, ChosenOrder: []string{"plan-a", "plan-b"},
		Candidates: []BatchCandidate{
			{PlanID: "plan-a", PlanTitle: "Plan A", RepoRoot: fixture.repoRoot, Branch: fixture.planBranch, SourceTip: planAHead, ReviewBase: base, ReviewHead: planAHead, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: base, CommitMessage: testBatchCommitMessage("plan-a", planAHead)},
			{PlanID: "plan-b", PlanTitle: "Plan B", RepoRoot: fixture.repoRoot, Branch: planBBranch, SourceTip: planBHead, ReviewBase: base, ReviewHead: planBHead, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: base, CommitMessage: testBatchCommitMessage("plan-b", planBHead)},
		},
	}
	integrated, err := (BatchIntegrator{Store: &recordingBatchTransitionStore{}, Service: NewService(fixture.repoRoot, nil)}).Integrate(context.Background(), state, integrationRoot, BatchIntegrateOptions{VerifyCommand: "true"})
	if err != nil {
		t.Fatal(err)
	}
	return fixture, integrated.State, integrationRoot
}

func batchIntegrateTestState(fixture realGitWorktree, sourceHead, defaultHead string) BatchState {
	candidate := BatchCandidate{PlanID: "plan-a", PlanTitle: "Batch candidate", Branch: fixture.planBranch, SourceTip: sourceHead, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead, CommitMessage: testBatchCommitMessage("plan-a", sourceHead)}
	return BatchState{Schema: BatchStateSchema, ID: "batch-test", Status: BatchStatusPlanned, RepoRoot: fixture.repoRoot, DefaultBranch: fixture.defaultBranch, DefaultStartSHA: defaultHead, Candidates: []BatchCandidate{candidate}, ChosenOrder: []string{"plan-a"}}
}

func interruptedBatchIntegrateTestState(fixture realGitWorktree, sourceHead, defaultHead string) BatchState {
	state := batchIntegrateTestState(fixture, sourceHead, defaultHead)
	state.Status = BatchStatusIntegrating
	state.IntegrationHead = defaultHead
	state.Integrations = []BatchIntegration{{PlanID: "plan-a", SourceHead: sourceHead, IntegrationBaseSHA: defaultHead, Status: batchIntegrationApplying, Attempts: 1}}
	return state
}

func testBatchCommitMessage(planID, sourceHead string) string {
	return "feat(batch): integrate reviewed candidate\n\nWhat:\nIntegrate the exact approved candidate changes.\n\nWhy:\nPreserve reviewed intent without another proposal session.\n\nTao-Plan: " + planID + "\nTao-Source-Head: " + sourceHead
}

func writeBatchTestFile(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

var _ GitClient = gitops.Client{}
