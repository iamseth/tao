package forge

import (
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
)

func TestGitHubFindUsesPushRepositoryIdentityAndRejectsForkCandidate(t *testing.T) {
	var calls []string
	github := NewGitHub(forgeRunner(t, &calls, func(args []string, stdout, _ io.Writer) error {
		if strings.Join(args, " ") != "pr list --base main --head feature/a --state open --json number,url,createdAt,labels,assignees,headRefName,headRepository,headRepositoryOwner" {
			t.Fatalf("unexpected gh args: %q", strings.Join(args, " "))
		}
		_, _ = io.WriteString(stdout, `[
{"number":9,"url":"https://github.com/fork/tao/pull/9","createdAt":"2026-08-13T01:00:00Z","labels":[],"assignees":[],"headRefName":"feature/a","headRepository":{"nameWithOwner":"fork/tao"},"headRepositoryOwner":{"login":"fork"}},
{"number":10,"url":"https://github.com/iamseth/tao/pull/10","createdAt":"2026-08-13T02:00:00Z","labels":[{"name":"feature"}],"assignees":[{"login":"seth"}],"headRefName":"feature/a","headRepository":{"nameWithOwner":"iamseth/tao"},"headRepositoryOwner":{"login":"iamseth"}}
]`)
		return nil
	}))

	pr, metadata, found, err := github.Find(context.Background(), FindRequest{RepoRoot: "/repo", BaseBranch: "main", Branch: "feature/a"})
	if err != nil {
		t.Fatal(err)
	}
	if !found || pr.Number != 10 || pr.URL != "https://github.com/iamseth/tao/pull/10" {
		t.Fatalf("unexpected pull request: found=%v pr=%#v", found, pr)
	}
	if !reflect.DeepEqual(metadata, Metadata{Labels: []string{"feature"}, Assignees: []string{"seth"}}) {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
	wantCalls := []string{
		"git -C /repo remote get-url --push origin",
		"gh pr list --base main --head feature/a --state open --json number,url,createdAt,labels,assignees,headRefName,headRepository,headRepositoryOwner",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", calls, wantCalls)
	}
}

func TestGitHubViewParsesIdentityMetadataAndCreatedAt(t *testing.T) {
	fallback := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	github := NewGitHub(forgeRunner(t, nil, func(args []string, stdout, _ io.Writer) error {
		if strings.Join(args, " ") != "pr view 42 --json number,url,createdAt,labels,assignees" {
			t.Fatalf("unexpected gh args: %q", strings.Join(args, " "))
		}
		_, _ = io.WriteString(stdout, `{"number":42,"url":"https://github.com/iamseth/tao/pull/42","createdAt":"2026-08-13T03:00:00Z","labels":[{"name":"bug"}],"assignees":[]}`)
		return nil
	}))

	pr, metadata, found, err := github.View(context.Background(), ViewRequest{RepoRoot: "/repo", Number: 42, FallbackCreatedAt: fallback})
	if err != nil {
		t.Fatal(err)
	}
	if !found || pr.Number != 42 || pr.CreatedAt.Format(time.RFC3339) != "2026-08-13T03:00:00Z" || !reflect.DeepEqual(metadata.Labels, []string{"bug"}) {
		t.Fatalf("unexpected view result: found=%v pr=%#v metadata=%#v", found, pr, metadata)
	}
}

func TestGitHubCreatePreservesEmittedIdentityAndStdoutOnOperationError(t *testing.T) {
	createErr := errors.New("exit status 1")
	createdAt := time.Date(2026, 8, 14, 2, 4, 0, 0, time.UTC)
	github := NewGitHub(forgeRunner(t, nil, func(args []string, stdout, stderr io.Writer) error {
		switch {
		case len(args) > 1 && args[0] == "label" && args[1] == "list":
			_, _ = io.WriteString(stdout, `[{"name":"Feature"}]`)
			return nil
		case len(args) > 1 && args[0] == "pr" && args[1] == "create":
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "--label Feature --assignee @me") {
				t.Fatalf("create did not preserve existing label identity: %q", joined)
			}
			_, _ = io.WriteString(stdout, "created https://github.com/iamseth/tao/pull/323\n")
			_, _ = io.WriteString(stderr, "failed to assign pull request to @me")
			return createErr
		default:
			t.Fatalf("unexpected gh args: %#v", args)
			return nil
		}
	}))

	outcome := github.Create(context.Background(), CreateRequest{RepoRoot: "/repo", BaseBranch: "main", Branch: "feature/a", Title: "feat(pr): split forge", BodyFile: "/tmp/body", Label: "feature", FallbackNow: func() time.Time { return createdAt }})
	if !errors.Is(outcome.OperationErr, createErr) || !strings.Contains(outcome.OperationErr.Error(), "create pull request with assignee @me") {
		t.Fatalf("unexpected create error: %v", outcome.OperationErr)
	}
	if outcome.Stdout != "created https://github.com/iamseth/tao/pull/323\n" || outcome.PullRequest.Number != 323 || outcome.PullRequest.URL != "https://github.com/iamseth/tao/pull/323" || !outcome.PullRequest.CreatedAt.Equal(createdAt) {
		t.Fatalf("partial creation outcome lost identity: %#v", outcome)
	}
}

func TestGitHubCreateCapturesFallbackAfterCommandReturns(t *testing.T) {
	beforeCreate := time.Date(2026, 8, 14, 2, 4, 0, 0, time.UTC)
	afterCreate := beforeCreate.Add(15 * time.Minute)
	current := beforeCreate
	commandReturned := false
	github := NewGitHub(forgeRunner(t, nil, func(args []string, stdout, _ io.Writer) error {
		if len(args) < 2 || args[0] != "pr" || args[1] != "create" {
			t.Fatalf("unexpected gh args: %#v", args)
		}
		_, _ = io.WriteString(stdout, "https://github.com/iamseth/tao/pull/324\n")
		current = afterCreate
		commandReturned = true
		return nil
	}))

	outcome := github.Create(context.Background(), CreateRequest{
		RepoRoot: "/repo", BaseBranch: "main", Branch: "feature/a", Title: "feat(pr): split forge", BodyFile: "/tmp/body",
		FallbackNow: func() time.Time {
			if !commandReturned {
				t.Fatal("fallback clock read before gh pr create returned")
			}
			return current
		},
	})
	if outcome.OperationErr != nil || outcome.ParseErr != nil {
		t.Fatalf("unexpected creation errors: operation=%v parse=%v", outcome.OperationErr, outcome.ParseErr)
	}
	if !outcome.PullRequest.CreatedAt.Equal(afterCreate) {
		t.Fatalf("created at = %s, want post-command fallback %s", outcome.PullRequest.CreatedAt, afterCreate)
	}
}

func TestGitHubEnsureMetadataLeavesExistingMetadataUnchanged(t *testing.T) {
	var calls []string
	github := NewGitHub(forgeRunner(t, &calls, func(args []string, stdout, _ io.Writer) error {
		if strings.Join(args, " ") != "api user --jq .login" {
			t.Fatalf("unexpected metadata mutation: %#v", args)
		}
		_, _ = io.WriteString(stdout, "seth\n")
		return nil
	}))

	err := github.EnsureMetadata(context.Background(), MetadataRequest{
		RepoRoot:    "/repo",
		PullRequest: PullRequest{Number: 7},
		Metadata:    Metadata{Labels: []string{"Feature"}, Assignees: []string{"Seth"}},
		Label:       "feature",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"gh api user --jq .login"}) {
		t.Fatalf("unexpected calls: %#v", calls)
	}
}

func TestGitHubEnsureMetadataRepairsLabelAndAssignment(t *testing.T) {
	var calls []string
	github := NewGitHub(forgeRunner(t, &calls, func(args []string, stdout, _ io.Writer) error {
		switch strings.Join(args, " ") {
		case "api user --jq .login":
			_, _ = io.WriteString(stdout, "seth\n")
		case "label list --search feature --json name --limit 100":
			_, _ = io.WriteString(stdout, `[]`)
		case "label create feature --color " + pullRequestLabelColor + " --description " + pullRequestLabelDescription:
		case "pr edit 7 --add-label feature --add-assignee @me":
		default:
			t.Fatalf("unexpected gh args: %#v", args)
		}
		return nil
	}))

	err := github.EnsureMetadata(context.Background(), MetadataRequest{RepoRoot: "/repo", PullRequest: PullRequest{Number: 7}, Label: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"gh api user --jq .login",
		"gh label list --search feature --json name --limit 100",
		"gh label create feature --color 1D76DB --description Repository change category",
		"gh pr edit 7 --add-label feature --add-assignee @me",
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestParsePullRequestOutputRejectsMultipleDistinctURLs(t *testing.T) {
	createdAt := time.Date(2026, 5, 28, 3, 0, 0, 0, time.UTC)
	output := "created https://github.com/iamseth/tao/pull/123 and https://github.com/other/repo/pull/456"
	if _, err := ParsePullRequestOutput(output, createdAt); err == nil || !strings.Contains(err.Error(), "multiple distinct") {
		t.Fatalf("expected ambiguous pull request URL error, got %v", err)
	}
}

func TestParsePullRequestOutputAllowsRepeatedSameURL(t *testing.T) {
	createdAt := time.Date(2026, 5, 28, 3, 0, 0, 0, time.UTC)
	output := "created https://github.com/iamseth/tao/pull/123\nretry output: https://github.com/iamseth/tao/pull/123"
	pr, err := ParsePullRequestOutput(output, createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 123 || pr.URL != "https://github.com/iamseth/tao/pull/123" || !pr.CreatedAt.Equal(createdAt) {
		t.Fatalf("unexpected repeated pull request metadata: %#v", pr)
	}
}

func forgeRunner(t *testing.T, calls *[]string, gh func(args []string, stdout, stderr io.Writer) error) commandrunner.Runner {
	t.Helper()
	return func(_ context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if (name == "gh" && cwd != "/repo") || (name == "git" && cwd != "") {
			t.Fatalf("unexpected %s cwd %q", name, cwd)
		}
		if calls != nil {
			*calls = append(*calls, name+" "+strings.Join(args, " "))
		}
		switch name {
		case "git":
			if strings.Join(args, " ") != "-C /repo remote get-url --push origin" {
				t.Fatalf("unexpected git args: %#v", args)
			}
			_, _ = io.WriteString(stdout, "git@github.com:iamseth/tao.git\n")
			return nil
		case "gh":
			return gh(args, stdout, stderr)
		default:
			t.Fatalf("unexpected command: %s", name)
			return nil
		}
	}
}
