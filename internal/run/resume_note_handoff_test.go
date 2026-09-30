package run

import (
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

func TestResumeNoteAdmittedContinuation(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, mode := range []string{"continue", "transport", "stale", "unavailable", "refused"} {
			t.Run(provider+"/"+mode, func(t *testing.T) {
				home := resumeNoteTempDir(t)
				t.Setenv("TAO_DATA_HOME", home)
				root := t.TempDir()
				detail := interruptedServiceRunDetail(t, root)
				persistRunArtifacts(t, detail.Dir, detail)
				repository := plan.NewFileRepository(filepath.Dir(detail.Dir))
				for _, event := range detail.Events {
					if err := repository.AppendEvent(detail.Dir, event); err != nil {
						t.Fatal(err)
					}
				}
				record, err := plan.NewPlanRecord(detail.Dir, detail)
				if err != nil {
					t.Fatal(err)
				}
				blocked := detail.Slices.Slices[0].Timing.StartedAt.Add(time.Minute)
				const note = "last action: inspected; next action: fix; why: test; do-not: trust prose"
				// Exercise activation through the neutral fake session, then the ordinary
				// block/cache path. Continuation below must still pass normal admission.
				warningCalls := 0
				runtime := agentRuntimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
					warningCalls++
					select {
					case <-session.WarningMessages:
					case <-ctx.Done():
						return agent.SessionResult{}, ctx.Err()
					}
					if err := record.BlockSlice("001-a", "waiting", blocked); err != nil {
						return agent.SessionResult{}, err
					}
					return agent.SessionResult{}, (ResumeNoteStore{}).Save(detail, "001-a", note)
				})
				runner := newAgentSessionRunner(agentSessionRunnerConfig{
					runtimeEnv: runEnvSnapshot(map[string]string{"TAO_SESSION_WARN_PERCENT": "1"}), sessionTimeout: time.Second,
					descriptor:  agent.Descriptor{Label: provider, NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }},
					logAppender: repository, eventAppender: repository,
					commandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error { return nil },
					now:           func() time.Time { return blocked.Add(time.Second) },
				})
				if _, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: detail.Dir, RepoRoot: root, Metrics: &AgentSessionMetricsRequest{Role: plan.AgentRoleExecution, SliceID: "001-a"}}); err != nil || warningCalls != 1 {
					t.Fatalf("warning calls=%d err=%v", warningCalls, err)
				}
				detail, err = repository.GetPlan(context.Background(), filepath.Base(detail.Dir))
				if err != nil {
					t.Fatal(err)
				}
				metricsFound := false
				for _, event := range detail.Events {
					if event.Type == plan.EventTypeAgentMetrics && event.SliceID == "001-a" && event.Timestamp.After(blocked) {
						metricsFound = true
					}
				}
				if !metricsFound {
					t.Fatal("normal session runner did not persist post-block metrics")
				}
				record, err = plan.NewPlanRecord(detail.Dir, detail)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "stale" {
					if err := record.BlockSlice("001-a", "waiting", blocked.Add(time.Second)); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "unavailable" {
					if err := os.RemoveAll(home); err != nil {
						t.Fatal(err)
					}
				}
				stopped := errors.New("stop provider")
				if mode == "transport" {
					stopped = retryableTransportTestError{error: stopped}
				}
				calls := 0
				wantNote := mode == "continue" || mode == "transport"
				err = NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
					CommandRunner:       interruptedServiceGitRunner(t, root, &[]string{}, func() string { return " M partial.go\n" }, "tao/plan-a", "base"),
					EventAppender:       eventAppenderFunc(func(string, plan.Event) error { return nil }),
					PlanRecordFactory:   memoryPlanRecordFactory,
					TransportRetryDelay: func(context.Context, time.Duration) error { return nil },
					SliceExecutor: sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
						calls++
						if !run.Resuming || run.ResumeAttempt != calls || strings.Contains(run.RunPacket, note) != wantNote {
							t.Fatalf("bad admitted packet: %+v", run)
						}
						return stopped
					}),
				}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{Agent: runtimeconfig.AgentKind(provider), Continue: mode != "refused", ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
				if mode == "refused" {
					if err == nil || calls != 0 {
						t.Fatalf("unadmitted handoff: calls=%d err=%v", calls, err)
					}
					return
				}
				if !errors.Is(err, stopped) {
					t.Fatal(err)
				}
				wantCalls := 1
				if mode == "transport" {
					wantCalls = 3
				}
				if calls != wantCalls {
					t.Fatalf("calls=%d want %d", calls, wantCalls)
				}
				for _, event := range detail.Events {
					if strings.Contains(event.Message, note) || strings.Contains(event.Reason, note) {
						t.Fatal("note leaked to event")
					}
				}
				if text, _ := (ResumeNoteStore{}).Load(detail, "001-a"); text != "" {
					t.Fatal("live continued state reused blocked note")
				}
			})
		}
	}
}
