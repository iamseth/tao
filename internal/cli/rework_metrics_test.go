package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	runpkg "github.com/iamseth/tao/internal/run"
)

// Only telemetry appends are injected; triage and lifecycle persistence keep
// using the ordinary file repository and its locks.
type reworkMetricsRepository struct {
	planRunRepository
	appendErr error
	attempts  []plan.Event
}

func (r *reworkMetricsRepository) AppendEvent(dir string, event plan.Event) error {
	r.attempts = append(r.attempts, event)
	if r.appendErr != nil {
		return r.appendErr
	}
	return r.planRunRepository.AppendEvent(dir, event)
}

func TestReworkPRClassifierMetrics(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, outcome := range []string{"reported", "partial", "unavailable", "malformed", "failure", "timeout", "start-failure", "reopen"} {
			for _, appendFails := range []bool{false, true} {
				name := provider + "/" + outcome
				if appendFails {
					name += "/append-failure"
				}
				t.Run(name, func(t *testing.T) {
					t.Setenv("TAO_AGENT", provider)
					t.Setenv("TAO_SESSION_TIMEOUT", "5s")
					if outcome == "timeout" {
						t.Setenv("TAO_SESSION_TIMEOUT", "1s")
					}
					root := t.TempDir()
					const id = "20260628-1200-pr-metrics"
					dir := writeCLIReworkPlan(t, root, id, plan.StatusCompleted, reworkReview(plan.ReviewVerdictApprove, nil))
					addCLIReworkPullRequest(t, dir)
					threads := []reworkpkg.PRThread{{NodeID: "PRRT_change", Path: "internal/cli/rework.go", Comments: []reworkpkg.PRThreadComment{{AuthorLogin: "owner", Body: "Please fix this."}}}}
					oldRead := readReworkPRThreads
					readReworkPRThreads = func(context.Context, App, reworkpkg.PRThreadReadRequest) (reworkpkg.PRThreadReadResult, error) {
						return reworkpkg.PRThreadReadResult{Threads: threads}, nil
					}
					t.Cleanup(func() { readReworkPRThreads = oldRead })
					repo := &reworkMetricsRepository{planRunRepository: plan.NewFileRepository(root)}
					if appendFails {
						repo.appendErr = errors.New("telemetry append unavailable")
					}
					calls := 0
					providerErr := errors.New("provider failed")
					fixed := time.Date(2026, 6, 28, 13, 0, 0, 0, time.UTC)
					app := App{Out: &bytes.Buffer{}, Now: func() time.Time { return fixed }, ProcessStarter: reworkMetricsProcessStarter(t, provider, outcome, providerErr, &calls)}
					args := []string{"--from-pr", "--dry-run", id}
					if outcome == "reopen" {
						args = []string{"--from-pr", id}
					}
					err := app.rework(context.Background(), repo, args)
					switch outcome {
					case "failure":
						if err == nil || !strings.Contains(err.Error(), providerErr.Error()) {
							t.Fatalf("provider error lost: %v", err)
						}
					case "start-failure":
						if !errors.Is(err, providerErr) {
							t.Fatalf("original start error lost: %v", err)
						}
					case "timeout":
						var timeout *agent.SessionTimeoutError
						if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("original timeout lost: %v", err)
						}
					case "malformed":
						if err == nil || !strings.Contains(err.Error(), "decode pull-request thread triage result") {
							t.Fatalf("classification error lost: %v", err)
						}
					default:
						if err != nil {
							t.Fatal(err)
						}
					}
					if calls != 1 || len(repo.attempts) != 1 {
						t.Fatalf("provider calls=%d telemetry attempts=%d; want one each", calls, len(repo.attempts))
					}
					event := repo.attempts[0] // This recorder owns only metrics appends.
					if event.Type != plan.EventTypeAgentMetrics || event.PlanID != id || event.SliceID != "" || event.Agent != provider || !event.Timestamp.Equal(fixed) || event.Metrics == nil {
						t.Fatalf("untrusted identity or missing metrics: %#v", event)
					}
					m := event.Metrics
					wantStatus := plan.StatusCompleted
					if outcome == "failure" || outcome == "timeout" || outcome == "start-failure" {
						wantStatus = "failed"
					}
					wantAvailability := plan.AgentMetricsReported
					switch outcome {
					case "partial", "failure", "timeout":
						wantAvailability = plan.AgentMetricsPartial
					case "unavailable", "start-failure":
						wantAvailability = plan.AgentMetricsUnavailable
					}
					if m.Role != plan.AgentRoleRework || m.Status != wantStatus || m.Result != wantStatus || m.Availability != wantAvailability {
						t.Fatalf("metrics = %#v, want %s/%s", m, wantStatus, wantAvailability)
					}
					if m.InputTokens != 0 || m.InputTokensPresent != (wantAvailability != plan.AgentMetricsUnavailable) {
						t.Fatalf("measured zero versus unavailable lost: %#v", m)
					}
					if wantAvailability == plan.AgentMetricsReported && (!m.OutputTokensPresent || m.OutputTokens != 7 || !m.CostPresent || m.Cost != 0) {
						t.Fatalf("reported measurements lost: %#v", m)
					}
					// Cached triage and an empty thread set must not produce another
					// session, even when the first telemetry append failed.
					if err == nil && outcome != "reopen" {
						if err := app.rework(context.Background(), repo, args); err != nil {
							t.Fatal(err)
						}
						threads = nil
						if err := app.rework(context.Background(), repo, args); err != nil {
							t.Fatal(err)
						}
						if calls != 1 || len(repo.attempts) != 1 {
							t.Fatal("cached/no-thread triage invoked agent or emitted metrics")
						}
					}
					detail, resolveErr := repo.ResolvePlan(context.Background(), dir)
					if resolveErr != nil {
						t.Fatal(resolveErr)
					}
					metricsCount := 0
					for _, event := range detail.Events {
						if event.Type == plan.EventTypeAgentMetrics {
							metricsCount++
						}
						if outcome != "reopen" && (event.Type == plan.EventTypePlanReopened || event.Type == plan.EventTypeReworkStopped) {
							t.Fatalf("telemetry granted lifecycle authority: %#v", event)
						}
					}
					wantCount := 1
					if appendFails {
						wantCount = 0
					}
					if metricsCount != wantCount {
						t.Fatalf("persisted metrics=%d, want %d", metricsCount, wantCount)
					}
					if outcome == "reopen" {
						if detail.State.Status != plan.StatusInProgress || len(detail.State.Plan.PendingSlices) != 1 {
							t.Fatalf("ordinary reopen changed: %#v", detail.State)
						}
					} else if detail.State.Status != plan.StatusCompleted || len(detail.State.Plan.PendingSlices) != 0 || len(detail.Slices.Slices) != 1 {
						t.Fatalf("dry run/failure reopened plan: %#v", detail.State)
					}
					if (err == nil) != (len(detail.State.Plan.PRFeedbackTriage) == 1) {
						t.Fatalf("unexpected triage persistence: %#v, error %v", detail.State.Plan.PRFeedbackTriage, err)
					}
				})
			}
		}
	}
}

func reworkMetricsProcessStarter(t *testing.T, provider, outcome string, providerErr error, calls *int) runpkg.ProcessStarter {
	t.Helper()
	return func(ctx context.Context, _ string, name string, _ []string) (runpkg.Process, error) {
		(*calls)++
		if name != provider {
			t.Fatalf("provider=%s, want %s", name, provider)
		}
		if outcome == "start-failure" {
			return nil, providerErr
		}
		text := `{"classifications":[{"thread_node_id":"PRRT_change","kind":"change","rationale":"Fix the behavior."}]}`
		if outcome == "malformed" {
			text = "not JSON"
		}
		usage := `"input_tokens":0,"output_tokens":7`
		if outcome == "partial" || outcome == "failure" || outcome == "timeout" {
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
				if outcome == "reported" || outcome == "malformed" || outcome == "reopen" {
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
			proc.writeEvent(`{"type":"agent_end","session_id":"triage-session"}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			proc.writeEvent(`{"type":"state","session_id":"triage-session"}`)
			if _, err := proc.readCommand(); err != nil {
				return
			}
			stats := `{"type":"session_stats","session_id":"triage-session"`
			if usage != "" {
				stats += "," + usage
			}
			if outcome == "reported" || outcome == "malformed" || outcome == "reopen" {
				stats += `,"cost":0,"total_tokens":7`
			}
			proc.writeEvent(stats + "}")
		}()
		return proc, nil
	}
}

type reworkFailedProcess struct {
	runpkg.Process
	err error
}

func (p reworkFailedProcess) Wait() error {
	_ = p.Process.Wait()
	return p.err
}
