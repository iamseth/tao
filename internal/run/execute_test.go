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

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestExecuteSessionTimeoutSettlesOnlyCompletedSlice(t *testing.T) {
	for _, completed := range []bool{true, false} {
		t.Run(map[bool]string{true: "completed", false: "incomplete"}[completed], func(t *testing.T) {
			root := t.TempDir()
			plansRoot := t.TempDir()
			detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
			detail.Dir = filepath.Join(plansRoot, "plan-a")
			if err := os.MkdirAll(detail.Dir, 0o700); err != nil {
				t.Fatal(err)
			}
			detail.State.Repo.Root = root
			detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
			persistRunArtifacts(t, detail.Dir, detail)
			repository := plan.NewFileRepository(plansRoot)
			timeoutErr := &agent.SessionTimeoutError{Timeout: time.Minute}
			calls := 0
			var out bytes.Buffer
			options := Options{
				ExecutionConfig: ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeCurrent, MaxSlices: 1}},
				RunDependencies: RunDependencies{
					CommandRunner: runGitFake(&[]string{}, nil),
					SliceExecutor: sliceExecutorFunc(func(ctx context.Context, run SliceRun) error {
						calls++
						if completed {
							active, err := repository.GetPlan(ctx, "plan-a")
							if err != nil {
								return err
							}
							record, err := repository.PlanRecord(active)
							if err != nil {
								return err
							}
							if err := record.CompleteSlice(run.SliceID, "completed inside grace", nil, time.Now().UTC()); err != nil {
								return err
							}
						}
						return timeoutErr
					}),
				},
			}
			err := NewService(repository, &out, options).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: options.ResolvedRunOptions})
			if completed {
				if err != nil {
					t.Fatalf("completed timeout: %v", err)
				}
				if !strings.Contains(out.String(), "Slice completed: 001-a") {
					t.Fatalf("completed handoff missing: %s", &out)
				}
			} else if !errors.Is(err, timeoutErr) || !strings.Contains(err.Error(), "failed while running slice 001-a") {
				t.Fatalf("error = %v, want provider timeout", err)
			}
			if calls != 1 {
				t.Fatalf("handoffs = %d, want 1", calls)
			}
			reloaded, err := repository.GetPlan(context.Background(), "plan-a")
			if err != nil {
				t.Fatal(err)
			}
			if plan.SliceCompleted(reloaded, "001-a") != completed {
				t.Fatal("unexpected durable completion")
			}
			for _, event := range reloaded.Events {
				if !completed && event.Type == plan.EventTypeSliceCompleted {
					t.Fatal("unexpected completion event")
				}
			}
		})
	}
}

func TestExecuteRecordedReworkModel(t *testing.T) {
	for _, selection := range []struct {
		name   string
		events []plan.Event
		models runtimeconfig.ModelSelection
	}{
		{name: "recorded", events: []plan.Event{{Type: plan.EventTypeReworkRound, Round: 4, Model: "m"}}, models: runtimeconfig.ModelSelection{Base: "base", Run: "run"}},
		{name: "legacy round", events: []plan.Event{{Type: plan.EventTypeReworkRound, Round: 4}}, models: runtimeconfig.ModelSelection{Run: "run"}},
		{name: "no round", models: runtimeconfig.ModelSelection{Run: "run"}},
		{name: "base fallback", models: runtimeconfig.ModelSelection{Base: "base"}},
		{name: "unset"},
	} {
		for _, kind := range []AgentKind{AgentPi, AgentClaude} {
			for _, sliceID := range []string{"r401-fix", "r301-fix", "001-a"} {
				t.Run(selection.name+"/"+string(kind)+"/"+sliceID, func(t *testing.T) {
					root := t.TempDir()
					detail := interruptedServiceRunDetail(t, root)
					detail.State.Plan.CurrentSlice = &sliceID
					detail.State.Plan.PendingSlices = []string{sliceID}
					detail.Slices.Slices[0].ID = sliceID
					detail.Events[0].SliceID = sliceID
					detail.Events = append(detail.Events, selection.events...)
					want := selection.models.For(runtimeconfig.ModelRoleRun)
					if selection.name == "recorded" && sliceID == "r401-fix" {
						want = "m"
					}

					// Exercise the initial handoff and both transport retry handoffs
					// through the real slice/session wiring to the fake runtime.
					transportErr := retryableTransportTestError{error: errors.New("transport dropped")}
					calls := 0
					descriptor, _ := agent.Lookup(kind)
					descriptor.NewRuntime = func(agent.RuntimeDeps) agent.Runtime {
						return agentRuntimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
							calls++
							if session.Model != want {
								t.Errorf("handoff %d model = %q, want %q", calls, session.Model, want)
							}
							return agent.SessionResult{}, transportErr
						})
					}
					options := ResolvedRunOptions{Agent: kind, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice, Models: selection.models}
					dependencies := RunDependencies{
						CommandRunner:       interruptedServiceGitRunner(t, root, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
						TransportRetryDelay: func(context.Context, time.Duration) error { return nil },
						LogAppender:         plan.NewFileRepository(""),
					}
					dependencies.SliceExecutor = newAgentExecutor(descriptor, ExecutionConfig{ResolvedRunOptions: options}, dependencies, "", nil)
					service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: dependencies})
					if err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: options}); !errors.Is(err, transportErr) {
						t.Fatalf("execute error = %v, want transport failure", err)
					}
					if calls != 3 {
						t.Fatalf("runtime calls = %d, want 3", calls)
					}
				})
			}
		}
	}
}
