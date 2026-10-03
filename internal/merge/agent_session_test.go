package merge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	agentmetrics "github.com/iamseth/tao/internal/agent/metrics"
	"github.com/iamseth/tao/internal/agentsession"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestMergeSessionModels(t *testing.T) {
	for _, tt := range []struct {
		op   BatchAgentOperation
		want string
	}{
		{BatchAgentOperationCandidateResolution, "r"},
		{BatchAgentOperationSinglePlanResolution, "r"},
		{BatchAgentOperationAggregateRework, "r"},
		{BatchAgentOperationAggregateReview, "v"},
		{BatchAgentOperationSinglePlanReview, "v"},
		{BatchAgentOperationProposalGeneration, "base"},
	} {
		t.Run(string(tt.op), func(t *testing.T) {
			t.Setenv("TAO_MODEL", "ignored-env-model")
			session, err := NewBatchAgentSession(BatchAgentSessionConfig{
				Models: runtimeconfig.ModelSelection{Base: "base", Resolver: "r", MergeReview: "v", Effort: "base-effort", ResolverEffort: "r-effort", MergeReviewEffort: "v-effort"},
			})
			if err != nil {
				t.Fatal(err)
			}
			session.run = func(_ context.Context, request agentsession.Request) (agentsession.Result, error) {
				if request.Effort != tt.want+"-effort" {
					t.Fatalf("effort = %q, want %q", request.Effort, tt.want+"-effort")
				}
				if request.Model != tt.want {
					t.Fatalf("model = %q, want %q", request.Model, tt.want)
				}
				return agentsession.Result{}, nil
			}
			result, err := session.Resolve(context.Background(), BatchAgentSessionRequest{Operation: tt.op})
			if err != nil {
				t.Fatal(err)
			}
			if result.ReasoningEffort != tt.want+"-effort" {
				t.Fatalf("recorded request effort = %q", result.ReasoningEffort)
			}
		})
	}
}

func TestMergeSessionDoesNotLoadModelEnvironment(t *testing.T) {
	t.Setenv("TAO_MODEL", "invalid model")
	session, err := NewBatchAgentSession(BatchAgentSessionConfig{})
	if err != nil {
		t.Fatal(err)
	}
	session.run = func(_ context.Context, request agentsession.Request) (agentsession.Result, error) {
		if request.Model != "" {
			t.Fatalf("unset model = %q", request.Model)
		}
		return agentsession.Result{}, nil
	}
	if _, err := session.Resolve(context.Background(), BatchAgentSessionRequest{Operation: BatchAgentOperationCandidateResolution}); err != nil {
		t.Fatal(err)
	}
}

func TestSingleMergePreflightRunsReadOnlyGitProbeBeforeProviderProbes(t *testing.T) {
	for _, provider := range []string{"pi", "claude"} {
		t.Run(provider, func(t *testing.T) {
			t.Setenv("TAO_AGENT", provider)
			var models []string
			if provider == "pi" {
				installSingleMergeReadinessFixture(t, &models, "rejected")
			} else {
				fakeConfinementExecutable(t)
			}
			root, protected := singleMergeAgentTestBoundary(t)
			wantRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			stopped := errors.New("read-only git sandbox confinement failed")
			calls, starts := 0, 0
			stubReadOnlyGitProbe(t, func(_ context.Context, policy singleMergeFilesystemConfinement) error {
				calls++
				if policy.integrationRoot != wantRoot {
					t.Fatalf("integration root = %q, want %q", policy.integrationRoot, wantRoot)
				}
				return stopped
			})
			session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
				ProviderLookPath: testProviderLookPath,
				ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
					starts++
					return nil, errors.New("unexpected provider launch")
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			err = session.Preflight(context.Background(), BatchAgentSessionRequest{
				Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: root, ProtectedGitObjectRoot: protected,
			})
			if !errors.Is(err, stopped) || calls != 1 || starts != 0 || len(models) != 0 {
				t.Fatalf("error=%v git probes=%d provider starts=%d models=%v", err, calls, starts, models)
			}
			if capability := SingleMergeStartupCapabilityForError(err); capability != plan.SingleMergeStartupConfinement {
				t.Fatalf("capability = %q", capability)
			}
		})
	}
}

func TestSingleMergeReadOnlyGitProbePassesInsideShippedConfinement(t *testing.T) {
	root, _ := singleMergeAgentTestBoundary(t)
	readme := filepath.Join(root, "README")
	if err := os.WriteFile(readme, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realGitOutput(t, root, "add", "README")
	realGitOutput(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "initial")
	policy := singleMergeFilesystemConfinement{integrationRoot: root, protectedPaths: []string{filepath.Join(root, ".git")}, allowEdits: true}
	if err := probeSingleMergeFilesystemConfinement(context.Background(), policy, "/usr/bin/true"); err != nil {
		if runtime.GOOS == "linux" && os.Getenv("TAO_REQUIRE_CONFINEMENT_TESTS") == "1" {
			t.Fatal(err)
		}
		t.Skipf("OS confinement unavailable: %v", err)
	}
	entries := func() []string {
		t.Helper()
		var names []string
		err := filepath.WalkDir(filepath.Join(root, ".git"), func(path string, _ os.DirEntry, err error) error {
			names = append(names, path)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return names
	}
	before := entries()
	runtimes, err := filepath.Glob("/tmp/tao-merge-agent-runtime-*")
	if err != nil {
		t.Fatal(err)
	}
	if err := probeSingleMergeReadOnlyGit(context.Background(), policy); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, entries()) {
		t.Fatal("protected Git entries changed")
	}
	contents, err := os.ReadFile(readme) // #nosec G304 -- Test-owned README under t.TempDir.
	if err != nil || string(contents) != "unchanged\n" {
		t.Fatalf("README changed: %q, %v", contents, err)
	}
	after, err := filepath.Glob("/tmp/tao-merge-agent-runtime-*")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range after {
		if !slices.Contains(runtimes, path) {
			t.Fatalf("probe runtime survived: %s", path)
		}
	}
}

func TestSingleMergeReadOnlyGitProbeFailsClosedUnderDenyAllSandbox(t *testing.T) {
	confiner, err := singleMergeFilesystemConfinementExecutable()
	if err != nil {
		if runtime.GOOS == "linux" && os.Getenv("TAO_REQUIRE_CONFINEMENT_TESTS") == "1" {
			t.Fatalf("OS confinement unavailable: %v", err)
		}
		t.Skipf("OS confinement unavailable: %v", err)
	}
	root, _ := singleMergeAgentTestBoundary(t)
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	realGitOutput(t, root, "add", "README")
	realGitOutput(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "initial")
	protected := filepath.Join(root, ".git")
	policy := singleMergeFilesystemConfinement{integrationRoot: root, protectedPaths: []string{protected}, allowEdits: true}
	snapshot := func() map[string]string {
		t.Helper()
		entries := make(map[string]string)
		if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			entries[path] = info.Mode().String()
			if entry.Type().IsRegular() {
				contents, err := os.ReadFile(path) //nolint:gosec // snapshot reads only the test-owned repository.
				if err != nil {
					return err
				}
				entries[path] += string(contents)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return entries
	}
	before := snapshot()
	// A failure after the swap must be attributable to the profile, not the host.
	if err := probeSingleMergeReadOnlyGit(context.Background(), policy); err != nil {
		t.Fatalf("positive control: %v", err)
	}
	var script string
	switch runtime.GOOS {
	case "darwin":
		script = `#!/bin/sh
while [ "$#" -gt 0 ]; do
    arg=$1
    shift
    [ "$arg" = "--" ] && break
done
exec /usr/bin/sandbox-exec -p '(version 1)(allow default)(deny file-write*)' -- "$@"
`
	case "linux":
		// Replace the device mount with an empty tmpfs, removing /dev/null.
		t.Setenv("TAO_TEST_REAL_CONFINER", confiner)
		script = `#!/bin/sh
remaining=$#
while [ "$remaining" -gt 0 ]; do
    arg=$1
    shift
    remaining=$((remaining - 1))
    if [ "$arg" = "--dev" ] && [ "$remaining" -gt 0 ] && [ "$1" = "/dev" ]; then
        shift
        remaining=$((remaining - 1))
        set -- "$@" --tmpfs /dev
    else
        set -- "$@" "$arg"
    fi
done
exec "$TAO_TEST_REAL_CONFINER" "$@"
`
	default:
		t.Skipf("no deny-all fixture for %s", runtime.GOOS)
	}
	wrapper := filepath.Join(t.TempDir(), "deny-all-confiner")
	if err := os.WriteFile(wrapper, []byte(script), 0o700); err != nil { //nolint:gosec // executable mode is required for the fixture confiner.
		t.Fatal(err)
	}
	setConfinementExecutable(t, func() (string, error) { return wrapper, nil })
	assertFailure := func(err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "sandbox") {
			t.Fatalf("expected sandbox failure, got %v", err)
		}
		if runtime.GOOS == "darwin" && !strings.Contains(err.Error(), "/dev/null") {
			t.Fatalf("missing git /dev/null diagnostic: %v", err)
		}
		// The executor appends captured output after the exit status. Require it
		// without depending on platform-specific Git wording.
		_, status, ok := strings.Cut(err.Error(), "exit status ")
		_, diagnostic, hasDiagnostic := strings.Cut(status, ": ")
		if !ok || !hasDiagnostic || strings.TrimSpace(diagnostic) == "" {
			t.Fatalf("missing bounded git diagnostic: %v", err)
		}
		if capability := SingleMergeStartupCapabilityForError(err); capability != plan.SingleMergeStartupConfinement {
			t.Fatalf("capability = %q", capability)
		}
		t.Logf("deny-all failure: %v", err)
	}
	assertFailure(probeSingleMergeReadOnlyGit(context.Background(), policy))
	t.Setenv("TAO_AGENT", "claude")
	starts := 0
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		ProviderLookPath: func(string) (string, error) { return "/usr/bin/true", nil },
		ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
			starts++
			return nil, errors.New("unexpected provider launch")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFailure(session.Preflight(context.Background(), BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: root, ProtectedGitObjectRoot: protected,
	}))
	if starts != 0 {
		t.Fatalf("provider starts = %d, want zero", starts)
	}
	if !maps.Equal(before, snapshot()) {
		t.Fatal("protected Git entries or worktree changed")
	}
}

func TestSingleMergePreflightUsesResolverModel(t *testing.T) {
	fakeConfinementExecutable(t)
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	original := agent.DefaultProcessStarter
	t.Cleanup(func() { agent.DefaultProcessStarter = original })
	stopped := errors.New("stop readiness fixture")
	calls := 0
	agent.DefaultProcessStarter = func(_ context.Context, _, _ string, args []string) (agent.Process, error) {
		calls++
		if !strings.Contains(strings.Join(args, " "), "--model r --thinking resolver-effort") {
			t.Fatalf("readiness args = %v", args)
		}
		return nil, stopped
	}
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		Agent: runtimeconfig.AgentPi, Models: runtimeconfig.ModelSelection{Resolver: "r", MergeReview: "v", ResolverEffort: "resolver-effort"}, ProviderLookPath: testProviderLookPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	root, protected := singleMergeAgentTestBoundary(t)
	err = session.Preflight(context.Background(), BatchAgentSessionRequest{Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: root, ProtectedGitObjectRoot: protected})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), stopped.Error()) {
		t.Fatalf("calls=%d error=%v", calls, err)
	}
}

func TestSingleMergePreflightProbesDistinctEffectiveModels(t *testing.T) {
	for _, tt := range []struct {
		name   string
		models runtimeconfig.ModelSelection
		want   []string
	}{
		{"distinct roles", runtimeconfig.ModelSelection{Resolver: "r", MergeReview: "v", ResolverEffort: "low", MergeReviewEffort: "high"}, []string{"r/low", "v/high"}},
		{"same model distinct efforts", runtimeconfig.ModelSelection{Base: "b", ResolverEffort: "low", MergeReviewEffort: "high"}, []string{"b/low", "b/high"}},
		{"same model and effort", runtimeconfig.ModelSelection{Base: "b", Effort: "high"}, []string{"b/high"}},
		{"identical roles", runtimeconfig.ModelSelection{Resolver: "r", MergeReview: "r"}, []string{"r"}},
		{"base fallback", runtimeconfig.ModelSelection{Base: "base", Resolver: "r"}, []string{"r", "base"}},
		{"identical effective roles", runtimeconfig.ModelSelection{Base: "base", Resolver: "base"}, []string{"base"}},
		{"unset", runtimeconfig.ModelSelection{}, []string{""}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var models []string
			installSingleMergeReadinessFixture(t, &models, "rejected")
			session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
				Agent: runtimeconfig.AgentPi, Models: tt.models, ProviderLookPath: testProviderLookPath,
			})
			if err != nil {
				t.Fatal(err)
			}
			root, protected := singleMergeAgentTestBoundary(t)
			if err := session.Preflight(context.Background(), BatchAgentSessionRequest{
				Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: root, ProtectedGitObjectRoot: protected,
			}); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(models, tt.want) {
				t.Fatalf("probed models = %q, want %q", models, tt.want)
			}
		})
	}
}

func TestSingleMergeRejectedReviewerPreservesResolutionAuthority(t *testing.T) {
	var models []string
	installSingleMergeReadinessFixture(t, &models, "rejected")
	fixture, request, git := preparedSingleResolutionFixture(t)
	starts := 0
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		Agent: runtimeconfig.AgentPi, Models: runtimeconfig.ModelSelection{Resolver: "r", MergeReview: "rejected"},
		ProviderLookPath: testProviderLookPath,
		ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
			starts++
			return nil, errors.New("unexpected resolver invocation")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	store := &recordingSingleResolutionStore{current: request.Intent}
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: store, Agent: session}
	_, err = resolver.ResolveConflict(context.Background(), request)
	if !errors.Is(err, ErrSingleResolutionPreflight) || !strings.Contains(err.Error(), "unknown model: rejected") {
		t.Fatalf("reviewer rejection = %v, want model readiness preflight failure", err)
	}
	if !slices.Equal(models, []string{"r", "rejected"}) {
		t.Fatalf("probed models = %q", models)
	}
	if starts != 0 || store.records != 0 || store.advances != 0 || store.current.Resolution != nil {
		t.Fatalf("reviewer rejection consumed resolution authority: starts=%d store=%+v", starts, store)
	}
	if head := strings.TrimSpace(realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")); head != request.Intent.DefaultParent {
		t.Fatalf("reviewer rejection created an integration commit: HEAD=%s", head)
	}
}

func TestProbeSingleMergePiReadinessRunsReadOnlyGitBeforeRPC(t *testing.T) {
	fakeConfinementExecutable(t)
	original := agent.DefaultProcessStarter
	t.Cleanup(func() { agent.DefaultProcessStarter = original })
	starts := 0
	agent.DefaultProcessStarter = func(context.Context, string, string, []string) (agent.Process, error) {
		starts++
		return nil, errors.New("unexpected RPC process")
	}
	wantErr := errors.New("read-only git probe failed")
	var roots []string
	stubReadOnlyGitProbe(t, func(_ context.Context, policy singleMergeFilesystemConfinement) error {
		roots = append(roots, policy.integrationRoot)
		roots = append(roots, policy.protectedPaths...)
		info, err := os.Stat(filepath.Join(policy.integrationRoot, ".git"))
		if err != nil || !info.IsDir() {
			t.Fatalf("probe integration root is not a git repository: info=%v err=%v", info, err)
		}
		return wantErr
	})
	err := ProbeSingleMergePiReadiness(context.Background(), "pi")
	if err != wantErr { //nolint:errorlint // The probe error must be returned unchanged, not wrapped.
		t.Errorf("readiness error = %v, want unchanged sentinel %v", err, wantErr)
	}
	if starts != 0 {
		t.Errorf("started %d RPC processes before Git readiness", starts)
	}
	if len(roots) != 2 {
		t.Fatalf("probe roots = %v, want integration and protected roots", roots)
	}
	for _, root := range roots {
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("temporary root %s was not removed: %v", root, err)
		}
	}
}

// Exercise the production RPC readiness path without launching a provider or confiner.
func installSingleMergeReadinessFixture(t *testing.T, models *[]string, rejected string) {
	t.Helper()
	fakeConfinementExecutable(t)
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	original := agent.DefaultProcessStarter
	t.Cleanup(func() { agent.DefaultProcessStarter = original })
	agent.DefaultProcessStarter = func(_ context.Context, _, _ string, args []string) (agent.Process, error) {
		model := ""
		if i := slices.Index(args, "--model"); i >= 0 && i+1 < len(args) {
			model = args[i+1]
		}
		selection := model
		if i := slices.Index(args, "--thinking"); i >= 0 && i+1 < len(args) {
			selection += "/" + args[i+1]
		}
		*models = append(*models, selection)
		output := `{"id":"tao-readiness-state","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"fixture","id":"valid"}}}` + "\n" +
			`{"id":"tao-readiness-models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"fixture","id":"valid"}]}}` + "\n"
		if model == rejected {
			output = `{"id":"tao-readiness-state","type":"response","command":"get_state","success":false,"error":"unknown model: ` + rejected + `"}` + "\n"
		}
		proc := &singleMergeReadinessProcess{cleanupTestProcess: newCleanupTestProcess(nil), output: output}
		t.Cleanup(func() {
			if strings.Contains(proc.input.String(), `"type":"prompt"`) {
				t.Error("readiness sent a model prompt")
			}
		})
		return proc, nil
	}
}

type singleMergeReadinessProcess struct {
	*cleanupTestProcess
	input  bytes.Buffer
	output string
}

func (p *singleMergeReadinessProcess) Stdin() io.WriteCloser {
	return cleanupTestWriteCloser{Writer: &p.input}
}
func (p *singleMergeReadinessProcess) Stdout() io.Reader { return strings.NewReader(p.output) }

func testProviderLookPath(name string) (string, error) { return name, nil }
func successfulConfinementProbe() error                { return nil }

// fakeConfinementExecutable supplies a plausible path that tests must never execute.
// Tests using this package-level seam must not call t.Parallel().
func fakeConfinementExecutable(t *testing.T) {
	t.Helper()
	setConfinementExecutable(t, func() (string, error) { return "/usr/bin/fake-confiner", nil })
	stubReadOnlyGitProbe(t, func(context.Context, singleMergeFilesystemConfinement) error { return nil })
}

// Tests using this package-level seam must not call t.Parallel().
func stubReadOnlyGitProbe(t *testing.T, probe func(context.Context, singleMergeFilesystemConfinement) error) {
	t.Helper()
	original := singleMergeReadOnlyGitProbe
	singleMergeReadOnlyGitProbe = probe
	t.Cleanup(func() { singleMergeReadOnlyGitProbe = original })
}

// Tests using setConfinementExecutable must not call t.Parallel().
func setConfinementExecutable(t *testing.T, lookup func() (string, error)) {
	t.Helper()
	original := singleMergeConfinementExecutable
	singleMergeConfinementExecutable = lookup
	t.Cleanup(func() { singleMergeConfinementExecutable = original })
}

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
					return agentsession.Result{Invoked: true, Output: " partial ", AgentLabel: "pi", MetricsUsable: true, Metrics: &agent.Metrics{SessionID: "session-a", OutputTokens: 7}}, tt.err
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
			return agentsession.Result{Invoked: true, Output: "partial", AgentLabel: "test-agent", MetricsUsable: true}, providerErr
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

func mergeTestRuntimeEnv() *runtimeconfig.EnvSnapshot {
	snapshot := runtimeconfig.RuntimeEnv()
	return &snapshot
}

func TestBatchAgentSessionHonorsConfiguredProviderPermissionsAndRoot(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	t.Setenv("TAO_DANGEROUSLY_SKIP_PERMISSIONS", "true")
	t.Setenv("TAO_SESSION_TIMEOUT", "30s")
	var got mergeFakeClaudeStart
	var metricsCalled bool
	var progress bytes.Buffer
	session, err := NewBatchAgentSession(BatchAgentSessionConfig{
		RuntimeEnv: mergeTestRuntimeEnv(),
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

func TestSingleMergeAgentSessionExposesMetricsWithoutBatchPersistence(t *testing.T) {
	fakeConfinementExecutable(t)
	t.Setenv("TAO_AGENT", "claude")
	var got mergeFakeClaudeStart
	batchEvents := &recordingBatchAgentEvents{}
	var metrics agent.Metrics
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		RuntimeEnv:       mergeTestRuntimeEnv(),
		ProviderLookPath: testProviderLookPath, ConfinementProbe: successfulConfinementProbe,
		ProcessStarter: mergeFakeProcessStarter(t, &got, `{"type":"result","result":"resolved"}`),
		EventAppender:  batchEvents,
		Metrics:        func(value agent.Metrics, _ string) { metrics = value },
	})
	if err != nil {
		t.Fatal(err)
	}
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	result, err := session.Resolve(context.Background(), BatchAgentSessionRequest{
		BatchID: "must-not-persist", Operation: BatchAgentOperationSinglePlanResolution,
		Attempt: 1, IntegrationRoot: integrationRoot, Prompt: "resolve", CandidatePlanID: "plan-a",
		ProtectedGitObjectRoot: protectedRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalIntegration, err := canonicalConfinementDirectory(integrationRoot, "test integration worktree")
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "resolved" || got.cwd != canonicalIntegration || got.prompt != "resolve" {
		t.Fatalf("unexpected single-merge provider result: %#v / %#v", result, got)
	}
	if len(batchEvents.events) != 0 {
		t.Fatalf("single-plan session leaked repository batch telemetry: %#v", batchEvents.events)
	}
	if metrics.SessionID != "" {
		t.Fatalf("unexpected fabricated metrics: %#v", metrics)
	}
}

func TestSingleMergeAgentSessionMissingProviderDoesNotProbeOrStart(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	starts := 0
	probes := 0
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		RuntimeEnv: mergeTestRuntimeEnv(),
		ProviderLookPath: func(name string) (string, error) {
			return "", fmt.Errorf("%s missing: %w", name, exec.ErrNotFound)
		},
		ConfinementProbe: func() error {
			probes++
			return nil
		},
		ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
			starts++
			return nil, errors.New("unexpected provider start")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	request := BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: integrationRoot,
		ProtectedGitObjectRoot: protectedRoot,
	}
	if err := session.Preflight(context.Background(), request); err == nil || !strings.Contains(err.Error(), `resolve provider executable "claude"`) {
		t.Fatalf("missing-provider preflight error = %v", err)
	}
	if starts != 0 || probes != 0 {
		t.Fatalf("missing provider started %d processes and %d confinement probes", starts, probes)
	}
}

func TestConfinementCleanupProcessWaitsForStopAndSettlesOnce(t *testing.T) {
	for _, tt := range []struct {
		name    string
		waitErr error
		kill    bool
	}{
		{name: "normal completion"},
		{name: "explicit process error", waitErr: errors.New("process failed")},
		{name: "kill", kill: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runtimeRoot := t.TempDir()
			runtime := &singleMergeInvocationRuntime{root: runtimeRoot}
			child := newCleanupTestProcess(tt.waitErr)
			process := newConfinementCleanupProcess(child, runtime)
			waitResult := make(chan error, 1)
			go func() { waitResult <- process.Wait() }()
			<-child.waitStarted
			if _, err := os.Stat(runtimeRoot); err != nil {
				t.Fatalf("runtime removed before child stopped: %v", err)
			}
			if tt.kill {
				if err := process.Kill(); err != nil {
					t.Fatalf("Kill() error = %v", err)
				}
			} else {
				child.complete()
			}
			if err := <-waitResult; !errors.Is(err, tt.waitErr) {
				t.Fatalf("Wait() error = %v, want %v", err, tt.waitErr)
			}
			if _, err := os.Stat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("settled runtime survived: %v", err)
			}
			_ = process.Kill()
			_ = process.Wait()
			waitCalls, killCalls := child.calls()
			if waitCalls != 1 || killCalls != 1 {
				t.Fatalf("child wait/kill calls = %d/%d, want 1/1", waitCalls, killCalls)
			}
		})
	}
}

func TestSingleMergePiRuntimeProjectionUsesPrivateModesAndAllowlist(t *testing.T) {
	hostRoot := t.TempDir()
	t.Setenv("PI_CODING_AGENT_DIR", hostRoot)
	if err := os.WriteFile(filepath.Join(hostRoot, "auth.json"), []byte("{\"fixture\":\"credentials\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostRoot, "models-store.json"), []byte("{\"selected\":\"fixture\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hostRoot, "not-allowlisted.json"), []byte("excluded\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(hostRoot, "npm"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := newSingleMergeInvocationRuntime("pi")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = runtime.cleanup() }()
	for _, path := range []string{runtime.path(), filepath.Join(runtime.path(), "agent"), filepath.Join(runtime.path(), "sessions")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("private directory mode for %s = %v", filepath.Base(path), info.Mode().Perm())
		}
	}
	projectedAuth := filepath.Join(runtime.path(), "agent", "auth.json")
	info, err := os.Stat(projectedAuth)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("projected auth mode = %v", info.Mode().Perm())
	}
	if got, err := os.ReadFile(projectedAuth); err != nil || string(got) != "{\"fixture\":\"credentials\"}\n" { //nolint:gosec // fixture projection validates byte preservation.
		t.Fatalf("projected auth = %q, %v", got, err)
	}
	projectedModels := filepath.Join(runtime.path(), "agent", "models-store.json")
	if got, err := os.ReadFile(projectedModels); err != nil || string(got) != "{\"selected\":\"fixture\"}\n" { //nolint:gosec // fixture projection validates the production model catalog name and byte preservation.
		t.Fatalf("projected models store = %q, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(runtime.path(), "agent", "not-allowlisted.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected non-allowlisted projection: %v", err)
	}
	target, err := os.Readlink(filepath.Join(runtime.path(), "agent", "npm"))
	if err != nil {
		t.Fatal(err)
	}
	canonicalNPM, err := filepath.EvalSymlinks(filepath.Join(hostRoot, "npm"))
	if err != nil || target != canonicalNPM {
		t.Fatalf("npm projection target = %q, %v; want %q", target, err, canonicalNPM)
	}
}

func TestSingleMergeConfiningStarterCleansRuntimeOnStartupError(t *testing.T) {
	fakeConfinementExecutable(t)
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	policy := singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}
	startupErr := errors.New("fixture startup failed")
	var runtimeRoot string
	starter := singleMergeFilesystemConfiningProcessStarter(func(_ context.Context, _ string, _ string, args []string) (agent.Process, error) {
		for _, arg := range args {
			if strings.HasPrefix(arg, "TMPDIR=") {
				runtimeRoot = strings.TrimPrefix(arg, "TMPDIR=")
				break
			}
		}
		return nil, startupErr
	}, func(string) (string, error) { return "/usr/bin/true", nil })
	ctx := context.WithValue(context.Background(), singleMergeFilesystemConfinementContextKey{}, policy)
	if _, err := starter(ctx, integrationRoot, "claude", []string{"--version"}); !errors.Is(err, startupErr) {
		if strings.Contains(fmt.Sprint(err), "confinement executable") || strings.Contains(fmt.Sprint(err), "bubblewrap") {
			t.Skipf("OS confinement unavailable: %v", err)
		}
		t.Fatalf("startup error = %v", err)
	}
	if runtimeRoot == "" {
		t.Fatal("startup did not receive a generated runtime")
	}
	if _, err := os.Stat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime survived provider startup error: %v", err)
	}
}

func TestSingleMergeConfinementProbeBudgetsAndCleanup(t *testing.T) {
	if singleMergeFilesystemProbeTimeout != 5*time.Second {
		t.Fatal("production filesystem probe budget changed")
	}
	for _, name := range []string{"production", "supplied", "parent", "expired", "cancelled", "failure"} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			budget := singleMergeFilesystemProbeTimeout
			if name == "supplied" {
				budget = 30 * time.Second
			}
			var parentDeadline time.Time
			if name == "parent" || name == "expired" {
				parentDeadline = time.Now().Add(time.Second)
				if name == "expired" {
					parentDeadline = time.Now().Add(-time.Second)
				}
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, parentDeadline)
				defer cancel()
			}
			if name == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			runtimeRoot := t.TempDir()
			script := "exit 0"
			if name == "failure" {
				script = "printf '%s' '" + strings.Repeat("x", 2*maxSingleMergeConfinementProbeOutputBytes) + "'; exit 17"
			}
			spec := &singleMergeLaunchSpec{
				name: "/bin/sh", args: []string{"-c", script}, cwd: t.TempDir(),
				runtime: &singleMergeInvocationRuntime{root: runtimeRoot},
			}
			before := time.Now()
			observed := false
			err := executeSingleMergeFilesystemProbe(ctx, spec, budget, func(probeCtx context.Context) {
				observed = true
				deadline, ok := probeCtx.Deadline()
				if !ok {
					t.Fatal("probe has no deadline")
				}
				if name == "parent" || name == "expired" {
					if !deadline.Equal(parentDeadline) {
						t.Fatalf("deadline = %v, want parent %v", deadline, parentDeadline)
					}
				} else if deadline.Before(before.Add(budget)) || deadline.After(time.Now().Add(budget)) {
					t.Fatalf("deadline %v does not select budget %v", deadline, budget)
				}
				if name == "cancelled" && !errors.Is(probeCtx.Err(), context.Canceled) {
					t.Fatalf("cancelled parent not inherited: %v", probeCtx.Err())
				}
			})
			if !observed {
				t.Fatal("probe context not observed")
			}
			switch name {
			case "cancelled":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled probe = %v", err)
				}
			case "expired":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("expired probe = %v", err)
				}
			case "failure":
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || exitErr.ExitCode() != 17 {
					t.Fatalf("nonzero provider did not fail closed: %v", err)
				}
				if len(err.Error()) > maxSingleMergeConfinementProbeOutputBytes+128 || !strings.Contains(err.Error(), strings.Repeat("x", maxSingleMergeConfinementProbeOutputBytes)) {
					t.Fatal("provider diagnostics did not retain bounded prefix")
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := os.Stat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("probe runtime survived: %v", err)
			}
		})
	}
}

func TestSingleMergeConfinementProbeOutputRetainsOnlyBoundedPrefix(t *testing.T) {
	var output singleMergeConfinementProbeOutput
	prefix := bytes.Repeat([]byte("p"), maxSingleMergeConfinementProbeOutputBytes)
	oversized := append(append([]byte{}, prefix...), bytes.Repeat([]byte("tail"), 1024)...)
	written, err := output.Write(oversized)
	if err != nil || written != len(oversized) {
		t.Fatalf("bounded probe write = %d, %v; want %d, nil", written, err, len(oversized))
	}
	if retained := output.String(); len(retained) != maxSingleMergeConfinementProbeOutputBytes || retained != string(prefix) {
		t.Fatalf("retained probe output bytes = %d, want bounded prefix of %d", len(retained), maxSingleMergeConfinementProbeOutputBytes)
	}
	if written, err := output.Write([]byte("more")); err != nil || written != len("more") || len(output.String()) != maxSingleMergeConfinementProbeOutputBytes {
		t.Fatalf("drained post-limit write = %d, %v; retained=%d", written, err, len(output.String()))
	}
}

func TestSingleMergeConfinementProbeKeepsIntegrationReadOnlyAndRuntimeWritable(t *testing.T) {
	integrationRoot, protectedRoot := t.TempDir(), t.TempDir()
	if err := probeSingleMergeFilesystemConfinementWithTimeout(context.Background(), singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}, "/usr/bin/true", 30*time.Second); err != nil {
		t.Skipf("OS confinement is unavailable for provider launch regression: %v", err)
	}

	target := filepath.Join(integrationRoot, "prepared-conflict.txt")
	if err := os.WriteFile(target, []byte("prepared conflict\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_TEST_VERSION_PROBE_TARGET", target)
	provider := filepath.Join(t.TempDir(), "provider")
	const script = `#!/bin/sh
printf runtime >"$TMPDIR/version-probe-runtime" || exit 41
if printf mutation >"$TAO_TEST_VERSION_PROBE_TARGET"; then
  exit 42
fi
printf 'provider version\n'
`
	if err := os.WriteFile(provider, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(provider, 0o700); err != nil { //nolint:gosec // executable mode is required for the fixture provider.
		t.Fatal(err)
	}
	if err := probeSingleMergeFilesystemConfinementWithTimeout(context.Background(), singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}, provider, 30*time.Second); err != nil {
		t.Fatalf("read-only provider launch probe failed: %v", err)
	}
	if contents, err := os.ReadFile(target); err != nil || string(contents) != "prepared conflict\n" { //nolint:gosec // fixture-owned worktree content is the confinement assertion.
		t.Fatalf("provider launch probe changed prepared worktree: %q, %v", contents, err)
	}
}

func TestSingleMergePiProjectionRejectsMalformedConfigurationWithoutCopyingIt(t *testing.T) {
	hostAgentRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(hostAgentRoot, "settings.json"), []byte("{not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PI_CODING_AGENT_DIR", hostAgentRoot)
	destination := t.TempDir()
	err := materializeSingleMergePiView(destination)
	if err == nil || !strings.Contains(err.Error(), "settings.json is malformed JSON") {
		t.Fatalf("malformed configuration error = %v", err)
	}
	if capability := SingleMergeStartupCapabilityForError(err); capability != plan.SingleMergeStartupConfigProjection {
		t.Fatalf("malformed configuration capability = %q", capability)
	}
	if _, statErr := os.Stat(filepath.Join(destination, "settings.json")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("malformed configuration was projected: %v", statErr)
	}
}

func TestSingleMergePiRPCProjectsConfigAndResourcesIntoEphemeralRuntime(t *testing.T) {
	t.Setenv("TAO_AGENT", "pi")
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	if err := probeSingleMergeFilesystemConfinement(context.Background(), singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}, "/usr/bin/true"); err != nil {
		t.Skipf("OS confinement is unavailable for Pi RPC launch regression: %v", err)
	}

	hostAgentRoot := t.TempDir()
	hostSessionRoot := t.TempDir()
	hostInputs := map[string]string{
		"settings.json":     "{\"model\":\"fixture\"}\n",
		"auth.json":         "{\"token\":\"fixture-secret\"}\n",
		"models.json":       "{\"models\":[\"fixture\"]}\n",
		"models-store.json": "{\"selected\":\"fixture\"}\n",
		"trust.json":        "{\"trusted\":true}\n",
	}
	for name, contents := range hostInputs {
		if err := os.WriteFile(filepath.Join(hostAgentRoot, name), []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	promptRoot := filepath.Join(hostAgentRoot, "prompts")
	npmRoot := filepath.Join(hostAgentRoot, "npm")
	for _, root := range []string{promptRoot, npmRoot} {
		if err := os.Mkdir(root, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(promptRoot, "fixture.md"), []byte("projected resource\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(npmRoot, "package.json"), []byte("{\"name\":\"fixture-package\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostSentinel := filepath.Join(hostSessionRoot, "host-sentinel")
	if err := os.WriteFile(hostSentinel, []byte("unchanged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeAgentEntries := directoryEntryNames(t, hostAgentRoot)
	beforeSessionEntries := directoryEntryNames(t, hostSessionRoot)
	t.Setenv("PI_CODING_AGENT_DIR", hostAgentRoot)
	t.Setenv("PI_CODING_AGENT_SESSION_DIR", hostSessionRoot)
	t.Setenv("TAO_TEST_HOST_PI_DIR", hostAgentRoot)
	t.Setenv("TAO_TEST_HOST_PI_SESSION_DIR", hostSessionRoot)
	provider := filepath.Join(t.TempDir(), "pi")
	// The canary defeats 2>/dev/null masking of device-write denial.
	const script = `#!/bin/sh
set -eu
: >/dev/null || exit 48
if [ "${1:-}" = "--version" ]; then
  printf 'pi fixture version\n'
  exit 0
fi
if [ "${1:-}" != "--mode" ] || [ "${2:-}" != "rpc" ] || [ "${3:-}" != "--no-session" ]; then exit 31; fi
if [ "${PI_OFFLINE:-}" != "1" ]; then exit 47; fi
if [ "$PI_CODING_AGENT_DIR" = "$TAO_TEST_HOST_PI_DIR" ] || [ "$PI_CODING_AGENT_SESSION_DIR" = "$TAO_TEST_HOST_PI_SESSION_DIR" ]; then exit 32; fi
case "$PI_CODING_AGENT_DIR" in "$TMPDIR"/*) ;; *) exit 33;; esac
case "$PI_CODING_AGENT_SESSION_DIR" in "$TMPDIR"/*) ;; *) exit 34;; esac
case "$XDG_CACHE_HOME" in "$TMPDIR"/*) ;; *) exit 35;; esac
case "$XDG_STATE_HOME" in "$TMPDIR"/*) ;; *) exit 36;; esac
cmp "$PI_CODING_AGENT_DIR/settings.json" "$TAO_TEST_HOST_PI_DIR/settings.json" || exit 37
cmp "$PI_CODING_AGENT_DIR/auth.json" "$TAO_TEST_HOST_PI_DIR/auth.json" || exit 38
cmp "$PI_CODING_AGENT_DIR/models-store.json" "$TAO_TEST_HOST_PI_DIR/models-store.json" || exit 39
[ "$(cat "$PI_CODING_AGENT_DIR/prompts/fixture.md")" = "projected resource" ] || exit 40
[ "$(cat "$PI_CODING_AGENT_DIR/npm/package.json")" = '{"name":"fixture-package"}' ] || exit 41
if printf denied >"$TAO_TEST_HOST_PI_DIR/settings.json.lock" 2>/dev/null; then exit 42; fi
if printf denied >"$TAO_TEST_HOST_PI_DIR/models-store.json" 2>/dev/null; then exit 43; fi
if printf denied >"$TAO_TEST_HOST_PI_SESSION_DIR/session.jsonl" 2>/dev/null; then exit 44; fi
if printf denied >"$PI_CODING_AGENT_DIR/prompts/fixture.md" 2>/dev/null; then exit 45; fi
if printf denied >"$PI_CODING_AGENT_DIR/npm/package.json" 2>/dev/null; then exit 46; fi
printf private >"$PI_CODING_AGENT_DIR/settings.json.lock"
printf private >"$PI_CODING_AGENT_DIR/auth.json.lock"
printf private >"$PI_CODING_AGENT_DIR/models-store.json"
printf cache >"$XDG_CACHE_HOME/cache-entry"
printf state >"$XDG_STATE_HOME/state-entry"
sidecar="$PI_CODING_AGENT_SESSION_DIR/session.jsonl"
printf 'ephemeral session\n' >"$sidecar"
IFS= read -r _
printf '{"id":"tao-readiness-state","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"fixture","id":"fixture"}}}\n'
IFS= read -r _
printf '{"id":"tao-readiness-models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"fixture","id":"fixture"}]}}\n'
IFS= read -r command
case "$command" in *'"type":"abort"'*) exit 0;; *'"type":"prompt"'*) ;; *) exit 43;; esac
printf 'one attributed request\n' >"$PWD/prompt-marker"
printf '{"id":"tao-prompt","type":"response","command":"prompt","success":true}\n'
printf '{"type":"message","role":"assistant","text":"%s"}\n' "$sidecar"
printf '{"type":"agent_end","session_id":"fixture-session"}\n'
IFS= read -r _
printf '{"id":"2","type":"response","command":"get_state","success":true,"data":{"sessionId":"fixture-session","model":{"provider":"fixture","id":"fixture"}}}\n'
IFS= read -r _
printf '{"id":"3","type":"response","command":"get_session_stats","success":true,"data":{"sessionId":"fixture-session","tokens":{"total":1}}}\n'
`
	if err := os.WriteFile(provider, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(provider, 0o700); err != nil { //nolint:gosec // executable mode is required for the fixture provider.
		t.Fatal(err)
	}
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		ProviderLookPath: func(string) (string, error) { return provider, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanResolution, Attempt: 1,
		IntegrationRoot: integrationRoot, Prompt: "resolve", CandidatePlanID: "plan-a",
		ProtectedGitObjectRoot: protectedRoot,
	}
	if err := session.Preflight(context.Background(), request); err != nil {
		t.Fatalf("Pi RPC readiness failed: %v", err)
	}
	result, err := session.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("Pi RPC resolution failed: %v", err)
	}
	runtimeRoot := filepath.Dir(filepath.Dir(result.Output))
	if !strings.HasPrefix(filepath.Base(runtimeRoot), "tao-merge-agent-runtime-") {
		t.Fatalf("Pi session sidecar was not redirected into the invocation runtime: %q", result.Output)
	}
	if _, err := os.Stat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private Pi runtime survived process cleanup: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(integrationRoot, "prompt-marker")); err != nil || string(got) != "one attributed request\n" { //nolint:gosec // fixture output proves readiness did not receive the prompt.
		t.Fatalf("attributed request marker = %q, %v", got, err)
	}
	for name, want := range hostInputs {
		if got, err := os.ReadFile(filepath.Join(hostAgentRoot, name)); err != nil || string(got) != want { //nolint:gosec // fixture-owned host input is the immutability assertion.
			t.Fatalf("host Pi %s changed: %q, %v", name, got, err)
		}
	}
	if got, err := os.ReadFile(filepath.Join(npmRoot, "package.json")); err != nil || string(got) != "{\"name\":\"fixture-package\"}\n" { //nolint:gosec // fixture-owned host resource is the immutability assertion.
		t.Fatalf("host Pi npm resource changed: %q, %v", got, err)
	}
	if got := directoryEntryNames(t, hostAgentRoot); !slices.Equal(got, beforeAgentEntries) {
		t.Fatalf("host Pi directory entries changed: got %v want %v", got, beforeAgentEntries)
	}
	if got := directoryEntryNames(t, hostSessionRoot); !slices.Equal(got, beforeSessionEntries) {
		t.Fatalf("host Pi session entries changed: got %v want %v", got, beforeSessionEntries)
	}
}

func directoryEntryNames(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func TestSingleMergePiRuntimeCleansAfterCancellationAndTimeout(t *testing.T) {
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	if err := probeSingleMergeFilesystemConfinement(context.Background(), singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot,
	}, "/usr/bin/true"); err != nil {
		t.Skipf("OS confinement is unavailable for Pi RPC cleanup regression: %v", err)
	}
	provider := filepath.Join(t.TempDir(), "pi")
	const script = `#!/bin/sh
set -eu
IFS= read -r _
printf '{"id":"tao-readiness-state","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"fixture","id":"fixture"}}}\n'
IFS= read -r _
printf '{"id":"tao-readiness-models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"fixture","id":"fixture"}]}}\n'
IFS= read -r command
case "$command" in
  *'"type":"abort"'*) exit 0;;
  *'"type":"prompt"'*)
    printf '{"id":"tao-prompt","type":"response","command":"prompt","success":true}\n'
    while :; do sleep 1; done;;
  *) exit 44;;
esac
`
	if err := os.WriteFile(provider, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(provider, 0o700); err != nil { //nolint:gosec // executable mode is required for the fixture provider.
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		timeout time.Duration
		cancel  bool
		wantErr error
	}{
		{name: "cancellation", cancel: true, wantErr: context.Canceled},
		{name: "timeout", timeout: time.Second, wantErr: context.DeadlineExceeded},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("TAO_AGENT", "pi")
			t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
			var timeout *time.Duration
			if tt.timeout > 0 {
				timeout = &tt.timeout
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			var runtimeRoot string
			session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
				ProviderLookPath: func(string) (string, error) { return provider, nil }, Timeout: timeout,
				ProcessStarter: func(ctx context.Context, cwd, name string, args []string) (agent.Process, error) {
					// Capture Tao's runtime before launch: a timeout can stop the
					// provider before it has completed RPC readiness or written files.
					for _, arg := range args {
						if strings.HasPrefix(arg, "TMPDIR=") {
							runtimeRoot = strings.TrimPrefix(arg, "TMPDIR=")
							break
						}
					}
					process, err := agent.DefaultProcessStarter(ctx, cwd, name, args)
					if err == nil && tt.cancel {
						// Cancel only after a real process exists, not after an
						// elapsed-time guess about how quickly it will start.
						cancel()
					}
					return process, err
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = session.Resolve(ctx, BatchAgentSessionRequest{
				Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: integrationRoot, Prompt: "resolve",
				ProtectedGitObjectRoot: protectedRoot,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("session error = %v, want %v", err, tt.wantErr)
			}
			if !strings.HasPrefix(filepath.Base(runtimeRoot), "tao-merge-agent-runtime-") {
				t.Fatalf("launch did not identify private runtime: %q", runtimeRoot)
			}
			if _, err := os.Stat(runtimeRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("runtime survived %s: %v", tt.name, err)
			}
		})
	}
}

func TestSingleMergePiPreflightRejectsRPCStartupAfterVersionWouldSucceed(t *testing.T) {
	t.Setenv("TAO_AGENT", "pi")
	t.Setenv("PI_CODING_AGENT_DIR", t.TempDir())
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	if err := probeSingleMergeFilesystemConfinement(context.Background(), singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot,
	}, "/usr/bin/true"); err != nil {
		t.Skipf("OS confinement is unavailable for Pi RPC launch regression: %v", err)
	}
	provider := filepath.Join(t.TempDir(), "pi")
	const script = `#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  printf 'version succeeds\n'
  exit 0
fi
IFS= read -r _
printf '{"id":"tao-readiness-state","type":"response","command":"get_state","success":false,"error":"fixture-readiness-prefix:'
i=0
while [ "$i" -lt 2048 ]; do
  printf x
  i=$((i + 1))
done
printf ':fixture-readiness-tail"}\n'
`
	if err := os.WriteFile(provider, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(provider, 0o700); err != nil { //nolint:gosec // executable mode is required for the fixture provider.
		t.Fatal(err)
	}
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		ProviderLookPath: func(string) (string, error) { return provider, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	err = session.Preflight(context.Background(), BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: integrationRoot,
		ProtectedGitObjectRoot: protectedRoot,
	})
	if err == nil || !strings.Contains(err.Error(), "pi rpc readiness") || !strings.Contains(err.Error(), "fixture-readiness-prefix") {
		t.Fatalf("RPC startup failure = %v", err)
	}
	if len(err.Error()) > maxSingleMergeConfinementProbeOutputBytes || strings.Contains(err.Error(), "fixture-readiness-tail") {
		t.Fatalf("RPC startup diagnostic was not bounded: %d bytes: %v", len(err.Error()), err)
	}
}

func TestSingleMergeAgentSessionMissingConfinerDoesNotStartProvider(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	const unavailable = "protect provider filesystem boundary: bubblewrap is unavailable; install bwrap and run tao doctor"
	setConfinementExecutable(t, func() (string, error) { return "", errors.New(unavailable) })
	starts := 0
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		RuntimeEnv:       mergeTestRuntimeEnv(),
		ProviderLookPath: testProviderLookPath, ConfinementProbe: successfulConfinementProbe,
		ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
			starts++
			t.Error("missing confiner must not start the provider")
			return nil, errors.New("unexpected provider start")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	_, err = session.Resolve(context.Background(), BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: integrationRoot,
		ProtectedGitObjectRoot: protectedRoot,
	})
	if err == nil || !strings.Contains(err.Error(), unavailable) {
		t.Fatalf("missing-confiner resolve error = %v, want %q", err, unavailable)
	}
	if starts != 0 {
		t.Fatalf("missing confiner started %d provider processes", starts)
	}
}

func TestSingleMergeFilesystemConfinementExecutableDefaultDiscovery(t *testing.T) {
	path, err := singleMergeFilesystemConfinementExecutable()
	switch runtime.GOOS {
	case "darwin":
		if err != nil || path != "/usr/bin/sandbox-exec" {
			t.Fatalf("default discovery = %q, %v; want /usr/bin/sandbox-exec", path, err)
		}
	case "linux":
		if err != nil {
			const unavailable = "protect provider filesystem boundary: bubblewrap is unavailable; install bwrap and run tao doctor"
			if path != "" || err.Error() != unavailable {
				t.Fatalf("default discovery = %q, %v; want %q", path, err, unavailable)
			}
		} else if path != "/usr/bin/bwrap" && path != "/bin/bwrap" {
			t.Fatalf("default discovery = %q; want /usr/bin/bwrap or /bin/bwrap", path)
		}
	default:
		want := "protect provider filesystem boundary: confinement is unsupported on " + runtime.GOOS
		if path != "" || err == nil || err.Error() != want {
			t.Fatalf("default discovery = %q, %v; want %q", path, err, want)
		}
	}
}

func TestSingleMergeAgentSessionUnavailableConfinementDoesNotStartProvider(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	probeErr := errors.New("bubblewrap unavailable")
	starts := 0
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		RuntimeEnv:       mergeTestRuntimeEnv(),
		ProviderLookPath: func(name string) (string, error) { return "/installed/" + name, nil },
		ConfinementProbe: func() error { return probeErr },
		ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
			starts++
			return nil, errors.New("unexpected provider start")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	request := BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: integrationRoot,
		ProtectedGitObjectRoot: protectedRoot,
	}
	if err := session.Preflight(context.Background(), request); !errors.Is(err, probeErr) {
		t.Fatalf("preflight error = %v", err)
	}
	if _, err := session.Resolve(context.Background(), request); !errors.Is(err, probeErr) {
		t.Fatalf("resolve error = %v", err)
	}
	if starts != 0 {
		t.Fatalf("unavailable confinement started %d provider processes", starts)
	}
}

func TestSingleMergeAgentSessionFailsClosedWithoutProtectedFilesystemBoundary(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	starts := 0
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		RuntimeEnv: mergeTestRuntimeEnv(),
		ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
			starts++
			return nil, errors.New("unexpected provider start")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.Resolve(context.Background(), BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "no protected Git paths") {
		t.Fatalf("missing-boundary error = %v", err)
	}
	if starts != 0 {
		t.Fatalf("unsafe single-plan session started %d providers", starts)
	}
}

func TestSingleMergeAgentSessionConfinesProtectedObjectRootAtProcessBoundary(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	objectRoot := t.TempDir()
	integrationRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	if _, _, err := singleMergeFilesystemConfinementCommand(singleMergeFilesystemConfinement{
		protectedPaths: []string{objectRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}, runtimeRoot, "claude", nil); err != nil {
		t.Skipf("OS-enforced provider filesystem confinement unavailable: %v", err)
	}
	var got mergeFakeClaudeStart
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		RuntimeEnv:       mergeTestRuntimeEnv(),
		ProviderLookPath: testProviderLookPath, ConfinementProbe: successfulConfinementProbe,
		ProcessStarter: mergeFakeProcessStarter(t, &got, `{"type":"result","result":"done"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.Resolve(context.Background(), BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanResolution, Attempt: 1,
		IntegrationRoot: integrationRoot, Prompt: "resolve", CandidatePlanID: "plan-a",
		ProtectedGitObjectRoot: objectRoot,
	})
	if err != nil || result.Output != "done" {
		t.Fatalf("confined result/error = %#v / %v", result, err)
	}
	if got.name == "claude" || !strings.Contains(strings.Join(got.args, "\x00"), "claude") {
		t.Fatalf("provider was not launched through OS confinement: name=%q args=%q", got.name, got.args)
	}
	canonical, err := canonicalGitObjectRoot(objectRoot)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got.args, "\x00")
	if !strings.Contains(joined, canonical) || !strings.Contains(joined, integrationRoot) || !strings.Contains(joined, "TMPDIR=") {
		t.Fatalf("confinement omitted protected, integration, or runtime boundary: %q", got.args)
	}
}

func TestSingleMergeProcessBoundaryRejectsExistingExternalHardLink(t *testing.T) {
	root := t.TempDir()
	integration := filepath.Join(root, "integration")
	external := filepath.Join(root, "external.txt")
	protected := filepath.Join(root, "protected")
	runtimeRoot := filepath.Join(root, "runtime")
	for _, path := range []string{integration, protected, runtimeRoot, filepath.Join(runtimeRoot, "cache"), filepath.Join(runtimeRoot, "state")} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(external, []byte("external original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(integration, "tracked.txt")
	if err := os.Link(external, alias); err != nil {
		t.Skipf("filesystem does not support hard links: %v", err)
	}

	_, _, err := singleMergeFilesystemConfinementCommand(singleMergeFilesystemConfinement{
		protectedPaths: []string{protected}, integrationRoot: integration, allowEdits: true,
	}, runtimeRoot, "/bin/sh", []string{"-c", `printf compromised >"$1"`, "sh", alias})
	if err == nil || !strings.Contains(err.Error(), `multiply linked regular file "tracked.txt"`) {
		t.Fatalf("existing-hard-link confinement error = %v", err)
	}
	if contents, readErr := os.ReadFile(external); readErr != nil || string(contents) != "external original\n" { //nolint:gosec // fixture-owned external alias is the security assertion.
		t.Fatalf("rejected resolver changed external inode: %q, %v", contents, readErr)
	}
}

func TestSingleMergeProcessSandboxDeniesHostWritesByDefault(t *testing.T) {
	root := t.TempDir()
	integration := filepath.Join(root, "integration")
	metadata := filepath.Join(integration, ".git")
	linked := filepath.Join(root, "linked-checkout")
	external := filepath.Join(root, "unrelated-repository")
	taoDataHome := filepath.Join(root, "tao-data-home")
	t.Setenv("TAO_DATA_HOME", taoDataHome)
	runtimeRoot := filepath.Join(root, "provider-runtime")
	for _, path := range []string{integration, metadata, linked, external, taoDataHome, runtimeRoot} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{"cache", "state"} {
		if err := os.Mkdir(filepath.Join(runtimeRoot, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	originals := []string{
		filepath.Join(metadata, "config"), filepath.Join(linked, "README.md"),
		filepath.Join(external, "README.md"), filepath.Join(taoDataHome, "state.json"),
	}
	for _, path := range originals {
		if err := os.WriteFile(path, []byte("original\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// The canary defeats 2>/dev/null masking of device-write denial.
	const resolverScript = `set -u
: >/dev/null || exit 22
printf allowed >"$1/allowed.txt" || exit 11
printf runtime >"$TMPDIR/scratch" || exit 12
if printf overwritten >"$2/config" 2>/dev/null; then exit 13; fi
if printf overwritten >"$3/README.md" 2>/dev/null; then exit 14; fi
if printf overwritten >"$4/README.md" 2>/dev/null; then exit 15; fi
if printf overwritten >"$5/state.json" 2>/dev/null; then exit 16; fi
if printf created >"$2/new-ref" 2>/dev/null; then exit 17; fi
if printf created >"$3/new-file" 2>/dev/null; then exit 18; fi
if printf created >"$4/new-file" 2>/dev/null; then exit 19; fi
if printf created >"$5/new-plan" 2>/dev/null; then exit 20; fi
if [ "$6" = linux ] && ln "$4/README.md" "$1/external-hard-link" 2>/dev/null; then exit 21; fi`
	resolverPolicy := singleMergeFilesystemConfinement{
		protectedPaths: []string{metadata, linked}, integrationRoot: integration, allowEdits: true,
	}
	name, args, err := singleMergeFilesystemConfinementCommand(resolverPolicy, runtimeRoot, "/bin/sh", []string{
		"-c", resolverScript, "sh", integration, metadata, linked, external, taoDataHome, runtime.GOOS,
	})
	if err != nil {
		t.Skipf("OS-enforced provider filesystem confinement unavailable: %v", err)
	}
	if output, err := exec.Command(name, args...).CombinedOutput(); err != nil { //nolint:gosec // fixed test shell probes the generated confinement boundary.
		t.Skipf("OS-enforced provider filesystem confinement cannot start: %v: %s", err, output)
	}
	if contents, err := os.ReadFile(filepath.Join(integration, "allowed.txt")); err != nil || string(contents) != "allowed" { //nolint:gosec // fixture-owned integration path.
		t.Fatalf("resolver integration edit was denied: %q, %v", contents, err)
	}
	if contents, err := os.ReadFile(filepath.Join(runtimeRoot, "scratch")); err != nil || string(contents) != "runtime" { //nolint:gosec // fixture-owned bounded runtime path.
		t.Fatalf("bounded runtime write was denied: %q, %v", contents, err)
	}
	for _, path := range originals {
		if contents, err := os.ReadFile(path); err != nil || string(contents) != "original\n" { //nolint:gosec // fixture-owned denied path.
			t.Fatalf("denied file %s changed: %q, %v", path, contents, err)
		}
	}
	for _, path := range []string{
		filepath.Join(metadata, "new-ref"), filepath.Join(linked, "new-file"),
		filepath.Join(external, "new-file"), filepath.Join(taoDataHome, "new-plan"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("denied path was created: %s: %v", path, err)
		}
	}
	if runtime.GOOS == "linux" {
		if _, err := os.Lstat(filepath.Join(integration, "external-hard-link")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("resolver created a hard link across the writable mount boundary: %v", err)
		}
	}

	// The canary defeats 2>/dev/null masking of device-write denial.
	const reviewerScript = `set -u
: >/dev/null || exit 25
if printf forbidden >"$1/reviewer-edit" 2>/dev/null; then exit 21; fi
if printf overwritten >"$2/README.md" 2>/dev/null; then exit 22; fi
if printf overwritten >"$3/state.json" 2>/dev/null; then exit 23; fi
printf runtime >"$TMPDIR/reviewer-scratch" || exit 24`
	reviewerPolicy := singleMergeFilesystemConfinement{
		protectedPaths: []string{metadata, linked}, integrationRoot: integration,
	}
	name, args, err = singleMergeFilesystemConfinementCommand(reviewerPolicy, runtimeRoot, "/bin/sh", []string{
		"-c", reviewerScript, "sh", integration, external, taoDataHome,
	})
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(name, args...).CombinedOutput(); err != nil { //nolint:gosec // fixed test shell probes the generated confinement boundary.
		t.Fatalf("reviewer confinement failed: %v: %s", err, output)
	}
	if _, err := os.Lstat(filepath.Join(integration, "reviewer-edit")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reviewer mutated integration worktree: %v", err)
	}
	for _, path := range originals {
		if contents, err := os.ReadFile(path); err != nil || string(contents) != "original\n" { //nolint:gosec // fixture-owned denied path.
			t.Fatalf("reviewer changed denied file %s: %q, %v", path, contents, err)
		}
	}
}

func TestSingleMergeProcessSandboxPermitsReadOnlyGitAndDeviceWrites(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	root := t.TempDir()
	integration := filepath.Join(root, "integration")
	metadata := filepath.Join(integration, ".git")
	runtimeRoot := filepath.Join(root, "provider-runtime")
	for _, path := range []string{integration, runtimeRoot, filepath.Join(runtimeRoot, "cache"), filepath.Join(runtimeRoot, "state")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(integration, "README.md"), []byte("original\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "--local", "maintenance.auto", "false"},
		{"config", "--local", "gc.auto", "0"},
		{"add", "README.md"},
		{"-c", "user.name=Tao Test", "-c", "user.email=tao@example.com", "commit", "-q", "-m", "seed"},
	} {
		command := exec.Command(gitPath, args...) //nolint:gosec // fixed git fixture setup.
		command.Dir = integration
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v failed: %v: %s", args, err, output)
		}
	}
	before, err := os.ReadDir(metadata)
	if err != nil {
		t.Fatal(err)
	}

	// The reviewer boundary is the strictest projection: no integration writes
	// at all. Read-only Git and pseudo-device redirects must still work there,
	// because the independent integration reviewer cannot inspect the exact
	// diff without them.
	const reviewerScript = `set -u
cd "$1" || exit 31
git --version >/dev/null || exit 32
[ "$(git rev-parse --git-dir)" = .git ] || exit 33
git --no-pager diff --no-ext-diff --stat HEAD >/dev/null || exit 34
git --no-pager log --oneline -1 HEAD >/dev/null || exit 35
: >/dev/null || exit 36
echo probe >/dev/stderr || exit 37
if printf created >"$2/new-ref" 2>/dev/null; then exit 38; fi
if printf edited >"$1/README.md" 2>/dev/null; then exit 39; fi`
	policy := singleMergeFilesystemConfinement{protectedPaths: []string{metadata}, integrationRoot: integration}
	name, args, err := singleMergeFilesystemConfinementCommand(policy, runtimeRoot, "/bin/sh", []string{"-c", reviewerScript, "sh", integration, metadata})
	if err != nil {
		t.Skipf("OS-enforced provider filesystem confinement unavailable: %v", err)
	}
	command := exec.Command(name, args...) //nolint:gosec // fixed test shell probes the generated confinement boundary.
	command.Stdin = nil
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("read-only git inside provider confinement failed: %v: %s", err, output)
	}
	if contents, err := os.ReadFile(filepath.Join(integration, "README.md")); err != nil || string(contents) != "original\n" { //nolint:gosec // fixture-owned denied path.
		t.Fatalf("reviewer mutated integration worktree: %q, %v", contents, err)
	}
	after, err := os.ReadDir(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("protected git metadata changed: before %d entries, after %d", len(before), len(after))
	}
}

func TestSingleMergeAgentSessionStartsFreshProviderForResolverAndReviewer(t *testing.T) {
	fakeConfinementExecutable(t)
	t.Setenv("TAO_AGENT", "claude")
	starts := 0
	var got mergeFakeClaudeStart
	starter := mergeFakeProcessStarter(t, &got, `{"type":"result","result":"done"}`)
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		RuntimeEnv:       mergeTestRuntimeEnv(),
		ProviderLookPath: testProviderLookPath, ConfinementProbe: successfulConfinementProbe,
		ProcessStarter: func(ctx context.Context, cwd, name string, args []string) (agent.Process, error) {
			starts++
			return starter(ctx, cwd, name, args)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	for _, operation := range []BatchAgentOperation{BatchAgentOperationSinglePlanResolution, BatchAgentOperationSinglePlanReview} {
		result, resolveErr := session.Resolve(context.Background(), BatchAgentSessionRequest{
			Operation: operation, Attempt: 1, IntegrationRoot: integrationRoot, Prompt: string(operation), CandidatePlanID: "plan-a",
			ProtectedGitObjectRoot: protectedRoot,
		})
		if resolveErr != nil || result.Output != "done" {
			t.Fatalf("%s result/error = %#v / %v", operation, result, resolveErr)
		}
	}
	if starts != 2 {
		t.Fatalf("single-plan operations started %d providers, want one fresh provider each", starts)
	}
}

func TestFreshSingleMergeAgentSessionDefersConfigurationAndStartsEachOperationOnce(t *testing.T) {
	fakeConfinementExecutable(t)
	t.Setenv("TAO_AGENT", "invalid")
	deferred := NewFreshSingleMergeAgentSession(SingleMergeAgentSessionConfig{RuntimeEnv: mergeTestRuntimeEnv()})
	if _, err := deferred.Resolve(context.Background(), BatchAgentSessionRequest{Operation: BatchAgentOperationSinglePlanResolution}); err == nil || !strings.Contains(err.Error(), "unsupported agent") {
		t.Fatalf("deferred runtime error = %v", err)
	}

	t.Setenv("TAO_AGENT", "claude")
	starts := 0
	var progress bytes.Buffer
	var got mergeFakeClaudeStart
	starter := mergeFakeProcessStarter(t, &got, `{"type":"result","result":"done"}`)
	fresh := NewFreshSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		RuntimeEnv: mergeTestRuntimeEnv(),
		Log:        &progress, ProviderLookPath: testProviderLookPath, ConfinementProbe: successfulConfinementProbe,
		ProcessStarter: func(ctx context.Context, cwd, name string, args []string) (agent.Process, error) {
			starts++
			return starter(ctx, cwd, name, args)
		},
	})
	integrationRoot, protectedRoot := singleMergeAgentTestBoundary(t)
	for _, operation := range []BatchAgentOperation{BatchAgentOperationSinglePlanResolution, BatchAgentOperationSinglePlanReview} {
		result, err := fresh.Resolve(context.Background(), BatchAgentSessionRequest{
			Operation: operation, Attempt: 1, IntegrationRoot: integrationRoot, Prompt: string(operation), CandidatePlanID: "plan-a",
			ProtectedGitObjectRoot: protectedRoot,
		})
		if err != nil || result.Output != "done" {
			t.Fatalf("%s result/error = %#v / %v", operation, result, err)
		}
	}
	if starts != 2 {
		t.Fatalf("fresh single-plan agent started %d processes, want 2", starts)
	}
	for _, want := range []string{"Automatic squash conflict resolution started (attempt 1 of 1).", "Independent exact-integration review started in a fresh session (attempt 1 of 1)."} {
		if !strings.Contains(progress.String(), want) {
			t.Fatalf("progress missing %q: %q", want, progress.String())
		}
	}
}

func TestSingleMergeAgentMetricsEventUsesGenericPlanTelemetry(t *testing.T) {
	timestamp := time.Date(2026, 9, 2, 20, 0, 0, 0, time.UTC)
	if event := SingleMergeAgentMetricsEvent(BatchAgentSessionRequest{}, BatchAgentSessionResult{}, nil, timestamp); event != nil {
		t.Fatalf("unusable metrics produced event %#v", event)
	}
	request := BatchAgentSessionRequest{Operation: BatchAgentOperationSinglePlanReview, CandidatePlanID: "plan-a"}
	result := BatchAgentSessionResult{ReasoningEffort: "high", Provider: agentsession.Result{
		Invoked: true, AgentLabel: "claude", MetricsUsable: true,
		Metrics: &agent.Metrics{SessionID: "session-a", ProviderID: "anthropic", ModelID: "model-a", OutputTokens: 17, ToolCalls: 2},
	}}
	event := SingleMergeAgentMetricsEvent(request, result, errors.New("provider failed"), timestamp)
	if event == nil || event.Type != plan.EventTypeAgentMetrics || event.PlanID != "plan-a" || event.Agent != "claude" || event.Timestamp != timestamp {
		t.Fatalf("generic metrics event = %#v", event)
	}
	if event.Message != "Captured independent integration reviewer agent metrics" || event.Metrics == nil || event.Metrics.ReasoningEffort != "high" || event.Metrics.SessionID != "session-a" || event.Metrics.OutputTokens != 17 || event.Metrics.ToolCalls != 2 || event.Metrics.Status != "failed" || event.Metrics.Result != "failed" {
		t.Fatalf("projected metrics = %#v", event)
	}
}

func TestBatchAgentSessionRendersMetricsWarningAsReadableProgress(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	var progress bytes.Buffer
	var got mergeFakeClaudeStart
	session, err := NewBatchAgentSession(BatchAgentSessionConfig{
		RuntimeEnv:     mergeTestRuntimeEnv(),
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

func TestDeferredMergeSessionsRequireCapturedSettingsBeforeLaunch(t *testing.T) {
	for _, key := range []string{runtimeconfig.EnvAgent, runtimeconfig.EnvSessionTimeout, runtimeconfig.EnvSkipPermissions} {
		t.Run(key, func(t *testing.T) {
			starts := 0
			config := BatchAgentSessionConfig{
				RuntimeEnv: mergeSnapshotWith(map[string]string{key: "invalid"}),
				ProcessStarter: func(context.Context, string, string, []string) (agent.Process, error) {
					starts++
					return nil, errors.New("unexpected launch")
				},
			}
			single := NewFreshSingleMergeAgentSession(config)
			batch := NewDeferredBatchAgentSession(config)
			proposal, err := NewMergeProposalGenerator(config)
			if err != nil {
				t.Fatalf("eager proposal validation: %v", err)
			}
			request := BatchAgentSessionRequest{Operation: BatchAgentOperationSinglePlanResolution}
			for _, check := range []func() error{
				func() error { return single.Preflight(context.Background(), request) },
				func() error { _, err := single.Resolve(context.Background(), request); return err },
				func() error { _, err := batch.Resolve(context.Background(), request); return err },
				func() error {
					_, err := proposal.GenerateMergeProposal(context.Background(), mergeProposalContext())
					return err
				},
			} {
				if err := check(); err == nil || !strings.Contains(err.Error(), key) {
					t.Fatalf("required setting %s: %v", key, err)
				}
			}
			if starts != 0 {
				t.Fatalf("invalid configuration launched %d providers", starts)
			}
		})
	}
}

func TestMergeSessionSharedProjection(t *testing.T) {
	for _, single := range []bool{false, true} {
		t.Run(fmt.Sprintf("single=%t", single), func(t *testing.T) {
			calls := 0
			config := BatchAgentSessionConfig{
				RuntimeEnv: mergeSnapshotWith(map[string]string{runtimeconfig.EnvAgent: "invalid"}),
				ResolveOptions: func() (runtimeconfig.CommandOptions, error) {
					calls++
					return runtimeconfig.CommandOptions{RunOptions: runtimeconfig.ResolvedRunOptions{
						Agent: runtimeconfig.AgentClaude, Models: runtimeconfig.ModelSelection{Base: "shared"},
					}, SkipPermissions: true}, nil
				},
			}
			session, err := newBatchAgentSession(config, single)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || session.providerToolName != "claude" || session.models.Base != "shared" {
				t.Fatalf("calls=%d session=%+v", calls, session)
			}
			want := errors.New("applicable option rejected")
			config.ResolveOptions = func() (runtimeconfig.CommandOptions, error) { return runtimeconfig.CommandOptions{}, want }
			if _, err := newBatchAgentSession(config, single); !errors.Is(err, want) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestDeferredBatchSessionUsesStableSnapshot(t *testing.T) {
	var got mergeFakeClaudeStart
	session := NewDeferredBatchAgentSession(BatchAgentSessionConfig{
		RuntimeEnv: mergeSnapshotWith(map[string]string{
			runtimeconfig.EnvAgent: "claude", runtimeconfig.EnvSkipPermissions: "true", runtimeconfig.EnvSessionTimeout: "0",
		}),
		ProcessStarter: mergeFakeProcessStarter(t, &got, `{"type":"result","result":"resolved"}`),
	})
	for _, key := range []string{runtimeconfig.EnvAgent, runtimeconfig.EnvSessionTimeout, runtimeconfig.EnvSkipPermissions} {
		t.Setenv(key, "invalid")
	}
	result, err := session.Resolve(context.Background(), BatchAgentSessionRequest{IntegrationRoot: "/integration", Prompt: "review"})
	if err != nil || result.Output != "resolved" || got.name != "claude" || !strings.Contains(strings.Join(got.args, " "), "bypassPermissions") {
		t.Fatalf("captured session: result=%#v start=%#v err=%v", result, got, err)
	}
}

func TestMergeAgentOmittedSnapshotUsesBuiltins(t *testing.T) {
	t.Setenv(runtimeconfig.EnvAgent, "invalid")
	t.Setenv(runtimeconfig.EnvSessionTimeout, "invalid")
	session, err := NewBatchAgentSession(BatchAgentSessionConfig{})
	if err != nil {
		t.Fatalf("omitted snapshot read ambient settings: %v", err)
	}
	if session.providerToolName != "pi" {
		t.Fatalf("provider = %q, want built-in pi", session.providerToolName)
	}
}

func TestMergeProposalGeneratorDefersRuntimeConfigurationUntilGeneration(t *testing.T) {
	t.Setenv("TAO_AGENT", "invalid")
	starts := 0
	generator, err := NewMergeProposalGenerator(MergeProposalGeneratorConfig{
		RuntimeEnv: mergeTestRuntimeEnv(),
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
		RuntimeEnv: mergeTestRuntimeEnv(),
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

func TestBatchMergeProposalGeneratorUsesTrustedTransactionIdentity(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	var got mergeFakeClaudeStart
	store := &recordingBatchAgentEvents{}
	planEvents := 0
	generator, err := NewMergeProposalGenerator(MergeProposalGeneratorConfig{
		RuntimeEnv:     mergeTestRuntimeEnv(),
		ProcessStarter: mergeFakeProcessStarter(t, &got, `{"type":"result","result":"{\"type\":\"fix\",\"scope\":\"merge\",\"summary\":\"preserve batch identity\",\"what\":\"Generate an exact proposal.\",\"why\":\"Support legacy approvals.\"}"}`),
		EventAppender:  store,
		Observe: func(request BatchAgentSessionRequest, result BatchAgentSessionResult, err error) {
			if event := SingleMergeAgentMetricsEvent(request, result, err, time.Now()); event != nil {
				planEvents++
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withBatchProposalSessionIdentity(context.Background(), "batch-a", 3, "plan-a")
	if _, err := generator.GenerateMergeProposal(ctx, mergeProposalContext()); err != nil {
		t.Fatal(err)
	}
	if planEvents != 0 {
		t.Fatal("batch proposal leaked into plan channel")
	}
	count := 0
	for _, event := range store.events {
		if event.Type != BatchAgentEventTypeMetrics {
			continue
		}
		count++
		if event.Operation != BatchAgentOperationProposalGeneration || event.BatchID != "batch-a" || event.PlanID != "plan-a" || event.Attempt != 3 || event.Metrics.Availability != agentmetrics.Unavailable {
			t.Fatalf("batch proposal attribution = %#v", event)
		}
	}
	if count != 1 {
		t.Fatalf("proposal metrics = %d", count)
	}
}

func TestMergeSessionTelemetryCoverage(t *testing.T) {
	operations := []struct {
		op    BatchAgentOperation
		batch bool
		role  plan.AgentRole
	}{
		{BatchAgentOperationSinglePlanResolution, false, plan.AgentRoleMerge},
		{BatchAgentOperationSinglePlanReview, false, plan.AgentRoleReview},
		{BatchAgentOperationProposalGeneration, false, plan.AgentRoleMerge},
		{BatchAgentOperationCandidateResolution, true, ""},
		{BatchAgentOperationAggregateReview, true, ""},
		{BatchAgentOperationAggregateRework, true, ""},
		{BatchAgentOperationProposalGeneration, true, ""},
	}
	for _, provider := range []string{"pi", "claude"} {
		for _, operation := range operations {
			for _, availability := range []agentmetrics.Availability{agentmetrics.Reported, agentmetrics.Partial, agentmetrics.Unavailable} {
				for _, outcome := range []string{BatchAgentOutcomeCompleted, BatchAgentOutcomeFailed, BatchAgentOutcomeTimedOut} {
					t.Run(fmt.Sprintf("%s/%s/batch=%t/%s/%s", provider, operation.op, operation.batch, availability, outcome), func(t *testing.T) {
						var providerErr error
						if outcome == BatchAgentOutcomeFailed {
							providerErr = errors.New("provider failure")
						}
						if outcome == BatchAgentOutcomeTimedOut {
							providerErr = &agent.SessionTimeoutError{Timeout: time.Minute}
						}
						result := agentsession.Result{Invoked: true, AgentLabel: provider, FinalText: "preserved", MetricsAvailability: availability}
						if availability != agentmetrics.Unavailable {
							result.Metrics = &agent.Metrics{InputTokensPresent: true}
							if availability == agentmetrics.Reported {
								result.Metrics.OutputTokensPresent = true
								result.Metrics.TotalTokensPresent = true
								result.Metrics.CostPresent = true
							}
						}
						store := &recordingBatchAgentEvents{}
						var planEvents []plan.Event
						calls := 0
						session := BatchAgentSession{
							run: func(context.Context, agentsession.Request) (agentsession.Result, error) {
								calls++
								return result, providerErr
							},
							eventAppender: store, now: time.Now,
							observe: func(request BatchAgentSessionRequest, result BatchAgentSessionResult, err error) {
								if event := SingleMergeAgentMetricsEvent(request, result, err, time.Now()); event != nil {
									planEvents = append(planEvents, *event)
								}
							},
						}
						request := BatchAgentSessionRequest{Operation: operation.op, Attempt: 2, CandidatePlanID: "plan-a"}
						if operation.batch {
							request.BatchID = "batch-a"
						}
						got, err := session.Resolve(context.Background(), request)
						if calls != 1 || got.Output != "preserved" || !errors.Is(err, providerErr) {
							t.Fatalf("session changed: %d %#v %v", calls, got, err)
						}
						if operation.batch {
							if len(planEvents) != 0 {
								t.Fatalf("batch leaked plan events: %#v", planEvents)
							}
							count := 0
							for _, event := range store.events {
								if err := event.validate(); err != nil {
									t.Fatal(err)
								}
								if event.Type != BatchAgentEventTypeMetrics {
									continue
								}
								count++
								m := event.Metrics
								if event.Operation != operation.op || event.Outcome != outcome || event.Attempt != 2 || event.PlanID != "plan-a" || m.Availability != availability || m.InputTokens != 0 || m.InputTokensPresent != (availability != agentmetrics.Unavailable) || m.CostPresent != (availability == agentmetrics.Reported) {
									t.Fatalf("batch measurement lost: %#v / %#v", event, m)
								}
							}
							if count != 1 {
								t.Fatalf("metrics count = %d", count)
							}
						} else {
							if len(store.events) != 0 || len(planEvents) != 1 {
								t.Fatalf("wrong destinations: batch=%#v plan=%#v", store.events, planEvents)
							}
							event := planEvents[0] // This collection contains only the owned metrics event.
							m := event.Metrics
							if event.PlanID != "plan-a" || event.SliceID != "" || m.Role != operation.role || string(m.Availability) != string(availability) || m.InputTokens != 0 || m.InputTokensPresent != (availability != agentmetrics.Unavailable) || m.CostPresent != (availability == agentmetrics.Reported) || (m.Status == "failed") != (providerErr != nil) {
								t.Fatalf("plan measurement lost: %#v / %#v", event, m)
							}
						}
					})
				}
			}
		}
	}
}

func TestMergeTelemetryDoesNotRecordUninvokedSessions(t *testing.T) {
	store := &recordingBatchAgentEvents{}
	session := BatchAgentSession{eventAppender: store, now: time.Now}
	request := BatchAgentSessionRequest{BatchID: "batch-a", Operation: BatchAgentOperationProposalGeneration, CandidatePlanID: "plan-a", Attempt: 1}
	session.recordTelemetry(request, agentsession.Result{AgentLabel: "pi"}, errors.New("preflight failed"))
	request.BatchID = ""
	if event := SingleMergeAgentMetricsEvent(request, BatchAgentSessionResult{}, nil, time.Now()); event != nil || len(store.events) != 0 {
		t.Fatalf("fabricated usage: %#v / %#v", event, store.events)
	}
}

func singleMergeAgentTestBoundary(t *testing.T) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git unavailable: %v", err)
	}
	root := t.TempDir()
	realGitOutput(t, root, "init", "-q", "-b", "main")
	return root, t.TempDir()
}

func mergeProposalContext() commitcontract.MergeProposalContext {
	return commitcontract.MergeProposalContext{
		RepoRoot: "/repo", PlanID: "plan-a", DefaultBranch: "main", DefaultParent: "parent123",
		MergeBase: "base123", SourceBranch: "tao/plan-a", SourceHead: "head456", Diff: "diff --git a/a.go b/a.go\n+change\n",
	}
}

type cleanupTestProcess struct {
	waitStarted chan struct{}
	stopped     chan struct{}
	stopOnce    sync.Once
	startOnce   sync.Once
	mu          sync.Mutex
	waitCalls   int
	killCalls   int
	waitErr     error
}

func newCleanupTestProcess(waitErr error) *cleanupTestProcess {
	return &cleanupTestProcess{waitStarted: make(chan struct{}), stopped: make(chan struct{}), waitErr: waitErr}
}

func (p *cleanupTestProcess) Stdin() io.WriteCloser {
	return cleanupTestWriteCloser{Writer: io.Discard}
}
func (p *cleanupTestProcess) Stdout() io.Reader { return strings.NewReader("") }
func (p *cleanupTestProcess) Stderr() io.Reader { return strings.NewReader("") }
func (p *cleanupTestProcess) Wait() error {
	p.mu.Lock()
	p.waitCalls++
	p.mu.Unlock()
	p.startOnce.Do(func() { close(p.waitStarted) })
	<-p.stopped
	return p.waitErr
}
func (p *cleanupTestProcess) Kill() error {
	p.mu.Lock()
	p.killCalls++
	p.mu.Unlock()
	p.complete()
	return nil
}
func (p *cleanupTestProcess) complete() { p.stopOnce.Do(func() { close(p.stopped) }) }
func (p *cleanupTestProcess) calls() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.waitCalls, p.killCalls
}

type cleanupTestWriteCloser struct{ io.Writer }

func (cleanupTestWriteCloser) Close() error { return nil }

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
