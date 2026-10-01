package run

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestAgentFactorySelectsIndependentReviewer(t *testing.T) {
	for _, implementation := range []AgentKind{AgentPi, AgentClaude} {
		for _, selection := range []AgentKind{"", AgentPi, AgentClaude} {
			t.Run(string(implementation)+"/"+string(selection), func(t *testing.T) {
				options := ResolvedRunOptions{Agent: implementation, ReviewAgent: selection}
				capabilities := newAgentFactory(testRunExecution(ExecutionConfig{ResolvedRunOptions: options}, RunDependencies{})).runCapabilities()
				if got := capabilities.reviewCreator.(agentExecutor).descriptor.Kind; got != options.ReviewAgentKind() {
					t.Fatalf("review runtime = %s, want %s", got, options.ReviewAgentKind())
				}
				if capabilities.sliceExecutor.(agentExecutor).descriptor.Kind != implementation || capabilities.pullRequestBodyGenerator.(agentExecutor).descriptor.Kind != implementation {
					t.Fatal("review selection changed implementation or PR runtime")
				}
			})
		}
	}
}

func TestAgentFactoryReviewLaunches(t *testing.T) {
	const output = "```tao-review-json\n{\"verdict\":\"comment\",\"summary\":\"Reviewed.\",\"findings\":[]}\n```"
	for _, implementation := range []AgentKind{AgentPi, AgentClaude} {
		var inheritedArgs []string
		for _, selection := range []AgentKind{"", implementation, AgentPi, AgentClaude} {
			t.Run(string(implementation)+"/"+string(selection), func(t *testing.T) {
				options := ResolvedRunOptions{Agent: implementation, ReviewAgent: selection, Models: runtimeconfig.ModelSelection{Base: "base-model", Run: "run-model", Review: "review-model"}}
				kind := options.ReviewAgentKind()
				root := t.TempDir()
				detail := runPathSessionDetail(t, root, plan.StatusInReview, nil, []string{"001-a"}, plan.StatusCompleted)
				detail.State.Repo.BaseCommit = "base123"
				persistReviewState(t, detail.Dir, detail)
				repository := plan.NewFileRepository("")
				calls := 0
				var launchArgs []string
				fake := phaseTelemetryStarter(t, kind, output, plan.AgentMetricsReported)
				starter := func(ctx context.Context, cwd, name string, args []string) (Process, error) {
					calls++
					if name != string(kind) {
						t.Fatalf("launched %s, want %s", name, kind)
					}
					launchArgs = append([]string(nil), args...)
					if joined := strings.Join(args, " "); !strings.Contains(joined, "review-model") || strings.Contains(joined, "run-model") || strings.Contains(joined, "base-model") {
						t.Fatalf("review model arguments: %v", args)
					}
					if kind == AgentPi {
						// This RPC fixture accepts the model-free launch after the assertion above.
						args = args[:3]
					}
					return fake(ctx, cwd, name, args)
				}
				caps := newAgentFactory(testRunExecution(ExecutionConfig{ResolvedRunOptions: options}, RunDependencies{ProcessStarter: starter, PlanRecordFactory: fileReviewRecordFactory(repository), LogAppender: repository, EventAppender: repository, reviewGitFactory: fixedReviewGit(&fakeReviewGit{head: "head123", currentBranch: "feature"})})).runCapabilities()
				review, err := caps.reviewCreator.CreateReview(context.Background(), ReviewRun{PlanDir: detail.Dir, Detail: detail, RepoRoot: root})
				if err != nil || calls != 1 || review.Agent != string(kind) || review.Base != "base123" || review.Head != "head123" {
					t.Fatalf("review = %+v, calls = %d, error = %v", review, calls, err)
				}
				if selection == "" {
					inheritedArgs = launchArgs
				} else if selection == implementation && !reflect.DeepEqual(launchArgs, inheritedArgs) {
					t.Fatalf("explicit same-runtime launch differs: %v vs %v", launchArgs, inheritedArgs)
				}
			})
		}
	}
}

func TestAgentFactoryUnavailableReviewerDoesNotFallback(t *testing.T) {
	for _, implementation := range []AgentKind{AgentPi, AgentClaude} {
		reviewer := AgentClaude
		if implementation == AgentClaude {
			reviewer = AgentPi
		}
		for _, failure := range []error{errors.New("review executable unavailable"), errors.New("provider rejected model")} {
			calls := 0
			starter := func(_ context.Context, _, name string, _ []string) (Process, error) {
				calls++
				if name != string(reviewer) {
					t.Fatalf("unexpected runtime %s", name)
				}
				return nil, failure
			}
			dir := t.TempDir()
			caps := newAgentFactory(testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: implementation, ReviewAgent: reviewer}}, RunDependencies{ProcessStarter: starter, LogAppender: plan.NewFileRepository("")})).runCapabilities()
			if calls != 0 {
				t.Fatal("factory probed runtime availability")
			}
			_, err := caps.reviewCreator.(AgentSessionExecutor).RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: dir, RepoRoot: dir, Prompt: "review", CaptureOutput: true})
			if err == nil || !strings.Contains(err.Error(), failure.Error()) || calls != 1 {
				t.Fatalf("error = %v; launches = %d", err, calls)
			}
		}
	}
}

func TestSelectedReviewerCancellationAndDeadline(t *testing.T) {
	for _, reviewer := range []AgentKind{AgentPi, AgentClaude} {
		for _, cancel := range []bool{false, true} {
			root := t.TempDir()
			detail := runPathSessionDetail(t, root, plan.StatusInReview, nil, []string{"001-a"}, plan.StatusCompleted)
			repository := plan.NewFileRepository("")
			implementation := AgentPi
			if reviewer == AgentPi {
				implementation = AgentClaude
			}
			caps := newAgentFactory(testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: implementation, ReviewAgent: reviewer, SessionTimeout: 20 * time.Millisecond}}, RunDependencies{LogAppender: repository, EventAppender: repository})).runCapabilities()
			executor := caps.reviewCreator.(agentExecutor)
			ctx, stop := context.WithCancel(context.Background())
			calls := 0
			executor.descriptor.NewRuntime = func(agent.RuntimeDeps) agent.Runtime {
				return agentRuntimeFunc(func(ctx context.Context, _ agent.Session) (agent.SessionResult, error) {
					calls++
					if cancel {
						stop()
					}
					<-ctx.Done()
					return agent.SessionResult{}, ctx.Err()
				})
			}
			_, err := executor.RunAgentSession(ctx, AgentSessionRequest{PlanDir: detail.Dir, RepoRoot: root, Prompt: "review", CaptureOutput: true, Metrics: &AgentSessionMetricsRequest{Role: plan.AgentRoleReview}})
			stop()
			if err == nil || calls != 1 {
				t.Fatalf("reviewer %s cancellation=%v: calls=%d error=%v", reviewer, cancel, calls, err)
			}
			var timeout *agent.SessionTimeoutError
			if cancel && !errors.Is(err, context.Canceled) || !cancel && !errors.As(err, &timeout) {
				t.Fatalf("cancellation=%v error=%v", cancel, err)
			}
			for _, event := range readAgentMetricEvents(t, detail.Dir) {
				if event.Metrics.Agent != string(reviewer) || event.Metrics.Role != plan.AgentRoleReview {
					t.Fatalf("wrong reviewer telemetry: %+v", event.Metrics)
				}
			}
		}
	}
}

func TestAgentFactoryBuildsPiRunExecutors(t *testing.T) {
	repo := plan.NewFileRepository(t.TempDir())
	starter := func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return nil, nil
	}
	sessionTimeout := 90 * time.Second
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent, SessionTimeout: sessionTimeout}}, RunDependencies{ProcessStarter: starter, LogAppender: repo, EventAppender: repo})
	execution.StartingBranch = "feature/pi"

	factory := newAgentFactory(execution)
	capabilities := factory.runCapabilities()
	sliceExecutor, ok := capabilities.sliceExecutor.(agentExecutor)
	if !ok || sliceExecutor.descriptor.Kind != AgentPi {
		t.Fatalf("expected Pi slice executor, got %T", capabilities.sliceExecutor)
	}
	if bodyGenerator, ok := capabilities.pullRequestBodyGenerator.(agentExecutor); !ok || bodyGenerator.descriptor.Kind != AgentPi {
		t.Fatalf("expected Pi pull request body generator, got %T", capabilities.pullRequestBodyGenerator)
	}
	if sliceExecutor.options.Deps.ProcessStarter == nil || sliceExecutor.options.CommitPolicy != CommitPolicySlice || sliceExecutor.options.ExecutionMode != ExecutionModeCurrent || sliceExecutor.options.SessionTimeout != sessionTimeout || sliceExecutor.options.StartingBranch != "feature/pi" {
		t.Fatalf("unexpected Pi run options: %+v", sliceExecutor.options)
	}
	if sliceExecutor.logAppender != repo || sliceExecutor.eventAppender != repo {
		t.Fatal("expected Pi executor to preserve log and event appenders")
	}
}

func TestAgentFactoryBuildsClaudeExecutorsAndAuxiliaryFallbacks(t *testing.T) {
	repo := plan.NewFileRepository(t.TempDir())
	starter := func(ctx context.Context, cwd string, name string, args []string) (Process, error) {
		return nil, nil
	}
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: AgentClaude, CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeCurrent}, SkipPermissions: true}, RunDependencies{ProcessStarter: starter, LogAppender: repo, EventAppender: repo})

	capabilities := newAgentFactory(execution).runCapabilities()
	sliceExecutor, ok := capabilities.sliceExecutor.(agentExecutor)
	if !ok || sliceExecutor.descriptor.Kind != AgentClaude {
		t.Fatalf("expected Claude slice executor, got %T", capabilities.sliceExecutor)
	}
	if bodyGenerator, ok := capabilities.pullRequestBodyGenerator.(agentExecutor); !ok || bodyGenerator.descriptor.Kind != AgentClaude {
		t.Fatalf("expected Claude pull request body generator, got %T", capabilities.pullRequestBodyGenerator)
	}
	if sliceExecutor.options.Deps.ProcessStarter == nil || sliceExecutor.options.CommitPolicy != CommitPolicySlice || sliceExecutor.options.ExecutionMode != ExecutionModeCurrent || !sliceExecutor.options.SkipPermissions {
		t.Fatalf("unexpected Claude run options: %+v", sliceExecutor.options)
	}
	if sliceExecutor.logAppender != repo || sliceExecutor.eventAppender != repo {
		t.Fatal("expected Claude executor to preserve log and event appenders")
	}
}
