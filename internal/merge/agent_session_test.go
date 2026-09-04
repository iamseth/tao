package merge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/iamseth/tao/internal/agentsession"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
)

func testProviderLookPath(name string) (string, error) { return name, nil }
func successfulConfinementProbe() error                { return nil }

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

func TestSingleMergeAgentSessionExposesMetricsWithoutBatchPersistence(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	var got mergeFakeClaudeStart
	batchEvents := &recordingBatchAgentEvents{}
	var metrics agent.Metrics
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
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
	if err := probeSingleMergeFilesystemConfinement(context.Background(), singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}, "/usr/bin/true"); err != nil {
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
	if err := probeSingleMergeFilesystemConfinement(context.Background(), singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}, provider); err != nil {
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
	const script = `#!/bin/sh
set -eu
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
    printf '%s' "$TMPDIR" >"$PWD/runtime-root"
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
			session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
				ProviderLookPath: func(string) (string, error) { return provider, nil }, Timeout: timeout,
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if tt.cancel {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				t.Cleanup(cancel)
				time.AfterFunc(time.Second, cancel)
			}
			_ = os.Remove(filepath.Join(integrationRoot, "runtime-root"))
			_, err = session.Resolve(ctx, BatchAgentSessionRequest{
				Operation: BatchAgentOperationSinglePlanResolution, IntegrationRoot: integrationRoot, Prompt: "resolve",
				ProtectedGitObjectRoot: protectedRoot,
			})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("session error = %v, want %v", err, tt.wantErr)
			}
			runtimeBytes, readErr := os.ReadFile(filepath.Join(integrationRoot, "runtime-root")) //nolint:gosec // fixture-owned resolver output identifies its runtime.
			if readErr != nil {
				t.Fatal(readErr)
			}
			runtimeRoot := string(runtimeBytes)
			if !strings.HasPrefix(filepath.Base(runtimeRoot), "tao-merge-agent-runtime-") {
				t.Fatalf("fixture did not identify private runtime: %q", runtimeRoot)
			}
			if _, err := os.Stat(runtimeRoot); !errors.Is(err, os.ErrNotExist) { //nolint:gosec // sandboxed fixture emits only its Tao-provided TMPDIR.
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

func TestSingleMergeAgentSessionUnavailableConfinementDoesNotStartProvider(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	probeErr := errors.New("bubblewrap unavailable")
	starts := 0
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
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

	const resolverScript = `set -u
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

	const reviewerScript = `set -u
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

func TestSingleMergeAgentSessionStartsFreshProviderForResolverAndReviewer(t *testing.T) {
	t.Setenv("TAO_AGENT", "claude")
	starts := 0
	var got mergeFakeClaudeStart
	starter := mergeFakeProcessStarter(t, &got, `{"type":"result","result":"done"}`)
	session, err := NewSingleMergeAgentSession(SingleMergeAgentSessionConfig{
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
	t.Setenv("TAO_AGENT", "invalid")
	deferred := NewFreshSingleMergeAgentSession(SingleMergeAgentSessionConfig{})
	if _, err := deferred.Resolve(context.Background(), BatchAgentSessionRequest{Operation: BatchAgentOperationSinglePlanResolution}); err == nil || !strings.Contains(err.Error(), "unsupported agent") {
		t.Fatalf("deferred runtime error = %v", err)
	}

	t.Setenv("TAO_AGENT", "claude")
	starts := 0
	var progress bytes.Buffer
	var got mergeFakeClaudeStart
	starter := mergeFakeProcessStarter(t, &got, `{"type":"result","result":"done"}`)
	fresh := NewFreshSingleMergeAgentSession(SingleMergeAgentSessionConfig{
		Log: &progress, ProviderLookPath: testProviderLookPath, ConfinementProbe: successfulConfinementProbe,
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
	result := BatchAgentSessionResult{Provider: agentsession.Result{
		AgentLabel: "claude", MetricsUsable: true,
		Metrics: &agent.Metrics{SessionID: "session-a", ProviderID: "anthropic", ModelID: "model-a", OutputTokens: 17, ToolCalls: 2},
	}}
	event := SingleMergeAgentMetricsEvent(request, result, errors.New("provider failed"), timestamp)
	if event == nil || event.Type != plan.EventTypeAgentMetrics || event.PlanID != "plan-a" || event.Agent != "claude" || event.Timestamp != timestamp {
		t.Fatalf("generic metrics event = %#v", event)
	}
	if event.Message != "Captured independent integration reviewer agent metrics" || event.Metrics == nil || event.Metrics.SessionID != "session-a" || event.Metrics.OutputTokens != 17 || event.Metrics.ToolCalls != 2 || event.Metrics.Status != "failed" || event.Metrics.Result != "failed" {
		t.Fatalf("projected metrics = %#v", event)
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

func singleMergeAgentTestBoundary(t *testing.T) (string, string) {
	t.Helper()
	return t.TempDir(), t.TempDir()
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
