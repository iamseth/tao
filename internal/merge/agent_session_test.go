package merge

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agentsession"
	commitcontract "github.com/iamseth/tao/internal/commit"
)

type recordingBatchAgentEvents struct {
	events []BatchAgentEvent
	err    error
}

func (r *recordingBatchAgentEvents) AppendAgentEvent(event BatchAgentEvent) error {
	r.events = append(r.events, event)
	return r.err
}

func TestBatchAgentSessionPersistsMetricsAndClassifiedTimeoutWithoutRetry(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantEvents  []string
		wantOutcome string
	}{
		{name: "provider error", err: errors.New("provider failed"), wantEvents: []string{BatchAgentEventTypeMetrics}, wantOutcome: BatchAgentOutcomeFailed},
		{name: "timeout", err: &agent.SessionTimeoutError{Timeout: 2 * time.Minute}, wantEvents: []string{BatchAgentEventTypeTimeout, BatchAgentEventTypeMetrics}, wantOutcome: BatchAgentOutcomeTimedOut},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			store := &recordingBatchAgentEvents{}
			session := BatchAgentSession{
				run: func(context.Context, agentsession.Request) (agentsession.Result, error) {
					calls++
					return agentsession.Result{Output: " partial ", AgentLabel: "pi", MetricsUsable: true, Metrics: &agent.Metrics{SessionID: "session-a", OutputTokens: 7}}, tt.err
				},
				eventAppender: store, now: func() time.Time { return time.Date(2026, 8, 10, 20, 0, 0, 0, time.UTC) },
			}
			result, err := session.Resolve(context.Background(), BatchAgentSessionRequest{
				BatchID: "batch-a", Operation: BatchAgentOperationCandidateResolution, Attempt: 3,
				IntegrationRoot: "/integration", Prompt: "resolve", CandidatePlanID: "plan-a",
			})
			if calls != 1 || result.Output != "partial" || !errors.Is(err, tt.err) {
				t.Fatalf("calls/result/error = %d, %#v, %v", calls, result, err)
			}
			if len(store.events) != len(tt.wantEvents) {
				t.Fatalf("events = %#v, want types %v", store.events, tt.wantEvents)
			}
			for i, event := range store.events {
				if event.Type != tt.wantEvents[i] || event.BatchID != "batch-a" || event.Operation != BatchAgentOperationCandidateResolution || event.Attempt != 3 || event.PlanID != "plan-a" || event.Outcome != tt.wantOutcome {
					t.Fatalf("event %d = %#v", i, event)
				}
			}
		})
	}
}

func TestBatchAgentSessionTelemetryAppendFailureWarnsAndPreservesProviderError(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	store := &recordingBatchAgentEvents{err: errors.New("disk full")}
	var progress bytes.Buffer
	session := BatchAgentSession{
		run: func(context.Context, agentsession.Request) (agentsession.Result, error) {
			return agentsession.Result{Output: "partial", AgentLabel: "test-agent", MetricsUsable: true}, providerErr
		},
		log: &progress, eventAppender: store, now: time.Now,
	}
	result, err := session.Resolve(context.Background(), BatchAgentSessionRequest{
		BatchID: "batch-a", Operation: BatchAgentOperationAggregateReview, Attempt: 1, IntegrationRoot: "/integration", Prompt: "review",
	})
	if result.Output != "partial" || !errors.Is(err, providerErr) {
		t.Fatalf("result/error = %#v, %v", result, err)
	}
	if len(store.events) != 1 || !strings.Contains(progress.String(), "tao telemetry warning: append merge-batch metrics event: disk full") {
		t.Fatalf("events/progress = %#v / %q", store.events, progress.String())
	}
}

func TestBatchAgentSessionHonorsConfiguredProviderPermissionsAndRoot(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	t.Setenv("TAO_DANGEROUSLY_SKIP_PERMISSIONS", "true")
	t.Setenv("TAO_SESSION_TIMEOUT", "30s")
	var got mergeFakeClaudeStart
	var metricsCalled bool
	var progress bytes.Buffer
	session, err := NewBatchAgentSession(BatchAgentSessionConfig{
		ProcessStarter: mergeFakeProcessStarter(t, &got,
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"repairing"}]}}`,
			`{"type":"result","result":"resolved"}`),
		Log: &progress, Metrics: func(_ agent.Metrics, _ string) { metricsCalled = true },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Resolve(context.Background(), BatchAgentSessionRequest{
		BatchID: "batch-a", Operation: BatchAgentOperationCandidateResolution, Attempt: 2,
		IntegrationRoot: "/integration", Prompt: "repair", CandidatePlanID: "plan-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "repairing" || result.Provider.FinalText != "repairing" || got.name != "claude" || got.cwd != "/integration" || got.prompt != "repair" {
		t.Fatalf("unexpected merge session: result=%#v start=%#v", result, got)
	}
	if !strings.Contains(strings.Join(got.args, " "), "--permission-mode bypassPermissions") {
		t.Fatalf("merge permission was not propagated: %v", got.args)
	}
	if !metricsCalled {
		t.Fatal("best-effort metrics callback was not invoked")
	}
	if strings.Contains(progress.String(), "@tao-agent-log-v1") || !strings.Contains(progress.String(), "assistant: repairing") {
		t.Fatalf("merge progress was not human-readable: %q", progress.String())
	}
}

func TestBatchAgentSessionRendersMetricsWarningAsReadableProgress(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	var progress bytes.Buffer
	var got mergeFakeClaudeStart
	session, err := NewBatchAgentSession(BatchAgentSessionConfig{
		ProcessStarter: mergeFakeProcessStarter(t, &got, `{"type":"result","result":"resolved"}`),
		Log:            &progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Resolve(context.Background(), BatchAgentSessionRequest{IntegrationRoot: "/integration", Prompt: "repair"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(progress.String(), "@tao-agent-log-v1") {
		t.Fatalf("merge metrics warning contained a framed record: %q", progress.String())
	}
	if !strings.Contains(progress.String(), "tao telemetry warning: claude metrics absent from stream output") {
		t.Fatalf("merge metrics warning was not human-readable: %q", progress.String())
	}
}

func TestMergeProposalGeneratorDefersRuntimeConfigurationUntilGeneration(t *testing.T) {
	t.Setenv("TAO_AGENT", "invalid")
	starts := 0
	generator, err := NewMergeProposalGenerator(MergeProposalGeneratorConfig{
		ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
			starts++
			return nil, errors.New("unexpected process start")
		},
	})
	if err != nil {
		t.Fatalf("constructor configured unused runtime: %v", err)
	}
	_, err = generator.GenerateMergeProposal(context.Background(), mergeProposalContext())
	if err == nil || !strings.Contains(err.Error(), "TAO_AGENT") {
		t.Fatalf("generation error = %v, want deferred runtime configuration error", err)
	}
	if starts != 0 {
		t.Fatalf("invalid runtime started %d processes", starts)
	}
}

func TestMergeProposalGeneratorUsesOneConfiguredNeutralSession(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	t.Setenv("TAO_DANGEROUSLY_SKIP_PERMISSIONS", "true")
	t.Setenv("TAO_SESSION_TIMEOUT", "30s")
	var got mergeFakeClaudeStart
	starts := 0
	starter := mergeFakeProcessStarter(t, &got, `{"type":"result","result":"{\"type\":\"feat\",\"scope\":\"merge\",\"summary\":\"generate legacy merge messages\",\"what\":\"Generate one exact proposal.\",\"why\":\"Keep legacy reviews mergeable.\"}"}`)
	metricsCalls := 0
	var observed BatchAgentSessionRequest
	generator, err := NewMergeProposalGenerator(MergeProposalGeneratorConfig{
		ProcessStarter: func(ctx context.Context, cwd, name string, args []string) (agent.Process, error) {
			starts++
			return starter(ctx, cwd, name, args)
		},
		Metrics: func(_ agent.Metrics, _ string) { metricsCalls++ },
		Observe: func(request BatchAgentSessionRequest, _ BatchAgentSessionResult, _ error) { observed = request },
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := generator.GenerateMergeProposal(context.Background(), mergeProposalContext())
	if err != nil {
		t.Fatal(err)
	}
	if proposal.Scope != "merge" || got.cwd != "/repo" || metricsCalls != 1 || starts != 1 {
		t.Fatalf("proposal/session/metrics/starts = %#v, %#v, %d, %d", proposal, got, metricsCalls, starts)
	}
	if observed.Operation != BatchAgentOperationProposalGeneration || observed.Attempt != 1 || observed.IntegrationRoot != "/repo" || observed.Prompt == "" {
		t.Fatalf("proposal session attribution = %#v", observed)
	}
	if !strings.Contains(got.prompt, "head456") || !strings.Contains(got.prompt, "diff --git") {
		t.Fatalf("proposal prompt lacks exact context: %s", got.prompt)
	}
	if !strings.Contains(strings.Join(got.args, " "), "--permission-mode bypassPermissions") {
		t.Fatalf("proposal permission was not propagated: %v", got.args)
	}
}

func mergeProposalContext() commitcontract.MergeProposalContext {
	return commitcontract.MergeProposalContext{
		RepoRoot: "/repo", PlanID: "plan-a", DefaultBranch: "main", DefaultParent: "parent123",
		MergeBase: "base123", SourceBranch: "tao/plan-a", SourceHead: "head456", Diff: "diff --git a/a.go b/a.go\n+change\n",
	}
}

type mergeFakeClaudeStart struct {
	cwd    string
	name   string
	args   []string
	prompt string
}

func mergeFakeProcessStarter(t *testing.T, got *mergeFakeClaudeStart, events ...string) agent.ProcessStarter {
	t.Helper()
	return func(_ context.Context, cwd, name string, args []string) (agent.Process, error) {
		got.cwd = cwd
		got.name = name
		got.args = append([]string{}, args...)
		proc := newMergeFakeClaudeProcess(t)
		go func() {
			defer proc.finish()
			prompt, _ := io.ReadAll(proc.stdinReader)
			got.prompt = string(prompt)
			for _, event := range events {
				proc.writeEvent(event)
			}
		}()
		return proc, nil
	}
}

type mergeFakeClaudeProcess struct {
	t            *testing.T
	stdinReader  *io.PipeReader
	stdinWriter  *io.PipeWriter
	stdoutReader *io.PipeReader
	stdoutWriter *io.PipeWriter
	done         chan struct{}
	once         sync.Once
}

func newMergeFakeClaudeProcess(t *testing.T) *mergeFakeClaudeProcess {
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	return &mergeFakeClaudeProcess{t: t, stdinReader: stdinReader, stdinWriter: stdinWriter, stdoutReader: stdoutReader, stdoutWriter: stdoutWriter, done: make(chan struct{})}
}

func (p *mergeFakeClaudeProcess) Stdin() io.WriteCloser { return p.stdinWriter }
func (p *mergeFakeClaudeProcess) Stdout() io.Reader     { return p.stdoutReader }
func (p *mergeFakeClaudeProcess) Stderr() io.Reader     { return strings.NewReader("") }
func (p *mergeFakeClaudeProcess) Wait() error           { <-p.done; return nil }
func (p *mergeFakeClaudeProcess) Kill() error           { return nil }
func (p *mergeFakeClaudeProcess) finish() {
	p.once.Do(func() {
		_ = p.stdoutWriter.Close()
		_ = p.stdinReader.Close()
		close(p.done)
	})
}
func (p *mergeFakeClaudeProcess) writeEvent(line string) {
	if _, err := io.WriteString(p.stdoutWriter, line+"\n"); err != nil {
		p.t.Error(err)
	}
}
