package run

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestRunPassesWorkspaceRootCwdToPi(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}
	completedDetail := runPlanDetail(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
	completedDetail.Dir = detail.Dir
	completedDetail.State.Repo.Root = repoRoot

	var gotCwd string
	promptSeen := false
	baseStarter := fakePiSessionStarter(t, "done", &promptSeen)
	starter := func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		gotCwd = cwd
		return baseStarter(ctx, cwd, name, args)
	}
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" {
			switch runGitKey(args) {
			case "branch --show-current":
				_, _ = io.WriteString(stdout, "main\n")
			case "symbolic-ref --quiet --short refs/remotes/origin/HEAD":
				_, _ = io.WriteString(stdout, "origin/main\n")
			}
			return nil
		}
		return nil
	}

	repo := plan.NewFileRepository(t.TempDir())
	err := executeDetailWithExecution(context.Background(), detail, func(ctx context.Context, detail *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completedDetail, nil
	}, io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone}}, RunDependencies{PlanRecordFactory: memoryPlanRecordFactory, CommandRunner: runner, ProcessStarter: starter, LogAppender: repo, EventAppender: repo, WorkspacePreparer: func(ctx context.Context, detail *plan.PlanDetail, input WorkspaceResolverInput) (string, error) {
		return workspaceRoot, nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	if !promptSeen {
		t.Fatal("expected Pi prompt command")
	}
	if gotCwd != workspaceRoot {
		t.Fatalf("expected Pi cwd %q, got %q", workspaceRoot, gotCwd)
	}
}

func TestDefaultCommandRunnerKeepsGenericEnvUnchanged(t *testing.T) {
	setTestEnv(t, "TAO_HELPER_PROCESS", "1")
	setTestEnv(t, "FORCE_COLOR", "0")
	setTestEnv(t, "CLICOLOR_FORCE", "0")
	setTestEnv(t, "TERM", "dumb")
	var out bytes.Buffer

	if err := defaultCommandRunner(context.Background(), "", os.Args[0], []string{"-test.run=TestHelperProcessGenericEnv", "--"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}

	got := strings.Split(strings.TrimSpace(out.String()), "\n")
	want := []string{"0", "0", "dumb"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expected generic env %#v, got %#v", want, got)
	}
}

func TestSharedAgentOperationsSetTransportRequests(t *testing.T) {
	executor := &recordingAgentSessionExecutor{result: AgentSessionResult{Output: "created https://github.com/iamseth/tao/pull/123"}}
	options := agentOperationOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, StartingBranch: "feature", Now: func() time.Time {
		return time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	}}
	ctx := context.Background()

	if err := runSliceWithAgentSession(ctx, executor, options, SliceRun{PlanDir: "/plans/a", SliceID: "001-a", RepoRoot: "/repo", RunPacket: "packet"}); err != nil {
		t.Fatal(err)
	}
	pr, err := createPullRequestWithAgentSession(ctx, executor, options, PullRequestRun{PlanDir: "/plans/a", PlanID: "plan-a", RepoRoot: "/repo"})
	if err != nil {
		t.Fatal(err)
	}

	if len(executor.requests) != 2 {
		t.Fatalf("expected 2 requests, got %#v", executor.requests)
	}
	assertAgentRequest(t, executor.requests[0], "running 001-a", false, "001-a")
	assertAgentRequest(t, executor.requests[1], "creating pull request for plan plan-a", true, "")
	if pr.Number != 123 || pr.URL != "https://github.com/iamseth/tao/pull/123" || !pr.CreatedAt.Equal(options.Now().UTC()) {
		t.Fatalf("unexpected pull request: %#v", pr)
	}
}

func TestHelperProcessGenericEnv(t *testing.T) {
	if os.Getenv("TAO_HELPER_PROCESS") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stdout, os.Getenv("FORCE_COLOR")+"\n"+os.Getenv("CLICOLOR_FORCE")+"\n"+os.Getenv("TERM")+"\n")
	os.Exit(0)
}

func setTestEnv(t *testing.T, key string, value string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Setenv(key, value); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, old)
			return
		}
		_ = os.Unsetenv(key)
	})
}

type recordingAgentSessionExecutor struct {
	requests []AgentSessionRequest
	result   AgentSessionResult
}

func (e *recordingAgentSessionExecutor) RunAgentSession(ctx context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
	e.requests = append(e.requests, request)
	return e.result, ctx.Err()
}

func assertAgentRequest(t *testing.T, request AgentSessionRequest, logAction string, captureOutput bool, metricsSliceID string) {
	t.Helper()
	if request.LogAction != logAction || request.CaptureOutput != captureOutput {
		t.Fatalf("unexpected request action/output: %#v", request)
	}
	if metricsSliceID == "" {
		if request.Metrics != nil {
			t.Fatalf("expected no metrics request, got %#v", request.Metrics)
		}
		return
	}
	if request.Metrics == nil || request.Metrics.SliceID != metricsSliceID {
		t.Fatalf("expected metrics slice %q, got %#v", metricsSliceID, request.Metrics)
	}
}

type fakeSliceExecutor struct{}

func (fakeSliceExecutor) RunSlice(ctx context.Context, run SliceRun) error { return ctx.Err() }

type pullRequestCreatorFunc func(ctx context.Context, run PullRequestRun) (plan.PullRequest, error)

func (f pullRequestCreatorFunc) CreatePullRequest(ctx context.Context, run PullRequestRun) (plan.PullRequest, error) {
	return f(ctx, run)
}

type countingSliceExecutor struct {
	calls int
}

func (e *countingSliceExecutor) RunSlice(ctx context.Context, run SliceRun) error {
	e.calls++
	return ctx.Err()
}

type packetCapturingExecutor struct {
	packet   string
	planDir  string
	repoRoot string
}

func (e *packetCapturingExecutor) RunSlice(ctx context.Context, run SliceRun) error {
	e.packet = run.RunPacket
	e.planDir = run.PlanDir
	e.repoRoot = run.RepoRoot
	return ctx.Err()
}
