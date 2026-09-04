package agentsession

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
)

type runtimeFunc func(context.Context, agent.Session) (agent.SessionResult, error)

func (f runtimeFunc) RunSession(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
	return f(ctx, session)
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
