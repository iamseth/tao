package run

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

func TestDeterministicPullRequestCreatorPushesLabelsAssignsAndCreatesNativePullRequest(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	createdAt := time.Date(2026, 5, 21, 20, 0, 0, 0, time.UTC)
	var calls []string
	var createdBody string
	runner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, stderr io.Writer) error {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "pr list "):
			_, _ = io.WriteString(stdout, "[]")
		case strings.HasPrefix(key, "label list "):
			_, _ = io.WriteString(stdout, "[]")
		case strings.HasPrefix(key, "pr create "):
			body, err := os.ReadFile(argValue(args, "--body-file"))
			if err != nil {
				return err
			}
			createdBody = string(body)
			_, _ = io.WriteString(stdout, "https://github.com/iamseth/tao/pull/123\n")
		}
		return nil
	})
	bodyGenerator := pullRequestBodyGeneratorFunc(func(_ context.Context, run PullRequestBodyRun) (string, error) {
		if strings.Contains(run.DraftBody, "plan-a") || run.BaseBranch != "main" || run.Branch != "feature/plan-a" {
			t.Fatalf("unexpected body generation input: %+v", run)
		}
		return run.DraftBody, nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, Now: func() time.Time { return createdAt }}), bodyGenerator: bodyGenerator}

	pr, err := creator.CreatePullRequest(context.Background(), PullRequestRun{PlanDir: detail.Dir, PlanID: "plan-a", Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 123 || pr.Branch != "feature/plan-a" || pr.HeadSHA != "head123" || !pr.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected pull request: %#v", pr)
	}
	requireNativePullRequestBody(t, createdBody, "internal/run/pull_request.go | 10 +++++-----")
	for _, want := range []string{
		"git ls-remote --heads origin refs/heads/feature/plan-a",
		"git push --set-upstream --force-with-lease=refs/heads/feature/plan-a: origin feature/plan-a:refs/heads/feature/plan-a",
		"git remote get-url --push origin",
		"gh pr list --base main --head feature/plan-a --state open --json number,url,createdAt,labels,assignees,headRefName,headRepository,headRepositoryOwner",
		"gh label list --search feature --json name --limit 100",
		"gh label create feature --color " + pullRequestLabelColor + " --description " + pullRequestLabelDescription,
		"gh pr create --base main --head feature/plan-a --title feat(pr): create native pull requests",
	} {
		if !hasCallPrefix(calls, want) {
			t.Fatalf("expected call prefix %q in %#v", want, calls)
		}
	}
	findCall := "gh pr list --base main --head feature/plan-a --state open --json number,url,createdAt,labels,assignees,headRefName,headRepository,headRepositoryOwner"
	requireStringOrder(t, calls, "git ls-remote --heads origin refs/heads/feature/plan-a", "git push --set-upstream --force-with-lease=refs/heads/feature/plan-a: origin feature/plan-a:refs/heads/feature/plan-a")
	requireStringOrder(t, calls, "git push --set-upstream --force-with-lease=refs/heads/feature/plan-a: origin feature/plan-a:refs/heads/feature/plan-a", "git remote get-url --push origin")
	requireStringOrder(t, calls, "git remote get-url --push origin", findCall)
	requireStringOrder(t, calls, findCall, "gh label list --search feature --json name --limit 100")
	requireStringOrder(t, calls, "gh label list --search feature --json name --limit 100", "gh label create feature --color "+pullRequestLabelColor+" --description "+pullRequestLabelDescription)
	createCall := calls[len(calls)-1]
	if !strings.Contains(createCall, "--label feature") || !strings.Contains(createCall, "--assignee @me") {
		t.Fatalf("PR create call missing label or assignment: %q", createCall)
	}
}

func TestDeterministicPullRequestCreatorIgnoresSameHeadPullRequestAgainstDifferentBase(t *testing.T) {
	var calls []string
	runner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, _ io.Writer) error {
		switch key := strings.Join(args, " "); {
		case strings.HasPrefix(key, "pr list "):
			if argValue(args, "--base") != "main" {
				_, _ = io.WriteString(stdout, `[{"number":999,"url":"https://github.com/iamseth/tao/pull/999","createdAt":"2026-05-21T20:00:00Z","labels":[],"assignees":[]}]`)
				return nil
			}
			_, _ = io.WriteString(stdout, "[]")
		case strings.HasPrefix(key, "label list "):
			_, _ = io.WriteString(stdout, "[]")
		case strings.HasPrefix(key, "pr create "):
			_, _ = io.WriteString(stdout, "https://github.com/iamseth/tao/pull/123")
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	pr, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(plan.ChangeTypeFeat, "head123"), RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 123 {
		t.Fatalf("pull request = %#v, want newly created default-branch PR #123 instead of other-base PR #999", pr)
	}
	if !hasCallPrefix(calls, "gh pr list --base main --head feature/plan-a ") || !hasCallPrefix(calls, "gh pr create --base main --head feature/plan-a ") {
		t.Fatalf("pull request discovery and creation were not constrained to main: %#v", calls)
	}
}

func TestDeterministicPullRequestCreatorIgnoresSameNamedForkPullRequest(t *testing.T) {
	var calls []string
	runner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, _ io.Writer) error {
		key := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(key, "pr list "):
			if strings.Contains(key, "--limit 1") {
				t.Fatalf("pull request discovery limited candidates before ownership validation: %q", key)
			}
			for _, field := range []string{"headRefName", "headRepository", "headRepositoryOwner"} {
				if !strings.Contains(argValue(args, "--json"), field) {
					t.Fatalf("pull request discovery did not request %s: %q", field, key)
				}
			}
			_, _ = io.WriteString(stdout, `[
				{"number":999,"url":"https://github.com/iamseth/tao/pull/999","createdAt":"2026-05-21T19:00:00Z","labels":[],"assignees":[],"headRefName":"feature/plan-a","headRepository":{"nameWithOwner":"fork-owner/tao"},"headRepositoryOwner":{"login":"fork-owner"}},
				{"number":123,"url":"https://github.com/iamseth/tao/pull/123","createdAt":"2026-05-21T20:00:00Z","labels":[],"assignees":[],"headRefName":"feature/plan-a","headRepository":{"nameWithOwner":"iamseth/tao"},"headRepositoryOwner":{"login":"iamseth"}}
			]`)
		case strings.HasPrefix(key, "pr create "):
			t.Fatalf("created a pull request instead of finding origin-owned PR: %q", key)
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	pr, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(plan.ChangeTypeFeat, "head123"), RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 123 {
		t.Fatalf("pull request = %#v, want origin-owned PR #123 instead of same-named fork PR #999", pr)
	}
}

func TestDeterministicPullRequestCreatorStopsWhenDiffStatFails(t *testing.T) {
	diffErr := errors.New("diff failed")
	var calls []string
	baseRunner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, _ io.Writer) error {
		if strings.HasPrefix(strings.Join(args, " "), "pr list ") {
			_, _ = io.WriteString(stdout, "[]")
		}
		return nil
	})
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "diff --stat main...HEAD" {
			return diffErr
		}
		return baseRunner(ctx, cwd, name, args, stdout, stderr)
	}
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	_, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(plan.ChangeTypeFeat, "head123"), RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"})
	if !errors.Is(err, diffErr) || !strings.Contains(err.Error(), "build pull request scope") {
		t.Fatalf("error = %v, want diff-stat failure", err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "gh label ") || strings.HasPrefix(call, "gh pr create ") {
			t.Fatalf("diff-stat failure triggered label or pull request creation: %#v", calls)
		}
	}
}

func TestDeterministicPullRequestCreatorFallsBackWithoutTaoNoise(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	var out bytes.Buffer
	var createdBody string
	runner := pullRequestCommandRunner(t, nil, func(args []string, stdout io.Writer, _ io.Writer) error {
		switch key := strings.Join(args, " "); {
		case strings.HasPrefix(key, "pr list "):
			_, _ = io.WriteString(stdout, "[]")
		case strings.HasPrefix(key, "label list "):
			_, _ = io.WriteString(stdout, `[{"name":"feature"}]`)
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

	if _, err := creator.CreatePullRequest(context.Background(), PullRequestRun{PlanID: "plan-a", Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"}); err != nil {
		t.Fatal(err)
	}
	requireNativePullRequestBody(t, createdBody, "internal/run/pull_request.go | 10 +++++-----")
	if !strings.Contains(out.String(), "using deterministic body: provider dropped") {
		t.Fatalf("expected body fallback warning, got %q", out.String())
	}
}

func TestDeterministicPullRequestCreatorRejectsNoisyAgentBody(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	var out bytes.Buffer
	creator := deterministicPullRequestCreator{
		execution: testRunExecution(ExecutionConfig{}, RunDependencies{OutputWriter: &out}),
		bodyGenerator: pullRequestBodyGeneratorFunc(func(context.Context, PullRequestBodyRun) (string, error) {
			return "## Problem\n\nplan-a\n\n## Fix\n\nTao slice complete.\n\n## Tests\n\nNone.\n\n## Deploy\n\nNone.\n\n## Scope\n\n<details>\n<summary>Changed files</summary>\n\nNo changed-file summary is available.\n\n</details>", nil
		}),
	}

	body, err := creator.pullRequestBody(context.Background(), PullRequestRun{PlanID: "plan-a", Detail: detail}, "main", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	requireNativePullRequestBody(t, body, "")
	if !strings.Contains(out.String(), "contains forbidden Tao-specific language") && !strings.Contains(out.String(), "contains the plan ID") {
		t.Fatalf("expected noisy-body warning, got %q", out.String())
	}
}

func TestDeterministicPullRequestBodyEscapesCommitProposalHeadings(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	detail.State.Plan.Review.CommitMessage.Body = "What:\nCreate native pull requests.\n\n## Notes\n\nPreserve reviewer context.\n\nRelease notes\n-------------\n\nWhy:\nMake repository changes familiar.\n\n  ## Rationale\n\nKeep the fallback structurally valid."
	if _, _, err := pullRequestPreflight(PullRequestRun{Detail: detail, HeadSHA: "head123"}); err != nil {
		t.Fatalf("multiline commit proposal is invalid: %v", err)
	}

	body, err := deterministicPullRequestBody(PullRequestRun{Detail: detail}, "main", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `\## Notes`) || !strings.Contains(body, `  \## Rationale`) || !strings.Contains(body, "Release notes\n\\-------------") {
		t.Fatalf("deterministic body did not escape proposal headings: %q", body)
	}
	if err := validatePullRequestBody(body, detail.State.Plan.ID, "", body); err != nil {
		t.Fatalf("deterministic fallback body rejected: %v", err)
	}
}

func TestDeterministicPullRequestBodySanitizesForbiddenReviewerNarrative(t *testing.T) {
	for _, phrase := range []string{"Tao", "slice", "lifecycle", "squash and merge", "merge guidance", "cleanup --dry-run"} {
		t.Run(phrase, func(t *testing.T) {
			detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
			detail.State.Plan.Review.CommitMessage.Body = fmt.Sprintf("What:\nDocument %s behavior.\n\nWhy:\nClarify %s behavior for reviewers.", phrase, phrase)
			if _, _, err := pullRequestPreflight(PullRequestRun{Detail: detail, HeadSHA: "head123"}); err != nil {
				t.Fatalf("commit proposal containing %q is invalid: %v", phrase, err)
			}

			body, err := deterministicPullRequestBody(PullRequestRun{PlanID: detail.State.Plan.ID, Detail: detail}, "main", "title", "")
			if err != nil {
				t.Fatalf("deterministic body containing %q: %v", phrase, err)
			}
			if err := validatePullRequestBody(body, detail.State.Plan.ID, "", body); err != nil {
				t.Fatalf("sanitized deterministic body containing %q rejected: %v", phrase, err)
			}
		})
	}
}

func TestDeterministicPullRequestBodyAllowsTaoPathsAndOmitsPrefixedTaoCommands(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	detail.Slices.Slices[0].VerificationResults = append(
		detail.Slices.Slices[0].VerificationResults,
		plan.VerificationRun{Command: "tao validate plan-a", Result: "passed"},
		plan.VerificationRun{Command: "cd subdir && tao validate", Result: "passed"},
		plan.VerificationRun{Command: "env X=1 tao validate", Result: "passed"},
		plan.VerificationRun{Command: "X=1 tao validate", Result: "passed"},
		plan.VerificationRun{Command: "go test ./cmd/tao", Result: "passed"},
	)
	diffStat := " cmd/tao/main.go | 2 +-\n"

	body, err := deterministicPullRequestBody(PullRequestRun{PlanID: "plan-a", Detail: detail}, "main", "title", diffStat)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, diffStat) {
		t.Fatalf("pull request body missing exact diff stat %q: %q", diffStat, body)
	}
	for _, noise := range []string{"tao validate", "cd subdir", "env X=1", "X=1 tao"} {
		if strings.Contains(body, noise) {
			t.Fatalf("pull request body includes lifecycle-only verification command %q: %q", noise, body)
		}
	}
	for _, command := range []string{"go test ./internal/run", "go test ./cmd/tao"} {
		if !strings.Contains(body, command) {
			t.Fatalf("pull request body omitted repository test command %q: %q", command, body)
		}
	}
	if err := validatePullRequestBody(body, detail.State.Plan.ID, diffStat, body); err != nil {
		t.Fatalf("deterministic fallback body rejected: %v", err)
	}
}

func TestIsTaoLifecycleVerificationCommandFindsExecutablesOnly(t *testing.T) {
	for _, tt := range []struct {
		command string
		want    bool
	}{
		{command: "tao validate", want: true},
		{command: "/usr/local/bin/tao validate", want: true},
		{command: "cd subdir && tao validate", want: true},
		{command: "prepare || /usr/local/bin/tao validate", want: true},
		{command: "prepare; env X=1 tao validate", want: true},
		{command: "prepare | X=1 ./bin/tao validate", want: true},
		{command: "env X=1 tao validate", want: true},
		{command: "/usr/bin/env -i X=1 ./bin/tao validate", want: true},
		{command: "X=1 tao validate", want: true},
		{command: "go test ./cmd/tao", want: false},
		{command: "go test ./internal/tao/...", want: false},
		{command: "env X=tao go test ./cmd/tao", want: false},
		{command: "echo tao validate", want: false},
		{command: "go test ./cmd/tao && echo done", want: false},
	} {
		t.Run(tt.command, func(t *testing.T) {
			if got := isTaoLifecycleVerificationCommand(tt.command); got != tt.want {
				t.Fatalf("isTaoLifecycleVerificationCommand(%q) = %t, want %t", tt.command, got, tt.want)
			}
		})
	}
}

func TestValidatePullRequestBodyRejectsLifecycleNoiseReintroducedInTests(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	detail.Slices.Slices[0].VerificationResults = []plan.VerificationRun{{Command: "go test ./cmd/tao", Result: "passed"}}
	draft, err := deterministicPullRequestBody(PullRequestRun{PlanID: "plan-a", Detail: detail}, "main", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePullRequestBody(draft, "plan-a", "", draft); err != nil {
		t.Fatalf("legitimate repository path containing tao was rejected: %v", err)
	}

	for _, tt := range []struct {
		name     string
		addition string
		want     string
	}{
		{name: "plan ID", addition: "- Verified plan-a.\n", want: "plan ID"},
		{name: "slice lifecycle", addition: "- Tao slice lifecycle completed.\n", want: "preserve Tests exactly as drafted"},
		{name: "direct Tao command", addition: "- `tao validate`: passed\n", want: "direct Tao lifecycle command"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			body := strings.Replace(draft, "\n## Deploy", "\n"+tt.addition+"\n## Deploy", 1)
			err := validatePullRequestBody(body, "plan-a", "", draft)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want rejection containing %q", err, tt.want)
			}
		})
	}
}

func TestValidatePullRequestBodyRequiresTestsToMatchDeterministicDraft(t *testing.T) {
	draft, err := deterministicPullRequestBody(PullRequestRun{}, "main", "title", "")
	if err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(draft, "No automated test results were recorded.", "- `go test ./...`: passed", 1)
	if err := validatePullRequestBody(body, "", "", draft); err == nil || !strings.Contains(err.Error(), "preserve Tests exactly as drafted") {
		t.Fatalf("error = %v, want deterministic Tests mismatch", err)
	}
}

func TestValidatePullRequestBodyRequiresExactHeadingSequenceAndScope(t *testing.T) {
	diffStat := " internal/run/pull_request.go | 1 +\n"
	valid, err := deterministicPullRequestBody(PullRequestRun{}, "main", "title", diffStat)
	if err != nil {
		t.Fatal(err)
	}
	if err := validatePullRequestBody(valid, "plan-a", diffStat, valid); err != nil {
		t.Fatalf("valid deterministic body rejected: %v", err)
	}
	fencedSetext := strings.Replace(valid, "See the commit description for change context.", "```text\nNotes\n-----\n```", 1)
	if err := validatePullRequestBody(fencedSetext, "plan-a", diffStat, valid); err != nil {
		t.Fatalf("Setext-like text inside a code fence was treated as a heading: %v", err)
	}

	scope := deterministicPullRequestScope(diffStat)
	tests := []struct {
		name string
		body string
	}{
		{
			name: "extra ATX level-two heading",
			body: strings.Replace(valid, "## Scope", "## Notes\n\nReviewer context.\n\n## Scope", 1),
		},
		{
			name: "extra Setext level-two heading",
			body: strings.Replace(valid, "## Scope", "Notes\n-----\n\nReviewer context.\n\n## Scope", 1),
		},
		{
			name: "changed files block outside scope",
			body: strings.Replace(valid, "## Scope\n\n"+scope, scope+"\n## Scope\n\nChanged files are listed above.\n", 1),
		},
		{
			name: "diff stat outside scope block",
			body: strings.Replace(
				valid,
				"## Scope\n\n"+scope,
				"```text\n"+diffStat+"```\n\n## Scope\n\n<details>\n<summary>Changed files</summary>\n\nNo changed-file summary is available.\n\n</details>\n",
				1,
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validatePullRequestBody(tt.body, "plan-a", diffStat, valid); err == nil {
				t.Fatal("expected invalid agent body to be rejected")
			}
		})
	}
}

func TestPullRequestPreflightRejectsTypeOrHeadMismatchBeforeRemoteMutation(t *testing.T) {
	for _, tt := range []struct {
		name       string
		changeType plan.ChangeType
		head       string
		want       string
	}{
		{name: "type", changeType: plan.ChangeTypeFix, head: "head123", want: "does not match plan change type"},
		{name: "head", changeType: plan.ChangeTypeFeat, head: "different", want: "match branch head"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: pullRequestCommandRunner(t, &calls, nil)})}
			_, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(tt.changeType, tt.head), RepoRoot: "/repo", Branch: "feature/a", HeadSHA: "head123"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			for _, call := range calls {
				if strings.Contains(call, "push") || strings.HasPrefix(call, "gh ") {
					t.Fatalf("remote mutation happened before preflight completed: %#v", calls)
				}
			}
		})
	}
}

func TestTypedPullRequestPushRejectsRemoteBranchCreatedAfterWorkspacePreparation(t *testing.T) {
	var calls []string
	baseRunner := pullRequestCommandRunner(t, &calls, nil)
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "ls-remote --heads origin refs/heads/feature/plan-a" {
			_, _ = io.WriteString(stdout, "unrelated123\trefs/heads/feature/plan-a\n")
		}
		return baseRunner(ctx, cwd, name, args, stdout, stderr)
	}
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	_, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(plan.ChangeTypeFeat, "head123"), RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"})
	if err == nil || !strings.Contains(err.Error(), "remote branch already exists at unrelated123") {
		t.Fatalf("error = %v, want remote branch ownership collision", err)
	}
	for _, call := range calls {
		if strings.Contains(call, "git push ") || strings.HasPrefix(call, "gh ") {
			t.Fatalf("remote collision triggered mutation: %#v", calls)
		}
	}
}

func TestTypedPullRequestPushAllowsAlreadyPushedIdenticalHead(t *testing.T) {
	var calls []string
	baseRunner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, _ io.Writer) error {
		if strings.HasPrefix(strings.Join(args, " "), "pr list ") {
			_, _ = io.WriteString(stdout, `[{"number":321,"url":"https://github.com/iamseth/tao/pull/321","createdAt":"2026-05-21T20:00:00Z","labels":[],"assignees":[],"headRefName":"feature/plan-a","headRepository":{"nameWithOwner":"iamseth/tao"},"headRepositoryOwner":{"login":"iamseth"}}]`)
		}
		return nil
	})
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "ls-remote --heads origin refs/heads/feature/plan-a" {
			_, _ = io.WriteString(stdout, "head123\trefs/heads/feature/plan-a\n")
		}
		return baseRunner(ctx, cwd, name, args, stdout, stderr)
	}
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	if _, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(plan.ChangeTypeFeat, "head123"), RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"}); err != nil {
		t.Fatal(err)
	}
	wantPush := "git push --set-upstream --force-with-lease=refs/heads/feature/plan-a:head123 origin feature/plan-a:refs/heads/feature/plan-a"
	if !hasCallPrefix(calls, wantPush) {
		t.Fatalf("identical remote head was not pushed under an exact lease: %#v", calls)
	}
}

func TestTypedPullRequestPushAdvancesOwnedBranchForPullRequestRework(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head-new")
	detail.State.Workspace = &plan.Workspace{Branch: "feature/plan-a", PushedSHA: "head-old"}
	detail.State.Plan.PullRequest = &plan.PullRequest{
		Number:  321,
		URL:     "https://github.com/iamseth/tao/pull/321",
		Branch:  "feature/plan-a",
		HeadSHA: "head-old",
	}
	var calls []string
	baseRunner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, _ io.Writer) error {
		if strings.HasPrefix(strings.Join(args, " "), "pr list ") {
			_, _ = io.WriteString(stdout, `[{"number":321,"url":"https://github.com/iamseth/tao/pull/321","createdAt":"2026-05-21T20:00:00Z","labels":[],"assignees":[],"headRefName":"feature/plan-a","headRepository":{"nameWithOwner":"iamseth/tao"},"headRepositoryOwner":{"login":"iamseth"}}]`)
		}
		return nil
	})
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "ls-remote --heads origin refs/heads/feature/plan-a" {
			_, _ = io.WriteString(stdout, "head-old\trefs/heads/feature/plan-a\n")
		}
		return baseRunner(ctx, cwd, name, args, stdout, stderr)
	}
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}
	run := PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head-new"}

	pr, err := creator.CreatePullRequest(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	wantPush := "git push --set-upstream --force-with-lease=refs/heads/feature/plan-a:head-old origin feature/plan-a:refs/heads/feature/plan-a"
	if !hasCallPrefix(calls, wantPush) {
		t.Fatalf("owned remote branch was not advanced under its recorded lease: %#v", calls)
	}
	record, err := memoryPlanRecordFactory(detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.RecordPullRequest(pr, run.Branch, run.HeadSHA); err != nil {
		t.Fatal(err)
	}
	if detail.State.Workspace.PushedSHA != "head-new" || detail.State.Plan.PullRequest.HeadSHA != "head-new" {
		t.Fatalf("reworked pull request head was not recorded: workspace=%#v pull_request=%#v", detail.State.Workspace, detail.State.Plan.PullRequest)
	}
}

func TestTypedPullRequestPushRejectsStaleOwnedRemoteHead(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head-new")
	detail.State.Workspace = &plan.Workspace{Branch: "feature/plan-a", PushedSHA: "head-old"}
	var calls []string
	baseRunner := pullRequestCommandRunner(t, &calls, nil)
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "ls-remote --heads origin refs/heads/feature/plan-a" {
			_, _ = io.WriteString(stdout, "head-foreign\trefs/heads/feature/plan-a\n")
		}
		return baseRunner(ctx, cwd, name, args, stdout, stderr)
	}
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	_, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head-new"})
	if err == nil || !strings.Contains(err.Error(), "want recorded Tao head head-old") {
		t.Fatalf("error = %v, want stale owned-head rejection", err)
	}
	for _, call := range calls {
		if strings.Contains(call, "git push ") || strings.HasPrefix(call, "gh ") {
			t.Fatalf("stale owned remote head triggered mutation: %#v", calls)
		}
	}
}

func TestTypedPullRequestPushAdvancesRecordedLegacyTaoBranch(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head-new")
	detail.State.Workspace = &plan.Workspace{Branch: "tao/plan-a", PushedSHA: "head-old"}
	var calls []string
	baseRunner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, _ io.Writer) error {
		if strings.HasPrefix(strings.Join(args, " "), "pr list ") {
			_, _ = io.WriteString(stdout, `[{"number":321,"url":"https://github.com/iamseth/tao/pull/321","createdAt":"2026-05-21T20:00:00Z","labels":[],"assignees":[],"headRefName":"tao/plan-a","headRepository":{"nameWithOwner":"iamseth/tao"},"headRepositoryOwner":{"login":"iamseth"}}]`)
		}
		return nil
	})
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && runGitKey(args) == "ls-remote --heads origin refs/heads/tao/plan-a" {
			_, _ = io.WriteString(stdout, "head-old\trefs/heads/tao/plan-a\n")
		}
		return baseRunner(ctx, cwd, name, args, stdout, stderr)
	}
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	if _, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "tao/plan-a", HeadSHA: "head-new"}); err != nil {
		t.Fatal(err)
	}
	wantPush := "git push --set-upstream --force-with-lease=refs/heads/tao/plan-a:head-old origin tao/plan-a:refs/heads/tao/plan-a"
	if !hasCallPrefix(calls, wantPush) {
		t.Fatalf("recorded legacy branch was not advanced under its recorded lease: %#v", calls)
	}
}

func TestPullRequestIntentHeadMismatchStopsBeforeRemoteMutation(t *testing.T) {
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head-b")
	detail.State.Plan.PullRequestIntent = &plan.PullRequest{
		Number:  321,
		URL:     "https://github.com/iamseth/tao/pull/321",
		Branch:  "feature/plan-a",
		HeadSHA: "head-a",
	}
	var calls []string
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: pullRequestCommandRunner(t, &calls, nil)})}

	_, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head-b"})
	if err == nil || !strings.Contains(err.Error(), "recorded branch and head do not match requested branch and head") {
		t.Fatalf("error = %v, want intent head mismatch", err)
	}
	for _, call := range calls {
		if strings.Contains(call, "push") || strings.HasPrefix(call, "gh ") {
			t.Fatalf("intent mismatch triggered remote access or mutation: %#v", calls)
		}
	}
}

func TestDeterministicPullRequestCreatorPreservesExistingLabelMetadataAndSupportsLegacyPlan(t *testing.T) {
	tests := []struct {
		name       string
		changeType plan.ChangeType
		wantLabel  string
	}{
		{name: "differently cased existing typed label", changeType: plan.ChangeTypeFeat, wantLabel: "Feature"},
		{name: "legacy plan", changeType: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			runner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, _ io.Writer) error {
				switch key := strings.Join(args, " "); {
				case strings.HasPrefix(key, "pr list "):
					_, _ = io.WriteString(stdout, "[]")
				case strings.HasPrefix(key, "label list "):
					_, _ = io.WriteString(stdout, `[{"name":"Feature"}]`)
				case strings.HasPrefix(key, "pr create "):
					_, _ = io.WriteString(stdout, "https://github.com/iamseth/tao/pull/125")
				}
				return nil
			})
			creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}
			if _, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(tt.changeType, "head123"), RepoRoot: "/repo", Branch: "branch", HeadSHA: "head123"}); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(calls, "\n")
			if strings.Contains(joined, "label create") {
				t.Fatalf("existing label metadata must not be replaced: %#v", calls)
			}
			if tt.wantLabel != "" {
				if !strings.Contains(joined, "--label "+tt.wantLabel) {
					t.Fatalf("label assignment mismatch in %#v", calls)
				}
			} else if strings.Contains(joined, "--label") {
				t.Fatalf("legacy plan unexpectedly assigned a label in %#v", calls)
			}
			if !strings.Contains(joined, "--title feat(pr): create native pull requests") || !strings.Contains(joined, "--assignee @me") {
				t.Fatalf("review title or assignment missing: %#v", calls)
			}
		})
	}
}

func TestDeterministicPullRequestCreatorReportsLabelAndAssignmentFailures(t *testing.T) {
	for _, tt := range []struct {
		name string
		fail string
		want string
	}{
		{name: "label permission", fail: "label create", want: `create pull request label "feature"`},
		{name: "assignment permission", fail: "pr create", want: "create pull request with assignee @me"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := pullRequestCommandRunner(t, nil, func(args []string, stdout io.Writer, stderr io.Writer) error {
				key := strings.Join(args, " ")
				switch {
				case strings.HasPrefix(key, "pr list "):
					_, _ = io.WriteString(stdout, "[]")
				case strings.HasPrefix(key, "pr view "):
					return errors.New("not found")
				case strings.HasPrefix(key, "label list "):
					_, _ = io.WriteString(stdout, "[]")
				case strings.HasPrefix(key, tt.fail):
					_, _ = io.WriteString(stderr, "permission denied")
					return errors.New("exit status 1")
				}
				return nil
			})
			creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}
			_, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(plan.ChangeTypeFeat, "head123"), RepoRoot: "/repo", Branch: "feature/a", HeadSHA: "head123"})
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "permission denied") {
				t.Fatalf("error = %v, want actionable %q permission failure", err, tt.want)
			}
		})
	}
}

func TestDeterministicPullRequestCreatorLeavesExistingPullRequestMetadataUnchanged(t *testing.T) {
	createdAt := "2026-05-21T20:00:00Z"
	var calls []string
	runner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, _ io.Writer) error {
		if strings.HasPrefix(strings.Join(args, " "), "pr list ") {
			_, _ = io.WriteString(stdout, `[{"number":321,"url":"https://github.com/iamseth/tao/pull/321","createdAt":"`+createdAt+`","labels":[],"assignees":[],"headRefName":"feature/plan-a","headRepository":{"nameWithOwner":"iamseth/tao"},"headRepositoryOwner":{"login":"iamseth"}}]`)
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	pr, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(plan.ChangeTypeFeat, "head123"), RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"})
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 321 || pr.Branch != "feature/plan-a" || pr.HeadSHA != "head123" || pr.CreatedAt.Format(time.RFC3339) != createdAt {
		t.Fatalf("unexpected existing pull request: %#v", pr)
	}
	pushCall := "git push --set-upstream --force-with-lease=refs/heads/feature/plan-a: origin feature/plan-a:refs/heads/feature/plan-a"
	findCall := "gh pr list --base main --head feature/plan-a --state open --json number,url,createdAt,labels,assignees,headRefName,headRepository,headRepositoryOwner"
	requireStringOrder(t, calls, pushCall, "git remote get-url --push origin")
	requireStringOrder(t, calls, "git remote get-url --push origin", findCall)
	if len(calls) != 5 || calls[len(calls)-1] != findCall {
		t.Fatalf("existing PR missing Tao metadata was not returned unchanged: %#v", calls)
	}
}

func TestDeterministicPullRequestCreatorStopsOnPreCreateLookupFailure(t *testing.T) {
	lookupErr := errors.New("authentication failed")
	var calls []string
	runner := pullRequestCommandRunner(t, &calls, func(args []string, _ io.Writer, stderr io.Writer) error {
		if strings.HasPrefix(strings.Join(args, " "), "pr list ") {
			_, _ = io.WriteString(stderr, "authentication required")
			return lookupErr
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner})}

	_, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: approvedPullRequestDetail(plan.ChangeTypeFeat, "head123"), RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"})
	if !errors.Is(err, lookupErr) || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("error = %v, want unchanged operational lookup failure", err)
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "gh pr create ") || strings.HasPrefix(call, "gh pr edit ") {
			t.Fatalf("lookup failure triggered pull request mutation: %#v", calls)
		}
	}
}

func TestDeterministicPullRequestCreatorDoesNotClaimConcurrentHumanPullRequestAfterFailedCreate(t *testing.T) {
	listCalls := 0
	var calls []string
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	runner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, stderr io.Writer) error {
		switch key := strings.Join(args, " "); {
		case strings.HasPrefix(key, "pr list "):
			listCalls++
			if listCalls == 1 {
				_, _ = io.WriteString(stdout, "[]")
			} else {
				// A human opened this matching PR while gh pr create was running.
				_, _ = io.WriteString(stdout, `[{"number":322,"url":"https://github.com/iamseth/tao/pull/322","createdAt":"2026-05-21T20:00:00Z","labels":[],"assignees":[]}]`)
			}
		case strings.HasPrefix(key, "label list "):
			_, _ = io.WriteString(stdout, `[{"name":"feature"}]`)
		case strings.HasPrefix(key, "pr create "):
			_, _ = io.WriteString(stderr, "a pull request for this branch already exists")
			return errors.New("exit status 1")
		case strings.HasPrefix(key, "api user "), strings.HasPrefix(key, "pr edit "):
			t.Fatalf("failed create without emitted identity mutated concurrent human PR: %q", key)
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, PlanRecordFactory: memoryPlanRecordFactory})}

	_, err := creator.CreatePullRequest(context.Background(), PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"})
	if err == nil || !strings.Contains(err.Error(), "a pull request for this branch already exists") {
		t.Fatalf("error = %v, want failed create", err)
	}
	if listCalls != 1 || detail.State.Plan.PullRequestIntent != nil {
		t.Fatalf("failed create used ambiguous discovery or intent: list calls=%d intent=%#v calls=%#v", listCalls, detail.State.Plan.PullRequestIntent, calls)
	}
}

func TestDeterministicPullRequestCreatorRepairsPartialPullRequestFromEmittedIdentity(t *testing.T) {
	createCalls := 0
	editCalls := 0
	var calls []string
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	runner := pullRequestCommandRunner(t, &calls, func(args []string, stdout io.Writer, stderr io.Writer) error {
		switch key := strings.Join(args, " "); {
		case strings.HasPrefix(key, "pr list "):
			_, _ = io.WriteString(stdout, "[]")
		case strings.HasPrefix(key, "pr view 323 "):
			intent := detail.State.Plan.PullRequestIntent
			if intent == nil || intent.Number != 323 || intent.URL != "https://github.com/iamseth/tao/pull/323" {
				t.Fatalf("emitted identity was not persisted before verification: %#v", intent)
			}
			_, _ = io.WriteString(stdout, `{"number":323,"url":"https://github.com/iamseth/tao/pull/323","createdAt":"2026-05-21T20:00:00Z","labels":[],"assignees":[]}`)
		case strings.HasPrefix(key, "api user "):
			_, _ = io.WriteString(stdout, "octocat\n")
		case strings.HasPrefix(key, "label list "):
			_, _ = io.WriteString(stdout, `[{"name":"feature"}]`)
		case strings.HasPrefix(key, "pr create "):
			createCalls++
			_, _ = io.WriteString(stdout, "https://github.com/iamseth/tao/pull/323\n")
			_, _ = io.WriteString(stderr, "failed to assign pull request to @me")
			return errors.New("exit status 1")
		case strings.HasPrefix(key, "pr edit 323 "):
			editCalls++
			if !strings.Contains(key, "--add-label feature") || !strings.Contains(key, "--add-assignee @me") {
				t.Fatalf("repair call did not add the category label and authenticated user: %q", key)
			}
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, PlanRecordFactory: memoryPlanRecordFactory})}
	run := PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"}

	pr, err := creator.CreatePullRequest(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 323 || createCalls != 1 || editCalls != 1 {
		t.Fatalf("recovered PR/create/edit calls = %#v/%d/%d, want #323/1/1", pr, createCalls, editCalls)
	}
	var prCalls []string
	for _, call := range calls {
		if strings.HasPrefix(call, "gh pr list ") || strings.HasPrefix(call, "gh pr view ") || strings.HasPrefix(call, "gh pr create ") || strings.HasPrefix(call, "gh pr edit ") {
			prCalls = append(prCalls, call)
		}
	}
	findCall := "gh pr list --base main --head feature/plan-a --state open --json number,url,createdAt,labels,assignees,headRefName,headRepository,headRepositoryOwner"
	if len(prCalls) != 4 || prCalls[0] != findCall || !strings.HasPrefix(prCalls[1], "gh pr create ") || !strings.HasPrefix(prCalls[2], "gh pr view 323 ") || !strings.HasPrefix(prCalls[3], "gh pr edit 323 ") {
		t.Fatalf("partial-create recovery PR call order = %#v, want list/create/view/edit", prCalls)
	}
}

func TestDeterministicPullRequestCreatorRecoversAssignmentFailureAcrossAttempts(t *testing.T) {
	created := false
	createCalls := 0
	editCalls := 0
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	runner := pullRequestCommandRunner(t, nil, func(args []string, stdout io.Writer, stderr io.Writer) error {
		switch key := strings.Join(args, " "); {
		case strings.HasPrefix(key, "pr list "):
			if !created {
				_, _ = io.WriteString(stdout, "[]")
			} else {
				_, _ = io.WriteString(stdout, `[{"number":324,"url":"https://github.com/iamseth/tao/pull/324","createdAt":"2026-05-21T20:00:00Z","labels":[{"name":"feature"}],"assignees":[]}]`)
			}
		case strings.HasPrefix(key, "pr view "):
			if key != "pr view 324 --json number,url,createdAt,labels,assignees" {
				t.Fatalf("intent recovery viewed ambiguous pull request identity: %q", key)
			}
			_, _ = io.WriteString(stdout, `{"number":324,"url":"https://github.com/iamseth/tao/pull/324","createdAt":"2026-05-21T20:00:00Z","labels":[{"name":"feature"}],"assignees":[]}`)
		case strings.HasPrefix(key, "api user "):
			_, _ = io.WriteString(stdout, "octocat\n")
		case strings.HasPrefix(key, "label list "):
			_, _ = io.WriteString(stdout, `[{"name":"feature"}]`)
		case strings.HasPrefix(key, "pr create "):
			createCalls++
			created = true
			_, _ = io.WriteString(stdout, "https://github.com/iamseth/tao/pull/324\n")
			_, _ = io.WriteString(stderr, "failed to assign pull request to @me")
			return errors.New("exit status 1")
		case strings.HasPrefix(key, "pr edit 324 "):
			editCalls++
			if !strings.Contains(key, "--add-assignee @me") || strings.Contains(key, "--add-label") {
				t.Fatalf("assignment repair call = %q", key)
			}
			if editCalls == 1 {
				_, _ = io.WriteString(stderr, "assignment permission denied")
				return errors.New("exit status 1")
			}
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, PlanRecordFactory: memoryPlanRecordFactory})}
	run := PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"}

	if _, err := creator.CreatePullRequest(context.Background(), run); err == nil || !strings.Contains(err.Error(), "assignment permission denied") {
		t.Fatalf("first attempt error = %v, want assignment repair failure", err)
	}
	intent := detail.State.Plan.PullRequestIntent
	if intent == nil || intent.Number != 324 || intent.Branch != run.Branch || intent.HeadSHA != run.HeadSHA {
		t.Fatalf("partial pull request intent = %#v", intent)
	}

	pr, err := creator.CreatePullRequest(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 324 || createCalls != 1 || editCalls != 2 {
		t.Fatalf("recovered PR/create/edit calls = %#v/%d/%d, want #324/1/2", pr, createCalls, editCalls)
	}
	record, err := memoryPlanRecordFactory(detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.RecordPullRequest(pr, run.Branch, run.HeadSHA); err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.PullRequestIntent != nil {
		t.Fatalf("successful PR recording retained recovery intent: %#v", detail.State.Plan.PullRequestIntent)
	}
}

func TestDeterministicPullRequestCreatorDoesNotClaimDelayedHumanPullRequestWithoutIdentity(t *testing.T) {
	listCalls := 0
	createCalls := 0
	editCalls := 0
	detail := approvedPullRequestDetail(plan.ChangeTypeFeat, "head123")
	runner := pullRequestCommandRunner(t, nil, func(args []string, stdout io.Writer, stderr io.Writer) error {
		switch key := strings.Join(args, " "); {
		case strings.HasPrefix(key, "pr list "):
			listCalls++
			if listCalls == 1 {
				_, _ = io.WriteString(stdout, "[]")
			} else {
				// A human opened the matching PR after the first uncertain attempt.
				_, _ = io.WriteString(stdout, `[{"number":325,"url":"https://github.com/iamseth/tao/pull/325","createdAt":"2026-05-21T20:00:00Z","labels":[],"assignees":[],"headRefName":"feature/plan-a","headRepository":{"nameWithOwner":"iamseth/tao"},"headRepositoryOwner":{"login":"iamseth"}}]`)
			}
		case strings.HasPrefix(key, "label list "):
			_, _ = io.WriteString(stdout, `[{"name":"feature"}]`)
		case strings.HasPrefix(key, "pr create "):
			createCalls++
			_, _ = io.WriteString(stderr, "connection reset")
			return errors.New("exit status 1")
		case strings.HasPrefix(key, "api user "), strings.HasPrefix(key, "pr edit "):
			editCalls++
			t.Fatalf("branch/head-only recovery mutated delayed human PR: %q", key)
		}
		return nil
	})
	creator := deterministicPullRequestCreator{execution: testRunExecution(ExecutionConfig{}, RunDependencies{CommandRunner: runner, PlanRecordFactory: memoryPlanRecordFactory})}
	run := PullRequestRun{Detail: detail, RepoRoot: "/repo", Branch: "feature/plan-a", HeadSHA: "head123"}

	if _, err := creator.CreatePullRequest(context.Background(), run); err == nil || !strings.Contains(err.Error(), "connection reset") {
		t.Fatalf("first attempt error = %v, want uncertain create failure", err)
	}
	if detail.State.Plan.PullRequestIntent != nil {
		t.Fatalf("failed create without identity persisted ambiguous intent: %#v", detail.State.Plan.PullRequestIntent)
	}
	// Preserve compatibility coverage for an intent written by an older Tao:
	// branch and head alone must not authorize metadata repair on retry.
	detail.State.Plan.PullRequestIntent = &plan.PullRequest{Branch: run.Branch, HeadSHA: run.HeadSHA}

	pr, err := creator.CreatePullRequest(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 325 || createCalls != 1 || editCalls != 0 || listCalls != 2 {
		t.Fatalf("delayed human PR/create/edit/list calls = %#v/%d/%d/%d, want #325/1/0/2", pr, createCalls, editCalls, listCalls)
	}
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
			case "remote get-url --push origin":
				_, _ = io.WriteString(stdout, "git@github.com:iamseth/tao.git\n")
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

func approvedPullRequestDetail(changeType plan.ChangeType, head string) *plan.PlanDetail {
	detail := runPlanDetail(plan.StatusReviewed, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	detail.State.Plan.ChangeType = changeType
	detail.State.Plan.Review = &plan.PlanReview{
		Status:  plan.ReviewStatusCompleted,
		Verdict: plan.ReviewVerdictApprove,
		Head:    head,
		CommitMessage: &plan.ReviewCommitMessage{
			Subject: "feat(pr): create native pull requests",
			Body:    "What:\nCreate repository-native pull requests without Tao slice details.\n\nWhy:\nKeep plan-a lifecycle metadata out of reviewer-facing context.",
		},
	}
	detail.Slices.Slices[0].VerificationResults = []plan.VerificationRun{{Command: "go test ./internal/run", Result: "passed"}}
	return detail
}

func requireNativePullRequestBody(t *testing.T, body, diffStat string) {
	t.Helper()
	for _, want := range []string{"## Problem", "## Fix", "## Tests", "## Deploy", "## Scope", "<details>", "<summary>Changed files</summary>", "</details>", "No special deployment steps are required."} {
		if !strings.Contains(body, want) {
			t.Fatalf("pull request body missing %q: %q", want, body)
		}
	}
	if diffStat != "" && !strings.Contains(body, diffStat) {
		t.Fatalf("pull request body missing exact diff stat %q: %q", diffStat, body)
	}
	lower := strings.ToLower(body)
	for _, noise := range []string{"tao", "plan-a", "slice", "lifecycle", "squash and merge", "cleanup --dry-run"} {
		if strings.Contains(lower, noise) {
			t.Fatalf("pull request body contains noise %q: %q", noise, body)
		}
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
