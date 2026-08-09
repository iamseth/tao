package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestRunPullRequestFailureSkipsCheckout(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	completed := time.Now().UTC()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, &started, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, &started, &completed)
	settleRunTestSlice(completedDetail)
	completedDetail.Dir = t.TempDir()
	var calls []string

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, PullRequest: true}}, RunDependencies: RunDependencies{PullRequestCreator: pullRequestCreatorFunc(func(ctx context.Context, run PullRequestRun) (plan.PullRequest, error) {
		return plan.PullRequest{}, errors.New("missing url")
	}), SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, nil)}})
	if err == nil || !strings.Contains(err.Error(), "create pull request") || !strings.Contains(err.Error(), "missing url") {
		t.Fatalf("expected PR failure, got %v", err)
	}
	if runHasGitCall(calls, "checkout main") {
		t.Fatalf("expected no checkout after PR failure, got %#v", calls)
	}
}

func TestRunPullRequestDisabledByDefault(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	completed := time.Now().UTC()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, &started, nil)
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, &started, &completed)
	settleRunTestSlice(completedDetail)
	var calls []string
	creator := pullRequestCreatorFunc(func(ctx context.Context, run PullRequestRun) (plan.PullRequest, error) {
		t.Fatal("pull request should not be created by default")
		return plan.PullRequest{}, nil
	})

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice}}, RunDependencies: RunDependencies{PullRequestCreator: creator, SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&calls, nil)}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunPullRequestNotCreatedForMaxSlicesStop(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a", "002-b"}, nil, "001-a", plan.StatusPending, nil, nil)
	reloaded := runPlanDetail(plan.StatusInProgress, []string{"002-b"}, []string{"001-a"}, "002-b", plan.StatusPending, nil, nil)
	reloaded.Slices.Slices = append([]plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}, reloaded.Slices.Slices...)
	settleRunTestSlice(reloaded)
	creator := pullRequestCreatorFunc(func(ctx context.Context, run PullRequestRun) (plan.PullRequest, error) {
		t.Fatal("pull request should not be created for a partial run")
		return plan.PullRequest{}, nil
	})

	err := executeDetail(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return reloaded, nil
	}, io.Discard, Options{ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{MaxSlices: 1, CommitPolicy: CommitPolicySlice, PullRequest: true}}, RunDependencies: RunDependencies{PullRequestCreator: creator, SliceExecutor: fakeSliceExecutor{}, PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runGitFake(&[]string{}, nil)}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestServiceExecutePullRequestRejectsCommitPolicyNone(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail}}
	executor := &countingSliceExecutor{}

	err := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{SliceExecutor: executor}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, PullRequest: true}})
	if err == nil || !strings.Contains(err.Error(), "--pull-request requires commit policy") {
		t.Fatalf("expected pull request commit policy error, got %v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("expected no slice execution, got %d", executor.calls)
	}
}

func TestServiceExecutePullRequestRejectsCurrentMode(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	repo := &memoryRunRepository{details: []*plan.PlanDetail{detail}}
	executor := &countingSliceExecutor{}
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" {
			switch runGitKey(args) {
			case "branch --show-current":
				_, _ = io.WriteString(stdout, "main\n")
			case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
				_, _ = io.WriteString(stdout, "origin/main\n")
			}
		}
		return nil
	}

	err := NewService(repo, io.Discard, Options{RunDependencies: RunDependencies{SliceExecutor: executor, CommandRunner: runner}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent, PullRequest: true}})
	if err == nil || !strings.Contains(err.Error(), "--pull-request requires --execution-mode isolated") {
		t.Fatalf("expected current-mode pull request rejection, got %v", err)
	}
	if executor.calls != 0 {
		t.Fatalf("expected no slice execution, got %d", executor.calls)
	}
}

func TestDeterministicPullRequestCreatorPushesAndCreatesWithAgentBody(t *testing.T) {
	detail := runPlanDetail(plan.StatusReviewed, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	detail.State.Plan.Review = &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Summary: "looks good"}
	createdAt := time.Date(2026, 5, 21, 20, 0, 0, 0, time.UTC)
	var calls []string
	var createdBody string
	runner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, stderr io.Writer) error {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "pr view "):
			_, _ = io.WriteString(stderr, "no pull request found")
			return errors.New("not found")
		case strings.HasPrefix(key, "pr create "):
			bodyPath := argValue(args, "--body-file")
			body, err := os.ReadFile(bodyPath) //nolint:gosec // G304: body file path is produced by Tao in this test.
			if err != nil {
				return err
			}
			createdBody = string(body)
			_, _ = io.WriteString(stdout, "https://github.com/iamseth/tao/pull/123\n")
			return nil
		default:
			return nil
		}
	})
	bodyGenerator := pullRequestBodyGeneratorFunc(func(ctx context.Context, run PullRequestBodyRun) (string, error) {
		if !strings.Contains(run.DraftBody, "plan-a") || run.BaseBranch != "main" || run.Branch != "tao/plan-a" {
			t.Fatalf("unexpected body generation input: %+v", run)
		}
		return "## Agent body\n\nReady for review.\n", nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, Now: func() time.Time { return createdAt }}), bodyGenerator: bodyGenerator}

	pr, err := creator.CreatePullRequest(context.Background(), PullRequestRun{PlanDir: detail.Dir, PlanID: "plan-a", Detail: detail, RepoRoot: "/repo", Branch: "tao/plan-a", HeadSHA: "head123"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 123 || pr.URL != "https://github.com/iamseth/tao/pull/123" || pr.Branch != "tao/plan-a" || pr.HeadSHA != "head123" || !pr.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected pull request: %#v", pr)
	}
	if !strings.HasPrefix(createdBody, "## Agent body\n\nReady for review.") {
		t.Fatalf("expected agent-authored body, got %q", createdBody)
	}
	requirePullRequestMergeGuidance(t, createdBody)
	for _, want := range []string{"git push --set-upstream origin tao/plan-a", "gh pr view --head tao/plan-a --json number,url,createdAt", "gh pr create --base main --head tao/plan-a --title Plan A"} {
		if !hasCallPrefix(calls, want) {
			t.Fatalf("expected call prefix %q in %#v", want, calls)
		}
	}
}

func TestDeterministicPullRequestCreatorFallsBackToDeterministicBodyWhenAgentFails(t *testing.T) {
	detail := runPlanDetail(plan.StatusReviewed, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	var createdBody string
	runner := pullRequestCommandRunner(t, nil, func(args []string, stdout io.Writer, stderr io.Writer) error {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "pr view "):
			return errors.New("not found")
		case strings.HasPrefix(key, "pr create "):
			body, err := os.ReadFile(argValue(args, "--body-file"))
			if err != nil {
				return err
			}
			createdBody = string(body)
			_, _ = io.WriteString(stdout, "https://github.com/iamseth/tao/pull/124\n")
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, OutputWriter: &out}), bodyGenerator: pullRequestBodyGeneratorFunc(func(context.Context, PullRequestBodyRun) (string, error) {
		return "", errors.New("provider dropped")
	})}

	pr, err := creator.CreatePullRequest(context.Background(), PullRequestRun{PlanDir: detail.Dir, PlanID: "plan-a", Detail: detail, RepoRoot: "/repo", Branch: "tao/plan-a", HeadSHA: "head123"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 124 {
		t.Fatalf("unexpected pull request: %#v", pr)
	}
	if !strings.Contains(createdBody, "## Summary") || !strings.Contains(createdBody, "plan-a") || !strings.Contains(createdBody, "## Completed slices") {
		t.Fatalf("expected deterministic body, got %q", createdBody)
	}
	requirePullRequestMergeGuidance(t, createdBody)
	if !strings.Contains(out.String(), "using deterministic body: provider dropped") {
		t.Fatalf("expected body fallback warning, got %q", out.String())
	}
}

func TestDeterministicPullRequestCreatorRejectsObsoleteAgentMergeGuidance(t *testing.T) {
	detail := runPlanDetail(plan.StatusReviewed, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	var out bytes.Buffer
	creator := deterministicPullRequestCreator{
		execution: testRunExecution(ExecutionConfig{}, RunDependencies{OutputWriter: &out}),
		bodyGenerator: pullRequestBodyGeneratorFunc(func(context.Context, PullRequestBodyRun) (string, error) {
			return "## Summary\n\nReady.\n\n## Merge\n\nRun tao merge --record-only --force plan-a after merging.", nil
		}),
	}

	body := creator.pullRequestBody(context.Background(), PullRequestRun{PlanID: "plan-a", Detail: detail}, "main", "Plan A", "")
	if !strings.Contains(body, "## Completed slices") {
		t.Fatalf("expected deterministic fallback body, got %q", body)
	}
	requirePullRequestMergeGuidance(t, body)
	if !strings.Contains(out.String(), "agent returned obsolete pull request merge guidance") {
		t.Fatalf("expected obsolete-guidance warning, got %q", out.String())
	}
}

func TestDeterministicPullRequestCreatorReusesExistingPullRequestAfterPush(t *testing.T) {
	createdAt := "2026-05-21T20:00:00Z"
	var calls []string
	runner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, stderr io.Writer) error {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "pr view "):
			_, _ = io.WriteString(stdout, `{"number":321,"url":"https://github.com/iamseth/tao/pull/321","createdAt":"`+createdAt+`"}`)
		case strings.HasPrefix(key, "pr create "):
			t.Fatal("did not expect gh pr create when a PR already exists")
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	pr, err := creator.CreatePullRequest(context.Background(), PullRequestRun{PlanID: "plan-a", RepoRoot: "/repo", Branch: "tao/plan-a", HeadSHA: "head123"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 321 || pr.URL != "https://github.com/iamseth/tao/pull/321" || pr.Branch != "tao/plan-a" || pr.HeadSHA != "head123" || pr.CreatedAt.Format(time.RFC3339) != createdAt {
		t.Fatalf("unexpected existing pull request: %#v", pr)
	}
	pushCall := "git push --set-upstream origin tao/plan-a"
	viewCall := "gh pr view --head tao/plan-a --json number,url,createdAt"
	if !hasCallPrefix(calls, pushCall) || !hasCallPrefix(calls, viewCall) {
		t.Fatalf("expected push and minimal view calls, got %#v", calls)
	}
	requireStringOrder(t, calls, pushCall, viewCall)
}

func TestExtractPullRequestFromAgentOutput(t *testing.T) {
	createdAt := time.Date(2026, 5, 21, 20, 0, 0, 0, time.UTC)
	pr, err := extractPullRequest("created https://github.com/iamseth/tao/pull/123", createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 123 || pr.URL != "https://github.com/iamseth/tao/pull/123" || !pr.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected pull request metadata: %#v", pr)
	}
}

func TestExtractPullRequestRejectsMultipleDistinctURLs(t *testing.T) {
	createdAt := time.Date(2026, 5, 21, 20, 0, 0, 0, time.UTC)
	output := "created https://github.com/iamseth/tao/pull/123 and https://github.com/iamseth/tao/pull/124"
	if _, err := extractPullRequest(output, createdAt); err == nil || !strings.Contains(err.Error(), "multiple distinct") {
		t.Fatalf("expected ambiguous pull request URL error, got %v", err)
	}
}

func TestExtractPullRequestAllowsRepeatedSameURL(t *testing.T) {
	createdAt := time.Date(2026, 5, 21, 20, 0, 0, 0, time.UTC)
	output := "created https://github.com/iamseth/tao/pull/123\nagain https://github.com/iamseth/tao/pull/123"
	pr, err := extractPullRequest(output, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 123 || pr.URL != "https://github.com/iamseth/tao/pull/123" {
		t.Fatalf("unexpected repeated pull request metadata: %#v", pr)
	}
}

type pullRequestBodyGeneratorFunc func(context.Context, PullRequestBodyRun) (string, error)

func (f pullRequestBodyGeneratorFunc) GeneratePullRequestBody(ctx context.Context, run PullRequestBodyRun) (string, error) {
	return f(ctx, run)
}

func pullRequestCommandRunner(t *testing.T, calls *[]string, gh func(args []string, stdout io.Writer, stderr io.Writer) error) CommandRunner {
	t.Helper()
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch name {
		case "git":
			key := runGitKey(args)
			if calls != nil {
				*calls = append(*calls, "git "+key)
			}
			switch key {
			case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
				_, _ = io.WriteString(stdout, "origin/main\n")
			case "diff --stat main...HEAD":
				_, _ = io.WriteString(stdout, " internal/run/pull_request.go | 10 +++++-----\n")
			}
			return nil
		case "gh":
			if calls != nil {
				*calls = append(*calls, "gh "+strings.Join(args, " "))
			}
			if gh != nil {
				return gh(args, stdout, stderr)
			}
		}
		return nil
	}
}

func requirePullRequestMergeGuidance(t *testing.T, body string) {
	t.Helper()
	for _, want := range []string{"**Squash and merge**", "After the merged change is present on your local default branch", "`tao cleanup --dry-run`", "`tao cleanup`"} {
		if !strings.Contains(body, want) {
			t.Fatalf("pull request body missing %q: %q", want, body)
		}
	}
	if strings.Contains(body, "tao merge --record-only --force") {
		t.Fatalf("pull request body retained forced record-only workaround: %q", body)
	}
}

func argValue(args []string, flag string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag {
			return args[i+1]
		}
	}
	return ""
}

func hasCallPrefix(calls []string, prefix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) {
			return true
		}
	}
	return false
}
