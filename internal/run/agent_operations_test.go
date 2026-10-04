package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptcapture"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/prompts"
)

func TestAgentSessionControlCheckoutAttribution(t *testing.T) {
	for _, tc := range []struct {
		name                                                                               string
		expected, owned, dirty, missingBase, invalidBase, missingSlice, headMoved, timeout bool
		wantLeak                                                                           bool
		declaration, changedPath, ownedPath                                                string
		persistent, disappearance                                                          bool
	}{
		{name: "expected path", expected: true, wantLeak: true},
		{name: "normalized expected", declaration: "./changed.txt", wantLeak: true},
		{name: "expected directory", declaration: "internal/pkg/", changedPath: "internal/pkg/new.go", wantLeak: true},
		{name: "expected directory committed", declaration: "internal/pkg/", changedPath: "internal/pkg/new.go", headMoved: true, wantLeak: true},
		{name: "expected glob", declaration: "./internal/**/*.go", changedPath: "internal/pkg/new.go", wantLeak: true},
		{name: "expected character glob", declaration: "internal/pkg/[ab]?.go", changedPath: "internal/pkg/a1.go", wantLeak: true},
		{name: "directory boundary", declaration: "internal/pkg/", changedPath: "internal/pkg-other/new.go"},
		{name: "glob boundary", declaration: "internal/*.go", changedPath: "internal/pkg/new.go"},
		{name: "git path stays literal", owned: true, ownedPath: "*.txt"},
		{name: "persistent owned with appearance", expected: true, persistent: true, wantLeak: true},
		{name: "persistent owned with disappearance", expected: true, persistent: true, disappearance: true, wantLeak: true},
		{name: "plan owned path", owned: true, wantLeak: true},
		{name: "unrelated clean"},
		{name: "unrelated timeout", timeout: true},
		{name: "unrelated head move", headMoved: true},
		{name: "expected path committed", expected: true, headMoved: true, wantLeak: true},
		{name: "plan owned path committed", owned: true, headMoved: true, wantLeak: true},
		{name: "dirty execution", dirty: true, wantLeak: true},
		{name: "missing base", missingBase: true, wantLeak: true},
		{name: "invalid base", invalidBase: true, wantLeak: true},
		{name: "missing slice", missingSlice: true, wantLeak: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			control, execution := t.TempDir(), t.TempDir()
			for _, root := range []string{control, execution} {
				lifecycleGitRun(t, root, "init")
				lifecycleGitRun(t, root, "config", "user.email", "test@example.com")
				lifecycleGitRun(t, root, "config", "user.name", "Test")
				lifecycleGitRun(t, root, "commit", "--allow-empty", "-m", "base")
			}
			changedPath := tc.changedPath
			if changedPath == "" {
				changedPath = "changed.txt"
			}
			write := func(root, name string) {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(root, name)), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, name), []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			detail := runPathSessionDetail(t, control, plan.StatusInProgress, []string{"001-a"}, nil, plan.StatusInProgress)
			detail.State.Repo.BaseCommit = lifecycleGitOutput(t, execution, "rev-parse", "HEAD")
			if tc.missingBase {
				detail.State.Repo.BaseCommit = ""
			}
			if tc.invalidBase {
				detail.State.Repo.BaseCommit = "missing-base"
			}
			if tc.expected {
				detail.Slices.Slices[0].ExpectedFiles = []string{"changed.txt"}
			}
			if tc.declaration != "" {
				detail.Slices.Slices[0].ExpectedFiles = []string{tc.declaration}
			}
			if tc.persistent {
				if err := os.WriteFile(filepath.Join(control, changedPath), []byte("before"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.disappearance {
				write(control, "unrelated.txt")
			}
			if tc.owned {
				ownedPath := tc.ownedPath
				if ownedPath == "" {
					ownedPath = changedPath
				}
				write(execution, ownedPath)
				lifecycleGitRun(t, execution, "add", ".")
				lifecycleGitRun(t, execution, "commit", "-m", "owned")
			}
			record, err := plan.NewPlanRecord(detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			if err := record.PersistArtifacts(); err != nil {
				t.Fatal(err)
			}
			var sessionErr error
			if tc.timeout {
				sessionErr = &agent.SessionTimeoutError{Timeout: time.Second}
			}
			repository := plan.NewFileRepository(filepath.Dir(detail.Dir))
			var log bytes.Buffer
			runner := newAgentSessionRunner(agentSessionRunnerConfig{
				logAppender: repository, eventAppender: repository, sessionLogWriter: &log,
				descriptor: agent.Descriptor{Label: "test", NewRuntime: func(agent.RuntimeDeps) agent.Runtime {
					return agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
						write(control, changedPath)
						if tc.persistent {
							if tc.disappearance {
								if err := os.Remove(filepath.Join(control, "unrelated.txt")); err != nil {
									t.Fatal(err)
								}
							} else {
								write(control, "unrelated.txt")
							}
						}
						if tc.headMoved {
							lifecycleGitRun(t, control, "add", ".")
							lifecycleGitRun(t, control, "commit", "-m", "external")
						}
						if tc.dirty {
							write(execution, "other.txt")
						}
						return agent.SessionResult{Output: "output", FinalText: "final"}, sessionErr
					})
				}},
			})
			sliceID := "001-a"
			if tc.missingSlice {
				sliceID = "missing"
			}
			result, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: detail.Dir, RepoRoot: execution, Metrics: &AgentSessionMetricsRequest{SliceID: sliceID}})
			var leak ControlCheckoutLeakError
			if tc.wantLeak {
				if !errors.As(err, &leak) {
					t.Fatalf("error=%v, want leak", err)
				}
				if classifyRunAbort(err) != plan.RunAbortKindControlCheckoutLeak {
					t.Fatalf("abort classification for %v", err)
				}
			} else if !errors.Is(err, sessionErr) {
				t.Fatalf("error=%v, want %v", err, sessionErr)
			}
			if result.Output != "output" || result.FinalText != "final" {
				t.Fatalf("result=%+v", result)
			}
			loaded, loadErr := repository.GetPlan(context.Background(), filepath.Base(detail.Dir))
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			var changed *plan.Event
			for i := range loaded.Events {
				if loaded.Events[i].Type == "control_checkout_changed" {
					changed = &loaded.Events[i]
				}
			}
			if tc.wantLeak {
				if changed != nil {
					t.Fatalf("unexpected warning: %+v", changed)
				}
				return
			}
			if changed == nil {
				t.Fatal("missing control_checkout_changed event")
			}
			if changed.PlanID != detail.State.Plan.ID || changed.SliceID != sliceID || changed.Agent != "test" || !strings.Contains(changed.Message, "possibly by another process") || !strings.Contains(log.String(), "tao leak-guard warning:") {
				t.Fatalf("event=%+v log=%s", changed, log.String())
			}
			if tc.headMoved {
				if changed.HeadSHA != lifecycleGitOutput(t, control, "rev-parse", "HEAD") {
					t.Fatalf("head=%q", changed.HeadSHA)
				}
			} else if !slices.Equal(changed.Paths, []string{changedPath}) || changed.HeadSHA != "" {
				t.Fatalf("event=%+v", changed)
			}
		})
	}
}

func TestAgentOperationModels(t *testing.T) {
	const approval = "```tao-review-json\n{\"verdict\":\"approve\",\"summary\":\"Approved.\",\"findings\":[]}\n```"
	const correction = "```tao-review-proposal-json\n{\"commit_message\":{\"subject\":\"fix(review): preserve exact approval\",\"body\":\"What:\\nPreserve the exact approval.\\n\\nWhy:\\nAvoid unnecessary sessions.\"}}\n```"
	for _, selection := range []struct {
		name              string
		models            runtimeconfig.ModelSelection
		run, review, base string
	}{
		{name: "unset"},
		{name: "effort only", models: runtimeconfig.ModelSelection{Effort: "base-effort", RunEffort: "run-effort", ReviewEffort: "review-effort"}},
		{name: "roles", models: runtimeconfig.ModelSelection{Base: "b", Run: "r", Review: "v", Effort: "base-effort", RunEffort: "run-effort", ReviewEffort: "review-effort"}, run: "r", review: "v", base: "b"},
		{name: "base fallback", models: runtimeconfig.ModelSelection{Base: "b", Effort: "base-effort"}, run: "b", review: "b", base: "b"},
	} {
		for _, kind := range []AgentKind{AgentPi, AgentClaude} {
			for _, operation := range []string{"execution", "rework", "escalation", "review and correction", "pr", "body"} {
				t.Run(selection.name+"/"+string(kind)+"/"+operation, func(t *testing.T) {
					repoRoot := t.TempDir()
					detail := runPathSessionDetail(t, repoRoot, plan.StatusInReview, nil, []string{"001-a"}, plan.StatusCompleted)
					detail.State.Plan.ChangeType = plan.ChangeTypeFix
					detail.State.Repo.BaseCommit = "base123"
					persistReviewState(t, detail.Dir, detail)
					repository := plan.NewFileRepository("")
					want := selection.base
					role := runtimeconfig.ModelRoleDefault
					switch operation {
					case "execution", "rework", "escalation":
						want = selection.run
						role = runtimeconfig.ModelRoleRun
					case "review and correction":
						want = selection.review
						role = runtimeconfig.ModelRoleReview
					}
					override := ""
					if operation == "escalation" {
						override = "escalated-model"
						want = override
					}
					wantEffort := selection.models.EffortFor(role)
					calls := 0
					descriptor, _ := agent.Lookup(kind)
					providerFactory := descriptor.NewRuntime
					descriptor.NewRuntime = func(agent.RuntimeDeps) agent.Runtime {
						return agentRuntimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
							calls++
							if session.Effort != wantEffort {
								t.Fatalf("session effort = %q, want %q", session.Effort, wantEffort)
							}
							if session.Model != want {
								t.Fatalf("session %d model = %q, want %q", calls, session.Model, want)
							}
							output := "created https://github.com/iamseth/tao/pull/123"
							if operation == "review and correction" {
								output = approval
								if calls == 2 {
									output = correction
								}
							}
							starter := phaseTelemetryStarter(t, kind, output, plan.AgentMetricsUnavailable)
							return providerFactory(agent.RuntimeDeps{ProcessStarter: func(ctx context.Context, cwd, name string, args []string) (Process, error) {
								effortFlag := "--thinking"
								if kind == AgentClaude {
									effortFlag = "--effort"
								}
								effortIndex := slices.Index(args, effortFlag)
								if wantEffort == "" {
									if effortIndex != -1 {
										t.Fatalf("unset effort changed args: %v", args)
									}
								} else {
									if effortIndex != len(args)-2 || args[effortIndex+1] != wantEffort {
										t.Fatalf("effort args = %v, want %q", args, wantEffort)
									}
									args = args[:effortIndex]
								}
								index := slices.Index(args, "--model")
								if want == "" {
									if index != -1 {
										t.Fatalf("unset model changed launch arguments: %v", args)
									}
								} else {
									if index != len(args)-2 || args[index+1] != want {
										t.Fatalf("launch arguments = %v, want appended --model %s", args, want)
									}
									// The legacy fake also checks the unchanged launch prefix.
									args = args[:index]
								}
								return starter(ctx, cwd, name, args)
							}}).RunSession(ctx, session)
						})
					}
					executor := newAgentExecutor(descriptor, ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Models: selection.models}}, RunDependencies{
						LogAppender: repository, EventAppender: repository, PlanRecordFactory: fileReviewRecordFactory(repository),
						reviewGitFactory: fixedReviewGit(&fakeReviewGit{head: "head123", currentBranch: "feature"}),
					}, "", nil)
					var err error
					wantCalls := 1
					switch operation {
					case "execution", "rework", "escalation":
						sliceID := "001-a"
						if operation == "rework" || operation == "escalation" {
							sliceID = "r101-fix"
						}
						err = executor.RunSlice(context.Background(), SliceRun{Model: override, PlanDir: detail.Dir, RepoRoot: repoRoot, SliceID: sliceID})
					case "review and correction":
						wantCalls = 2
						var review plan.PlanReview
						review, err = executor.CreateReview(context.Background(), ReviewRun{PlanDir: detail.Dir, Detail: detail, RepoRoot: repoRoot})
						if err == nil && (review.Verdict != plan.ReviewVerdictApprove || review.CommitMessage == nil) {
							t.Fatalf("review = %+v", review)
						}
					case "pr":
						_, err = executor.CreatePullRequest(context.Background(), PullRequestRun{PlanDir: detail.Dir, PlanID: "plan-a", RepoRoot: repoRoot})
					case "body":
						_, err = executor.GeneratePullRequestBody(context.Background(), PullRequestBodyRun{PlanDir: detail.Dir, PlanID: "plan-a", RepoRoot: repoRoot})
					}
					if err != nil || calls != wantCalls {
						t.Fatalf("error=%v provider calls=%d, want %d", err, calls, wantCalls)
					}
				})
			}
		}
	}
}

func TestRunSliceEscalationPreservesRoleEffort(t *testing.T) {
	for _, override := range []string{"", "escalated-model"} {
		t.Run("override="+override, func(t *testing.T) {
			models := runtimeconfig.ModelSelection{Run: "run-model", Effort: "base-effort", RunEffort: "run-effort"}
			calls := 0
			executor := agentSessionExecutorFunc(func(_ context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
				calls++
				wantModel := models.Run
				if override != "" {
					wantModel = override
				}
				if request.Model != wantModel || request.Effort != "run-effort" {
					t.Fatalf("model/effort = %q/%q, want %q/run-effort", request.Model, request.Effort, wantModel)
				}
				return AgentSessionResult{}, nil
			})
			if err := runSliceWithAgentSession(context.Background(), executor, agentOperationOptions{Models: models}, SliceRun{PlanDir: t.TempDir(), SliceID: "r101-fix", Model: override}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d", calls)
			}
		})
	}
}

func TestHistoricalProposalCorrectionModel(t *testing.T) {
	for _, model := range []string{"", "v"} {
		t.Run("model="+model, func(t *testing.T) {
			fixture := newPullRequestOrchestrationFixture(t)
			repository := plan.NewFileRepository(fixture.plansRoot)
			detail, err := repository.ResolvePlan(context.Background(), fixture.planDir)
			if err != nil {
				t.Fatal(err)
			}
			detail.State.Plan.Review.Agent = "historical-reviewer"
			calls := 0
			reviewer := struct {
				ReviewCreator
				AgentSessionExecutor
			}{AgentSessionExecutor: agentSessionExecutorFunc(func(_ context.Context, request AgentSessionRequest) (AgentSessionResult, error) {
				calls++
				if request.Effort != "review-effort" {
					t.Fatalf("correction effort = %q", request.Effort)
				}
				if request.Model != model {
					t.Fatalf("correction model = %q, want %q", request.Model, model)
				}
				return AgentSessionResult{Output: "```tao-review-proposal-json\n{\"commit_message\":{\"subject\":\"fix(pr): recover exact finalization\",\"body\":\"What:\\nRecover the exact approved head.\\n\\nWhy:\\nFinish pull request handoff safely.\"}}\n```"}, nil
			})}
			models := runtimeconfig.ModelSelection{ReviewEffort: "review-effort"}
			if model != "" {
				models = runtimeconfig.ModelSelection{Base: "b", Run: "r", Review: model, Effort: "base-effort", ReviewEffort: "review-effort"}
			}
			finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Models: models, Agent: AgentPi, ReviewAgent: AgentClaude}}, RunDependencies{
				CommandRunner: defaultCommandRunner, PlanRecordFactory: fileReviewRecordFactory(repository), ReviewCreator: reviewer,
			}))
			if err := finalizer.ensureApprovedReviewProposal(context.Background(), detail, fixture.worktreeRoot, fixture.branch, fixture.head); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("correction calls = %d, want 1", calls)
			}
			if detail.State.Plan.Review.Agent != "historical-reviewer" {
				t.Fatalf("historical review relabeled: %+v", detail.State.Plan.Review)
			}
			found := false
			for _, event := range detail.Events {
				if event.Type == plan.EventTypePlanReviewed && event.Agent == "claude" {
					found = true
				}
			}
			if !found {
				t.Fatal("missing correction event attributed to selected reviewer")
			}
		})
	}
}

func TestRunSliceWithAgentSessionCarriesInterruptedResumePrompt(t *testing.T) {
	executor := &recordingAgentSessionExecutor{}
	err := runSliceWithAgentSession(context.Background(), executor, agentOperationOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated}, SliceRun{
		PlanDir: "/plans/a", SliceID: "001-a", RepoRoot: "/repo", Resuming: true, ResumeAttempt: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("agent requests = %d, want 1", len(executor.requests))
	}
	prompt := executor.requests[0].Prompt
	for _, want := range []string{"This is resume attempt 2", "staged, unstaged, and untracked", "Never run `git commit` manually"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("resume session prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestGeneratePullRequestBodySessionCarriesNativeAcceptanceContract(t *testing.T) {
	executor := &recordingAgentSessionExecutor{result: AgentSessionResult{FinalText: "body"}}
	body, err := generatePullRequestBodyWithAgentSession(context.Background(), executor, agentOperationOptions{}, PullRequestBodyRun{
		PlanDir: "/plans/a", PlanID: "plan-a", RepoRoot: "/repo", Title: "feat(pr): native format", DraftBody: "## Problem\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body != "body" || len(executor.requests) != 1 {
		t.Fatalf("body/session = %q/%d, want body/1", body, len(executor.requests))
	}
	assertPlanTelemetryRequest(t, executor.requests[0], plan.AgentRolePullRequest)
	prompt := executor.requests[0].Prompt
	for _, want := range []string{"Problem, Fix, Tests, Deploy, Scope", "Use only ## ATX syntax for level-two headings; do not add Setext headings", "Keep Tests exactly as drafted", "legitimate repository paths that contain the word Tao", "complete collapsed Changed files details block containing the exact diff stat", "paths that happen to contain the word Tao", "omits Tao lifecycle verification commands", "Do not include plan IDs", "Tao-specific prose in Problem, Fix, Tests, or Deploy", "merge guidance", "Do not add claims"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("body session prompt missing %q:\n%s", want, prompt)
		}
	}
}

// Exercise the operation requests through both real provider adapters and the
// durable plan adapter. Errors are injected after provider measurement so the
// operation must preserve failures/timeouts even when telemetry can be saved.
func TestReviewAndPullRequestTelemetry(t *testing.T) {
	const approval = "```tao-review-json\n{\"verdict\":\"approve\",\"summary\":\"Approved.\",\"findings\":[],\"commit_message\":{\"subject\":\"fix(review): preserve exact approval\",\"body\":\"What:\\nPreserve the exact approval.\\n\\nWhy:\\nAvoid unnecessary sessions.\"}}\n```"
	const incompleteApproval = "```tao-review-json\n{\"verdict\":\"approve\",\"summary\":\"Approved.\",\"findings\":[]}\n```"
	const correction = "```tao-review-proposal-json\n{\"commit_message\":{\"subject\":\"fix(review): preserve exact approval\",\"body\":\"What:\\nPreserve the exact approval.\\n\\nWhy:\\nAvoid unnecessary sessions.\"}}\n```"
	for _, kind := range []AgentKind{AgentPi, AgentClaude} {
		for _, operation := range []string{"review", "correction", "pr", "body"} {
			for _, measurement := range []plan.AgentMetricsAvailability{plan.AgentMetricsReported, plan.AgentMetricsPartial, plan.AgentMetricsUnavailable} {
				for _, outcome := range []string{"success", "failed", "timeout", "append failure"} {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", kind, operation, measurement, outcome), func(t *testing.T) {
						kind := kind
						t.Setenv(runtimeconfig.EnvMaxSliceOutputTokensDeprecated, "1")
						t.Setenv(runtimeconfig.EnvMaxSliceCostDeprecated, "1")
						repoRoot := t.TempDir()
						detail := runPathSessionDetail(t, repoRoot, plan.StatusInReview, nil, []string{"001-a"}, plan.StatusCompleted)
						detail.State.Plan.ChangeType = plan.ChangeTypeFix
						detail.State.Repo.BaseCommit = "base123"
						persistReviewState(t, detail.Dir, detail)
						if err := os.WriteFile(filepath.Join(detail.Dir, "events.jsonl"), nil, 0o600); err != nil {
							t.Fatal(err)
						}
						repository := plan.NewFileRepository("")
						var sessionErr error
						switch outcome {
						case "failed":
							sessionErr = errors.New("private provider failure")
						case "timeout":
							sessionErr = &agent.SessionTimeoutError{Timeout: time.Minute}
						}
						calls := 0
						implementation := AgentPi
						if kind == AgentPi {
							implementation = AgentClaude
						}
						capabilities := newAgentFactory(testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{Agent: implementation, ReviewAgent: kind}}, RunDependencies{})).runCapabilities()
						executor := capabilities.reviewCreator.(agentExecutor)
						if operation == "pr" || operation == "body" {
							executor = capabilities.pullRequestBodyGenerator.(agentExecutor)
							kind = implementation
						}
						descriptor := executor.descriptor
						providerFactory := descriptor.NewRuntime
						descriptor.NewRuntime = func(agent.RuntimeDeps) agent.Runtime {
							return agentRuntimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
								calls++
								if !session.CollectMetrics {
									t.Fatal("operation did not request metrics")
								}
								if operation == "body" {
									deadline, ok := ctx.Deadline()
									if !ok || time.Until(deadline) > pullRequestBodyAgentTimeout {
										t.Fatal("PR body lost its bounded timeout")
									}
								}
								output := "created https://github.com/iamseth/tao/pull/123"
								switch operation {
								case "review":
									output = approval
								case "correction":
									output = correction
									if calls == 1 {
										output = incompleteApproval
									}
								}
								starter := phaseTelemetryStarter(t, kind, output, measurement)
								result, err := providerFactory(agent.RuntimeDeps{ProcessStarter: starter}).RunSession(ctx, session)
								if err != nil {
									t.Fatalf("provider fixture: %v", err)
								}
								if operation == "correction" && calls == 1 {
									return result, nil
								}
								return result, sessionErr
							})
						}
						appendCalls := 0
						timeouts := 0
						appender := eventAppenderFunc(func(dir string, event plan.Event) error {
							if event.Type == plan.EventTypeSessionTimeout {
								timeouts++
								if event.Agent != string(kind) || event.PlanID != detail.State.Plan.ID {
									t.Fatalf("timeout attribution: %+v", event)
								}
							}
							if event.Type == plan.EventTypeAgentMetrics {
								appendCalls++
								if outcome == "append failure" {
									return errors.New("metrics storage unavailable")
								}
							}
							return repository.AppendEvent(dir, event)
						})
						runner := newAgentSessionRunner(agentSessionRunnerConfig{descriptor: descriptor, logAppender: repository, eventAppender: appender})
						role := plan.AgentRolePullRequest
						var err error
						switch operation {
						case "review", "correction":
							role = plan.AgentRoleReview
							var review plan.PlanReview
							review, err = createReviewWithAgentSession(context.Background(), runner, agentOperationOptions{Agent: string(kind), reviewGitFactory: fixedReviewGit(&fakeReviewGit{head: "head123", currentBranch: "feature"})}, ReviewRun{PlanDir: detail.Dir, Detail: detail, RepoRoot: repoRoot}, fileReviewRecordFactory(repository))
							if sessionErr == nil && (review.Agent != string(kind) || review.Verdict != plan.ReviewVerdictApprove || review.CommitMessage == nil) {
								t.Fatalf("telemetry changed approval: %+v", review)
							}
							if operation == "correction" && sessionErr != nil && (detail.State.Plan.Review.Verdict != plan.ReviewVerdictComment || detail.State.Plan.Review.CommitMessage != nil) {
								t.Fatalf("failed correction retained approval: %+v", detail.State.Plan.Review)
							}
						case "pr":
							var pr plan.PullRequest
							pr, err = createPullRequestWithAgentSession(context.Background(), runner, agentOperationOptions{}, PullRequestRun{PlanDir: detail.Dir, PlanID: "plan-a", RepoRoot: repoRoot})
							if sessionErr == nil && pr.Number != 123 {
								t.Fatalf("PR changed: %+v", pr)
							}
						case "body":
							var body string
							body, err = generatePullRequestBodyWithAgentSession(context.Background(), runner, agentOperationOptions{}, PullRequestBodyRun{PlanDir: detail.Dir, PlanID: "plan-a", RepoRoot: repoRoot})
							if sessionErr == nil && body != "created https://github.com/iamseth/tao/pull/123" {
								t.Fatalf("body changed: %q", body)
							}
						}
						if !errors.Is(err, sessionErr) {
							t.Fatalf("error = %v, want %v", err, sessionErr)
						}
						if (timeouts == 1) != (outcome == "timeout") {
							t.Fatalf("timeout events = %d, outcome = %s", timeouts, outcome)
						}
						wantCalls := 1
						if operation == "correction" {
							wantCalls = 2
						}
						if calls != wantCalls || appendCalls != wantCalls {
							t.Fatalf("provider/metrics calls = %d/%d, want %d", calls, appendCalls, wantCalls)
						}
						events := readAgentMetricEvents(t, detail.Dir)
						if outcome == "append failure" {
							wantCalls = 0
						}
						if len(events) != wantCalls {
							t.Fatalf("metrics events = %d, want %d", len(events), wantCalls)
						}
						failed := 0
						for _, event := range events {
							m := event.Metrics
							if event.PlanID != detail.State.Plan.ID || event.SliceID != "" || m.Role != role || m.Agent != string(kind) || m.Availability != measurement {
								t.Fatalf("event = %+v; metrics = %+v", event, m)
							}
							if m.OutputTokensPresent != (measurement != plan.AgentMetricsUnavailable) || m.CostPresent != (measurement == plan.AgentMetricsReported) || m.Cost != 0 {
								t.Fatalf("measurement presence changed: %+v", m)
							}
							if m.Status == "failed" && m.Result == "failed" {
								failed++
							}
						}
						if (failed == 1) != (sessionErr != nil) {
							t.Fatalf("failed metrics events = %d; session error = %v", failed, sessionErr)
						}
					})
				}
			}
		}
	}
}

func phaseTelemetryStarter(t *testing.T, kind AgentKind, output string, availability plan.AgentMetricsAvailability) ProcessStarter {
	t.Helper()
	encoded, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	if kind == AgentClaude {
		usage := ""
		switch availability {
		case plan.AgentMetricsReported:
			usage = `,"usage":{"input_tokens":5,"output_tokens":7},"total_cost_usd":0`
		case plan.AgentMetricsPartial:
			usage = `,"usage":{"output_tokens":7}`
		}
		return fakeProcessStarter(t, &fakeClaudeStart{}, `{"type":"result","result":`+string(encoded)+usage+`}`)
	}
	stats := `{"type":"session_stats","session_id":"session-1"}`
	switch availability {
	case plan.AgentMetricsReported:
		stats = `{"type":"session_stats","session_id":"session-1","input_tokens":5,"output_tokens":7,"total_tokens":12,"cost":0}`
	case plan.AgentMetricsPartial:
		stats = `{"type":"session_stats","session_id":"session-1","output_tokens":7}`
	}
	return fakePiSessionStarterWithStats(t, output, new(bool), stats)
}

func TestRunAgentSessionInvokesProviderExactlyOnce(t *testing.T) {
	calls := 0
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		calls++
		return agent.SessionResult{Output: "done"}, nil
	})
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, nil, io.Discard, time.Now())

	if _, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
		PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a",
	}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func TestRunAgentSessionCarriesVerificationCommandsToNeutralRuntime(t *testing.T) {
	want := []string{"cd packages/commonStudent && pnpm test"}
	var got agent.Session
	runtime := agentRuntimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
		got = session
		return agent.SessionResult{Output: "done"}, nil
	})
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, nil, io.Discard, time.Now())

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
		PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a", VerificationCommands: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.VerificationCommands, "\n") != strings.Join(want, "\n") {
		t.Fatalf("neutral session verification commands = %#v, want %#v", got.VerificationCommands, want)
	}
}

func TestRunPathAgentSessionTimeoutIsClassified(t *testing.T) {
	const sessionTimeout = time.Millisecond
	repoRoot := t.TempDir()
	detail := runPathSessionDetail(t, repoRoot, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)

	runtime := agentRuntimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
		if session.Timeout != sessionTimeout {
			t.Fatalf("session timeout = %s, want %s", session.Timeout, sessionTimeout)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected timeout decorator to set a run-path deadline")
		}
		<-ctx.Done()
		return agent.SessionResult{Output: "partial"}, ctx.Err()
	})
	execution := timeoutTestRunExecution(sessionTimeout, runtime, repoRoot)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	err := executeDetailWithExecution(ctx, detail, func(context.Context, *plan.PlanDetail) (*plan.PlanDetail, error) {
		return detail, nil
	}, io.Discard, execution)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	var timeoutErr *agent.SessionTimeoutError
	if !errors.As(err, &timeoutErr) {
		t.Fatalf("error = %T %v, want SessionTimeoutError", err, err)
	}
	if timeoutErr.Timeout != sessionTimeout {
		t.Fatalf("timeout error duration = %s, want %s", timeoutErr.Timeout, sessionTimeout)
	}
	if !strings.Contains(err.Error(), "agent session timed out after "+sessionTimeout.String()) {
		t.Fatalf("expected classified timeout message, got %v", err)
	}
}

func TestRunPathAgentSessionTimeoutLeavesFastSessionUnaffected(t *testing.T) {
	const sessionTimeout = time.Hour
	repoRoot := t.TempDir()
	detail := runPathSessionDetail(t, repoRoot, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
	completed := runPathSessionDetail(t, repoRoot, plan.StatusCompleted, nil, []string{"001-a"}, plan.StatusCompleted)
	completed.Dir = detail.Dir

	called := false
	runtime := agentRuntimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
		called = true
		if session.Timeout != sessionTimeout {
			t.Fatalf("session timeout = %s, want %s", session.Timeout, sessionTimeout)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected timeout decorator to set a run-path deadline")
		}
		if err := ctx.Err(); err != nil {
			t.Fatalf("fast session context error before return: %v", err)
		}
		return agent.SessionResult{Output: "done"}, nil
	})
	execution := timeoutTestRunExecution(sessionTimeout, runtime, repoRoot)

	err := executeDetailWithExecution(context.Background(), detail, func(context.Context, *plan.PlanDetail) (*plan.PlanDetail, error) {
		return completed, nil
	}, io.Discard, execution)
	if err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("expected runtime to be called")
	}
}

func TestRunAgentSessionAppendsSessionTimeoutEvent(t *testing.T) {
	const sessionTimeout = 95 * time.Second
	timedOutAt := time.Date(2026, 7, 14, 2, 30, 0, 0, time.FixedZone("test", -5*60*60))
	timeoutErr := &agent.SessionTimeoutError{Timeout: sessionTimeout}
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{}, timeoutErr
	})
	var events []plan.Event
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, eventAppenderFunc(func(_ string, event plan.Event) error {
		events = append(events, event)
		return nil
	}), io.Discard, timedOutAt)

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
		PlanDir:   planDir,
		RepoRoot:  repoRoot,
		LogAction: "running 001-a",
		Metrics:   &AgentSessionMetricsRequest{SliceID: "001-a"},
	})
	if !errors.Is(err, timeoutErr) {
		t.Fatalf("error = %v, want original timeout error", err)
	}
	var event plan.Event
	for _, candidate := range events {
		if candidate.Type == plan.EventTypeSessionTimeout {
			event = candidate
		}
	}
	if event.Type != plan.EventTypeSessionTimeout || event.PlanID != "plan-a" || event.SliceID != "001-a" || event.Agent != "test" {
		t.Fatalf("session timeout event identity = %+v", event)
	}
	if event.DurationSeconds == nil || *event.DurationSeconds != 95 {
		t.Fatalf("duration_seconds = %v, want 95", event.DurationSeconds)
	}
	if !event.Timestamp.Equal(timedOutAt.UTC()) {
		t.Fatalf("timestamp = %s, want %s", event.Timestamp, timedOutAt.UTC())
	}
	if !strings.Contains(event.Message, "test") || !strings.Contains(event.Message, sessionTimeout.String()) {
		t.Fatalf("message = %q, want agent and timeout", event.Message)
	}
}

func TestRunAgentSessionSliceBudgetCaps(t *testing.T) {
	tests := []struct {
		name       string
		outputCap  string
		costCap    string
		metrics    agent.Metrics
		wantMetric string
		wantValue  float64
	}{
		{name: "caps unset", metrics: agent.Metrics{OutputTokens: 200, Cost: 3}},
		{name: "output token cap", outputCap: "100", metrics: agent.Metrics{OutputTokens: 101}, wantMetric: "output_tokens", wantValue: 101},
		{name: "cost cap", costCap: "2.5", metrics: agent.Metrics{Cost: 2.75}, wantMetric: "cost", wantValue: 2.75},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Deprecated aliases still feed the unified budget; STOP may not sit
			// below WARN, so lower the warn thresholds to the cap under test.
			t.Setenv(runtimeconfig.EnvMaxSliceOutputTokensDeprecated, tt.outputCap)
			t.Setenv(runtimeconfig.EnvBudgetSliceOutputTokensWarn, tt.outputCap)
			t.Setenv(runtimeconfig.EnvMaxSliceCostDeprecated, tt.costCap)
			t.Setenv(runtimeconfig.EnvBudgetSliceCostWarn, tt.costCap)
			runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
				metrics := tt.metrics
				return agent.SessionResult{Output: "partial", Metrics: &metrics}, nil
			})
			repository := plan.NewFileRepository("")
			runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, repository, io.Discard, time.Now())
			runner.runtimeEnv = runtimeconfig.RuntimeEnv()

			got, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
				PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a", Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleExecution, EnforceSliceCaps: true},
			})
			if got.Output != "partial" {
				t.Fatalf("output = %q, want partial", got.Output)
			}
			var budgetErr *budgetExceededError
			if (errors.As(err, &budgetErr)) != (tt.wantMetric != "") {
				t.Fatalf("error = %v, want budget exceeded=%t", err, tt.wantMetric != "")
			}
			detail, loadErr := plan.NewFileRepository(filepath.Dir(planDir)).GetPlan(context.Background(), filepath.Base(planDir))
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			var budgetEvent *plan.Event
			for i := range detail.Events {
				if detail.Events[i].Type == plan.EventTypeBudgetExceeded {
					budgetEvent = &detail.Events[i]
				}
			}
			if tt.wantMetric == "" {
				if budgetEvent != nil {
					t.Fatalf("unexpected budget event: %+v", *budgetEvent)
				}
				return
			}
			if budgetEvent == nil || budgetEvent.Metric != tt.wantMetric || budgetEvent.Observed == nil || *budgetEvent.Observed != tt.wantValue {
				t.Fatalf("budget event = %+v, want metric=%s observed=%g", budgetEvent, tt.wantMetric, tt.wantValue)
			}
		})
	}
}

func TestRunAgentSessionCanonicalStopCaps(t *testing.T) {
	t.Run("explicit zero stop still blocks", func(t *testing.T) {
		runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
			return agent.SessionResult{Output: "partial", Metrics: &agent.Metrics{Cost: 0.25}}, nil
		})
		runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, plan.NewFileRepository(""), io.Discard, time.Now())
		runner.runtimeEnv = *runEnvSnapshot(map[string]string{runtimeconfig.EnvBudgetSliceCostStop: "0", runtimeconfig.EnvBudgetSliceCostWarn: "0"})

		got, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
			PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a", Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleExecution, EnforceSliceCaps: true},
		})
		var budgetErr *budgetExceededError
		if got.Output != "partial" || !errors.As(err, &budgetErr) || budgetErr.metric != "cost" || budgetErr.threshold != 0 || budgetErr.observed != 0.25 {
			t.Fatalf("zero stop cap: output=%q err=%#v", got.Output, err)
		}
		detail, loadErr := plan.NewFileRepository(filepath.Dir(planDir)).GetPlan(context.Background(), filepath.Base(planDir))
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		if !slices.ContainsFunc(detail.Events, func(event plan.Event) bool {
			return event.Type == plan.EventTypeBudgetExceeded && event.Metric == "cost"
		}) {
			t.Fatalf("budget_exceeded event missing: %+v", detail.Events)
		}
	})
	t.Run("stop below warn rejects the session naming both keys", func(t *testing.T) {
		calls := 0
		runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
			calls++
			return agent.SessionResult{Metrics: &agent.Metrics{Cost: 0.25}}, nil
		})
		runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, plan.NewFileRepository(""), io.Discard, time.Now())
		runner.runtimeEnv = *runEnvSnapshot(map[string]string{runtimeconfig.EnvBudgetSliceCostStop: "1", runtimeconfig.EnvBudgetSliceCostWarn: "2"})

		_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
			PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a", Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleExecution, EnforceSliceCaps: true},
		})
		if err == nil || calls != 0 || !strings.Contains(err.Error(), runtimeconfig.EnvBudgetSliceCostStop) || !strings.Contains(err.Error(), runtimeconfig.EnvBudgetSliceCostWarn) {
			t.Fatalf("stop below warn: calls=%d err=%v", calls, err)
		}
	})
}

func TestRunAgentSessionSliceBudgetAccumulatesPriorMetrics(t *testing.T) {
	t.Setenv(runtimeconfig.EnvMaxSliceOutputTokensDeprecated, "100")
	t.Setenv(runtimeconfig.EnvBudgetSliceOutputTokensWarn, "100")
	t.Setenv(runtimeconfig.EnvMaxSliceCostDeprecated, "")
	repository := plan.NewFileRepository("")
	current := agent.Metrics{SessionID: "current", OutputTokens: 41}
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{Metrics: &current}, nil
	})
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, repository, io.Discard, time.Now())
	runner.runtimeEnv = runtimeconfig.RuntimeEnv()
	prior := plan.AgentMetrics{SessionID: "prior", OutputTokens: 60}
	if err := repository.AppendEvent(planDir, plan.Event{Type: plan.EventTypeAgentMetrics, Timestamp: time.Now().UTC(), PlanID: "plan-a", SliceID: "001-a", Metrics: &prior, Message: "prior"}); err != nil {
		t.Fatal(err)
	}

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{
		PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running 001-a", Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleExecution, EnforceSliceCaps: true},
	})
	var budgetErr *budgetExceededError
	if !errors.As(err, &budgetErr) || budgetErr.observed != 101 {
		t.Fatalf("error = %#v, want cumulative output token observation 101", err)
	}
}

func TestSliceBudgetExceededBlocksCompletedSliceAndGuardsContinuation(t *testing.T) {
	plansRoot := t.TempDir()
	planDir := filepath.Join(plansRoot, "plan-a")
	if err := os.MkdirAll(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	repoRoot := t.TempDir()
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.Dir = planDir
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	persistRunArtifacts(t, planDir, detail)

	repository := plan.NewFileRepository(plansRoot)
	agentCalls := 0
	budgetErr := &budgetExceededError{metric: "output_tokens", threshold: 100, observed: 101}
	executor := sliceExecutorFunc(func(ctx context.Context, run SliceRun) error {
		agentCalls++
		active, err := repository.GetPlan(ctx, "plan-a")
		if err != nil {
			return err
		}
		record, err := repository.PlanRecord(active)
		if err != nil {
			return err
		}
		completedAt := time.Now().UTC()
		if err := record.RecordSliceCommitIntent(run.SliceID, plan.SliceCommitIntent{Hash: "intent", Policy: "slice", Message: "test completion", CreatedAt: completedAt}); err != nil {
			return err
		}
		if err := record.CompleteSliceWithOutcome(run.SliceID, "completed before metrics arrived", nil, plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "commit-a"}, completedAt); err != nil {
			return err
		}
		return budgetErr
	})
	options := Options{
		ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent}},
		RunDependencies: RunDependencies{SliceExecutor: executor, CommandRunner: runGitFake(&[]string{}, nil)},
	}
	service := NewService(repository, io.Discard, options)
	err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: options.ResolvedRunOptions})
	if err == nil {
		t.Fatal("run error = nil, want blocked telemetry-cap recovery guidance")
	}
	for _, want := range []string{
		"Blocked slice 001-a: slice agent metrics output_tokens cap exceeded: observed 101, threshold 100",
		"Resolve this blocker before continuing, then run:\n  tao run --continue plan-a",
		"Agent log:",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("run error = %v, want %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "may be resumed with --continue") {
		t.Fatalf("run error retains retry-first guidance: %v", err)
	}
	if !errors.Is(err, budgetErr) {
		t.Fatalf("run error = %v, want original agent budget error", err)
	}

	blocked, err := repository.GetPlan(context.Background(), "plan-a")
	if err != nil {
		t.Fatal(err)
	}
	blockedSlice := blocked.Slices.Slices[0]
	if blocked.State.Status != plan.StatusBlocked || blockedSlice.Status != plan.StatusBlocked || blocked.State.Plan.CurrentSlice == nil || *blocked.State.Plan.CurrentSlice != "001-a" {
		t.Fatalf("persisted blocked lifecycle = state=%+v slice=%+v", blocked.State, blockedSlice)
	}
	if slices.Contains(blocked.State.Plan.CompletedSlices, "001-a") || !slices.Contains(blocked.State.Plan.PendingSlices, "001-a") {
		t.Fatalf("persisted queues = completed %v pending %v", blocked.State.Plan.CompletedSlices, blocked.State.Plan.PendingSlices)
	}
	if blockedSlice.CommitIntent == nil || blockedSlice.Completion == nil {
		t.Fatalf("completion recovery evidence was discarded: %+v", blockedSlice)
	}

	continueOptions := options
	continueOptions.Continue = true
	continueService := NewService(repository, io.Discard, continueOptions)
	err = continueService.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: continueOptions.ResolvedRunOptions})
	if err == nil || !strings.Contains(err.Error(), "interrupted post-intent completion transaction") {
		t.Fatalf("continue error = %v, want guarded completion recovery", err)
	}
	if agentCalls != 1 {
		t.Fatalf("agent calls = %d, want continuation not to rerun completed agent", agentCalls)
	}
}

func TestSliceBudgetExceededUsesInterruptedResumeBoundary(t *testing.T) {
	input := interruptedInput()
	before := input.Detail.Slices.Slices[0]
	input.Detail.State.Plan.ID = "plan-a"
	input.Detail.Slices.PlanID = "plan-a"
	input.Detail.Dir = t.TempDir()
	persistRunArtifacts(t, input.Detail.Dir, input.Detail)
	record, err := plan.NewPlanRecord(input.Detail.Dir, input.Detail)
	if err != nil {
		t.Fatal(err)
	}
	budgetErr := &budgetExceededError{metric: "output_tokens", threshold: 100, observed: 101}
	if err := record.BlockSliceForBudget(input.SliceID, budgetErr.Error(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := plan.NewFileRepository(filepath.Dir(input.Detail.Dir)).GetPlan(context.Background(), filepath.Base(input.Detail.Dir))
	if err != nil {
		t.Fatal(err)
	}
	input.Detail = reloaded
	input.ContinueBlocked = true

	got := ClassifyInterruptedSlice(input)
	if got.Disposition != InterruptedSliceBlockedContinue || got.ContinuationDisposition != InterruptedSliceResume {
		t.Fatalf("budget stop recovery = %#v, want blocked continuation through ordinary interrupted resume", got)
	}
	after := input.Detail.Slices.Slices[0]
	if before.ExecutionRoot != after.ExecutionRoot || before.ExecutionStart == nil || after.ExecutionStart == nil || *before.ExecutionStart != *after.ExecutionStart {
		t.Fatalf("recovery boundary changed: before=%+v after=%+v", before, after)
	}
}

func TestRunAgentSessionTimeoutAppendFailurePreservesResult(t *testing.T) {
	timeoutErr := &agent.SessionTimeoutError{Timeout: 2 * time.Minute}
	want := AgentSessionResult{Output: "partial output", FinalText: "partial final text"}
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{Output: want.Output, FinalText: want.FinalText}, timeoutErr
	})
	var log bytes.Buffer
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, eventAppenderFunc(func(string, plan.Event) error {
		return errors.New("journal unavailable")
	}), &log, time.Now())

	got, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, LogAction: "reviewing plan"})
	if got != want || !errors.Is(err, timeoutErr) {
		t.Fatalf("result, error = %+v, %v; want %+v, original timeout error", got, err, want)
	}
	if !strings.Contains(log.String(), "tao telemetry warning: append session timeout event: journal unavailable") {
		t.Fatalf("session log missing append warning: %q", log.String())
	}
}

func TestRunAgentSessionNonTimeoutErrorSkipsSessionTimeoutEvent(t *testing.T) {
	runErr := errors.New("agent failed")
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{}, runErr
	})
	appendCalls := 0
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, eventAppenderFunc(func(string, plan.Event) error {
		appendCalls++
		return nil
	}), io.Discard, time.Now())

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, LogAction: "running"})
	if !errors.Is(err, runErr) {
		t.Fatalf("error = %v, want original run error", err)
	}
	if appendCalls != 0 {
		t.Fatalf("event append calls = %d, want 0", appendCalls)
	}
}

func TestRunAgentSessionStateReadErrorSkipsSessionTimeoutEvent(t *testing.T) {
	timeoutErr := &agent.SessionTimeoutError{Timeout: time.Minute}
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{}, timeoutErr
	})
	appendCalls := 0
	planDir := t.TempDir()
	runner := newAgentSessionRunner(agentSessionRunnerConfig{
		descriptor: agent.Descriptor{
			Label: "test", NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime },
		},
		logAppender:   plan.NewFileRepository(""),
		eventAppender: eventAppenderFunc(func(string, plan.Event) error { appendCalls++; return nil }),
		now:           time.Now,
	})

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, LogAction: "running"})
	if !errors.Is(err, timeoutErr) {
		t.Fatalf("error = %v, want original timeout error", err)
	}
	if appendCalls != 0 {
		t.Fatalf("event append calls = %d, want 0", appendCalls)
	}
}

func TestRunAgentSessionPromptCapture(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(fmt.Sprint(blocked), func(t *testing.T) {
			var log bytes.Buffer
			runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
				return agent.SessionResult{Output: "output", FinalText: "final", Metrics: &agent.Metrics{OutputTokens: 12}}, nil
			})
			timestamp := time.Date(2026, 10, 3, 6, 0, 0, 0, time.UTC)
			runner, dir, root := sessionEventTestRunner(t, runtime, plan.NewFileRepository(""), &log, timestamp)
			if blocked {
				if err := os.WriteFile(promptcapture.Dir(dir), []byte("blocked"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: dir, RepoRoot: root, Prompt: "rendered prompt", PromptTemplate: prompts.PromptRun, Metrics: &AgentSessionMetricsRequest{Role: plan.AgentRoleExecution, SliceID: "001-a"}})
			if err != nil || got.Output != "output" || got.FinalText != "final" {
				t.Fatalf("outcome = %+v, %v", got, err)
			}
			events := readAgentMetricEvents(t, dir)
			if len(events) != 1 || events[0].Metrics == nil {
				t.Fatalf("metrics = %+v", events)
			}
			metrics := events[0].Metrics
			if metrics.OutputTokens != 12 || metrics.PromptHash != promptcapture.Hash("rendered prompt") || metrics.PromptTemplate != prompts.PromptRun {
				t.Fatalf("metrics = %+v", metrics)
			}
			if blocked {
				if !strings.Contains(log.String(), "tao prompt-capture warning:") {
					t.Fatalf("missing diagnostic: %s", &log)
				}
				return
			}
			entries, err := promptcapture.List(promptcapture.Dir(dir))
			if err != nil || len(entries) != 1 {
				t.Fatalf("captures = %+v, %v", entries, err)
			}
			header, body, err := promptcapture.Read(entries[0].Path)
			hash, versionErr := prompts.TemplateVersion(prompts.PromptRun)
			if err != nil || versionErr != nil || body != "rendered prompt" || header.Role != "execution" || header.SHA256 != metrics.PromptHash || header.TemplateHash != hash || header.StartedAt != timestamp.Format(time.RFC3339) || !strings.Contains(entries[0].Name, "-001-a-") {
				t.Fatalf("capture = %+v, %q, %v", entries[0], body, err)
			}
		})
	}
}

func TestPullRequestSessionsCaptureDistinctPrompts(t *testing.T) {
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{Output: "https://github.com/example/repo/pull/1", FinalText: "body"}, nil
	})
	runner, dir, root := sessionEventTestRunner(t, runtime, plan.NewFileRepository(""), io.Discard, time.Date(2026, 10, 3, 6, 0, 0, 0, time.UTC))
	if _, err := createPullRequestWithAgentSession(context.Background(), runner, agentOperationOptions{}, PullRequestRun{PlanDir: dir, PlanID: "plan-a", RepoRoot: root}); err != nil {
		t.Fatal(err)
	}
	if _, err := generatePullRequestBodyWithAgentSession(context.Background(), runner, agentOperationOptions{}, PullRequestBodyRun{PlanDir: dir, PlanID: "plan-a", RepoRoot: root}); err != nil {
		t.Fatal(err)
	}
	entries, err := promptcapture.List(promptcapture.Dir(dir))
	if err != nil || len(entries) != 2 {
		t.Fatalf("captures = %+v, %v", entries, err)
	}
	templates := map[string]bool{}
	for _, entry := range entries {
		templates[entry.Header.Template] = true
		if entry.Header.Role != string(plan.AgentRolePullRequest) {
			t.Fatalf("role = %s", entry.Header.Role)
		}
		if entry.Header.Template == "pr-body" && entry.Header.TemplateHash != promptcapture.Hash(pullRequestBodyPromptTemplate) {
			t.Fatal("body template hash mismatch")
		}
	}
	if !templates[prompts.PromptPR] || !templates["pr-body"] || entries[0].Header.StartedAt != entries[1].Header.StartedAt {
		t.Fatalf("captures = %+v", entries)
	}
}

func TestRunAgentSessionPlanTelemetryAndCapIsolation(t *testing.T) {
	t.Setenv(runtimeconfig.EnvMaxSliceOutputTokensDeprecated, "1")
	t.Setenv(runtimeconfig.EnvMaxSliceCostDeprecated, "1")
	for _, request := range []AgentSessionMetricsRequest{
		{Role: plan.AgentRoleReview},
		{Role: plan.AgentRoleExecution, EnforceSliceCaps: true}, // no invented slice
		{SliceID: "001-a", Role: plan.AgentRoleExecution},       // collection is not cap authority
		{SliceID: "001-a", Role: plan.AgentRoleReview, EnforceSliceCaps: true},
	} {
		for _, runErr := range []error{nil, &agent.SessionTimeoutError{Timeout: time.Minute}, retryableTransportTestError{error: errors.New("private transport failure")}} {
			runtime := agentRuntimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
				if !session.CollectMetrics {
					t.Fatal("plan-scoped collection not requested")
				}
				return agent.SessionResult{Output: "partial output", FinalText: "final", Metrics: &agent.Metrics{OutputTokens: 100, Cost: 5}}, runErr
			})
			repository := plan.NewFileRepository("")
			runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, repository, io.Discard, time.Now())
			runner.runtimeEnv = runtimeconfig.RuntimeEnv()
			got, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, Metrics: &request})
			if !errors.Is(err, runErr) || got.Output != "partial output" || got.FinalText != "final" {
				t.Fatalf("result/error changed: %+v, %v; want %v", got, err, runErr)
			}
			events := readAgentMetricEvents(t, planDir)
			if len(events) != 1 || events[0].Metrics == nil {
				t.Fatalf("metrics events = %+v", events)
			}
			event := events[0]
			if event.SliceID != request.SliceID || event.Metrics.Role != request.Role || event.Metrics.OutputTokens != 100 || (event.Metrics.Status == "failed") != (runErr != nil) {
				t.Fatalf("metrics = %+v, payload = %+v", event, event.Metrics)
			}
			detail, loadErr := plan.NewFileRepository(filepath.Dir(planDir)).GetPlan(context.Background(), filepath.Base(planDir))
			if loadErr != nil {
				t.Fatal(loadErr)
			}
			for _, event := range detail.Events {
				if event.Type == plan.EventTypeBudgetExceeded {
					t.Fatalf("non-execution cap: %+v", event)
				}
				if event.Type == plan.EventTypeSessionTimeout && event.SliceID != request.SliceID {
					t.Fatalf("fabricated timeout slice: %+v", event)
				}
			}
		}
	}
}

func TestRunAgentSessionTelemetryFailuresPreserveProviderOutcome(t *testing.T) {
	for _, failure := range []string{"state", "metrics append", "budget append"} {
		for _, runErr := range []error{nil, &agent.SessionTimeoutError{Timeout: time.Minute}, retryableTransportTestError{error: errors.New("transport failed")}} {
			t.Run(fmt.Sprintf("%s/%v", failure, runErr), func(t *testing.T) {
				t.Setenv(runtimeconfig.EnvMaxSliceOutputTokensDeprecated, "1")
				t.Setenv(runtimeconfig.EnvBudgetSliceOutputTokensWarn, "1")
				calls := 0
				runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
					calls++
					return agent.SessionResult{Output: "output", Metrics: &agent.Metrics{OutputTokens: 100}}, runErr
				})
				repository := plan.NewFileRepository("")
				appendCalls := 0
				appender := eventAppenderFunc(func(dir string, event plan.Event) error {
					appendCalls++
					if failure == "metrics append" || event.Type == plan.EventTypeBudgetExceeded {
						return errors.New("append failed")
					}
					return repository.AppendEvent(dir, event)
				})
				runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, appender, io.Discard, time.Now())
				runner.runtimeEnv = runtimeconfig.RuntimeEnv()
				if failure == "state" {
					if err := os.Remove(filepath.Join(planDir, "state.json")); err != nil {
						t.Fatal(err)
					}
				}
				got, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleExecution, EnforceSliceCaps: true}})
				if !errors.Is(err, runErr) || got.Output != "output" || calls != 1 {
					t.Fatalf("outcome changed: %+v, %v, calls=%d", got, err, calls)
				}
				if failure == "state" && appendCalls != 0 {
					t.Fatal("appended without plan identity")
				}
			})
		}
	}
}

func TestRunAgentSessionPreSessionFailureDoesNotEmitMetrics(t *testing.T) {
	repoRoot := t.TempDir()
	detail := runPathSessionDetail(t, repoRoot, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
	guardErr := errors.New("fingerprint failed")
	runner := newAgentSessionRunner(agentSessionRunnerConfig{
		descriptor: agent.Descriptor{Label: "test", NewRuntime: func(agent.RuntimeDeps) agent.Runtime {
			return agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
				t.Fatal("unexpected provider invocation")
				return agent.SessionResult{}, nil
			})
		}},
		logAppender:   plan.NewFileRepository(""),
		eventAppender: eventAppenderFunc(func(string, plan.Event) error { t.Fatal("event for nonexistent session"); return nil }),
		commandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error { return guardErr },
	})
	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: detail.Dir, RepoRoot: t.TempDir(), Metrics: &AgentSessionMetricsRequest{Role: plan.AgentRoleReview}})
	if !errors.Is(err, guardErr) {
		t.Fatalf("error = %v", err)
	}
}

func TestRunAgentSessionUnavailableMetricsRemainUnmeasured(t *testing.T) {
	t.Setenv(runtimeconfig.EnvMaxSliceOutputTokensDeprecated, "1")
	t.Setenv(runtimeconfig.EnvBudgetSliceOutputTokensWarn, "1")
	for _, missing := range []*agent.Metrics{nil, {Availability: agentmetrics.Unavailable, SessionID: "session"}} {
		runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
			return agent.SessionResult{Metrics: missing, MetricsWarning: "private warning"}, nil
		})
		repository := plan.NewFileRepository("")
		runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, repository, io.Discard, time.Now())
		runner.runtimeEnv = runtimeconfig.RuntimeEnv()
		// Prior usage must not turn this unavailable measurement into a cap check.
		if err := repository.AppendEvent(planDir, plan.Event{Type: plan.EventTypeAgentMetrics, SliceID: "001-a", Metrics: &plan.AgentMetrics{OutputTokens: 100}}); err != nil {
			t.Fatal(err)
		}
		_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleExecution, EnforceSliceCaps: true}})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, event := range readAgentMetricEvents(t, planDir) {
			if event.Metrics.Role != plan.AgentRoleExecution {
				continue
			}
			found = true
			if event.Metrics.Availability != plan.AgentMetricsUnavailable || event.Metrics.OutputTokensPresent || event.Metrics.CostPresent || strings.Contains(event.Message, "private") {
				t.Fatalf("unavailable event = %+v", event)
			}
		}
		if !found {
			t.Fatal("unavailable event missing")
		}
	}
}

func TestRunAgentSessionCapsExcludeAttributedNonExecutionHistory(t *testing.T) {
	t.Setenv(runtimeconfig.EnvMaxSliceOutputTokensDeprecated, "100")
	t.Setenv(runtimeconfig.EnvBudgetSliceOutputTokensWarn, "100")
	repository := plan.NewFileRepository("")
	runtime := agentRuntimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		return agent.SessionResult{Metrics: &agent.Metrics{OutputTokens: 40}}, nil
	})
	runner, planDir, repoRoot := sessionEventTestRunner(t, runtime, repository, io.Discard, time.Now())
	runner.runtimeEnv = runtimeconfig.RuntimeEnv()
	for _, role := range []plan.AgentRole{plan.AgentRoleReview, plan.AgentRolePullRequest, plan.AgentRoleMerge} {
		if err := repository.AppendEvent(planDir, plan.Event{Type: plan.EventTypeAgentMetrics, PlanID: "plan-a", SliceID: "001-a", Timestamp: time.Now(), Metrics: &plan.AgentMetrics{Role: role, OutputTokens: 1000}}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: repoRoot, Metrics: &AgentSessionMetricsRequest{SliceID: "001-a", Role: plan.AgentRoleRework, EnforceSliceCaps: true}})
	if err != nil {
		t.Fatal(err)
	}
	summary := plan.SummarizeAgentMetrics(plan.AgentMetricsEvents(readAgentMetricEvents(t, planDir)))
	if summary.Totals.OutputTokens != 3040 {
		t.Fatalf("plan totals changed: %+v", summary.Totals)
	}
}

func TestImplementationCompletionGrace(t *testing.T) {
	for _, tc := range []struct {
		name     string
		role     plan.AgentRole
		timeout  time.Duration
		commands []string
		want     time.Duration
	}{
		{"execution", plan.AgentRoleExecution, time.Hour, []string{"one", "two", "three"}, 30 * time.Minute},
		{"rework", plan.AgentRoleRework, time.Hour, nil, 10 * time.Minute},
		{"review", plan.AgentRoleReview, time.Hour, nil, 0},
		{"pull request", plan.AgentRolePullRequest, time.Hour, nil, 0},
		{"unbounded", plan.AgentRoleExecution, 0, nil, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			detail := runPathSessionDetail(t, root, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
			calls := 0
			runtime := agentRuntimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
				calls++
				if tc.want == 0 {
					if session.Grace != nil {
						t.Fatal("unexpected grace")
					}
					return agent.SessionResult{}, nil
				}
				if session.Grace == nil || session.Grace.Max != tc.want || session.Grace.Active == nil {
					t.Fatalf("grace = %+v, want %s", session.Grace, tc.want)
				}
				if session.Grace.Active() {
					t.Fatal("active without managed completion")
				}
				owner, err := readCompletionOwner(detail.Dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv(sliceCompletionOwnerEnv, owner.Token)
				guard, err := BindSliceCompletionLifetime(context.Background(), detail.Dir, "001-a")
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = guard.Close() }()
				if !session.Grace.Active() {
					t.Fatal("managed completion is not active")
				}
				if err := guard.Close(); err != nil {
					t.Fatal(err)
				}
				if session.Grace.Active() {
					t.Fatal("active after completion closed")
				}
				return agent.SessionResult{}, nil
			})
			runner := newAgentSessionRunner(agentSessionRunnerConfig{
				runtimeEnv: runEnvSnapshot(nil), sessionTimeout: tc.timeout,
				descriptor:  agent.Descriptor{Label: "fake", NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }},
				logAppender: plan.NewFileRepository(""),
			})
			_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: detail.Dir, RepoRoot: root, VerificationCommands: tc.commands, Metrics: &AgentSessionMetricsRequest{Role: tc.role, SliceID: "001-a"}})
			if err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestImplementationWrapUpPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		role        plan.AgentRole
		slice       string
		timeout     time.Duration
		want        int
		invalid     bool
	}{
		{"default", "", plan.AgentRoleExecution, "001-a", time.Hour, 80, false},
		{"rework", "45", plan.AgentRoleRework, "001-a", time.Hour, 45, false},
		{"disabled", "0", plan.AgentRoleExecution, "001-a", time.Hour, 0, false},
		{"unbounded", "80", plan.AgentRoleExecution, "001-a", 0, 0, false},
		{"invalid", "100", plan.AgentRoleExecution, "001-a", time.Hour, 0, true},
		{"invalid unbounded", "bad", plan.AgentRoleRework, "001-a", 0, 0, true},
		{"review", "bad", plan.AgentRoleReview, "001-a", time.Hour, 0, false},
		{"pr", "bad", plan.AgentRolePullRequest, "", time.Hour, 0, false},
		{"no slice", "bad", plan.AgentRoleExecution, "", time.Hour, 0, false},
		{"no operation", "bad", "", "", time.Hour, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			runtime := agentRuntimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
				calls++
				if tc.want == 0 {
					if session.Warning != nil || session.WarningMessages != nil {
						t.Fatal("unexpected warning")
					}
				} else if session.Warning == nil || session.Warning.Percent != tc.want || session.WarningMessages == nil {
					t.Fatalf("warning = %+v, want %d", session.Warning, tc.want)
				}
				return agent.SessionResult{}, nil // Unsupported providers may ignore the channel.
			})
			root := t.TempDir()
			detail := runPathSessionDetail(t, root, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
			values := map[string]string{}
			if tc.value != "" {
				values[runtimeconfig.EnvSessionWarnPercent] = tc.value
			}
			runner := newAgentSessionRunner(agentSessionRunnerConfig{
				runtimeEnv: runEnvSnapshot(values), sessionTimeout: tc.timeout,
				descriptor:  agent.Descriptor{Label: "fake", NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }},
				logAppender: plan.NewFileRepository(""),
			})
			// Configuration is invocation-local, even if the process environment changes.
			t.Setenv(runtimeconfig.EnvSessionWarnPercent, "invalid-after-snapshot")
			_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: detail.Dir, RepoRoot: root, Metrics: &AgentSessionMetricsRequest{Role: tc.role, SliceID: tc.slice}})
			if tc.invalid {
				if err == nil || !strings.Contains(err.Error(), runtimeconfig.EnvSessionWarnPercent) || calls != 0 {
					t.Fatalf("calls=%d err=%v", calls, err)
				}
			} else if err != nil || calls != 1 {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
		})
	}
}

func sessionEventTestRunner(t *testing.T, runtime agent.Runtime, appender plan.EventAppender, logWriter io.Writer, timestamp time.Time) (agentSessionRunner, string, string) {
	t.Helper()
	repoRoot := t.TempDir()
	detail := runPathSessionDetail(t, repoRoot, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
	return newAgentSessionRunner(agentSessionRunnerConfig{
		descriptor: agent.Descriptor{
			Label: "test", MetricsMessage: "captured test metrics",
			NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime },
		},
		logAppender:      plan.NewFileRepository(""),
		eventAppender:    appender,
		sessionLogWriter: logWriter,
		now:              func() time.Time { return timestamp },
	}), detail.Dir, repoRoot
}

func runPathSessionDetail(t *testing.T, repoRoot string, status string, pending []string, completed []string, sliceStatus string) *plan.PlanDetail {
	t.Helper()
	detail := runPlanDetail(status, pending, completed, "001-a", sliceStatus, nil, nil)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	return detail
}

func timeoutTestRunExecution(sessionTimeout time.Duration, runtime agent.Runtime, repoRoot string) runExecution {
	execution := testRunExecution(ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, SessionTimeout: sessionTimeout}}, RunDependencies{
		AgentFactory: func(execution runExecution) agentRunCapabilities {
			descriptor := agent.Descriptor{Label: "test", NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }}
			executor := newAgentExecutor(descriptor, execution.Config, execution.Dependencies, execution.StartingBranch, execution.StartingDirtyPaths)
			return agentRunCapabilities{sliceExecutor: executor, pullRequestBodyGenerator: executor}
		},
		LogAppender:       plan.NewFileRepository(""),
		PlanRecordFactory: memoryPlanRecordFactory,
	})
	execution.ExecutionRoot = repoRoot
	return execution
}

type agentRuntimeFunc func(context.Context, agent.Session) (agent.SessionResult, error)

func (f agentRuntimeFunc) RunSession(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
	return f(ctx, session)
}
