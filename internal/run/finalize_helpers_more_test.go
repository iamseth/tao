package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/forge"
	"github.com/iamseth/tao/internal/plan"
)

func TestFinalizerHelperDefaultsAndPathUtilities(t *testing.T) {
	var out bytes.Buffer
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies{OutputWriter: &out, CommandRunner: runGitFake(&[]string{}, nil)})
	finalizer := newFinalizer(nil, execution)
	if finalizer.outputWriter() != &out {
		t.Fatal("expected output writer fallback")
	}
	finalizer = newFinalizer(io.Discard, execution)
	if finalizer.outputWriter() != io.Discard {
		t.Fatal("expected explicit output writer")
	}
	if !suspectedSecretPath("config/credentials.json") || !suspectedSecretPath(".env.local") || !generatedPath("bin/tao") || !generatedPath("coverage.out") {
		t.Fatal("expected secret/generated path detection")
	}
	if suspectedSecretPath("internal/run/run.go") || generatedPath("internal/run/run.go") {
		t.Fatal("ordinary source file should be safe")
	}
	if planCommitGlobRegexp("[bad") != `^\[bad$` {
		t.Fatalf("unexpected malformed glob regexp %q", planCommitGlobRegexp("[bad"))
	}
}

func TestFinalizerStopsBeforeRemoteMutationWhenPlanRecordBindingFails(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFix, "head123")
	bindErr := errors.New("injected file-backed plan record binding failure")
	factoryCalls := 0
	creatorCalls := 0
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		PlanRecordFactory: func(*plan.PlanDetail) (PlanMutationRecord, error) {
			factoryCalls++
			return nil, bindErr
		},
		PullRequestCreator: pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
			creatorCalls++
			return plan.PullRequest{}, nil
		}),
	}))

	err := finalizer.createAndRecordPullRequestAtHead(context.Background(), detail, "/repo", "feature", "head123")
	if !errors.Is(err, bindErr) || !strings.Contains(err.Error(), "before pull request creation") {
		t.Fatalf("finalization error = %v, want pre-creation binding failure", err)
	}
	if factoryCalls != 1 || creatorCalls != 0 {
		t.Fatalf("factory/creator calls = %d/%d, want 1/0", factoryCalls, creatorCalls)
	}
	if detail.State.Plan.PullRequestIntent != nil || detail.State.Plan.PullRequest != nil || detail.State.Plan.FinalizationFailure != nil {
		t.Fatalf("binding failure changed finalization state: intent=%#v pull_request=%#v failure=%#v", detail.State.Plan.PullRequestIntent, detail.State.Plan.PullRequest, detail.State.Plan.FinalizationFailure)
	}
}

func TestFinalizerClassifiesPullRequestFailureRecovery(t *testing.T) {
	tests := []struct {
		category string
		want     string
	}{
		{category: "publication_failed", want: plan.FinalizationRecoveryResumePullRequest},
		{category: "workspace_mismatch", want: plan.FinalizationRecoveryRestoreBoundary},
		{category: "head_drift", want: plan.FinalizationRecoveryRestoreBoundary},
		{category: "review_head_mismatch", want: plan.FinalizationRecoveryRerunReview},
		{category: "intent_mismatch", want: plan.FinalizationRecoveryRepairIntent},
		{category: "identity_mismatch", want: plan.FinalizationRecoveryRepairIdentity},
	}
	for _, test := range tests {
		t.Run(test.category, func(t *testing.T) {
			detail := approvedPullRequestDetail(plan.ChangeTypeFix, "head123")
			finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
				PlanRecordFactory: memoryPlanRecordFactory,
				Now:               func() time.Time { return time.Date(2026, 8, 31, 17, 0, 0, 0, time.UTC) },
			}))

			err := errors.New("classified failure")
			if got := finalizer.failPullRequestFinalization(detail, "feature", "head123", test.category, err); !errors.Is(got, err) {
				t.Fatalf("failure error = %v, want %v", got, err)
			}
			failure := detail.State.Plan.FinalizationFailure
			if failure == nil || failure.Category != test.category || failure.RecoveryAction != test.want {
				t.Fatalf("classified failure = %#v, want action %q", failure, test.want)
			}
		})
	}
}

func TestPullRequestRecoveryBoundarySeparatesStructuralWorkspaceFailuresFromInspectionErrors(t *testing.T) {
	tests := []struct {
		name         string
		prepare      func(*testing.T) (string, string)
		cleanup      string
		runner       CommandRunner
		wantCategory string
		wantAction   string
	}{
		{
			name: "missing recorded worktree",
			prepare: func(t *testing.T) (string, string) {
				root := t.TempDir()
				return root, filepath.Join(root, "missing-worktree")
			},
			cleanup:      plan.WorkspaceCleanupStatusPending,
			wantCategory: "workspace_mismatch",
			wantAction:   plan.FinalizationRecoveryRestoreBoundary,
		},
		{
			name: "incompatible cleanup state",
			prepare: func(t *testing.T) (string, string) {
				root := t.TempDir()
				return root, filepath.Join(root, "worktree")
			},
			cleanup:      plan.WorkspaceCleanupStatusRunning,
			wantCategory: "workspace_mismatch",
			wantAction:   plan.FinalizationRecoveryRestoreBoundary,
		},
		{
			name: "mismatched linked worktree",
			prepare: func(t *testing.T) (string, string) {
				root := t.TempDir()
				worktree := t.TempDir()
				runCommitTestGitCommand(t, root, "init")
				runCommitTestGitCommand(t, worktree, "init")
				return root, worktree
			},
			cleanup:      plan.WorkspaceCleanupStatusPending,
			runner:       defaultCommandRunner,
			wantCategory: "workspace_mismatch",
			wantAction:   plan.FinalizationRecoveryRestoreBoundary,
		},
		{
			name: "retryable Git inspection error",
			prepare: func(t *testing.T) (string, string) {
				root := t.TempDir()
				worktree := t.TempDir()
				return root, worktree
			},
			cleanup: plan.WorkspaceCleanupStatusPending,
			runner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
				return errors.New("temporary Git inspection failure")
			},
			wantCategory: "workspace_preflight_failed",
			wantAction:   plan.FinalizationRecoveryResumePullRequest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			repoRoot, worktreeRoot := test.prepare(t)
			detail := approvedPullRequestDetail(plan.ChangeTypeFix, "head123")
			detail.State.Repo.Root = repoRoot
			detail.State.Workspace = &plan.Workspace{
				Strategy: plan.WorkspaceStrategyWorktree, Root: filepath.Dir(worktreeRoot), Path: worktreeRoot,
				Branch: "feature", HeadSHA: "head123", LifecycleStatus: plan.WorkspaceStatusReady, CleanupStatus: test.cleanup,
			}
			finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
				CommandRunner: test.runner, PlanRecordFactory: memoryPlanRecordFactory,
			}))

			if _, _, _, err := finalizer.pullRequestRecoveryBoundary(context.Background(), detail); err == nil {
				t.Fatal("recovery boundary unexpectedly succeeded")
			}
			failure := detail.State.Plan.FinalizationFailure
			if failure == nil || failure.Category != test.wantCategory || failure.RecoveryAction != test.wantAction {
				t.Fatalf("workspace failure = %#v, want category %q action %q", failure, test.wantCategory, test.wantAction)
			}
			action := plan.DeriveNextAction(detail).Primary
			if test.wantCategory == "workspace_mismatch" && (action.Command != "" || !strings.Contains(action.Instruction, "Repair or restore") || !strings.Contains(action.Instruction, "recorded path, branch, and HEAD")) {
				t.Fatalf("structural workspace next action = %#v", action)
			}
			if test.wantCategory == "workspace_preflight_failed" && action.Command != "tao run --pull-request plan-a" {
				t.Fatalf("retryable inspection next action = %#v", action)
			}
		})
	}
}

func TestFinalizerMissingFailureRecorderSurfacesWrappedError(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFix, "head123")
	record := struct{ PlanMutationRecord }{}
	localErr := errors.New("push rejected")

	err := (Finalizer{}).failPullRequestFinalizationWithRecord(record, detail, "feature", "head123", "publication_failed", localErr)
	if !errors.Is(err, localErr) {
		t.Fatalf("error = %v, want original finalization error", err)
	}
	want := "push rejected; record pull request finalization failure: plan mutation record does not implement FinalizationFailureRecorder"
	if err == nil || err.Error() != want {
		t.Fatalf("error = %v, want %q", err, want)
	}
}

func TestFinalizerReplacementFailurePreservesCurrentRecoveryEvidence(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFix, "head123")
	original := plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhasePullRequest, Category: "publication_failed", Branch: "feature", HeadSHA: "head123",
		FailedAt: time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC), RecoveryAction: plan.FinalizationRecoveryResumePullRequest,
	}
	detail.State.Plan.FinalizationFailure = &original
	baseRecord, err := memoryPlanRecordFactory(detail)
	if err != nil {
		t.Fatal(err)
	}
	record := &failingFinalizationReplacementRecord{PlanMutationRecord: baseRecord, detail: detail}
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		PlanRecordFactory: func(*plan.PlanDetail) (PlanMutationRecord, error) { return record, nil },
		Now:               func() time.Time { return original.FailedAt.Add(time.Hour) },
	}))

	localErr := errors.New("push still rejected")
	err = finalizer.failPullRequestFinalization(detail, "feature", "head123", "publication_failed", localErr)
	if !errors.Is(err, localErr) || !strings.Contains(err.Error(), "injected atomic replacement failure") {
		t.Fatalf("failure error = %v, want local and replacement errors", err)
	}
	if failure := detail.State.Plan.FinalizationFailure; failure == nil || *failure != original {
		t.Fatalf("failed replacement lost current recovery evidence: %#v", failure)
	}
	if record.replaceCalls != 1 || record.clearCalls != 0 || record.recordCalls != 0 {
		t.Fatalf("mutation calls: replace=%d clear=%d record=%d", record.replaceCalls, record.clearCalls, record.recordCalls)
	}
}

func TestFinalizerRunsReviewWhenEnabled(t *testing.T) {
	planDir := t.TempDir()
	detail := completedReviewPlanDetail(planDir)
	var out bytes.Buffer
	reviewer := &recordingReviewCreator{review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: "approve"}}
	finalizer := newFinalizer(&out, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, ReviewEnabled: true}}, RunDependencies{ReviewCreator: reviewer, RootResolver: staticRootResolver()}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected completed plan")
	}
	if reviewer.calls != 1 {
		t.Fatalf("expected one review call, got %d", reviewer.calls)
	}
	if got := reviewer.runs[0]; got.PlanID != "plan-a" || got.PlanDir == "" || got.RepoRoot != "/repo" || got.Base != "base123" {
		t.Fatalf("unexpected review run: %+v", got)
	}
	if detail.State.Plan.Review == nil || detail.State.Plan.Review.Verdict != "approve" {
		t.Fatalf("review result was not visible on detail: %+v", detail.State.Plan.Review)
	}
	if !strings.Contains(out.String(), "Review: approve (0 findings)") {
		t.Fatalf("expected review completion line, got:\n%s", out.String())
	}
}

func TestFinalizerSkipsReviewWhenDisabled(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	var out bytes.Buffer
	reviewer := &recordingReviewCreator{review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: "approve"}}
	finalizer := newFinalizer(&out, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, ReviewEnabled: false}}, RunDependencies{ReviewCreator: reviewer, RootResolver: staticRootResolver()}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected completed plan")
	}
	if reviewer.calls != 0 {
		t.Fatalf("expected review to be skipped, got %d call(s)", reviewer.calls)
	}
	if strings.Contains(out.String(), "Review:") {
		t.Fatalf("disabled review should preserve prior output, got:\n%s", out.String())
	}
}

func TestFinalizerStopsPullRequestRunAfterFreshReviewCorrectionExhaustion(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	review := plan.PlanReview{
		Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictComment,
		Summary: "The exact review approved the change but its proposal could not be repaired.",
		Base:    "base123", Head: "head123", ReviewedAt: time.Date(2026, 8, 31, 16, 0, 0, 0, time.UTC),
	}
	reviewCalls := 0
	reviewer := reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
		reviewCalls++
		return review, &reviewProposalRepairError{
			category: "proposal_invalid",
			cause:    errors.New("proposal-only correction did not return a valid typed commit proposal"),
		}
	})
	creatorCalls := 0
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
		CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, ReviewEnabled: true, PullRequest: true,
	}}, RunDependencies{
		CommandRunner: runGitFake(&[]string{}, nil), PlanRecordFactory: memoryPlanRecordFactory,
		PullRequestCreator: pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
			creatorCalls++
			return plan.PullRequest{}, nil
		}),
		ReviewCreator: reviewer, RootResolver: staticRootResolver(),
		Now: func() time.Time { return time.Date(2026, 8, 31, 16, 1, 0, 0, time.UTC) },
	}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
	if !complete || err == nil || !strings.Contains(err.Error(), "valid typed commit proposal") {
		t.Fatalf("full pull-request finalization = complete %t, error %v", complete, err)
	}
	if reviewCalls != 1 || creatorCalls != 0 {
		t.Fatalf("review/creator calls = %d/%d, want 1/0", reviewCalls, creatorCalls)
	}
	failure := detail.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhaseProposalRepair || failure.Category != "proposal_invalid" || failure.ReviewBase != "base123" || failure.ReviewHead != "head123" || failure.RecoveryAction != "rerun_review" {
		t.Fatalf("fresh review proposal failure = %#v", failure)
	}
	action := plan.DeriveNextAction(detail).Primary
	if action.Kind != plan.PlanActionReview || action.Command != "tao review --run plan-a" {
		t.Fatalf("fresh review recovery action = %+v", action)
	}
}

func TestFinalizerReportsPullRequestCompletionOnlyForMatchingApproval(t *testing.T) {
	validProposal := &plan.ReviewCommitMessage{
		Subject: "fix(pr): finalize approved pull request",
		Body:    "What:\nFinalize the approved branch head.\n\nWhy:\nHand off the exact reviewed change.",
	}
	tests := []struct {
		name          string
		reviewEnabled bool
		review        plan.PlanReview
		reviewErr     error
		wantPR        bool
		wantError     string
	}{
		{name: "approved exact head", reviewEnabled: true, review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: "base123", Head: "head123", CommitMessage: validProposal}, wantPR: true},
		{name: "comment", reviewEnabled: true, review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictComment, Head: "head123"}},
		{name: "changes requested", reviewEnabled: true, review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Head: "head123"}},
		{name: "review error", reviewEnabled: true, reviewErr: errors.New("review failed")},
		{name: "stale review head", reviewEnabled: true, review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: "base123", Head: "head456", CommitMessage: validProposal}, wantError: "exact approved review"},
		{name: "review disabled", review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
			var out bytes.Buffer
			var gitCalls []string
			reviewer := &recordingReviewCreator{review: tt.review, err: tt.reviewErr}
			creatorCalls := 0
			creator := pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
				creatorCalls++
				return plan.PullRequest{Number: 42, URL: "https://github.com/iamseth/tao/pull/42"}, nil
			})
			finalizer := newFinalizer(&out, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{
				CommitPolicy:  CommitPolicySlice,
				ExecutionMode: ExecutionModeIsolated,
				ReviewEnabled: tt.reviewEnabled,
				PullRequest:   true,
			}}, RunDependencies{
				CommandRunner:      runGitFake(&gitCalls, nil),
				PlanRecordFactory:  memoryPlanRecordFactory,
				PullRequestCreator: creator,
				ReviewCreator:      reviewer,
				RootResolver:       staticRootResolver(),
			}))

			complete, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
			if tt.wantError == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantError != "" && (err == nil || !strings.Contains(err.Error(), tt.wantError)) {
				t.Fatalf("error = %v, want %q", err, tt.wantError)
			}
			if !complete {
				t.Fatal("expected completed slice run")
			}
			wantReviewCalls := 0
			if tt.reviewEnabled {
				wantReviewCalls = 1
			}
			if got := reviewer.calls; got != wantReviewCalls {
				t.Fatalf("review calls = %d, want %d", got, wantReviewCalls)
			}
			if got := creatorCalls == 1; got != tt.wantPR {
				t.Fatalf("pull request creator called = %t, want %t", got, tt.wantPR)
			}
			if got := detail.State.Plan.PullRequest != nil; got != tt.wantPR {
				t.Fatalf("pull request recorded = %t, want %t: %+v", got, tt.wantPR, detail.State.Plan.PullRequest)
			}
			if tt.wantPR && detail.State.Plan.PullRequest.HeadSHA != "head123" {
				t.Fatalf("pull request was not recorded against the live head: %+v", detail.State.Plan.PullRequest)
			}
			if got := plan.PlanIsPullRequestComplete(detail); got != tt.wantPR {
				t.Fatalf("PlanIsPullRequestComplete() = %t, want %t", got, tt.wantPR)
			}

			text := out.String()
			if got := strings.Contains(text, "Pull request: #42 https://github.com/iamseth/tao/pull/42"); got != tt.wantPR {
				t.Fatalf("pull request output present = %t, want %t:\n%s", got, tt.wantPR, text)
			}
			for _, marker := range []string{"Plan complete in Tao: plan-a", "Next: use the host's Squash and merge action", "`tao cleanup --dry-run`"} {
				if got := strings.Contains(text, marker); got != tt.wantPR {
					t.Fatalf("output contains %q = %t, want %t:\n%s", marker, got, tt.wantPR, text)
				}
			}
			if strings.Contains(text, "tao merge --record-only --force") {
				t.Fatalf("output retained forced record-only workaround:\n%s", text)
			}
		})
	}
}

type captureReviewRecord struct {
	PlanMutationRecord
	detail *plan.PlanDetail
	wrote  *plan.State
}

func (r captureReviewRecord) RecordFinalVerification(verification plan.FinalVerification) error {
	if err := plan.MarkFinalVerification(r.detail, verification); err != nil {
		return err
	}
	*r.wrote = r.detail.State
	return nil
}

func (r captureReviewRecord) RecordReviewError(review plan.PlanReview, _ string) error {
	reviewedAt := review.ReviewedAt
	r.detail.State.Plan.Review = &review
	r.detail.State.UpdatedAt = reviewedAt
	r.detail.State.Plan.Timing.LastActivityAt = &reviewedAt
	*r.wrote = r.detail.State
	return nil
}

func TestFinalizerReviewErrorIsBestEffort(t *testing.T) {
	planDir := t.TempDir()
	detail := completedReviewPlanDetail(planDir)
	var out bytes.Buffer
	var log bytes.Buffer
	var wroteState plan.State
	reviewer := &recordingReviewCreator{err: errors.New("review timed out")}
	finalizer := newFinalizer(&out, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentPi, CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, ReviewEnabled: true}}, RunDependencies{
		ReviewCreator:    reviewer,
		RootResolver:     staticRootResolver(),
		SessionLogWriter: &log,
		PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return captureReviewRecord{detail: detail, wrote: &wroteState}, nil
		},
	}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected completed plan")
	}
	if reviewer.calls != 1 {
		t.Fatalf("expected review attempt, got %d", reviewer.calls)
	}
	if got := log.String(); !strings.Contains(got, "Warning: plan review failed; continuing without failing the run: review timed out") {
		t.Fatalf("expected best-effort review warning in session log, got:\n%s", got)
	}
	if detail.State.Plan.Review == nil || detail.State.Plan.Review.Status != plan.ReviewStatusError || detail.State.Plan.Review.Verdict != plan.ReviewStatusError {
		t.Fatalf("review error not recorded on detail: %+v", detail.State.Plan.Review)
	}
	if wroteState.Plan.Review == nil || wroteState.Plan.Review.Status != plan.ReviewStatusError || !strings.Contains(wroteState.Plan.Review.Summary, "review timed out") {
		t.Fatalf("review error not persisted: %+v", wroteState.Plan.Review)
	}
}

func TestRunRecoversPersistedPullRequestIntentAfterDefaultBranchAdvancesWithoutRefinalizing(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	repoRoot := filepath.Join(plansDir, "repo")
	worktreeRoot := filepath.Join(plansDir, "worktrees", "plan-a")
	commonDir := filepath.Join(repoRoot, ".git")
	worktreeGitDir := filepath.Join(commonDir, "worktrees", "plan-a")
	for _, dir := range []string{planDir, worktreeRoot, worktreeGitDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	detail := completedReviewPlanDetail(planDir)
	detail.State.Schema = "tao.plan.state.v1"
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{
		Strategy:        plan.WorkspaceStrategyWorktree,
		Root:            filepath.Dir(worktreeRoot),
		Path:            worktreeRoot,
		Branch:          "feature",
		HeadSHA:         "head123",
		LifecycleStatus: plan.WorkspaceStatusReady,
		CleanupStatus:   plan.WorkspaceCleanupStatusPending,
	}
	detail.State.Plan.Review = &plan.PlanReview{
		Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: "base123", Head: "head123",
		CommitMessage: &plan.ReviewCommitMessage{
			Subject: "fix(pr): recover pull request finalization",
			Body:    "What:\nRecover the exact approved pull request head.\n\nWhy:\nComplete interrupted finalization without rerunning slices.",
		},
	}
	detail.Slices.Schema = "tao.plan.slices.v1"
	detail.Slices.PlanID = "plan-a"
	persistRunArtifacts(t, planDir, detail)

	repo := plan.NewFileRepository(plansDir)
	createdAt := time.Date(2026, 8, 13, 3, 15, 0, 0, time.UTC)
	createdPR := plan.PullRequest{Number: 42, URL: "https://github.com/iamseth/tao/pull/42", CreatedAt: createdAt, Branch: "feature", HeadSHA: "head123"}
	createCalls := 0
	creator := pullRequestCreatorFunc(func(_ context.Context, run PullRequestRun) (plan.PullRequest, error) {
		createCalls++
		if createCalls == 1 {
			record, err := repo.PlanRecord(run.Detail)
			if err != nil {
				return plan.PullRequest{}, err
			}
			if err := record.RecordPullRequestIntent(createdPR, run.Branch, run.HeadSHA); err != nil {
				return plan.PullRequest{}, err
			}
			return plan.PullRequest{}, errors.New("assignment permission denied")
		}
		if run.RepoRoot != worktreeRoot || run.Branch != createdPR.Branch || run.HeadSHA != createdPR.HeadSHA {
			return plan.PullRequest{}, errors.New("pull request recovery received a mutated worktree head")
		}
		return createdPR, nil
	})
	planRecords := func(detail *plan.PlanDetail) (PlanMutationRecord, error) { return repo.PlanRecord(detail) }
	verificationCalls := 0
	gitCalls := []string{}
	liveHead := "head123"
	defaultHead := "base123"
	runner := func(ctx context.Context, cwd, name string, args []string, stdout, _ io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "sh" {
			verificationCalls++
			return nil
		}
		if name != "git" {
			return nil
		}
		gitRoot := cwd
		if len(args) >= 2 && args[0] == "-C" {
			gitRoot = args[1]
		}
		key := runGitKey(args)
		gitCalls = append(gitCalls, key)
		switch key {
		case "rev-parse --show-toplevel":
			_, _ = io.WriteString(stdout, gitRoot+"\n")
		case "rev-parse --git-common-dir":
			_, _ = io.WriteString(stdout, commonDir+"\n")
		case "rev-parse --git-dir":
			gitDir := commonDir
			if filepath.Clean(gitRoot) == filepath.Clean(worktreeRoot) {
				gitDir = worktreeGitDir
			}
			_, _ = io.WriteString(stdout, gitDir+"\n")
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "feature\n")
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, liveHead+"\n")
		case "rev-parse main":
			_, _ = io.WriteString(stdout, defaultHead+"\n")
		case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
			_, _ = io.WriteString(stdout, "origin/main\n")
		}
		return nil
	}

	firstDetail, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	first := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeIsolated, PullRequest: true}}, RunDependencies{
		CommandRunner: runner, PlanRecordFactory: planRecords, PullRequestCreator: creator,
		RootResolver: ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) { return worktreeRoot, nil }),
	}))
	if _, err := first.FinalizeIfComplete(context.Background(), 1, firstDetail, plan.RunCapabilities{Complete: true}); err == nil || !strings.Contains(err.Error(), "assignment permission denied") {
		t.Fatalf("first invocation error = %v, want persisted PR failure", err)
	}

	reloaded, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.State.Plan.PullRequestIntent == nil || *reloaded.State.Plan.PullRequestIntent != createdPR {
		t.Fatalf("persisted pull request intent = %#v, want %#v", reloaded.State.Plan.PullRequestIntent, createdPR)
	}
	if failure := reloaded.State.Plan.FinalizationFailure; failure == nil || failure.Phase != plan.FinalizationFailurePhasePullRequest || failure.Branch != createdPR.Branch || failure.HeadSHA != createdPR.HeadSHA {
		t.Fatalf("persisted pull request failure = %#v, want exact branch/head evidence", failure)
	}
	requireOwnedEvent(t, reloaded.Events, plan.EventTypeFinalizationFailed, func(event plan.Event) bool {
		failure := event.FinalizationFailure
		return failure != nil && failure.Phase == plan.FinalizationFailurePhasePullRequest && failure.Category == "pull_request_failed" && failure.Branch == createdPR.Branch && failure.HeadSHA == createdPR.HeadSHA && failure.RecoveryAction == "resume_pull_request"
	})

	// The default branch moves after the failed attempt. The normal workspace
	// preparer would rebase this stale clean worktree and rewrite its HEAD.
	defaultHead = "base456"
	prepareCalls := 0
	workspacePreparer := func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
		prepareCalls++
		if defaultHead != "base123" {
			liveHead = "rebased456"
		}
		return worktreeRoot, nil
	}
	verificationCalls = 0
	reviewer := &recordingReviewCreator{}
	var out bytes.Buffer
	err = executeDetail(context.Background(), reloaded, nil, &out, Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated, PullRequest: true, ReviewEnabled: true}},
		RunDependencies: RunDependencies{CommandRunner: runner, PlanRecordFactory: planRecords, PullRequestCreator: creator, ReviewCreator: reviewer, WorkspacePreparer: workspacePreparer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepareCalls != 0 || liveHead != createdPR.HeadSHA {
		t.Fatalf("recovery prepared or mutated worktree after default advanced: prepare calls=%d HEAD=%s", prepareCalls, liveHead)
	}
	if createCalls != 2 {
		t.Fatalf("pull request create calls = %d, want 2", createCalls)
	}
	if verificationCalls != 0 || reviewer.calls != 0 {
		t.Fatalf("recovery reran completed phases: verification=%d review=%d", verificationCalls, reviewer.calls)
	}
	for _, call := range gitCalls {
		if strings.HasPrefix(call, "rebase") || strings.HasPrefix(call, "reset") || strings.HasPrefix(call, "checkout") {
			t.Fatalf("pull request recovery mutated Git with %q", call)
		}
	}

	finalDetail, err := repo.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if finalDetail.State.Plan.PullRequest == nil || *finalDetail.State.Plan.PullRequest != createdPR || finalDetail.State.Plan.PullRequestIntent != nil {
		t.Fatalf("recovered pull request state: pull_request=%#v intent=%#v", finalDetail.State.Plan.PullRequest, finalDetail.State.Plan.PullRequestIntent)
	}
	if finalDetail.State.Plan.FinalizationFailure != nil {
		t.Fatalf("successful recovery retained failure evidence: %#v", finalDetail.State.Plan.FinalizationFailure)
	}
	if !strings.Contains(out.String(), "Pull request: #42 https://github.com/iamseth/tao/pull/42") {
		t.Fatalf("missing recovered pull request output: %q", out.String())
	}
}

func TestPendingPullRequestIntentAndReplacementNonApprovalRecommendsActionableNextStep(t *testing.T) {
	for _, verdict := range []string{plan.ReviewVerdictComment, plan.ReviewVerdictChangesRequested} {
		t.Run(verdict, func(t *testing.T) {
			fixture := newPullRequestOrchestrationFixture(t)
			repo := plan.NewFileRepository(fixture.plansRoot)
			detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
			if err != nil {
				t.Fatal(err)
			}
			record, err := repo.PlanRecord(detail)
			if err != nil {
				t.Fatal(err)
			}
			intent := plan.PullRequest{
				Number: 73, URL: "https://github.com/iamseth/tao/pull/73",
				CreatedAt: time.Date(2026, 8, 31, 13, 30, 0, 0, time.UTC),
				Branch:    fixture.branch, HeadSHA: fixture.head,
			}
			if err := record.RecordPullRequestIntent(intent, fixture.branch, fixture.head); err != nil {
				t.Fatal(err)
			}
			replacement := plan.PlanReview{
				Status: plan.ReviewStatusCompleted, Verdict: verdict, Summary: "approval replaced",
				Base: fixture.base, Head: fixture.head, ReviewedAt: intent.CreatedAt.Add(time.Minute),
			}
			if verdict == plan.ReviewVerdictChangesRequested {
				replacement.Findings = []plan.ReviewFinding{{Severity: "major", File: "change.txt", Line: 1, Message: "revise the change"}}
				replacement.FindingsCount = 1
			}
			if err := record.RecordReviewCompleted(replacement, "pi"); err != nil {
				t.Fatal(err)
			}

			reloaded, err := repo.ResolvePlan(context.Background(), fixture.planDir)
			if err != nil {
				t.Fatal(err)
			}
			wantKind := plan.PlanActionReview
			wantCommand := "tao review --run plan-a"
			if verdict == plan.ReviewVerdictChangesRequested {
				wantKind = plan.PlanActionRework
				wantCommand = "tao rework plan-a"
			}
			advertised := plan.DeriveNextAction(reloaded).Primary
			if advertised.Kind != wantKind || advertised.Command != wantCommand {
				t.Fatalf("pending-intent action = %#v, want kind %q command %q", advertised, wantKind, wantCommand)
			}
			if reloaded.State.Plan.PullRequestIntent == nil || *reloaded.State.Plan.PullRequestIntent != intent {
				t.Fatalf("read-only derivation changed remote identity intent: %#v", reloaded.State.Plan.PullRequestIntent)
			}
			if reloaded.State.Plan.FinalizationFailure != nil {
				t.Fatalf("read-only derivation recorded failure evidence: %#v", reloaded.State.Plan.FinalizationFailure)
			}
		})
	}
}

func TestFinalizerRepairsHistoricalApprovedProposalBeforePullRequestPreflight(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	repo := plan.NewFileRepository(fixture.plansRoot)
	detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the legacy fallback to the immutable plan base while retaining a
	// real linked worktree for the post-session safety inspection.
	detail.State.Plan.Review.Base = ""
	requests := 0
	reviewer := struct {
		ReviewCreator
		AgentSessionExecutor
	}{
		ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			t.Fatal("substantive review must not rerun during proposal repair")
			return plan.PlanReview{}, nil
		}),
		AgentSessionExecutor: agentSessionExecutorFunc(func(_ context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
			requests++
			if !request.CaptureOutput || !strings.Contains(request.Prompt, "COMMIT PROPOSAL CORRECTION mode") {
				t.Fatalf("unexpected correction request: %#v", request)
			}
			return AgentSessionResult{Output: "```tao-review-proposal-json\n{\"commit_message\":{\"subject\":\"fix(pr): recover exact finalization\",\"body\":\"What:\\nRecover the exact approved head.\\n\\nWhy:\\nFinish pull request handoff safely.\"}}\n```"}, nil
		}),
	}
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner:     defaultCommandRunner,
		PlanRecordFactory: fileReviewRecordFactory(repo),
		ReviewCreator:     reviewer,
	}))

	if err := finalizer.ensureApprovedReviewProposal(context.Background(), detail, fixture.worktreeRoot, fixture.branch, fixture.head); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("proposal correction requests = %d, want 1", requests)
	}
	review := detail.State.Plan.Review
	if review == nil || review.Base != fixture.base || review.Head != fixture.head || review.CommitMessage == nil || review.CommitMessage.Subject != "fix(pr): recover exact finalization" {
		t.Fatalf("corrected historical review = %#v", review)
	}
	if detail.State.Plan.FinalizationFailure != nil {
		t.Fatalf("successful correction recorded failure: %#v", detail.State.Plan.FinalizationFailure)
	}
}

func TestFinalizerRecordsHistoricalProposalCorrectionFailureAgainstFallbackBase(t *testing.T) {
	fixture := newPullRequestOrchestrationFixture(t)
	repo := plan.NewFileRepository(fixture.plansRoot)
	detail, err := repo.ResolvePlan(context.Background(), fixture.planDir)
	if err != nil {
		t.Fatal(err)
	}
	detail.State.Plan.Review.Base = ""
	reviewer := struct {
		ReviewCreator
		AgentSessionExecutor
	}{
		ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			t.Fatal("substantive review must not rerun during proposal repair")
			return plan.PlanReview{}, nil
		}),
		AgentSessionExecutor: agentSessionExecutorFunc(func(context.Context, AgentSessionRequest) (AgentSessionResult, error) {
			return AgentSessionResult{Output: "not a proposal"}, nil
		}),
	}
	failedAt := time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC)
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner:     defaultCommandRunner,
		PlanRecordFactory: memoryPlanRecordFactory,
		ReviewCreator:     reviewer,
		Now:               func() time.Time { return failedAt },
	}))

	err = finalizer.ensureApprovedReviewProposal(context.Background(), detail, fixture.worktreeRoot, fixture.branch, fixture.head)
	if err == nil || !strings.Contains(err.Error(), "valid typed commit proposal") {
		t.Fatalf("error = %v, want invalid proposal", err)
	}
	failure := detail.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhaseProposalRepair || failure.Category != "proposal_invalid" || failure.ReviewBase != fixture.base || failure.ReviewHead != fixture.head || failure.FailedAt != failedAt {
		t.Fatalf("proposal correction failure = %#v", failure)
	}
}

func TestFinalizerTreatsHistoricalProposalFailureAsConsumedCorrection(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Plan.ChangeType = plan.ChangeTypeFix
	detail.State.Plan.Review = &plan.PlanReview{
		Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove,
		Base: "base123", Head: "head123",
	}
	detail.State.Plan.FinalizationFailure = &plan.FinalizationFailure{
		Phase: plan.FinalizationFailurePhaseProposalRepair, Category: "proposal_invalid",
		ReviewBase: "base123", ReviewHead: "head123",
		FailedAt: time.Date(2026, 8, 30, 9, 0, 0, 0, time.UTC), RecoveryAction: "resume_pull_request",
	}
	correctionCalls := 0
	reviewer := struct {
		ReviewCreator
		AgentSessionExecutor
	}{
		ReviewCreator: reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
			t.Fatal("substantive review must not rerun during proposal repair")
			return plan.PlanReview{}, nil
		}),
		AgentSessionExecutor: agentSessionExecutorFunc(func(context.Context, AgentSessionRequest) (AgentSessionResult, error) {
			correctionCalls++
			return AgentSessionResult{}, nil
		}),
	}
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		PlanRecordFactory: memoryPlanRecordFactory,
		ReviewCreator:     reviewer,
		Now:               func() time.Time { return time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC) },
	}))

	err := finalizer.ensureApprovedReviewProposal(context.Background(), detail, "/repo", "feature", "head123")
	if err == nil || !strings.Contains(err.Error(), "already attempted") || !strings.Contains(err.Error(), "tao review --run plan-a") {
		t.Fatalf("error = %v, want replacement-review guidance", err)
	}
	if correctionCalls != 0 {
		t.Fatalf("proposal correction calls = %d, want 0", correctionCalls)
	}
	failure := detail.State.Plan.FinalizationFailure
	if failure == nil || failure.Category != "proposal_invalid" || failure.ReviewBase != "base123" || failure.ReviewHead != "head123" || failure.RecoveryAction != "rerun_review" {
		t.Fatalf("upgraded proposal failure = %#v", failure)
	}
}

func TestFinalizerRecordsUninspectableWorktreeBeforePullRequestCreation(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFix, "head123")
	statusErr := errors.New("status unavailable")
	creatorCalls := 0
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			return statusErr
		},
		PlanRecordFactory: memoryPlanRecordFactory,
		PullRequestCreator: pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
			creatorCalls++
			return plan.PullRequest{}, nil
		}),
	}))

	err := finalizer.createAndRecordPullRequestAtHead(context.Background(), detail, "/repo", "feature", "head123")
	if !errors.Is(err, statusErr) || !strings.Contains(err.Error(), "inspect pull request worktree status before pull request creation") {
		t.Fatalf("uninspectable worktree error = %v", err)
	}
	if creatorCalls != 0 {
		t.Fatalf("pull request creator calls = %d, want 0", creatorCalls)
	}
	failure := detail.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhasePullRequest || failure.Category != "workspace_preflight_failed" || failure.Branch != "feature" || failure.HeadSHA != "head123" || failure.RecoveryAction != plan.FinalizationRecoveryResumePullRequest {
		t.Fatalf("uninspectable worktree failure = %#v", failure)
	}
	if action := plan.DeriveNextAction(detail).Primary; action.Command != "tao run --pull-request plan-a" {
		t.Fatalf("uninspectable worktree next action = %#v", action)
	}
}

func TestFinalizerReplacesPullRequestFailureOnlyAfterMatchingSuccess(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	failedAt := time.Date(2026, 8, 30, 10, 0, 0, 0, time.UTC)
	createdAt := failedAt.Add(time.Minute)
	calls := 0
	creator := pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
		calls++
		if calls == 1 {
			return plan.PullRequest{}, pullRequestFailure("publication_failed", errors.New("push rejected"))
		}
		return plan.PullRequest{Number: 42, URL: "https://github.com/iamseth/tao/pull/42", CreatedAt: createdAt}, nil
	})
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner:      pullRequestCommandRunner(t, nil, nil),
		PlanRecordFactory:  memoryPlanRecordFactory,
		PullRequestCreator: creator,
		Now:                func() time.Time { return failedAt },
	}))

	err := finalizer.createAndRecordPullRequestAtHead(context.Background(), detail, "/repo", "feature", "head123")
	if err == nil || !strings.Contains(err.Error(), "push rejected") {
		t.Fatalf("error = %v, want publication failure", err)
	}
	failure := detail.State.Plan.FinalizationFailure
	if failure == nil || failure.Phase != plan.FinalizationFailurePhasePullRequest || failure.Category != "publication_failed" || failure.Branch != "feature" || failure.HeadSHA != "head123" {
		t.Fatalf("pull request failure = %#v", failure)
	}

	if err := finalizer.createAndRecordPullRequestAtHead(context.Background(), detail, "/repo", "feature", "head123"); err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.PullRequest == nil || detail.State.Plan.PullRequest.Number != 42 {
		t.Fatalf("pull request was not recorded: %#v", detail.State.Plan.PullRequest)
	}
	if detail.State.Plan.FinalizationFailure != nil {
		t.Fatalf("matching success retained failure: %#v", detail.State.Plan.FinalizationFailure)
	}
}

type failFirstPullRequestRecord struct {
	PlanMutationRecord
	recordCalls int
}

func (r *failFirstPullRequestRecord) RecordPullRequest(pr plan.PullRequest, branch, headSHA string) error {
	r.recordCalls++
	if r.recordCalls == 1 {
		return errors.New("interrupted final pull request recording")
	}
	return r.PlanMutationRecord.RecordPullRequest(pr, branch, headSHA)
}

func TestFinalizerInterruptedUnownedDiscoveryDoesNotAuthorizeMetadataRepair(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	createdAt := time.Date(2026, 8, 31, 18, 0, 0, 0, time.UTC)
	identity := forge.PullRequest{Number: 42, URL: "https://github.com/iamseth/tao/pull/42", CreatedAt: createdAt}
	findCalls := 0
	metadataCalls := 0
	boundary := pullRequestsFake{
		find: func(context.Context, forge.FindRequest) (forge.PullRequest, forge.Metadata, bool, error) {
			findCalls++
			return identity, forge.Metadata{}, true, nil
		},
		view: func(context.Context, forge.ViewRequest) (forge.PullRequest, forge.Metadata, bool, error) {
			t.Fatal("unowned discovery must not become identity-based recovery")
			return forge.PullRequest{}, forge.Metadata{}, false, nil
		},
		create: func(context.Context, forge.CreateRequest) forge.CreationOutcome {
			t.Fatal("matching pull request discovery must not create another pull request")
			return forge.CreationOutcome{}
		},
		ensureMetadata: func(context.Context, forge.MetadataRequest) error {
			metadataCalls++
			return nil
		},
	}
	runner := pullRequestCommandRunner(t, nil, nil)
	baseRecord, err := memoryPlanRecordFactory(detail)
	if err != nil {
		t.Fatal(err)
	}
	record := &failFirstPullRequestRecord{PlanMutationRecord: baseRecord}
	recordFactory := func(*plan.PlanDetail) (PlanMutationRecord, error) { return record, nil }
	creator := deterministicPullRequestCreator{
		execution:    testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, PlanRecordFactory: recordFactory}),
		pullRequests: boundary,
	}
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: runner, PlanRecordFactory: recordFactory, PullRequestCreator: creator,
	}))

	err = finalizer.createAndRecordPullRequestAtHead(context.Background(), detail, "/repo", "feature/plan-a", "head123")
	if err == nil || !strings.Contains(err.Error(), "interrupted final pull request recording") {
		t.Fatalf("first finalization error = %v, want interrupted recording", err)
	}
	intent := detail.State.Plan.PullRequestIntent
	if intent == nil || intent.Branch != "feature/plan-a" || intent.HeadSHA != "head123" || pullRequestIntentHasIdentity(*intent) {
		t.Fatalf("unowned discovery intent = %#v, want branch/head-only creation intent", intent)
	}

	if err := finalizer.createAndRecordPullRequestAtHead(context.Background(), detail, "/repo", "feature/plan-a", "head123"); err != nil {
		t.Fatal(err)
	}
	if findCalls != 2 || metadataCalls != 0 {
		t.Fatalf("retry discovery/metadata calls = %d/%d, want 2/0", findCalls, metadataCalls)
	}
	if detail.State.Plan.PullRequest == nil || detail.State.Plan.PullRequest.Number != identity.Number || detail.State.Plan.PullRequestIntent != nil {
		t.Fatalf("settled pull request state = pr:%#v intent:%#v", detail.State.Plan.PullRequest, detail.State.Plan.PullRequestIntent)
	}
}

type failingFinalizationReplacementRecord struct {
	PlanMutationRecord
	detail       *plan.PlanDetail
	replaceCalls int
	clearCalls   int
	recordCalls  int
}

func (r *failingFinalizationReplacementRecord) RecordFinalizationFailure(failure plan.FinalizationFailure) error {
	r.recordCalls++
	r.detail.State.Plan.FinalizationFailure = &failure
	return nil
}

func (r *failingFinalizationReplacementRecord) ReplaceFinalizationFailure(_, _ plan.FinalizationFailure) error {
	r.replaceCalls++
	return errors.New("injected atomic replacement failure")
}

func (r *failingFinalizationReplacementRecord) ClearFinalizationFailure(_ plan.FinalizationFailure, _ time.Time) error {
	r.clearCalls++
	r.detail.State.Plan.FinalizationFailure = nil
	return errors.New("interrupted after clear")
}

func TestFinalizerCommitPolicyNoneSkipsReviewCleanlinessGate(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.Slices.Slices[0].ExpectedFiles = []string{"internal/run/finalize.go"}
	var out bytes.Buffer
	var gitCalls []string
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "git" {
			gitCalls = append(gitCalls, runGitKey(args))
			if runGitKey(args) == "status --porcelain" {
				_, _ = io.WriteString(stdout, "?? internal/run/finalize.go\n")
			}
		}
		return nil
	}
	reviewer := &recordingReviewCreator{review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: "approve"}}
	finalizer := newFinalizer(&out, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, ReviewEnabled: true}}, RunDependencies{
		CommandRunner: runner,
		ReviewCreator: reviewer,
		RootResolver:  staticRootResolver(),
	}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected completed plan")
	}
	if len(gitCalls) != 0 {
		t.Fatalf("commit policy none should skip review-cleanliness git calls, got %v", gitCalls)
	}
	if reviewer.calls != 1 {
		t.Fatalf("expected finalize to proceed to review, got %d call(s)", reviewer.calls)
	}
}

func TestFinalizerReviewCleanlinessGateRunsBeforeReview(t *testing.T) {
	detail := completedReviewPlanDetail(t.TempDir())
	detail.Slices.Slices[0].ExpectedFiles = []string{"internal/run/finalize.go"}
	var out bytes.Buffer
	var order []string
	git := newScriptedGitRunner("")
	git.Order = &order
	runner := git.Run
	reviewer := reviewCreatorFunc(func(ctx context.Context, run ReviewRun) (plan.PlanReview, error) {
		if err := ctx.Err(); err != nil {
			return plan.PlanReview{}, err
		}
		order = append(order, "review")
		return plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: "approve"}, nil
	})
	finalizer := newFinalizer(&out, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent, ReviewEnabled: true}}, RunDependencies{
		CommandRunner: runner,
		ReviewCreator: reviewer,
		RootResolver:  staticRootResolver(),
	}))

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if !complete {
		t.Fatal("expected completed plan")
	}
	requireStringOrder(t, order, "git status --porcelain", "review")
}

func TestFinalizerVerifiesExecutionRootBeforeReview(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Repo.Root = "/control-checkout"
	var order []string
	git := newScriptedGitRunner("")
	git.Order = &order
	runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name == "sh" {
			order = append(order, "verify "+cwd+" "+strings.Join(args, " "))
			return nil
		}
		return git.Run(ctx, cwd, name, args, stdout, stderr)
	}
	reviewer := reviewCreatorFunc(func(context.Context, ReviewRun) (plan.PlanReview, error) {
		order = append(order, "review")
		return plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove}, nil
	})
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent, ReviewEnabled: true}}, RunDependencies{
		CommandRunner: runner,
		ReviewCreator: reviewer,
		RootResolver:  ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) { return root, nil }),
	}))

	if _, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true}); err != nil {
		t.Fatal(err)
	}
	verifyCall := "verify " + root + " -c make verify"
	requireStringOrder(t, order, "git status --porcelain", verifyCall)
	requireStringOrder(t, order, verifyCall, "review")
	if got := detail.State.Plan.FinalVerification; got == nil || got.Command != "make verify" || got.CWD != root || got.Result != finalVerificationPassed {
		t.Fatalf("unexpected final verification: %+v", got)
	}
}

func TestFinalizerVerificationFailureBlocksReviewAndRecordsResult(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/failing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := completedReviewPlanDetail(t.TempDir())
	reviewer := &recordingReviewCreator{}
	runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name == "sh" {
			_, _ = io.WriteString(stderr, "tests failed")
			return errors.New("exit status 1")
		}
		return newScriptedGitRunner("").Run(ctx, cwd, name, args, stdout, stderr)
	}
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent, ReviewEnabled: true}}, RunDependencies{
		CommandRunner: runner,
		ReviewCreator: reviewer,
		RootResolver:  ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) { return root, nil }),
	}))

	_, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true})
	if err == nil || !strings.Contains(err.Error(), "go build ./... && go test ./...") || !strings.Contains(err.Error(), "tests failed") {
		t.Fatalf("expected actionable verification failure, got %v", err)
	}
	if reviewer.calls != 0 {
		t.Fatalf("review called after failed verification: %d", reviewer.calls)
	}
	if got := detail.State.Plan.FinalVerification; got == nil || got.Result != finalVerificationFailed || got.Details != "tests failed" {
		t.Fatalf("unexpected failed verification record: %+v", got)
	}
}

func TestFinalizerVerifiesWhenReviewDisabledAndRecordsNoDetection(t *testing.T) {
	root := t.TempDir()
	detail := completedReviewPlanDetail(t.TempDir())
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, ReviewEnabled: false}}, RunDependencies{
		CommandRunner: runGitFake(&[]string{}, nil),
		RootResolver:  ExecutionRootResolverFunc(func(context.Context, *plan.PlanDetail) (string, error) { return root, nil }),
	}))

	if _, err := finalizer.FinalizeIfComplete(context.Background(), 1, detail, plan.RunCapabilities{Complete: true}); err != nil {
		t.Fatal(err)
	}
	if got := detail.State.Plan.FinalVerification; got == nil || got.Result != finalVerificationSkipped || got.CWD != root || got.Command != "" {
		t.Fatalf("unexpected skipped verification record: %+v", got)
	}
}

func TestCurrentBranchHeadSuccessAndErrors(t *testing.T) {
	var calls []string
	branch, head, err := currentBranchHead(context.Background(), testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runGitFake(&calls, nil)}), "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if branch != "feature" || head != "head123" {
		t.Fatalf("unexpected branch/head %q %q", branch, head)
	}
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if strings.Contains(strings.Join(args, " "), "branch --show-current") {
			return nil
		}
		return runGitFake(&[]string{}, nil)(ctx, cwd, name, args, stdout, stderr)
	}
	_, _, err = currentBranchHead(context.Background(), testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner}), "/repo")
	if err == nil || !strings.Contains(err.Error(), "empty branch") {
		t.Fatalf("expected empty branch error, got %v", err)
	}
}

func completedReviewPlanDetail(planDir string) *plan.PlanDetail {
	detail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = "/repo"
	detail.State.Repo.BaseCommit = "base123"
	return detail
}

type reviewCreatorFunc func(context.Context, ReviewRun) (plan.PlanReview, error)

func (f reviewCreatorFunc) CreateReview(ctx context.Context, run ReviewRun) (plan.PlanReview, error) {
	return f(ctx, run)
}

// scriptedGitRunner is the shared scripted-git test double used by finalize,
// review-cleanliness, and leak-guard tests. It serves successive scripted
// responses from configured queues and records git calls for assertion.
type scriptedGitRunner struct {
	// Scripted responses (consumed in sequence).
	Branch    string   // response for "branch --show-current" (default "feature")
	Head      string   // response for "rev-parse HEAD" (default "head123")
	Origin    string   // response for "symbolic-ref --quiet --short refs/remotes/origin/HEAD" (default "origin/main")
	Statuses  []string // successive responses for "status --porcelain"
	Diffs     []string // successive responses for "diff HEAD"
	DiffNames []string // successive responses for "diff --name-only HEAD"

	// Error overrides: if a git key matches, return this error.
	Errors map[string]error

	// Observations.
	Calls []string  // git subcommand keys in call order (no prefix)
	Order *[]string // if non-nil, records "git <key>" (for ordering assertions)

	// internal sequence indices.
	statusIdx int
	diffIdx   int
	nameIdx   int
}

// newScriptedGitRunner returns a runner that serves statuses in order and
// defaults to branch="feature" and origin="origin/main".
func newScriptedGitRunner(statuses ...string) *scriptedGitRunner {
	return &scriptedGitRunner{Branch: "feature", Head: "head123", Origin: "origin/main", Statuses: statuses}
}

// Run implements CommandRunner.
func (r *scriptedGitRunner) Run(ctx context.Context, cwd, name string, args []string, stdout, _ io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if name != "git" {
		return nil
	}
	key := runGitKey(args)
	r.Calls = append(r.Calls, key)
	if r.Order != nil {
		*r.Order = append(*r.Order, "git "+key)
	}
	if r.Errors != nil {
		if err, ok := r.Errors[key]; ok {
			return err
		}
	}
	switch key {
	case "branch --show-current":
		if r.Branch != "" {
			_, _ = io.WriteString(stdout, r.Branch+"\n")
		}
	case "rev-parse HEAD":
		if r.Head != "" {
			_, _ = io.WriteString(stdout, r.Head+"\n")
		}
	case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
		if r.Origin != "" {
			_, _ = io.WriteString(stdout, r.Origin+"\n")
		}
	case "status --porcelain":
		if r.statusIdx < len(r.Statuses) {
			_, _ = io.WriteString(stdout, r.Statuses[r.statusIdx])
			r.statusIdx++
		}
	case "diff HEAD":
		if r.diffIdx < len(r.Diffs) {
			_, _ = io.WriteString(stdout, r.Diffs[r.diffIdx])
			r.diffIdx++
		}
	case "diff --name-only HEAD":
		if r.nameIdx < len(r.DiffNames) {
			_, _ = io.WriteString(stdout, r.DiffNames[r.nameIdx])
			r.nameIdx++
		}
	}
	return nil
}

func requireStringOrder(t *testing.T, values []string, before string, after string) {
	t.Helper()
	beforeIndex := stringSliceIndex(values, before)
	afterIndex := stringSliceIndex(values, after)
	if beforeIndex < 0 || afterIndex < 0 || beforeIndex >= afterIndex {
		t.Fatalf("expected %q before %q in %v", before, after, values)
	}
}

func stringSliceIndex(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}

type recordingReviewCreator struct {
	review plan.PlanReview
	err    error
	calls  int
	runs   []ReviewRun
}

func (c *recordingReviewCreator) CreateReview(ctx context.Context, run ReviewRun) (plan.PlanReview, error) {
	if err := ctx.Err(); err != nil {
		return plan.PlanReview{}, err
	}
	c.calls++
	c.runs = append(c.runs, run)
	if c.err != nil {
		return plan.PlanReview{}, c.err
	}
	review := c.review
	if review.Status == "" {
		review.Status = plan.ReviewStatusCompleted
	}
	if run.Detail != nil {
		run.Detail.State.Plan.Review = &review
	}
	return review, nil
}

func staticRootResolver() ExecutionRootResolverFunc {
	return func(context.Context, *plan.PlanDetail) (string, error) {
		return "/repo", nil
	}
}
