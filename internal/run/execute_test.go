package run

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

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
