package run

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/forge"
	"github.com/iamseth/tao/internal/plan"
)

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

func noGHCommands(t *testing.T) func([]string, io.Writer, io.Writer) error {
	t.Helper()
	return func(args []string, _, _ io.Writer) error {
		t.Fatalf("run orchestration executed gh directly: %s", strings.Join(args, " "))
		return nil
	}
}
