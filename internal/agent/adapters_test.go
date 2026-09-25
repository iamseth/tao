package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/agent/process"
)

func TestAdaptersMeasurementPresence(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, tt := range []struct {
			name, stats, usage, cost      string
			availability                  agentmetrics.Availability
			input, output, total, hasCost bool
		}{
			{name: "complete", stats: `{"tokens":{"input":3,"output":2,"total":5},"cost":1}`, usage: `{"input_tokens":3,"output_tokens":2}`, cost: `,"total_cost_usd":1`, availability: agentmetrics.Reported, input: true, output: true, total: true, hasCost: true},
			{name: "zero", stats: `{"tokens":{"input":0,"output":0,"total":0},"cost":0}`, usage: `{"input_tokens":0,"output_tokens":0}`, cost: `,"total_cost_usd":0`, availability: agentmetrics.Reported, input: true, output: true, total: true, hasCost: true},
			{name: "partial", stats: `{"tokens":{"input":0}}`, usage: `{"input_tokens":0}`, availability: agentmetrics.Partial, input: true},
			{name: "absent", stats: `{}`, usage: `{}`, availability: agentmetrics.Unavailable},
		} {
			t.Run(provider+"/"+tt.name, func(t *testing.T) {
				proc := newFakeProcess(t)
				calls := 0
				starter := func(context.Context, string, string, []string) (process.Process, error) { calls++; return proc, nil }
				var runtime Runtime = claudeRuntime{starter: starter}
				if provider == "pi" {
					runtime = piRuntime{starter: starter}
				}
				go func() {
					defer proc.finish()
					if provider == "pi" {
						_ = proc.readCommand()
						if tt.name == "complete" || tt.name == "zero" {
							proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","usage":{"input":99,"output":99,"totalTokens":198,"cost":{"total":9}}}}`)
						}
						proc.writeEvent(`{"type":"agent_end"}`)
						_ = proc.readCommand()
						proc.writeEvent(`{"id":"2","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"provider","id":"model"}}}`)
						_ = proc.readCommand()
						proc.writeEvent(fmt.Sprintf(`{"id":"3","type":"response","command":"get_session_stats","success":true,"data":%s}`, tt.stats))
					} else {
						_ = proc.readPrompt()
						proc.writeEvent(fmt.Sprintf(`{"type":"result","subtype":"success","session_id":"session","model":"model","usage":%s%s}`, tt.usage, tt.cost))
					}
				}()
				result, err := runtime.RunSession(context.Background(), Session{Prompt: "work", CollectMetrics: true})
				if err != nil {
					t.Fatal(err)
				}
				m := result.Metrics
				if calls != 1 || m == nil || result.MetricsAvailability() != tt.availability || m.InputTokensPresent != tt.input || m.OutputTokensPresent != tt.output || m.TotalTokensPresent != tt.total || m.CostPresent != tt.hasCost {
					t.Fatalf("calls=%d metrics=%+v", calls, m)
				}
				if tt.name == "zero" && (m.InputTokens != 0 || m.OutputTokens != 0 || m.TotalTokens != 0 || m.Cost != 0) {
					t.Fatalf("explicit zero lost: %+v", m)
				}
				if tt.name == "complete" && (m.InputTokens != 3 || m.OutputTokens != 2 || m.TotalTokens != 5 || m.Cost != 1) {
					t.Fatalf("final stats lost or double counted: %+v", m)
				}
			})
		}
	}
}

func TestAdaptersRetainMeasurementsOnFailureAndTimeout(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		for _, outcome := range []string{"failure", "timeout"} {
			t.Run(provider+"/"+outcome, func(t *testing.T) {
				proc := newFakeProcess(t)
				calls := 0
				serverDone := make(chan struct{})
				starter := func(ctx context.Context, _ string, _ string, _ []string) (process.Process, error) {
					calls++
					go func() {
						defer close(serverDone)
						defer proc.finish()
						if provider == "pi" {
							_ = proc.readCommand()
							proc.writeEvent(`{"type":"message_end","message":{"role":"assistant","usage":{"input":3,"output":0,"totalTokens":3,"cost":{"total":0}}}}`)
						} else {
							_ = proc.readPrompt()
							proc.writeEvent(`{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":3,"output_tokens":0}},"total_cost_usd":0}`)
						}
						if outcome == "failure" {
							proc.writeEvent(`{"type":"error","message":"original provider failure"}`)
						} else {
							<-ctx.Done()
						}
					}()
					return proc, nil
				}
				var runtime Runtime = claudeRuntime{starter: starter}
				if provider == "pi" {
					runtime = piRuntime{starter: starter}
				}
				result, err := WithSessionTimeout(runtime).RunSession(context.Background(), Session{Prompt: "work", CollectMetrics: true, Timeout: time.Second})
				<-serverDone
				if outcome == "timeout" {
					var timeout *SessionTimeoutError
					if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("timeout error = %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "original provider failure") {
					t.Fatalf("original error lost: %v", err)
				}
				want := agentmetrics.Reported
				if provider == "pi" {
					want = agentmetrics.Partial
				} // message usage is not final session stats
				m := result.Metrics
				if calls != 1 || m == nil || m.Availability != want || m.InputTokens != 3 || !m.OutputTokensPresent || m.OutputTokens != 0 || !m.CostPresent || m.Cost != 0 || !m.TotalTokensPresent || m.TotalTokens != 3 {
					t.Fatalf("calls=%d metrics=%+v", calls, m)
				}
			})
		}
	}
}

func TestAdaptersPreserveStartErrorWithoutInventingMeasurements(t *testing.T) {
	original := errors.New("original start failure")
	for _, provider := range []string{"pi", "claude"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			starter := func(context.Context, string, string, []string) (process.Process, error) {
				calls++
				return nil, original
			}
			var runtime Runtime = claudeRuntime{starter: starter}
			if provider == "pi" {
				runtime = piRuntime{starter: starter}
			}
			result, err := runtime.RunSession(context.Background(), Session{CollectMetrics: true})
			if calls != 1 || !errors.Is(err, original) || result.MetricsAvailability() != agentmetrics.Unavailable || result.Metrics == nil || result.Metrics.CostPresent {
				t.Fatalf("calls=%d result=%+v err=%v", calls, result, err)
			}
		})
	}
}
