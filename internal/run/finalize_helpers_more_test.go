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
