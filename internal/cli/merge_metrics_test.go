package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agentsession"
	commitcontract "github.com/iamseth/tao/internal/commit"
	mergepkg "github.com/iamseth/tao/internal/merge"
	"github.com/iamseth/tao/internal/plan"
	runpkg "github.com/iamseth/tao/internal/run"
)

func TestSingleMergeProposalTelemetryProviders(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, outcome := range []string{"reported", "partial", "unavailable", "malformed", "failure", "timeout", "start-failure"} {
			for _, appendFails := range []bool{false, true} {
				name := provider + "/" + outcome
				if appendFails {
					name += "/append-failure"
				}
				t.Run(name, func(t *testing.T) {
					t.Setenv("TAO_AGENT", provider)
					t.Setenv("TAO_SESSION_TIMEOUT", "5s")
					if outcome == "timeout" {
						t.Setenv("TAO_SESSION_TIMEOUT", "100ms")
					}
					detail := cliMergeDetail(t)
					before := len(detail.Events)
					appender := &recordingMergeMetricsAppender{}
					if appendFails {
						appender.err = errors.New("disk full")
					}
					calls := 0
					providerErr := errors.New("provider failed")
					var out bytes.Buffer
					app := App{Out: &out, ProcessStarter: mergeMetricsStarter(t, provider, outcome, providerErr, &calls)}
					config := newSingleMergeAgentConfig(app, detail, "", nil, appender)
					if config.EventAppender != nil {
						t.Fatal("single proposal wired batch persistence")
					}
					generator, err := mergepkg.NewMergeProposalGenerator(config)
					if err != nil {
						t.Fatal(err)
					}
					_, err = generator.GenerateMergeProposal(context.Background(), commitcontract.MergeProposalContext{
						RepoRoot: detail.State.Repo.Root, PlanID: "untrusted-not-the-observer-identity", DefaultBranch: "main", DefaultParent: "parent",
						MergeBase: "base", SourceBranch: "feature/test", SourceHead: "head", Diff: "diff --git a/a.go b/a.go\n+change\n",
					})
					switch outcome {
					case "failure":
						if err == nil || !strings.Contains(err.Error(), providerErr.Error()) {
							t.Fatalf("provider failure lost: %v", err)
						}
					case "start-failure":
						if !errors.Is(err, providerErr) {
							t.Fatalf("start failure lost: %v", err)
						}
					case "timeout":
						var timeout *agent.SessionTimeoutError
						if !errors.As(err, &timeout) {
							t.Fatalf("timeout lost: %v", err)
						}
					case "malformed":
						if err == nil {
							t.Fatal("invalid proposal accepted")
						}
					default:
						if err != nil {
							t.Fatal(err)
						}
					}
					if calls != 1 || len(appender.events) != 1 {
						t.Fatalf("calls=%d metrics=%d", calls, len(appender.events))
					}
					event := appender.events[0] // This appender owns only telemetry.
					m := event.Metrics
					wantAvailability := plan.AgentMetricsReported
					switch outcome {
					case "partial", "failure", "timeout":
						wantAvailability = plan.AgentMetricsPartial
					case "unavailable", "start-failure":
						wantAvailability = plan.AgentMetricsUnavailable
					}
					failed := outcome == "failure" || outcome == "timeout" || outcome == "start-failure"
					if event.Type != plan.EventTypeAgentMetrics || event.PlanID != mergePlanID(detail) || event.SliceID != "" || event.Agent != provider || m == nil {
						t.Fatalf("event identity: %#v", event)
					}
					if m.Role != plan.AgentRoleMerge || m.Availability != wantAvailability || (m.Status == "failed") != failed || m.Status != m.Result {
						t.Fatalf("metrics outcome: %#v", m)
					}
					if m.InputTokens != 0 || m.InputTokensPresent != (wantAvailability != plan.AgentMetricsUnavailable) || m.CostPresent != (wantAvailability == plan.AgentMetricsReported) || m.Cost != 0 {
						t.Fatalf("presence lost: %#v", m)
					}
					wantAppended := 1
					if appendFails {
						wantAppended = 0
					}
					if len(detail.Events) != before+wantAppended {
						t.Fatal("in-memory detail disagrees with append success")
					}
					if appendFails && !strings.Contains(out.String(), "disk full") {
						t.Fatal("missing append warning")
					}
				})
			}
		}
	}
}

type mergeBatchMetricsAppender struct {
	events []mergepkg.BatchAgentEvent
	err    error
}

func (a *mergeBatchMetricsAppender) AppendAgentEvent(event mergepkg.BatchAgentEvent) error {
	a.events = append(a.events, event)
	return a.err
}

func TestBatchMergeTelemetryProviders(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, op := range []mergepkg.BatchAgentOperation{mergepkg.BatchAgentOperationCandidateResolution, mergepkg.BatchAgentOperationAggregateReview, mergepkg.BatchAgentOperationAggregateRework, mergepkg.BatchAgentOperationProposalGeneration} {
			for _, outcome := range []string{"reported", "partial", "unavailable", "failure", "timeout", "start-failure"} {
				for _, appendFails := range []bool{false, true} {
					name := provider + "/" + string(op) + "/" + outcome
					if appendFails {
						name += "/append-failure"
					}
					t.Run(name, func(t *testing.T) {
						t.Setenv("TAO_AGENT", provider)
						t.Setenv("TAO_SESSION_TIMEOUT", "5s")
						if outcome == "timeout" {
							t.Setenv("TAO_SESSION_TIMEOUT", "100ms")
						}
						calls := 0
						providerErr := errors.New("provider failed")
						appender := &mergeBatchMetricsAppender{}
						if appendFails {
							appender.err = errors.New("disk full")
						}
						var out bytes.Buffer
						config := newMergeBatchAgentConfig(App{Out: &out, ProcessStarter: mergeMetricsStarter(t, provider, outcome, providerErr, &calls)}, "", nil, nil)
						if config.Observe != nil {
							t.Fatal("batch wired plan observer")
						}
						config.EventAppender = appender
						session, err := mergepkg.NewBatchAgentSession(config)
						if err != nil {
							t.Fatal(err)
						}
						_, err = session.Resolve(context.Background(), mergepkg.BatchAgentSessionRequest{BatchID: "batch-a", Operation: op, Attempt: 2, CandidatePlanID: "plan-a", IntegrationRoot: t.TempDir(), Prompt: "merge"})
						failed := outcome == "failure" || outcome == "timeout" || outcome == "start-failure"
						if (err != nil) != failed || calls != 1 {
							t.Fatalf("session result changed: calls=%d err=%v", calls, err)
						}
						wantAvailability := "reported"
						switch outcome {
						case "partial", "failure", "timeout":
							wantAvailability = "partial"
						case "unavailable", "start-failure":
							wantAvailability = "unavailable"
						}
						wantOutcome := mergepkg.BatchAgentOutcomeCompleted
						if failed {
							wantOutcome = mergepkg.BatchAgentOutcomeFailed
						}
						if outcome == "timeout" {
							wantOutcome = mergepkg.BatchAgentOutcomeTimedOut
						}
						count, timeouts := 0, 0
						for _, event := range appender.events {
							if event.Operation != op || event.BatchID != "batch-a" || event.PlanID != "plan-a" || event.Attempt != 2 || event.Outcome != wantOutcome || event.Agent != provider {
								t.Fatalf("batch attribution: %#v", event)
							}
							if event.Type == mergepkg.BatchAgentEventTypeTimeout {
								timeouts++
								continue
							}
							if event.Type != mergepkg.BatchAgentEventTypeMetrics {
								t.Fatalf("unexpected telemetry: %#v", event)
							}
							count++
							m := event.Metrics
							if m == nil || string(m.Availability) != wantAvailability || m.InputTokens != 0 || m.InputTokensPresent != (wantAvailability != "unavailable") || m.CostPresent != (wantAvailability == "reported") || m.Cost != 0 {
								t.Fatalf("batch measurements: %#v", m)
							}
						}
						if count != 1 || (timeouts == 1) != (outcome == "timeout") {
							t.Fatalf("metrics=%d timeouts=%d", count, timeouts)
						}
						if appendFails && !strings.Contains(out.String(), "disk full") {
							t.Fatal("missing append warning")
						}
					})
				}
			}
		}
	}
}

func TestMergeServiceProposalUsesPlanObserver(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	detail := cliMergeDetail(t)
	calls := 0
	app := App{Out: io.Discard, CommandRunner: newCLIMergeGitRunner(t, detail.State.Repo.Root), WorkspaceManager: func(string) (WorkspaceManager, error) { return &fakeWorkspaceManager{}, nil }, ProcessStarter: mergeMetricsStarter(t, "claude", "reported", nil, &calls)}
	runner, err := app.newMergeServiceRunner(detail)
	if err != nil {
		t.Fatal(err)
	}
	service := runner.(mergepkg.Service)
	_, err = service.ProposalGenerator.GenerateMergeProposal(context.Background(), commitcontract.MergeProposalContext{
		RepoRoot: detail.State.Repo.Root, PlanID: mergePlanID(detail), DefaultBranch: "main", DefaultParent: "parent", MergeBase: "base", SourceBranch: "feature/test", SourceHead: "head", Diff: "diff --git a/a.go b/a.go\n+change\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range detail.Events {
		if event.Type == plan.EventTypeAgentMetrics {
			count++
			if event.PlanID != mergePlanID(detail) || event.Metrics.Role != plan.AgentRoleMerge {
				t.Fatalf("wrong attribution: %#v", event)
			}
		}
	}
	if count != 1 || calls != 1 {
		t.Fatalf("events=%d calls=%d", count, calls)
	}
}

// Exercise the real provider adapters without live providers or filesystem edits.
func mergeMetricsStarter(t *testing.T, provider, outcome string, providerErr error, calls *int) runpkg.ProcessStarter {
	t.Helper()
	return func(ctx context.Context, _ string, name string, _ []string) (runpkg.Process, error) {
		(*calls)++
		if name != provider {
			t.Fatalf("provider=%s, want %s", name, provider)
		}
		if outcome == "start-failure" {
			return nil, providerErr
		}
		text := `{"type":"fix","scope":"merge","summary":"preserve merge attribution","what":"Attribute the exact merge proposal.","why":"Keep exceptional sessions observable."}`
		if outcome == "malformed" {
			text = "not JSON"
		}
		partial := outcome == "partial" || outcome == "failure" || outcome == "timeout"
		usage := `"input_tokens":0,"output_tokens":7`
		if partial {
			usage = `"input_tokens":0`
		}
		if outcome == "unavailable" {
			usage = ""
		}
		if provider == "claude" {
			proc := newFakeCLIClaudeProcess(t)
			go func() {
				defer proc.finish()
				if _, err := io.ReadAll(proc.stdinReader); err != nil {
					return
				}
				cost := ""
				if !partial && outcome != "unavailable" {
					cost = `,"total_cost_usd":0`
				}
				proc.writeEvent(`{"type":"result","result":` + strconv.Quote(text) + `,"usage":{` + usage + `}` + cost + `}`)
				if outcome == "timeout" {
					<-ctx.Done()
				}
			}()
			if outcome == "failure" {
				return reworkFailedProcess{Process: proc, err: providerErr}, nil
			}
			return proc, nil
		}
		proc := newFakeCLIPiProcess(t)
		go func() {
			defer proc.finish()
			for _, event := range []string{
				`{"id":"tao-readiness-state","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"test","id":"model"}}}`,
				`{"id":"tao-readiness-models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"test","id":"model"}]}}`,
				`{"id":"tao-prompt","type":"response","command":"prompt","success":true}`,
			} {
				if _, err := proc.readCommand(); err != nil {
					return
				}
				proc.writeEvent(event)
			}
			if outcome == "failure" || outcome == "timeout" {
				proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","usage":{"input":0}}}`)
				if outcome == "timeout" {
					<-ctx.Done()
				} else {
					proc.writeEvent(`{"type":"message","message":{"role":"assistant","stopReason":"error","errorMessage":"provider failed"}}`)
				}
				return
			}
			proc.writeEvent(`{"type":"message","role":"assistant","text":` + strconv.Quote(text) + `}`)
			proc.writeEvent(`{"type":"agent_end","session_id":"merge-session"}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(`{"type":"state","session_id":"merge-session"}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			stats := `{"type":"session_stats","session_id":"merge-session"`
			if usage != "" {
				stats += "," + usage
			}
			if !partial && outcome != "unavailable" {
				stats += `,"cost":0,"total_tokens":7`
			}
			proc.writeEvent(stats + "}")
		}()
		return proc, nil
	}
}

// A batch request must not be copied into plan telemetry even if misrouted to
// the single-plan observer. Readiness/configuration failures are not sessions.
func TestSingleMergeObserverIgnoresBatchAndUninvokedResults(t *testing.T) {
	detail := cliMergeDetail(t)
	appender := &recordingMergeMetricsAppender{}
	config := newSingleMergeAgentConfig(App{}, detail, "", nil, appender)
	config.Observe(mergepkg.BatchAgentSessionRequest{BatchID: "batch-a", Operation: mergepkg.BatchAgentOperationProposalGeneration}, mergepkg.BatchAgentSessionResult{Provider: agentsession.Result{Invoked: true, AgentLabel: "pi"}}, nil)
	config.Observe(mergepkg.BatchAgentSessionRequest{Operation: mergepkg.BatchAgentOperationProposalGeneration}, mergepkg.BatchAgentSessionResult{}, nil)
	if len(appender.events) != 0 {
		t.Fatal("batch or uninvoked session recorded")
	}
}
