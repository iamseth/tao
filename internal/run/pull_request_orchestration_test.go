package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/forge"
	"github.com/iamseth/tao/internal/plan"
)

func TestPullRequestOrchestrationRepairsWrongTypeAndRecordsExactHead(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	dirtyPath := filepath.Join(fixture.worktreeRoot, "dirty-before-correction.txt")
	if err := os.WriteFile(dirtyPath, []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 31, 14, 0, 0, 0, time.UTC)
	correctionCalls := 0
	reviewer := struct {
		ReviewCreator
		AgentSessionExecutor
	}{
		ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			t.Fatal("proposal correction must not rerun the substantive review")
			return plan.PlanReview{}, nil
		}),
		AgentSessionExecutor: agentSessionExecutorFunc(func(_ context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
			correctionCalls++
			if !request.CaptureOutput || !strings.Contains(request.Prompt, "COMMIT PROPOSAL CORRECTION mode") {
				t.Fatalf("unexpected proposal correction request: %#v", request)
			}
			return AgentSessionResult{Output: "```tao-review-proposal-json\n{\"commit_message\":{\"subject\":\"fix(pr): recover exact finalization\",\"body\":\"What:\\nRecover the exact approved pull request head.\\n\\nWhy:\\nComplete the interrupted handoff without rerunning implementation.\"}}\n```"}, nil
		}),
	}

	forgeCalls := []string{}
	boundary := pullRequestsFake{
		find: func(_ context.Context, request forge.FindRequest) (forge.PullRequest, forge.Metadata, bool, error) {
			forgeCalls = append(forgeCalls, "find")
			if request.RepoRoot != fixture.worktreeRoot || request.BaseBranch != "main" || request.Branch != fixture.branch {
				t.Fatalf("unexpected existing-PR discovery: %#v", request)
			}
			if got := strings.TrimSpace(runCommitTestGitOutput(t, fixture.originRoot, "rev-parse", "refs/heads/"+fixture.branch)); got != fixture.head {
				t.Fatalf("forge discovery ran before exact-head push: remote=%q want=%q", got, fixture.head)
			}
			return forge.PullRequest{}, forge.Metadata{}, false, nil
		},
		create: func(_ context.Context, request forge.CreateRequest) forge.CreationOutcome {
			forgeCalls = append(forgeCalls, "create")
			if request.Title != "fix(pr): recover exact finalization" || request.Branch != fixture.branch || request.BaseBranch != "main" || request.Label != "fix" {
				t.Fatalf("unexpected pull request creation: %#v", request)
			}
			body, err := os.ReadFile(request.BodyFile)
			if err != nil {
				t.Fatal(err)
			}
			requireNativePullRequestBody(t, string(body), "change.txt | 1 +")
			return forge.CreationOutcome{PullRequest: forge.PullRequest{Number: 404, URL: "https://github.com/iamseth/tao/pull/404", CreatedAt: createdAt}}
		},
		view: func(context.Context, forge.ViewRequest) (forge.PullRequest, forge.Metadata, bool, error) {
			t.Fatal("new pull request must not use identity recovery")
			return forge.PullRequest{}, forge.Metadata{}, false, nil
		},
		ensureMetadata: func(context.Context, forge.MetadataRequest) error {
			t.Fatal("successful creation must not use metadata repair")
			return nil
		},
	}

	gitCalls := []string{}
	runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name != "git" {
			return fmt.Errorf("unexpected external command %q", name)
		}
		gitCalls = append(gitCalls, strings.Join(args, " "))
		return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
	}
	repo := plan.NewFileRepository(fixture.plansRoot)
	detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	creator := deterministicPullRequestCreator{
		execution:    testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, PlanRecordFactory: fileReviewRecordFactory(repo)}),
		pullRequests: boundary,
	}
	var out bytes.Buffer
	options := Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, PullRequest: true, ReviewEnabled: true}},
		RunDependencies: RunDependencies{CommandRunner: runner, PlanRecordFactory: fileReviewRecordFactory(repo), PullRequestCreator: creator, ReviewCreator: reviewer},
	}
	err = executeDetail(context.Background(), detail, nil, &out, options)
	if err == nil || !strings.Contains(err.Error(), "worktree is dirty before proposal repair") {
		t.Fatalf("dirty pre-correction finalization error = %v", err)
	}
	if correctionCalls != 0 || len(forgeCalls) != 0 || containsPullRequestPush(gitCalls, fixture.branch) {
		t.Fatalf("dirty attempt correction/forge/git calls = %d/%#v/%#v", correctionCalls, forgeCalls, gitCalls)
	}
	dirtyDetail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	dirtyFailure := dirtyDetail.State.Plan.FinalizationFailure
	if dirtyFailure == nil || dirtyFailure.Phase != plan.FinalizationFailurePhasePullRequest || dirtyFailure.Category != "workspace_dirty" || dirtyFailure.Branch != fixture.branch || dirtyFailure.HeadSHA != fixture.head {
		t.Fatalf("dirty pre-correction failure = %#v", dirtyFailure)
	}
	if err := os.Remove(dirtyPath); err != nil {
		t.Fatal(err)
	}
	detail, err = repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := executeDetail(context.Background(), detail, nil, &out, options); err != nil {
		t.Fatal(err)
	}
	if correctionCalls != 1 {
		t.Fatalf("proposal correction calls = %d, want 1", correctionCalls)
	}
	if strings.Join(forgeCalls, ",") != "find,create" {
		t.Fatalf("forge calls = %#v, want deterministic find/create", forgeCalls)
	}
	if !containsPullRequestPush(gitCalls, fixture.branch) {
		t.Fatalf("missing exact-head branch push in git calls: %#v", gitCalls)
	}

	reloaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	review := reloaded.State.Plan.Review
	if review == nil || review.Base != fixture.base || review.Head != fixture.head || review.CommitMessage == nil || review.CommitMessage.Subject != "fix(pr): recover exact finalization" {
		t.Fatalf("corrected exact-head review = %#v", review)
	}
	pr := reloaded.State.Plan.PullRequest
	if pr == nil || pr.Number != 404 || pr.Branch != fixture.branch || pr.HeadSHA != fixture.head || reloaded.State.Plan.PullRequestIntent != nil {
		t.Fatalf("durable pull request state = pr:%#v intent:%#v", pr, reloaded.State.Plan.PullRequestIntent)
	}
	if reloaded.State.Status != plan.StatusCompleted || !plan.PlanIsPullRequestComplete(reloaded) {
		t.Fatalf("pull request lifecycle status = %q, want completed", reloaded.State.Status)
	}
	next := plan.Derive(reloaded, time.Time{}).NextAction
	if next.Primary.Kind != plan.PlanActionNone || next.Primary.Class != plan.PlanActionClassTerminal || !strings.Contains(next.Primary.Reason, "remote integration is not asserted") {
		t.Fatalf("completed pull request next action = %#v", next)
	}
	if !strings.Contains(out.String(), "Plan complete in Tao: plan-a") || !strings.Contains(out.String(), "Next: use the host's Squash and merge action") {
		t.Fatalf("missing completion guidance: %q", out.String())
	}
	requireOwnedEvent(t, reloaded.Events, plan.EventTypePlanReviewed, func(event plan.Event) bool {
		return event.Review != nil && event.Review.Head == fixture.head && event.Review.CommitMessage != nil && event.Review.CommitMessage.Subject == "fix(pr): recover exact finalization"
	})
	requireOwnedEvent(t, reloaded.Events, plan.EventTypePullRequestCreated, func(event plan.Event) bool {
		return event.PullRequest != nil && event.PullRequest.Number == 404 && event.PullRequest.HeadSHA == fixture.head
	})
	requireOwnedEvent(t, reloaded.Events, plan.EventTypeFinalizationFailed, func(event plan.Event) bool {
		failure := event.FinalizationFailure
		return failure != nil && failure.Phase == plan.FinalizationFailurePhaseProposalRepair && failure.Category == "proposal_correction_started" && failure.ReviewBase == fixture.base && failure.ReviewHead == fixture.head
	})
	requireOwnedEvent(t, reloaded.Events, plan.EventTypeFinalizationFailureCleared, func(event plan.Event) bool {
		failure := event.FinalizationFailure
		return failure != nil && failure.Phase == plan.FinalizationFailurePhasePullRequest && failure.Category == "workspace_dirty" && failure.Branch == fixture.branch && failure.HeadSHA == fixture.head
	})
	requireOwnedEvent(t, reloaded.Events, plan.EventTypeFinalizationFailureCleared, func(event plan.Event) bool {
		failure := event.FinalizationFailure
		return failure != nil && failure.Phase == plan.FinalizationFailurePhaseProposalRepair && failure.Category == "proposal_correction_started" && failure.ReviewBase == fixture.base && failure.ReviewHead == fixture.head
	})
}

func TestFinalizerConsumesProposalCorrectionBeforeInterruptedSessionCanReturn(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	repo := plan.NewFileRepository(fixture.plansRoot)
	detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}

	interruption := errors.New("simulated process interruption")
	correctionCalls := 0
	reviewer := struct {
		ReviewCreator
		AgentSessionExecutor
	}{
		ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			t.Fatal("proposal correction must not rerun the substantive review")
			return plan.PlanReview{}, nil
		}),
		AgentSessionExecutor: agentSessionExecutorFunc(func(context.Context, AgentSessionRequest) (AgentSessionResult, error) {
			correctionCalls++
			reloaded, loadErr := repo.ResolvePlan(context.Background(), fixture.planDir)
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			failure := reloaded.State.Plan.FinalizationFailure
			if failure == nil || failure.Phase != plan.FinalizationFailurePhaseProposalRepair || failure.Category != "proposal_correction_started" || failure.ReviewBase != fixture.base || failure.ReviewHead != fixture.head {
				t.Fatalf("durable consumed attempt at session launch = %#v", failure)
			}
			panic(interruption)
		}),
	}
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: defaultCommandRunner, PlanRecordFactory: fileReviewRecordFactory(repo), ReviewCreator: reviewer,
	}))

	func() {
		defer func() {
			recovered := recover()
			recoveredErr, ok := recovered.(error)
			if !ok || !errors.Is(recoveredErr, interruption) {
				t.Fatalf("recovered interruption = %#v, want sentinel", recovered)
			}
		}()
		_ = finalizer.ensureApprovedReviewProposal(context.Background(), detail, fixture.worktreeRoot, fixture.branch, fixture.head)
	}()

	reloaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := finalizer.ensureApprovedReviewProposal(context.Background(), reloaded, fixture.worktreeRoot, fixture.branch, fixture.head); err == nil || !strings.Contains(err.Error(), "already attempted") {
		t.Fatalf("interrupted correction retry error = %v", err)
	}
	if correctionCalls != 1 {
		t.Fatalf("proposal correction calls = %d, want exactly 1", correctionCalls)
	}
}

func TestFinalizerErroringCorrectionDirtyWorktreeCannotStartSecondSession(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	repo := plan.NewFileRepository(fixture.plansRoot)
	detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}

	correctionCalls := 0
	reviewer := struct {
		ReviewCreator
		AgentSessionExecutor
	}{
		ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			t.Fatal("proposal correction must not rerun the substantive review")
			return plan.PlanReview{}, nil
		}),
		AgentSessionExecutor: agentSessionExecutorFunc(func(context.Context, AgentSessionRequest) (AgentSessionResult, error) {
			correctionCalls++
			if err := os.WriteFile(filepath.Join(fixture.worktreeRoot, "dirty-after-correction.txt"), []byte("dirty\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return AgentSessionResult{}, errors.New("proposal correction session failed")
		}),
	}
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: defaultCommandRunner, PlanRecordFactory: fileReviewRecordFactory(repo), ReviewCreator: reviewer,
	}))

	if err := finalizer.ensureApprovedReviewProposal(context.Background(), detail, fixture.worktreeRoot, fixture.branch, fixture.head); err == nil || !strings.Contains(err.Error(), "worktree dirty") {
		t.Fatalf("dirty correction error = %v", err)
	}
	reloaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	failure := reloaded.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhaseProposalRepair || failure.Category != "workspace_dirty" || failure.ReviewBase != fixture.base || failure.ReviewHead != fixture.head || failure.RecoveryAction != plan.FinalizationRecoveryRestoreBoundary {
		t.Fatalf("dirty correction failure = %#v", failure)
	}
	if action := plan.DeriveNextAction(reloaded).Primary; action.Command != "" || !strings.Contains(action.Instruction, "clean plan worktree") {
		t.Fatalf("dirty correction next action = %#v", action)
	}
	if review := reloaded.State.Plan.Review; review == nil || review.CommitMessage == nil || review.CommitMessage.Subject != "feat(pr): propose the wrong type" {
		t.Fatalf("dirty correction replaced durable review: %#v", review)
	}
	if err := finalizer.ensureApprovedReviewProposal(context.Background(), reloaded, fixture.worktreeRoot, fixture.branch, fixture.head); err == nil || !strings.Contains(err.Error(), "already attempted") || !strings.Contains(err.Error(), "restore a clean worktree") || strings.Contains(err.Error(), "tao review --run") {
		t.Fatalf("dirty correction retry error = %v", err)
	}
	if correctionCalls != 1 {
		t.Fatalf("proposal correction calls = %d, want exactly 1", correctionCalls)
	}
}

func TestPullRequestOrchestrationRejectsHeadAdvancedByInvalidProposalCorrection(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	repo := plan.NewFileRepository(fixture.plansRoot)
	detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}

	correctionCalls := 0
	reviewer := struct {
		ReviewCreator
		AgentSessionExecutor
	}{
		ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			t.Fatal("proposal correction must not rerun the substantive review")
			return plan.PlanReview{}, nil
		}),
		AgentSessionExecutor: agentSessionExecutorFunc(func(_ context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
			correctionCalls++
			if request.RepoRoot != fixture.worktreeRoot {
				t.Fatalf("correction root = %q, want %q", request.RepoRoot, fixture.worktreeRoot)
			}
			if err := os.WriteFile(filepath.Join(fixture.worktreeRoot, "unreviewed.txt"), []byte("unreviewed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runCommitTestGitCommand(t, fixture.worktreeRoot, "add", "unreviewed.txt")
			runCommitTestGitCommand(t, fixture.worktreeRoot, "commit", "-m", "test: advance correction head")
			return AgentSessionResult{Output: "not a valid proposal"}, nil
		}),
	}
	creatorCalls := 0
	err = executeDetail(context.Background(), detail, nil, io.Discard, Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, PullRequest: true, ReviewEnabled: true}},
		RunDependencies: RunDependencies{
			CommandRunner:     defaultCommandRunner,
			PlanRecordFactory: fileReviewRecordFactory(repo),
			ReviewCreator:     reviewer,
			PullRequestCreator: pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
				creatorCalls++
				return plan.PullRequest{}, nil
			}),
		},
	})
	if err == nil || !strings.Contains(err.Error(), "proposal correction changed the reviewed worktree boundary") {
		t.Fatalf("head drift error = %v", err)
	}
	if correctionCalls != 1 || creatorCalls != 0 {
		t.Fatalf("correction/creator calls = %d/%d, want 1/0", correctionCalls, creatorCalls)
	}

	reloaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	review := reloaded.State.Plan.Review
	if review == nil || review.Head != fixture.head || review.CommitMessage == nil || review.CommitMessage.Subject != "feat(pr): propose the wrong type" {
		t.Fatalf("head drift persisted an unreviewed correction: %#v", review)
	}
	failure := reloaded.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhaseProposalRepair || failure.Category != "head_drift" || failure.ReviewBase != fixture.base || failure.ReviewHead != fixture.head || failure.RecoveryAction != plan.FinalizationRecoveryRestoreBoundary {
		t.Fatalf("head drift failure = %#v", failure)
	}
	if action := plan.DeriveNextAction(reloaded).Primary; action.Command != "" || !strings.Contains(action.Instruction, "recorded branch and HEAD") {
		t.Fatalf("head drift next action = %#v", action)
	}
	requireOwnedEvent(t, reloaded.Events, plan.EventTypeFinalizationFailed, func(event plan.Event) bool {
		failure := event.FinalizationFailure
		return failure != nil && failure.Phase == plan.FinalizationFailurePhaseProposalRepair && failure.Category == "head_drift" && failure.ReviewBase == fixture.base && failure.ReviewHead == fixture.head
	})
}

func TestPullRequestFullRunRejectsHeadAdvancedByFreshReview(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	repo := plan.NewFileRepository(fixture.plansRoot)
	detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}

	reviewCalls := 0
	var advancedHead string
	reviewer := reviewCreatorFunc(func(_ context.Context, run ReviewRun) (plan.PlanReview, error) {
		reviewCalls++
		if err := os.WriteFile(filepath.Join(fixture.worktreeRoot, "unreviewed.txt"), []byte("unreviewed\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		runCommitTestGitCommand(t, fixture.worktreeRoot, "add", "unreviewed.txt")
		runCommitTestGitCommand(t, fixture.worktreeRoot, "commit", "-m", "test: advance fresh review head")
		advancedHead = strings.TrimSpace(runCommitTestGitOutput(t, fixture.worktreeRoot, "rev-parse", "HEAD"))
		review := plan.PlanReview{
			Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove,
			Base: fixture.base, Head: advancedHead, Agent: "pi",
			ReviewedAt: time.Date(2026, 8, 31, 14, 30, 0, 0, time.UTC),
			CommitMessage: &plan.ReviewCommitMessage{
				Subject: "fix(pr): recover exact finalization",
				Body:    "What:\nRecover the exact approved pull request head.\n\nWhy:\nComplete the pull request handoff safely.",
			},
		}
		record, recordErr := repo.PlanRecord(run.Detail)
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		if recordErr := record.RecordReviewCompleted(review, "pi"); recordErr != nil {
			t.Fatal(recordErr)
		}
		return review, nil
	})
	creatorCalls := 0
	gitCalls := []string{}
	runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name == "git" {
			gitCalls = append(gitCalls, strings.Join(args, " "))
		}
		return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
	}
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
		Agent: AgentPi, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, PullRequest: true, ReviewEnabled: true,
	}}, RunDependencies{
		CommandRunner: runner, PlanRecordFactory: fileReviewRecordFactory(repo), ReviewCreator: reviewer,
		RootResolver: ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) {
			return fixture.worktreeRoot, nil
		}),
		PullRequestCreator: pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
			creatorCalls++
			return plan.PullRequest{}, nil
		}),
	}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
	if !complete || err == nil || !strings.Contains(err.Error(), "recorded workspace branch") || !strings.Contains(err.Error(), "does not match live branch") {
		t.Fatalf("fresh-review head drift = complete %t, error %v", complete, err)
	}
	if reviewCalls != 1 || creatorCalls != 0 {
		t.Fatalf("review/creator calls = %d/%d, want 1/0", reviewCalls, creatorCalls)
	}
	if advancedHead == "" || advancedHead == fixture.head {
		t.Fatalf("fresh review head = %q, want an advance from %q", advancedHead, fixture.head)
	}
	if containsPullRequestPush(gitCalls, fixture.branch) {
		t.Fatalf("fresh-review drift pushed the branch: %#v", gitCalls)
	}

	reloaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Workspace.HeadSHA != fixture.head {
		t.Fatalf("durable workspace head = %q, want %q", reloaded.State.Workspace.HeadSHA, fixture.head)
	}
	failure := reloaded.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhasePullRequest || failure.Category != "head_drift" || failure.Branch != fixture.branch || failure.HeadSHA != fixture.head {
		t.Fatalf("fresh-review drift failure = %#v", failure)
	}
	if recovery := plan.CurrentFinalizationRecovery(reloaded); recovery == nil || recovery.Category != "head_drift" || recovery.RecoveryAction != plan.FinalizationRecoveryRestoreBoundary {
		t.Fatalf("fresh-review drift recovery = %#v", recovery)
	}
	action := plan.DeriveNextAction(reloaded).Primary
	if action.Command != "" || !strings.Contains(action.Instruction, "recorded branch and HEAD") {
		t.Fatalf("fresh-review drift action = %#v", action)
	}
	review := plan.CurrentReview(reloaded)
	if review == nil || review.Head != advancedHead || !review.IsApproved() {
		t.Fatalf("fresh review evidence = %#v", review)
	}
	requireOwnedEvent(t, reloaded.Events, plan.EventTypeFinalizationFailed, func(event plan.Event) bool {
		failure := event.FinalizationFailure
		return failure != nil && failure.Category == "head_drift" && failure.Branch == fixture.branch && failure.HeadSHA == fixture.head
	})
}

func TestPullRequestFullRunRejectsDirtyExactHeadAfterFreshReview(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	repo := plan.NewFileRepository(fixture.plansRoot)
	detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}

	reviewCalls := 0
	reviewer := reviewCreatorFunc(func(_ context.Context, run ReviewRun) (plan.PlanReview, error) {
		reviewCalls++
		if err := os.WriteFile(filepath.Join(fixture.worktreeRoot, "unreviewed-local-change.txt"), []byte("dirty\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		review := plan.PlanReview{
			Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove,
			Base: fixture.base, Head: fixture.head, Agent: "pi",
			ReviewedAt: time.Date(2026, 8, 31, 14, 45, 0, 0, time.UTC),
			CommitMessage: &plan.ReviewCommitMessage{
				Subject: "fix(pr): recover exact finalization",
				Body:    "What:\nRecover the exact approved pull request head.\n\nWhy:\nComplete the pull request handoff safely.",
			},
		}
		record, recordErr := repo.PlanRecord(run.Detail)
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		if recordErr := record.RecordReviewCompleted(review, "pi"); recordErr != nil {
			t.Fatal(recordErr)
		}
		return review, nil
	})
	creatorCalls := 0
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
		Agent: AgentPi, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, PullRequest: true, ReviewEnabled: true,
	}}, RunDependencies{
		CommandRunner: defaultCommandRunner, PlanRecordFactory: fileReviewRecordFactory(repo), ReviewCreator: reviewer,
		RootResolver: ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) {
			return fixture.worktreeRoot, nil
		}),
		PullRequestCreator: pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
			creatorCalls++
			return plan.PullRequest{}, nil
		}),
	}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
	if !complete || err == nil || !strings.Contains(err.Error(), "worktree is dirty before pull request creation") {
		t.Fatalf("dirty fresh-review finalization = complete %t, error %v", complete, err)
	}
	if reviewCalls != 1 || creatorCalls != 0 {
		t.Fatalf("review/creator calls = %d/%d, want 1/0", reviewCalls, creatorCalls)
	}
	reloaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	failure := reloaded.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhasePullRequest || failure.Category != "workspace_dirty" || failure.Branch != fixture.branch || failure.HeadSHA != fixture.head || failure.RecoveryAction != plan.FinalizationRecoveryRestoreBoundary {
		t.Fatalf("dirty fresh-review failure = %#v", failure)
	}
	if action := plan.DeriveNextAction(reloaded).Primary; action.Command != "" || !strings.Contains(action.Instruction, "clean plan worktree") {
		t.Fatalf("dirty fresh-review next action = %#v", action)
	}
	intent := reloaded.State.Plan.PullRequestIntent
	if intent == nil || intent.Branch != fixture.branch || intent.HeadSHA != fixture.head {
		t.Fatalf("dirty fresh-review intent = %#v, want exact branch/head intent", intent)
	}
}

func TestPullRequestResumeRejectsDirtyExactHeadBeforeProposalRepair(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	if err := os.WriteFile(filepath.Join(fixture.worktreeRoot, "unreviewed-resume-change.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := plan.NewFileRepository(fixture.plansRoot)
	detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}

	correctionCalls := 0
	reviewer := struct {
		ReviewCreator
		AgentSessionExecutor
	}{
		ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			t.Fatal("resumed finalization must not rerun substantive review")
			return plan.PlanReview{}, nil
		}),
		AgentSessionExecutor: agentSessionExecutorFunc(func(context.Context, AgentSessionRequest) (AgentSessionResult, error) {
			correctionCalls++
			return AgentSessionResult{}, nil
		}),
	}
	creatorCalls := 0
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
		Agent: AgentPi, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, PullRequest: true, ReviewEnabled: true,
	}}, RunDependencies{
		CommandRunner: defaultCommandRunner, PlanRecordFactory: fileReviewRecordFactory(repo), ReviewCreator: reviewer,
		PullRequestCreator: pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
			creatorCalls++
			return plan.PullRequest{}, nil
		}),
	}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 0, detail, plan.RunCapabilities{Complete: true})
	if !complete || err == nil || !strings.Contains(err.Error(), "worktree is dirty before proposal repair") {
		t.Fatalf("dirty resumed finalization = complete %t, error %v", complete, err)
	}
	if correctionCalls != 0 || creatorCalls != 0 {
		t.Fatalf("correction/creator calls = %d/%d, want 0/0", correctionCalls, creatorCalls)
	}
	reloaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	failure := reloaded.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhasePullRequest || failure.Category != "workspace_dirty" || failure.Branch != fixture.branch || failure.HeadSHA != fixture.head || failure.RecoveryAction != plan.FinalizationRecoveryRestoreBoundary {
		t.Fatalf("dirty resumed failure = %#v", failure)
	}
	if action := plan.DeriveNextAction(reloaded).Primary; action.Command != "" || !strings.Contains(action.Instruction, "clean plan worktree") {
		t.Fatalf("dirty resumed next action = %#v", action)
	}
	if reloaded.State.Plan.PullRequestIntent != nil || reloaded.State.Plan.PullRequest != nil {
		t.Fatalf("dirty resumed finalization changed pull-request state: intent=%#v pull_request=%#v", reloaded.State.Plan.PullRequestIntent, reloaded.State.Plan.PullRequest)
	}
}

func TestPullRequestOrchestrationRecordsCorrectionExhaustionBeforeRemoteMutation(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	repo := plan.NewFileRepository(fixture.plansRoot)
	loaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	correctionCalls := 0
	reviewer := struct {
		ReviewCreator
		AgentSessionExecutor
	}{
		ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			t.Fatal("substantive review must not rerun")
			return plan.PlanReview{}, nil
		}),
		AgentSessionExecutor: agentSessionExecutorFunc(func(context.Context, AgentSessionRequest) (AgentSessionResult, error) {
			correctionCalls++
			return AgentSessionResult{Output: "```tao-review-proposal-json\n{\"commit_message\":{\"subject\":\"feat(pr): still wrong\",\"body\":\"What:\\nKeep the wrong type.\\n\\nWhy:\\nExercise correction exhaustion.\"}}\n```"}, nil
		}),
	}
	remoteCalls := 0
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: defaultCommandRunner, PlanRecordFactory: fileReviewRecordFactory(repo), ReviewCreator: reviewer,
		PullRequestCreator: pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
			remoteCalls++
			return plan.PullRequest{}, nil
		}),
		Now: func() time.Time { return time.Date(2026, 8, 31, 15, 1, 0, 0, time.UTC) },
	}))
	if err := finalizer.ensureApprovedReviewProposal(context.Background(), loaded, fixture.worktreeRoot, fixture.branch, fixture.head); err == nil || !strings.Contains(err.Error(), "valid typed commit proposal") {
		t.Fatalf("correction exhaustion error = %v", err)
	}
	if correctionCalls != 1 || remoteCalls != 0 {
		t.Fatalf("correction/remote calls = %d/%d, want 1/0", correctionCalls, remoteCalls)
	}
	reloaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	failure := reloaded.State.Plan.FinalizationFailure
	if failure == nil || failure.RecoveryAction != "rerun_review" {
		t.Fatalf("correction exhaustion recovery = %#v", failure)
	}
	if err := finalizer.ensureApprovedReviewProposal(context.Background(), reloaded, fixture.worktreeRoot, fixture.branch, fixture.head); err == nil || !strings.Contains(err.Error(), "already attempted") || !strings.Contains(err.Error(), "tao review --run plan-a") {
		t.Fatalf("repeated correction error = %v", err)
	}
	if correctionCalls != 1 || remoteCalls != 0 {
		t.Fatalf("repeated correction/remote calls = %d/%d, want 1/0", correctionCalls, remoteCalls)
	}
	requireOwnedEvent(t, reloaded.Events, plan.EventTypeFinalizationFailed, func(event plan.Event) bool {
		failure := event.FinalizationFailure
		return failure != nil && failure.Phase == plan.FinalizationFailurePhaseProposalRepair && failure.Category == "proposal_invalid" && failure.ReviewBase == fixture.base && failure.ReviewHead == fixture.head && failure.RecoveryAction == "rerun_review"
	})
}

func TestPullRequestOrchestrationPersistsPartialIdentityBeforeForgeRepair(t *testing.T) {
	createdAt := time.Date(2026, 8, 14, 2, 10, 0, 0, time.UTC)
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	operationErr := errors.New("assignment failed")
	var repaired bool
	boundary := pullRequestsFake{
		find: func(context.Context, forge.FindRequest) (forge.PullRequest, forge.Metadata, bool, error) {
			return forge.PullRequest{}, forge.Metadata{}, false, nil
		},
		create: func(context.Context, forge.CreateRequest) forge.CreationOutcome {
			return forge.CreationOutcome{PullRequest: forge.PullRequest{Number: 323, URL: "https://github.com/iamseth/tao/pull/323", CreatedAt: createdAt}, Stdout: "created https://github.com/iamseth/tao/pull/323\n", OperationErr: operationErr}
		},
		view: func(_ context.Context, request forge.ViewRequest) (forge.PullRequest, forge.Metadata, bool, error) {
			intent := detail.State.Plan.PullRequestIntent
			if intent == nil || intent.Number != request.Number || intent.Branch != "feature/plan-a" || intent.HeadSHA != "head123" {
				t.Fatalf("forge view happened before durable intent: %#v", intent)
			}
			return forge.PullRequest{Number: intent.Number, URL: intent.URL, CreatedAt: createdAt.Add(time.Hour)}, forge.Metadata{}, true, nil
		},
		ensureMetadata: func(_ context.Context, request forge.MetadataRequest) error {
			repaired = true
			if request.PullRequest.Number != 323 || request.Label != "feature" {
				t.Fatalf("unexpected metadata repair: %#v", request)
			}
			return nil
		},
	}
	creator := deterministicPullRequestCreator{
		execution:    testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: pullRequestCommandRunner(t, nil, noGHCommands(t)), PlanRecordFactory: memoryPlanRecordFactory}),
		pullRequests: boundary,
	}
	run := PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"}

	pr, err := creator.CreatePullRequest(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if !repaired || pr.Number != 323 || !pr.CreatedAt.Equal(createdAt) {
		t.Fatalf("partial creation was not durably repaired: repaired=%v pr=%#v", repaired, pr)
	}
}

func TestPullRequestOrchestrationDoesNotClaimFailedCreateWithoutIdentity(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	operationErr := errors.New("connection reset")
	boundary := pullRequestsFake{
		find: func(context.Context, forge.FindRequest) (forge.PullRequest, forge.Metadata, bool, error) {
			return forge.PullRequest{}, forge.Metadata{}, false, nil
		},
		create: func(context.Context, forge.CreateRequest) forge.CreationOutcome {
			return forge.CreationOutcome{Stdout: "request accepted\n", OperationErr: operationErr}
		},
		view: func(context.Context, forge.ViewRequest) (forge.PullRequest, forge.Metadata, bool, error) {
			t.Fatal("ambiguous failed creation must not trigger discovery by number")
			return forge.PullRequest{}, forge.Metadata{}, false, nil
		},
		ensureMetadata: func(context.Context, forge.MetadataRequest) error {
			t.Fatal("ambiguous failed creation must not trigger metadata repair")
			return nil
		},
	}
	creator := deterministicPullRequestCreator{
		execution:    testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: pullRequestCommandRunner(t, nil, noGHCommands(t)), PlanRecordFactory: memoryPlanRecordFactory}),
		pullRequests: boundary,
	}

	_, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"})
	if !errors.Is(err, operationErr) {
		t.Fatalf("expected operation error, got %v", err)
	}
	if detail.State.Plan.PullRequestIntent != nil {
		t.Fatalf("ambiguous failed creation persisted intent: %#v", detail.State.Plan.PullRequestIntent)
	}
}

func TestDefaultPullRequestCreatorWiresGitHubForge(t *testing.T) {
	creator, ok := defaultPullRequestCreatorWithBody(testRunExecution(ExecutionConfig{}, RunDependencies{}), nil).(deterministicPullRequestCreator)
	if !ok {
		t.Fatalf("unexpected pull request creator type")
	}
	if _, ok := creator.pullRequests.(forge.GitHub); !ok {
		t.Fatalf("expected GitHub forge, got %T", creator.pullRequests)
	}
}

type pullRequestsFake struct {
	find           func(context.Context, forge.FindRequest) (forge.PullRequest, forge.Metadata, bool, error)
	view           func(context.Context, forge.ViewRequest) (forge.PullRequest, forge.Metadata, bool, error)
	create         func(context.Context, forge.CreateRequest) forge.CreationOutcome
	ensureMetadata func(context.Context, forge.MetadataRequest) error
}

func (f pullRequestsFake) Find(ctx context.Context, request forge.FindRequest) (forge.PullRequest, forge.Metadata, bool, error) {
	return f.find(ctx, request)
}

func (f pullRequestsFake) View(ctx context.Context, request forge.ViewRequest) (forge.PullRequest, forge.Metadata, bool, error) {
	return f.view(ctx, request)
}

func (f pullRequestsFake) Create(ctx context.Context, request forge.CreateRequest) forge.CreationOutcome {
	return f.create(ctx, request)
}

func (f pullRequestsFake) EnsureMetadata(ctx context.Context, request forge.MetadataRequest) error {
	return f.ensureMetadata(ctx, request)
}

type pullRequestOrchestrationFixture struct {
	plansRoot    string
	planDir      string
	repoRoot     string
	worktreeRoot string
	originRoot   string
	branch       string
	base         string
	head         string
}

func newPullRequestOrchestrationFixture(t *testing.T) pullRequestOrchestrationFixture {
	t.Helper()
	root := t.TempDir()
	repoRoot := filepath.Join(root, "repo")
	originRoot := filepath.Join(root, "origin.git")
	worktreeRoot := filepath.Join(root, "worktree")
	plansRoot := filepath.Join(root, "plans")
	planDir := filepath.Join(plansRoot, "plan-a")
	for _, dir := range []string{repoRoot, originRoot, planDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runCommitTestGitCommand(t, repoRoot, "init")
	runCommitTestGitCommand(t, repoRoot, "config", "user.email", "tao@example.com")
	runCommitTestGitCommand(t, repoRoot, "config", "user.name", "Tao Test")
	runCommitTestGitCommand(t, repoRoot, "checkout", "-b", "main")
	if err := os.WriteFile(filepath.Join(repoRoot, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCommitTestGitCommand(t, repoRoot, "add", "README.md")
	runCommitTestGitCommand(t, repoRoot, "commit", "-m", "base")
	base := strings.TrimSpace(runCommitTestGitOutput(t, repoRoot, "rev-parse", "HEAD"))
	runCommitTestGitCommand(t, originRoot, "init", "--bare")
	runCommitTestGitCommand(t, repoRoot, "remote", "add", "origin", originRoot)
	runCommitTestGitCommand(t, repoRoot, "push", "--set-upstream", "origin", "main")
	const branch = "fix/pr-finalization-recovery"
	runCommitTestGitCommand(t, repoRoot, "worktree", "add", "-b", branch, worktreeRoot, "main")
	runCommitTestGitCommand(t, worktreeRoot, "config", "user.email", "tao@example.com")
	runCommitTestGitCommand(t, worktreeRoot, "config", "user.name", "Tao Test")
	if err := os.WriteFile(filepath.Join(worktreeRoot, "change.txt"), []byte("recover\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCommitTestGitCommand(t, worktreeRoot, "add", "change.txt")
	runCommitTestGitCommand(t, worktreeRoot, "commit", "-m", "fix: recover finalization")
	head := strings.TrimSpace(runCommitTestGitOutput(t, worktreeRoot, "rev-parse", "HEAD"))

	detail := approvedPullRequestDetail(plan.ChangeTypeFix, head)
	detail.Dir = planDir
	detail.State.Schema = "tao.plan.state.v1"
	detail.State.Repo = plan.Repo{Root: repoRoot, Branch: "main", BaseCommit: base}
	detail.State.Workspace = &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(worktreeRoot), Path: worktreeRoot,
		Branch: branch, HeadSHA: head, LifecycleStatus: plan.WorkspaceStatusReady, CleanupStatus: plan.WorkspaceCleanupStatusPending,
	}
	detail.State.Plan.LastRunCommitPolicy = CommitPolicySlice.String()
	detail.State.Plan.Review.Base = base
	detail.State.Plan.Review.ReviewedAt = time.Date(2026, 8, 31, 13, 0, 0, 0, time.UTC)
	detail.State.Plan.Review.CommitMessage = &plan.ReviewCommitMessage{
		Subject: "feat(pr): propose the wrong type",
		Body:    "What:\nPropose a mismatched type.\n\nWhy:\nExercise exact-head correction before publication.",
	}
	detail.Slices.Schema = "tao.plan.slices.v1"
	detail.Slices.PlanID = "plan-a"
	persistRunArtifacts(t, planDir, detail)
	return pullRequestOrchestrationFixture{plansRoot: plansRoot, planDir: planDir, repoRoot: repoRoot, worktreeRoot: worktreeRoot, originRoot: originRoot, branch: branch, base: base, head: head}
}

func containsPullRequestPush(calls []string, branch string) bool {
	want := "push --set-upstream --force-with-lease=refs/heads/" + branch + ": origin " + branch + ":refs/heads/" + branch
	for _, call := range calls {
		if strings.Contains(call, want) {
			return true
		}
	}
	return false
}

func requireOwnedEvent(t *testing.T, events []plan.Event, eventType string, owns func(plan.Event) bool) {
	t.Helper()
	for _, event := range events {
		if event.Type == eventType && owns(event) {
			return
		}
	}
	t.Fatalf("missing owned %s event in %#v", eventType, events)
}

func noGHCommands(t *testing.T) func([]string, io.Writer, io.Writer) error {
	t.Helper()
	return func(args []string, _, _ io.Writer) error {
		t.Fatalf("run orchestration executed gh directly: %s", strings.Join(args, " "))
		return nil
	}
}
