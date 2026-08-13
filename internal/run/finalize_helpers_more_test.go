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

func TestFinalizerReportsPullRequestCompletionOnlyForMatchingApproval(t *testing.T) {
	tests := []struct {
		name          string
		reviewEnabled bool
		review        plan.PlanReview
		reviewErr     error
		wantComplete  bool
	}{
		{name: "approved exact head", reviewEnabled: true, review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"}, wantComplete: true},
		{name: "comment", reviewEnabled: true, review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictComment, Head: "head123"}},
		{name: "changes requested", reviewEnabled: true, review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictChangesRequested, Head: "head123"}},
		{name: "review error", reviewEnabled: true, reviewErr: errors.New("review failed")},
		{name: "stale review head", reviewEnabled: true, review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head456"}},
		{name: "review disabled", review: plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := completedReviewPlanDetail(t.TempDir())
			detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
			var out bytes.Buffer
			var gitCalls []string
			reviewer := &recordingReviewCreator{review: tt.review, err: tt.reviewErr}
			creator := pullRequestCreatorFunc(func(context.Context, PullRequestRun) (plan.PullRequest, error) {
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
			if err != nil {
				t.Fatal(err)
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
			if detail.State.Plan.PullRequest == nil || detail.State.Plan.PullRequest.HeadSHA != "head123" {
				t.Fatalf("pull request was not recorded against the live head: %+v", detail.State.Plan.PullRequest)
			}
			if got := plan.PlanIsPullRequestComplete(detail); got != tt.wantComplete {
				t.Fatalf("PlanIsPullRequestComplete() = %t, want %t", got, tt.wantComplete)
			}

			text := out.String()
			if !strings.Contains(text, "Pull request: #42 https://github.com/iamseth/tao/pull/42") {
				t.Fatalf("missing recorded pull request output:\n%s", text)
			}
			for _, marker := range []string{"Plan complete in Tao: plan-a", "Next: use the host's Squash and merge action", "`tao cleanup --dry-run`"} {
				if got := strings.Contains(text, marker); got != tt.wantComplete {
					t.Fatalf("output contains %q = %t, want %t:\n%s", marker, got, tt.wantComplete, text)
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
	detail.State.Plan.Review = &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Head: "head123"}
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
	if !strings.Contains(out.String(), "Pull request: #42 https://github.com/iamseth/tao/pull/42") {
		t.Fatalf("missing recovered pull request output: %q", out.String())
	}
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
	return &scriptedGitRunner{Branch: "feature", Origin: "origin/main", Statuses: statuses}
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
