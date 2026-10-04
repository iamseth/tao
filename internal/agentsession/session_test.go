package agentsession

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/promptcapture"
)

type runtimeFunc func(context.Context, agent.Session) (agent.SessionResult, error)

func (f runtimeFunc) RunSession(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
	return f(ctx, session)
}

func TestRunnerControlCheckoutAttribution(t *testing.T) {
	for _, attributed := range []bool{false, true} {
		for _, sessionErr := range []error{nil, &agent.SessionTimeoutError{Timeout: time.Second}} {
			t.Run(fmt.Sprintf("attributed=%v/error=%v", attributed, sessionErr), func(t *testing.T) {
				root := t.TempDir()
				runGit(t, root, "init")
				runGit(t, root, "config", "user.email", "test@example.com")
				runGit(t, root, "config", "user.name", "Test")
				runGit(t, root, "commit", "--allow-empty", "-m", "base")
				runner := New(Config{Runtime: runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
					writeLeakFile(t, root, "other.txt", "changed")
					return agent.SessionResult{Output: "output", FinalText: "final"}, sessionErr
				})})
				called := false
				result, err := runner.Run(context.Background(), Request{ControlRoot: root, RepoRoot: t.TempDir(), AttributeControlCheckoutChange: func(_ context.Context, change ControlCheckoutChange) bool {
					called = true
					if !reflect.DeepEqual(change.Paths, []string{"other.txt"}) {
						t.Fatalf("change = %+v", change)
					}
					return attributed
				}})
				if !called || result.Output != "output" || result.FinalText != "final" {
					t.Fatalf("result = %+v, called=%v", result, called)
				}
				var leak ControlCheckoutLeakError
				if attributed {
					if !errors.As(err, &leak) || result.ControlCheckoutChange != nil {
						t.Fatalf("result=%+v err=%v", result, err)
					}
				} else if !errors.Is(err, sessionErr) || result.ControlCheckoutChange == nil {
					t.Fatalf("result=%+v err=%v, want %v", result, err, sessionErr)
				}
			})
		}
	}
}

func TestRunnerPromptCapture(t *testing.T) {
	for name, override := range map[string]bool{"default": false, "override": true} {
		t.Run(name, func(t *testing.T) {
			target := promptcapture.Target{Dir: filepath.Join(t.TempDir(), "prompts"), Role: "review", Template: "merge-review", TemplateHash: "template-hash"}
			now := time.Date(2026, 10, 3, 6, 0, 0, 0, time.UTC)
			model, effort := "base", "medium"
			if override {
				model, effort = "override", "high"
			}
			var path string
			runner := New(Config{Model: "base", Effort: "medium", Now: func() time.Time { return now }, Descriptor: agent.Descriptor{Label: "fake"}, Runtime: runtimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
				entries, err := promptcapture.List(target.Dir)
				if err != nil || len(entries) != 1 {
					t.Fatalf("capture before invocation: %v, %v", entries, err)
				}
				path = entries[0].Path
				h, body, err := promptcapture.Read(path)
				if err != nil || body != session.Prompt || h.Role != target.Role || h.Template != target.Template || h.TemplateHash != target.TemplateHash || h.Agent != "fake" || h.Model != model || h.Effort != effort || h.StartedAt != now.Format(time.RFC3339) {
					t.Fatalf("header=%+v body=%q err=%v", h, body, err)
				}
				return agent.SessionResult{Output: "done"}, nil
			})})
			request := Request{Prompt: "exact prompt\nrole: body", Capture: &target}
			if override {
				request.Model, request.Effort = model, effort
			}
			result, err := runner.Run(context.Background(), request)
			h, _, readErr := promptcapture.Read(path)
			if err != nil || readErr != nil || !result.Invoked || result.PromptHash != promptcapture.Hash(request.Prompt) || result.PromptHash != h.SHA256 || result.PromptTemplate != target.Template || result.PromptPath != path || result.PromptCaptureWarning != "" {
				t.Fatalf("result=%+v err=%v read=%v", result, err, readErr)
			}
		})
	}
}

func TestRunnerPromptCaptureFailureIsNonFatal(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, providerErr := range []error{nil, errors.New("provider failed")} {
		runner := New(Config{Runtime: runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
			return agent.SessionResult{Output: "output", FinalText: "final", Metrics: &agent.Metrics{OutputTokens: 9}}, providerErr
		})})
		request := Request{Prompt: "prompt", CollectMetrics: true}
		baseline, baseErr := runner.Run(context.Background(), request)
		if baseline.PromptHash != promptcapture.Hash(request.Prompt) || baseline.PromptPath != "" || baseline.PromptTemplate != "" || baseline.PromptCaptureWarning != "" {
			t.Fatalf("baseline=%+v", baseline)
		}
		request.Capture = &promptcapture.Target{Dir: filepath.Join(file, "prompts"), Template: "run"}
		got, err := runner.Run(context.Background(), request)
		if got.PromptCaptureWarning == "" || got.PromptTemplate != "run" {
			t.Fatalf("result=%+v", got)
		}
		got.PromptCaptureWarning, got.PromptTemplate = "", ""
		if !reflect.DeepEqual(got, baseline) || err != baseErr { //nolint:errorlint // Capture must preserve error identity.
			t.Fatalf("capture changed result: %+v vs %+v; errors %v vs %v", got, baseline, err, baseErr)
		}
	}
}

func TestRunnerForwardsWarningPolicy(t *testing.T) {
	warning := &agent.SessionWarning{Percent: 80, Message: "wrap up"}
	calls := 0
	runner := New(Config{Timeout: time.Minute, Runtime: runtimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
		calls++
		if session.Warning != warning || session.WarningMessages == nil || session.Timeout != time.Minute {
			t.Fatalf("session=%+v", session)
		}
		return agent.SessionResult{Output: "done"}, nil
	})})
	result, err := runner.Run(context.Background(), Request{Warning: warning})
	if err != nil || calls != 1 || result.Output != "done" {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
}

func TestRunnerPropagatesLifetimeWithoutMetrics(t *testing.T) {
	var bound context.Context
	closed := false
	runner := New(Config{Timeout: time.Minute, Runtime: runtimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
		if ctx != bound || session.CollectMetrics {
			t.Fatal("lifetime propagation depends on metrics or lost context")
		}
		return agent.SessionResult{Output: "done"}, nil
	})})
	result, err := runner.Run(context.Background(), Request{
		BindLifetime: func(ctx context.Context) (context.Context, func() error, error) {
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("binding ran before session timeout")
			}
			var cancel context.CancelFunc
			bound, cancel = context.WithCancel(ctx)
			return bound, func() error { closed = true; cancel(); return nil }, nil
		},
	})
	if err != nil || result.Output != "done" || !closed || bound.Err() == nil {
		t.Fatalf("result=%+v err=%v closed=%t", result, err, closed)
	}
}

func TestRunnerModelSelection(t *testing.T) {
	for _, tt := range []struct {
		name, configured, override, want string
	}{
		{name: "unset"},
		{name: "config default", configured: "base", want: "base"},
		{name: "request override", configured: "base", override: "review", want: "review"},
		{name: "request only", override: "run", want: "run"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			effort := func(value string) string {
				if value == "" {
					return ""
				}
				return "effort-" + value
			}
			runner := New(Config{Model: tt.configured, Effort: effort(tt.configured), Runtime: runtimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
				calls++
				if session.Model != tt.want || session.Effort != effort(tt.want) {
					t.Fatalf("model/effort = %q/%q, want %q", session.Model, session.Effort, tt.want)
				}
				return agent.SessionResult{}, nil
			})})
			if _, err := runner.Run(context.Background(), Request{Model: tt.override, Effort: effort(tt.override)}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 {
				t.Fatalf("provider calls = %d, want 1", calls)
			}
		})
	}
}

func TestTextGeneratorUsesConfiguredModel(t *testing.T) {
	for _, model := range []string{"", "base"} {
		t.Run("model="+model, func(t *testing.T) {
			calls := 0
			generator := NewTextGenerator(Config{Model: model, Effort: "text-effort", Runtime: runtimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
				calls++
				if session.Effort != "text-effort" {
					t.Fatalf("effort = %q", session.Effort)
				}
				if session.Model != model {
					t.Fatalf("model = %q, want %q", session.Model, model)
				}
				return agent.SessionResult{FinalText: "done"}, nil
			})}, nil)
			if text, err := generator.GenerateText(context.Background(), "/repo", "prompt"); err != nil || text != "done" || calls != 1 {
				t.Fatalf("text=%q error=%v calls=%d", text, err, calls)
			}
		})
	}
}

func TestRunnerInvokesOneProviderWithBoundedDescriptorPolicy(t *testing.T) {
	calls := 0
	var got agent.Session
	var progress bytes.Buffer
	metrics := agent.Metrics{OutputTokens: 12}
	runtime := runtimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
		calls++
		got = session
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("bounded session did not apply a deadline")
		}
		return agent.SessionResult{Output: "partial", FinalText: "done", PromptAcceptance: agent.PromptAcceptanceAccepted, Metrics: &metrics}, nil
	})
	descriptor := agent.Descriptor{
		Label: "test", MetricsMessage: "captured test metrics", SupportsBypassPermissions: true,
		NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime },
	}
	runner := New(Config{Descriptor: descriptor, SkipPermissions: true, Timeout: time.Minute, Progress: &progress})
	result, err := runner.Run(context.Background(), Request{
		RepoRoot: "/repo", Prompt: "work", CollectMetrics: true, NoProgressToolLimit: 4,
		VerificationCommands: []string{"go test ./..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
	if got.RepoRoot != "/repo" || got.Prompt != "work" || got.PermissionMode != agent.PermissionModeBypassPermissions || got.Timeout != time.Minute || !got.CollectMetrics {
		t.Fatalf("provider session = %+v", got)
	}
	if got.Progress != &progress || got.NoProgressToolLimit != 4 || len(got.VerificationCommands) != 1 {
		t.Fatalf("progress and run safeguards were not routed: %+v", got)
	}
	if result.Output != "partial" || result.FinalText != "done" || result.PromptAcceptance != agent.PromptAcceptanceAccepted || result.AgentLabel != "test" || result.MetricsMessage != "captured test metrics" || !result.MetricsUsable {
		t.Fatalf("result = %+v", result)
	}
}

func TestRunnerSkipPermissionsWithoutDescriptorSupportUsesAutoMode(t *testing.T) {
	var got agent.Session
	runner := New(Config{
		Descriptor: agent.Descriptor{
			SupportsBypassPermissions: false,
			NewRuntime: func(agent.RuntimeDeps) agent.Runtime {
				return runtimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
					got = session
					return agent.SessionResult{}, nil
				})
			},
		},
		SkipPermissions: true,
	})
	if _, err := runner.Run(context.Background(), Request{}); err != nil {
		t.Fatal(err)
	}
	if got.PermissionMode != agent.PermissionModeAuto {
		t.Fatalf("permission mode = %q, want %q", got.PermissionMode, agent.PermissionModeAuto)
	}
}

func TestRunnerClassifiesInformationalAndMissingMetricsWarnings(t *testing.T) {
	for _, tt := range []struct {
		name          string
		informational bool
		requested     bool
		wantCollect   bool
		wantReport    bool
		wantUsable    bool
	}{
		{name: "pi advisory without request", informational: true, wantCollect: true, wantReport: true, wantUsable: true},
		{name: "non-pi missing requested metrics", requested: true, wantCollect: true, wantReport: true},
		{name: "non-pi ignores unrequested warning"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var collected bool
			runtime := runtimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
				collected = session.CollectMetrics
				return agent.SessionResult{MetricsWarning: "unavailable"}, nil
			})
			runner := New(Config{Descriptor: agent.Descriptor{
				AlwaysCollectMetrics: tt.informational, MetricsWarningInformational: tt.informational,
				MetricsWarningPrefix: "collect: ", NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime },
			}})
			result, err := runner.Run(context.Background(), Request{CollectMetrics: tt.requested})
			if err != nil {
				t.Fatal(err)
			}
			if collected != tt.wantCollect || result.ReportMetricsWarning != tt.wantReport || result.MetricsUsable != tt.wantUsable || result.MetricsWarningMessage != "collect: unavailable" {
				t.Fatalf("classification = collected=%t result=%+v", collected, result)
			}
		})
	}
}

func TestRunnerMeasurementFactsAreIndependentOfWarningsAndOutcome(t *testing.T) {
	original := errors.New("original provider failure")
	for _, descriptor := range agent.All() {
		for _, outcome := range []string{"success", "failure", "timeout"} {
			for _, tt := range []struct {
				name         string
				metrics      *agent.Metrics
				availability agentmetrics.Availability
			}{
				{name: "nil", availability: agentmetrics.Unavailable},
				{name: "legacy allocated", metrics: &agent.Metrics{}},
				{name: "unavailable", metrics: &agent.Metrics{Availability: agentmetrics.Unavailable}, availability: agentmetrics.Unavailable},
				{name: "partial zero", metrics: &agent.Metrics{Availability: agentmetrics.Partial, CostPresent: true}, availability: agentmetrics.Partial},
				{name: "reported zero", metrics: &agent.Metrics{Availability: agentmetrics.Reported, InputTokensPresent: true, OutputTokensPresent: true, TotalTokensPresent: true, CostPresent: true}, availability: agentmetrics.Reported},
			} {
				t.Run(descriptor.Label+"/"+outcome+"/"+tt.name, func(t *testing.T) {
					calls := 0
					runtime := runtimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
						calls++
						raw := agent.SessionResult{Output: "original output", Metrics: tt.metrics, MetricsWarning: "unchanged warning"}
						if outcome == "timeout" {
							<-ctx.Done()
							return raw, ctx.Err()
						}
						if outcome == "failure" {
							return raw, original
						}
						return raw, nil
					})
					timeout := time.Minute
					if outcome == "timeout" {
						timeout = time.Millisecond
					}
					runner := New(Config{Descriptor: descriptor, Runtime: runtime, Timeout: timeout})
					got, err := runner.Run(context.Background(), Request{CollectMetrics: true})
					switch outcome {
					case "success":
						if err != nil {
							t.Fatal(err)
						}
					case "failure":
						if !errors.Is(err, original) {
							t.Fatalf("original error lost: %v", err)
						}
					case "timeout":
						var timeout *agent.SessionTimeoutError
						if !errors.As(err, &timeout) || !errors.Is(err, context.DeadlineExceeded) {
							t.Fatalf("timeout lost: %v", err)
						}
					}
					if calls != 1 || got.Metrics != tt.metrics || got.MetricsAvailability != tt.availability || got.Output != "original output" || got.MetricsWarning != "unchanged warning" || !got.ReportMetricsWarning || got.MetricsUsable != descriptor.MetricsWarningInformational {
						t.Fatalf("calls=%d result=%+v", calls, got)
					}
				})
			}
		}
	}
}

func TestRunnerPreservesPartialOutputWithSessionError(t *testing.T) {
	wantErr := errors.New("provider failed")
	runner := New(Config{Descriptor: agent.Descriptor{NewRuntime: func(agent.RuntimeDeps) agent.Runtime {
		return runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
			return agent.SessionResult{Output: "partial"}, wantErr
		})
	}}})
	result, err := runner.Run(context.Background(), Request{})
	if result.Output != "partial" || !errors.Is(err, wantErr) {
		t.Fatalf("result, error = %+v, %v", result, err)
	}
}

func TestRunnerForwardsGracePolicy(t *testing.T) {
	grace := &agent.SessionGrace{Max: time.Minute, Active: func() bool { return true }}
	calls := 0
	runner := New(Config{Timeout: time.Minute, Runtime: runtimeFunc(func(_ context.Context, session agent.Session) (agent.SessionResult, error) {
		calls++
		if session.Grace != grace {
			t.Fatalf("grace=%+v", session.Grace)
		}
		return agent.SessionResult{Output: "done"}, nil
	})})
	result, err := runner.Run(context.Background(), Request{Grace: grace})
	if err != nil || calls != 1 || result.Output != "done" {
		t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
	}
}
