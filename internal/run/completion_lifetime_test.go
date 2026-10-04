package run

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agent/process"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
)

func clearSliceCompletionOwnerEnv(t *testing.T) {
	t.Helper()
	t.Setenv(sliceCompletionOwnerEnv, "")
	if err := os.Unsetenv(sliceCompletionOwnerEnv); err != nil {
		t.Fatal(err)
	}
}

func TestClearSliceCompletionOwnerEnv(t *testing.T) {
	for _, initial := range []struct {
		name  string
		value string
		set   bool
	}{
		{name: "absent"},
		{name: "empty", set: true},
		{name: "nonempty", value: "inherited-owner", set: true},
	} {
		t.Run(initial.name, func(t *testing.T) {
			t.Setenv(sliceCompletionOwnerEnv, initial.value)
			if !initial.set {
				if err := os.Unsetenv(sliceCompletionOwnerEnv); err != nil {
					t.Fatal(err)
				}
			}
			t.Run("isolated", func(t *testing.T) {
				clearSliceCompletionOwnerEnv(t)
				if value, set := os.LookupEnv(sliceCompletionOwnerEnv); set || value != "" {
					t.Fatalf("isolated owner = %q, %t", value, set)
				}
				// Isolation must not disable subsequent intentional managed checks.
				t.Setenv(sliceCompletionOwnerEnv, "missing-owner")
				if _, err := BindSliceCompletionLifetime(context.Background(), t.TempDir(), "001"); err == nil || !strings.Contains(err.Error(), "managed invocation missing or replaced") {
					t.Fatalf("explicit invalid owner check = %v", err)
				}
			})
			if value, set := os.LookupEnv(sliceCompletionOwnerEnv); value != initial.value || set != initial.set {
				t.Fatalf("restored owner = %q, %t; want %q, %t", value, set, initial.value, initial.set)
			}
		})
	}
}

func TestVerifiedCompletionFixtureClearsInheritedOwner(t *testing.T) {
	t.Setenv(sliceCompletionOwnerEnv, "inherited-owner")
	t.Run("fixture", func(t *testing.T) {
		request := verifiedCompletionFixture(t, CommitPolicyNone, "true")
		if value, set := os.LookupEnv(sliceCompletionOwnerEnv); set || value != "" {
			t.Fatalf("fixture owner = %q, %t", value, set)
		}
		t.Setenv(sliceCompletionOwnerEnv, "missing-owner")
		if err := (SliceCompletionService{}).CompleteVerified(context.Background(), request); err == nil || !strings.Contains(err.Error(), "managed invocation missing or replaced") {
			t.Fatalf("fixture explicit owner check = %v", err)
		}
	})
	if value, set := os.LookupEnv(sliceCompletionOwnerEnv); !set || value != "inherited-owner" {
		t.Fatalf("fixture did not restore inherited owner: %q, %t", value, set)
	}
}

func TestWrapUpPreservesCompletionDeadline(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	root := t.TempDir()
	detail := runPathSessionDetail(t, root, plan.StatusInProgress, []string{"001-a"}, nil, plan.StatusInProgress)
	calls := 0
	runtime := agentRuntimeFunc(func(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
		calls++
		before, err := readCompletionOwner(detail.Dir)
		if err != nil {
			t.Fatal(err)
		}
		deadline, _ := ctx.Deadline()
		select {
		case notice := <-session.WarningMessages:
			for _, required := range []string{"slice-complete", "--resume-note-file", "pre-intent", "Do not weaken verification", "existing completion intent"} {
				if !strings.Contains(notice, required) {
					t.Fatalf("missing %q in notice", required)
				}
			}
		case <-ctx.Done():
			t.Fatal("warning missing")
		}
		after, err := readCompletionOwner(detail.Dir)
		if err != nil || before != after || !after.Deadline.Equal(deadline) {
			t.Fatalf("warning changed completion owner: %v", err)
		}
		// Simulate a tool/gate that cannot wrap up. The warning cannot extend its budget.
		<-ctx.Done()
		select {
		case <-session.WarningMessages:
			t.Fatal("duplicate warning")
		default:
		}
		return agent.SessionResult{}, ctx.Err()
	})
	repository := plan.NewFileRepository("")
	var events []plan.Event
	runner := newAgentSessionRunner(agentSessionRunnerConfig{
		runtimeEnv:     runEnvSnapshot(map[string]string{"TAO_SESSION_WARN_PERCENT": "10"}),
		sessionTimeout: 500 * time.Millisecond,
		descriptor:     agent.Descriptor{Label: "fake", NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }},
		logAppender:    repository, eventAppender: eventAppenderFunc(func(_ string, event plan.Event) error { events = append(events, event); return nil }),
	})
	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: detail.Dir, RepoRoot: root, Metrics: &AgentSessionMetricsRequest{Role: plan.AgentRoleExecution, SliceID: "001-a"}})
	var timeoutErr *agent.SessionTimeoutError
	if !errors.As(err, &timeoutErr) || timeoutErr.Timeout != 500*time.Millisecond || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	found := false
	for _, event := range events {
		if event.Type == plan.EventTypeSessionTimeout {
			found = true
			if event.SliceID != "001-a" {
				t.Fatal(event)
			}
		}
		if event.Type == plan.EventTypeSliceCompleted {
			t.Fatal("warning authorized completion")
		}
	}
	if !found {
		t.Fatal("timeout event missing")
	}
}

func TestSliceCompletionLifetimeActive(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	dir := t.TempDir()
	if sliceCompletionActive(dir) {
		t.Fatal("missing active file reported live")
	}
	direct, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = direct.Close() }()
	if sliceCompletionActive(dir) {
		t.Fatal("unmanaged lifetime reported live")
	}
	if _, err := os.Stat(filepath.Join(dir, completionActiveName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unmanaged lifetime or probe created active file: %v", err)
	}
	_, closeOwner, err := startSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeOwner() }()
	owner, err := readCompletionOwner(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(sliceCompletionOwnerEnv, owner.Token)
	for range 2 {
		first, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = first.Close() }()
		if !sliceCompletionActive(dir) {
			t.Fatal("bound lifetime not live")
		}
		second, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = second.Close() }()
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		if !sliceCompletionActive(dir) {
			t.Fatal("closing first lifetime released second lock")
		}
		if err := second.Close(); err != nil {
			t.Fatal(err)
		}
		if sliceCompletionActive(dir) {
			t.Fatal("closed lifetimes reported live")
		}
		if _, err := os.Stat(filepath.Join(dir, completionActiveName)); err != nil {
			t.Fatalf("active inode removed: %v", err)
		}
	}
}

func TestSliceCompletionLifetimeActiveChildDeath(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	dir := t.TempDir()
	owner := startCompletionHelper(t, dir, "owner", "")
	child := startCompletionHelper(t, dir, "active", owner.line(t))
	if got := child.line(t); got != "bound" {
		t.Fatal(got)
	}
	if !sliceCompletionActive(dir) {
		t.Fatal("child not live")
	}
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.cmd.Wait()
	if sliceCompletionActive(dir) {
		t.Fatal("killed child retained active lock")
	}
}

func TestSliceCompletionLifetimeDirect(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	guard, err := BindSliceCompletionLifetime(context.Background(), t.TempDir(), "001")
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.Check(); err != nil {
		t.Fatal(err)
	}
	if _, ok := guard.Context().Deadline(); ok {
		t.Fatal("standalone acquired a session budget")
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if guard.Check() == nil {
		t.Fatal("closed guard admitted mutation")
	}
	helper := startCompletionHelper(t, t.TempDir(), "direct", "")
	if got := helper.line(t); got != "direct admitted" {
		t.Fatal(got)
	}
	if err := helper.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestSliceCompletionLifetimeManaged(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, closeOwner, err := startSliceCompletionLifetime(ctx, dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeOwner() }()
	owner, err := readCompletionOwner(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(sliceCompletionOwnerEnv, owner.Token)
	guard, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Close() }()
	if err := guard.Check(); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-guard.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("owner cancellation did not cross lifetime boundary")
	}
	if guard.Check() == nil {
		t.Fatal("ended invocation admitted mutation")
	}
}

// This test executable stands in for both a driver and a nested CLI. Neither
// child shares Go contexts or descriptors with the other; only the lease token
// crosses the provider environment boundary.
func TestCompletionLifetimeHelperProcess(t *testing.T) {
	mode := os.Getenv("TAO_TEST_COMPLETION_HELPER")
	if mode == "" {
		return
	}
	dir := os.Getenv("TAO_TEST_COMPLETION_DIR")
	switch mode {
	case "direct":
		guard, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
		if err != nil {
			t.Fatal(err)
		}
		if err := guard.Check(); err != nil {
			t.Fatal(err)
		}
		if err := guard.Close(); err != nil {
			t.Fatal(err)
		}
		fmt.Println("direct admitted")
	case "provider-pi", "provider-claude":
		completionFakeProvider(t, dir, mode)
		os.Exit(0)
	case "verified", "verified-gate", "verified-boundary":
		completionVerifiedChild(t, dir, mode)
	case "owner", "timeout-owner", "verified-owner", "verified-timeout-owner":
		sliceID := "001"
		if strings.HasPrefix(mode, "verified-") {
			sliceID = "001-a"
			lock, err := plan.AcquireRunLock(dir, "plan-a", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = lock.Release() }()
		}
		ctx := context.Background()
		if mode == "timeout-owner" || mode == "verified-timeout-owner" {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
		}
		_, closeOwner, err := startSliceCompletionLifetime(ctx, dir, sliceID)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = closeOwner() }()
		owner, err := readCompletionOwner(dir)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(owner.Token)
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			switch scanner.Text() {
			case "end":
				if err := closeOwner(); err != nil {
					t.Fatal(err)
				}
				fmt.Println("ended")
			case "replace":
				_, closeNext, err := startSliceCompletionLifetime(context.Background(), dir, sliceID)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = closeNext() }()
				fmt.Println("replaced")
			case "quit":
				return
			}
		}
	case "active":
		guard, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = guard.Close() }()
		fmt.Println("bound")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	case "nested", "command-timeout", "grace-command-timeout":
		guard, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = guard.Close() }()
		// A shell's nested descendant owns the work and inherits output pipes.
		// Heartbeats prove work started and then actually stopped.
		command := `sh -c 'while :; do echo tick >> heartbeat; echo output; sleep 0.02; done' & wait`
		request := SliceVerificationRequest{ExecutionRoot: dir, Verification: plan.Verification{Commands: []string{command}}}
		var runs []plan.VerificationRun
		switch mode {
		case "command-timeout":
			if _, ok := guard.Context().Deadline(); ok {
				t.Fatal("disabled session timeout acquired a deadline")
			}
			if sliceVerificationCommandTimeout != 10*time.Minute {
				t.Fatal("production command cap changed")
			}
			runs, err = (SliceVerifier{}).verify(guard.Context(), request, 150*time.Millisecond)
			if !errors.Is(err, context.DeadlineExceeded) || guard.Check() != nil {
				t.Fatalf("command cap lost or expired session: %v", err)
			}
		case "grace-command-timeout":
			// Finish the command after the provider's soft timeout, releasing
			// grace without waiting for the production ten-minute command cap.
			runs, err = (SliceVerifier{}).verify(guard.Context(), request, 3*time.Second)
			if !errors.Is(err, context.DeadlineExceeded) || guard.Check() != nil {
				t.Fatalf("completion did not survive inside grace: %v", err)
			}
		default:
			runs, err = (SliceVerifier{}).Verify(guard.Context(), request)
			if guard.Check() == nil {
				t.Fatal("orphan guard downgraded to standalone")
			}
		}
		if err == nil || len(runs) != 1 || runs[0].Result != "failed" {
			t.Fatalf("verification = %+v, %v", runs, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "nested-result"), []byte("stopped"), 0o600); err != nil { // #nosec G703 -- test-local result handshake.
			t.Fatal(err)
		}
		fmt.Println("verification stopped")
	case "lock":
		release, err := acquireSliceCompletionLock(dir)
		if err != nil {
			fmt.Println("contended")
			return
		}
		defer func() { _ = release() }()
		fmt.Println("locked")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	default:
		t.Fatalf("unknown helper %q", mode)
	}
}

type completionHelper struct {
	cmd   *exec.Cmd
	input io.WriteCloser
	lines *bufio.Scanner
}

func startCompletionHelper(t *testing.T, dir, mode, token string) *completionHelper {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCompletionLifetimeHelperProcess$") // #nosec G204,G702 -- current test executable, no external commands.
	cmd.Env = append(os.Environ(), "TAO_TEST_COMPLETION_HELPER="+mode, "TAO_TEST_COMPLETION_DIR="+dir)
	if token != "" {
		cmd.Env = append(cmd.Env, sliceCompletionOwnerEnv+"="+token)
	}
	// A distinct group proves parent/provider group cancellation is insufficient.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = input.Close(); _ = cmd.Process.Kill(); _ = cmd.Wait() })
	return &completionHelper{cmd: cmd, input: input, lines: bufio.NewScanner(output)}
}

func (h *completionHelper) line(t *testing.T) string {
	t.Helper()
	result := make(chan string, 1)
	go func() {
		if h.lines.Scan() {
			result <- h.lines.Text()
		} else {
			result <- "EOF"
		}
	}()
	select {
	case line := <-result:
		return line
	case <-time.After(5 * time.Second):
		t.Fatal("helper handshake timed out")
		return ""
	}
}

func waitCompletionHeartbeat(t *testing.T, dir string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, _ := os.ReadFile(filepath.Join(dir, "heartbeat")); len(data) > 0 { // #nosec G304,G703 -- test-local handshake.
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("nested verification descendant never started")
}

func assertCompletionHeartbeatStopped(t *testing.T, dir string) {
	t.Helper()
	before, err := os.ReadFile(filepath.Join(dir, "heartbeat")) // #nosec G304,G703 -- test-local handshake.
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	after, err := os.ReadFile(filepath.Join(dir, "heartbeat")) // #nosec G304,G703 -- test-local handshake.
	if err != nil || string(before) != string(after) {
		t.Fatal("verification descendant survived cancellation")
	}
}

func TestSliceCompletionLifetimeAcrossProcesses(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	for _, mode := range []string{"death", "timeout", "normal-end", "supersession", "disabled-timeout"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			ownerMode := "owner"
			if mode == "timeout" {
				ownerMode = "timeout-owner"
			}
			owner := startCompletionHelper(t, dir, ownerMode, "")
			token := owner.line(t)
			nestedMode := "nested"
			if mode == "disabled-timeout" {
				nestedMode = "command-timeout"
			}
			nested := startCompletionHelper(t, dir, nestedMode, token)
			waitCompletionHeartbeat(t, dir)
			switch mode {
			case "death":
				if err := owner.cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := owner.cmd.Wait(); err == nil {
					t.Fatal("owner unexpectedly succeeded")
				}
			case "normal-end":
				_, _ = fmt.Fprintln(owner.input, "end")
				if got := owner.line(t); got != "ended" {
					t.Fatal(got)
				}
			case "supersession":
				_, _ = fmt.Fprintln(owner.input, "replace")
				if got := owner.line(t); got != "replaced" {
					t.Fatal(got)
				}
			}
			if got := nested.line(t); got != "verification stopped" {
				t.Fatal(got)
			}
			if err := nested.cmd.Wait(); err != nil {
				t.Fatal(err)
			}
			assertCompletionHeartbeatStopped(t, dir)
			if mode != "death" {
				_, _ = fmt.Fprintln(owner.input, "quit")
				if err := owner.cmd.Wait(); err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".slice-session-") || entry.Name() == completionOwnerName {
						t.Fatalf("leaked %s", entry.Name())
					}
				}
			}
		})
	}
}

func TestSliceCompletionLifetimeFailClosed(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	dir := t.TempDir()
	lock, err := plan.AcquireRunLock(dir, "plan", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Release() }()
	if _, err := BindSliceCompletionLifetime(context.Background(), dir, "001"); err == nil {
		t.Fatal("missing managed information admitted under live driver")
	}
	_, closeOwner, err := startSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	owner, err := readCompletionOwner(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(sliceCompletionOwnerEnv, owner.Token)
	if _, err := BindSliceCompletionLifetime(context.Background(), dir, "002"); err == nil {
		t.Fatal("wrong slice admitted")
	}
	if _, err := BindSliceCompletionLifetime(context.Background(), t.TempDir(), "001"); err == nil {
		t.Fatal("wrong plan admitted")
	}
	guard, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = guard.Close() }()
	// Replacing the marker synchronously refuses mutation, without waiting for
	// the polling goroutine; releasing an old owner cannot remove its successor.
	_, closeNext, err := startSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeNext() }()
	if err := guard.Check(); err == nil {
		t.Fatal("replaced invocation admitted")
	}
	if err := closeOwner(); err != nil {
		t.Fatal(err)
	}
	next, err := readCompletionOwner(dir)
	if err != nil || next.Token == owner.Token {
		t.Fatalf("successor removed: %+v, %v", next, err)
	}
	t.Setenv(sliceCompletionOwnerEnv, next.Token)
	successor, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = successor.Close() }()
	if err := successor.Check(); err != nil {
		t.Fatal(err)
	}
}

// Real provider paths exercise the shared session decorator, process starter,
// inherited environment, and a nested CLI in its own process group.
func TestSliceCompletionLifetimeLocalProviders(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	for _, kind := range []AgentKind{AgentPi, AgentClaude} {
		for _, outcome := range []string{"normal", "death", "timeout"} {
			t.Run(string(kind)+"/"+outcome, func(t *testing.T) {
				root := t.TempDir()
				dir := writeMetricsPlan(t, root, "plan-a")
				repository := plan.NewFileRepository(filepath.Dir(dir))
				timeout := time.Duration(0)
				if outcome == "timeout" {
					timeout = 2 * time.Second
				}
				starter := func(ctx context.Context, cwd, name string, args []string) (Process, error) {
					if name != string(kind) {
						t.Fatalf("provider = %s", name)
					}
					owner, err := readCompletionOwner(dir)
					if err != nil {
						return nil, err
					}
					deadline, _ := ctx.Deadline()
					if !owner.Deadline.Equal(deadline) {
						t.Fatalf("deadline restarted: %s != %s", owner.Deadline, deadline)
					}
					return process.DefaultProcessStarter(ctx, cwd, "env", []string{
						"TAO_TEST_COMPLETION_HELPER=provider-" + string(kind), "TAO_TEST_COMPLETION_DIR=" + dir,
						"TAO_TEST_PROVIDER_OUTCOME=" + outcome, os.Args[0], "-test.run=^TestCompletionLifetimeHelperProcess$",
					})
				}
				executor := testAgentExecutor(kind, agentExecutorOptions{SessionTimeout: timeout, Deps: agent.RuntimeDeps{ProcessStarter: starter}}, repository, repository)
				// Bound test failure even if a dead provider leaves inherited pipes
				// open forever. This is not a new session timeout setting.
				ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
				defer cancel()
				err := executor.RunSlice(ctx, SliceRun{PlanDir: dir, RepoRoot: root, SliceID: "001"})
				if outcome == "normal" && err != nil {
					t.Fatal(err)
				}
				if outcome != "normal" && err == nil {
					t.Fatal("provider failure lost")
				}
				if ctx.Err() != nil {
					t.Fatal("provider death was not observed until test deadline")
				}
				if outcome == "timeout" {
					var timeoutErr *agent.SessionTimeoutError
					if !errors.As(err, &timeoutErr) {
						t.Fatalf("timeout classification = %v", err)
					}
				}
				deadline := time.Now().Add(3 * time.Second)
				for {
					if result, _ := os.ReadFile(filepath.Join(dir, "nested-result")); string(result) == "stopped" { // #nosec G304,G703 -- test-local result handshake.
						break
					}
					if time.Now().After(deadline) {
						log, _ := os.ReadFile(filepath.Join(dir, "nested-log")) // #nosec G304,G703 -- test-local diagnostic log.
						t.Fatalf("nested completion survived %s: %s", outcome, log)
					}
					time.Sleep(5 * time.Millisecond)
				}
				assertCompletionHeartbeatStopped(t, dir)
			})
		}
	}
}

func completionFakeProvider(t *testing.T, dir, mode string) {
	t.Helper()
	outcome := os.Getenv("TAO_TEST_PROVIDER_OUTCOME")
	startNested := func() {
		log, err := os.Create(filepath.Join(dir, "nested-log")) // #nosec G304,G703 -- test-local diagnostic log.
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = log.Close() }()
		nested := exec.Command(os.Args[0], "-test.run=^TestCompletionLifetimeHelperProcess$") // #nosec G204,G702 -- current test executable.
		nestedMode := "nested"
		if outcome == "timeout" {
			nestedMode = "grace-command-timeout"
		}
		nested.Env = append(os.Environ(), "TAO_TEST_COMPLETION_HELPER="+nestedMode)
		nested.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		nested.Stdout, nested.Stderr = log, log
		if err := nested.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { _ = nested.Wait() }()
		waitCompletionHeartbeat(t, dir)
		// Retain provider pipes until nested verification has actually stopped.
		// Waiting for inherited-pipe EOF before observing death would deadlock.
		// The bound is only a test failure cleanup backstop.
		holder := exec.Command("sh", "-c", `i=0; while [ ! -f "$1/nested-result" ] && [ "$i" -lt 500 ]; do sleep 0.02; i=$((i+1)); done`, "sh", dir) // #nosec G204,G702 -- fixed test script with test-owned directory passed as a positional argument.
		holder.Stdout, holder.Stderr = os.Stdout, os.Stderr
		if err := holder.Start(); err != nil {
			t.Fatal(err)
		}
		go func() { _ = holder.Wait() }()
		if outcome == "death" {
			os.Exit(7)
		}
	}
	if mode == "provider-claude" {
		if _, err := io.ReadAll(os.Stdin); err != nil {
			t.Fatal(err)
		}
		startNested()
		if outcome == "timeout" {
			for {
				time.Sleep(time.Second)
			}
		}
		fmt.Println(`{"type":"result","result":"done"}`)
		return
	}
	prompted := false
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var command map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &command); err != nil {
			t.Fatal(err)
		}
		if prompted && outcome == "timeout" {
			continue
		}
		switch command["type"] {
		case "get_state":
			if prompted {
				fmt.Println(`{"type":"state","session_id":"local"}`)
			} else {
				fmt.Println(`{"id":"tao-readiness-state","type":"response","command":"get_state","success":true,"data":{"model":{"provider":"test-provider","id":"test-model"}}}`)
			}
		case "get_available_models":
			fmt.Println(`{"id":"tao-readiness-models","type":"response","command":"get_available_models","success":true,"data":{"models":[{"provider":"test-provider","id":"test-model"}]}}`)
		case "prompt":
			fmt.Println(`{"id":"tao-prompt","type":"response","command":"prompt","success":true}`)
			prompted = true
			startNested()
			if outcome == "normal" {
				fmt.Println(`{"type":"message","role":"assistant","text":"done"}`)
				fmt.Println(`{"type":"agent_end","session_id":"local"}`)
			}
		case "get_session_stats":
			fmt.Println(`{"type":"session_stats","session_id":"local"}`)
		}
	}
}

func TestSliceCompletionLifetimeRoleScope(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	for _, role := range []plan.AgentRole{plan.AgentRoleExecution, plan.AgentRoleRework, plan.AgentRoleReview, plan.AgentRolePullRequest} {
		t.Run(string(role), func(t *testing.T) {
			dir := writeMetricsPlan(t, "/repo", "plan-a")
			repository := plan.NewFileRepository(filepath.Dir(dir))
			seen := false
			fake := fakePiSessionStarter(t, "done", &seen)
			starter := func(ctx context.Context, cwd, name string, args []string) (Process, error) {
				owner, err := readCompletionOwner(dir)
				managed := role == plan.AgentRoleExecution || role == plan.AgentRoleRework
				if managed {
					if err != nil || owner.SliceID != "001" {
						t.Fatalf("missing %s owner: %+v %v", role, owner, err)
					}
				} else if !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("unexpected %s owner: %+v %v", role, owner, err)
				}
				return fake(ctx, cwd, name, args)
			}
			executor := testAgentExecutor(AgentPi, agentExecutorOptions{Deps: agent.RuntimeDeps{ProcessStarter: starter}}, repository, repository)
			_, err := executor.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: dir, RepoRoot: "/repo", Metrics: &AgentSessionMetricsRequest{Role: role, SliceID: "001"}})
			if err != nil {
				t.Fatal(err)
			}
			if !seen {
				t.Fatal("provider not invoked")
			}
			if _, err := readCompletionOwner(dir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("session owner not cleaned: %v", err)
			}
		})
	}
}

func TestSliceCompletionLifetimeDeadOwnerCleanup(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	dir := t.TempDir()
	owner := startCompletionHelper(t, dir, "owner", "")
	token := owner.line(t)
	if err := owner.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = owner.cmd.Wait()
	// A concurrent dead-owner probe must not masquerade as a live owner.
	probe, err := os.OpenFile(filepath.Join(dir, token), os.O_RDWR, 0) // #nosec G304 -- test-owned lease path.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = probe.Close() }()
	if err := syscall.Flock(int(probe.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sliceCompletionOwnerEnv, token)
	if _, err := BindSliceCompletionLifetime(context.Background(), dir, "001"); err == nil {
		t.Fatal("orphan rebound as standalone")
	}
	_ = os.Unsetenv(sliceCompletionOwnerEnv)
	direct, err := BindSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	_ = direct.Close()
	_, closeOwner, err := startSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, token)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dead lease not reaped: %v", err)
	}
	if err := closeOwner(); err != nil {
		t.Fatal(err)
	}
}

// Exercise the full transaction in a different process, not just the lease probe.
func completionVerifiedChild(t *testing.T, dir, mode string) {
	t.Helper()
	record, err := plan.NewFileRepository("").ResolvePlanRecord(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "verified completion", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC()}
	gateDone, paused := false, false
	pause := func() {
		fmt.Println("boundary ready")
		_, _ = bufio.NewReader(os.Stdin).ReadString('\n')
	}
	service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		if name == "sh" {
			gateDone = true
			if mode == "verified-gate" {
				return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
			}
			if mode == "verified" {
				pause()
			}
			return nil
		}
		if mode == "verified-boundary" && gateDone && !paused {
			paused = true // Snapshot persisted; second boundary inspection precedes intent.
			pause()
		}
		return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
	}}
	if err := service.CompleteVerified(context.Background(), request); err != nil {
		fmt.Println("refused: " + err.Error())
	} else {
		fmt.Println("completed")
	}
}

func TestCompleteVerifiedConcurrentChildren(t *testing.T) {
	request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte("one writer"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := startCompletionHelper(t, request.Record.Dir(), "verified-owner", "")
	token := owner.line(t)
	first := startCompletionHelper(t, request.Record.Dir(), "verified", token)
	if got := first.line(t); got != "boundary ready" {
		t.Fatal(got)
	}
	second := startCompletionHelper(t, request.Record.Dir(), "verified", token)
	if got := second.line(t); !strings.Contains(got, "refused:") || !strings.Contains(got, "ownership unavailable") {
		t.Fatalf("second writer: %s", got)
	}
	if err := second.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(first.input, "continue")
	if got := first.line(t); got != "completed" {
		t.Fatal(got)
	}
	if err := first.cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	detail := reloadVerifiedCompletion(t, request).Detail()
	if slice := detail.Slices.Slices[0]; slice.Completion == nil || slice.Completion.Outcome != plan.SliceCompletionCommitted {
		t.Fatalf("completion = %+v", slice.Completion)
	}
	if countPlanEvents(detail.Events, plan.EventTypeSliceCompleted) != 1 {
		t.Fatal("multiple writers settled intent/completion")
	}
	if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-list", "--count", request.Record.Detail().Slices.Slices[0].ExecutionStart.Head+"..HEAD")); got != "1" {
		t.Fatalf("commit count = %s", got)
	}
}

func TestCompleteVerifiedParentLossAcrossProcesses(t *testing.T) {
	for _, point := range []string{"verified-gate", "verified-boundary"} {
		for _, loss := range []string{"death", "expiration", "supersession"} {
			t.Run(point+"/"+loss, func(t *testing.T) {
				request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
				root := request.Record.Detail().Slices.Slices[0].ExecutionRoot
				// Heartbeat is ignored: process liveness must not itself cause boundary drift.
				if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("heartbeat\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				request.Record.Detail().Slices.Slices[0].Verification.Commands = []string{`sh -c 'while :; do echo tick >> heartbeat; sleep 0.02; done' & wait`}
				if err := request.Record.PersistArtifacts(); err != nil {
					t.Fatal(err)
				}
				ownerMode := "verified-owner"
				if loss == "expiration" {
					ownerMode = "verified-timeout-owner"
				}
				owner := startCompletionHelper(t, request.Record.Dir(), ownerMode, "")
				token := owner.line(t)
				child := startCompletionHelper(t, request.Record.Dir(), point, token)
				if point == "verified-gate" {
					waitCompletionHeartbeat(t, root)
				} else if got := child.line(t); got != "boundary ready" {
					t.Fatal(got)
				}
				switch loss {
				case "death":
					_ = owner.cmd.Process.Kill()
					_ = owner.cmd.Wait()
				case "supersession":
					_, _ = fmt.Fprintln(owner.input, "replace")
					if got := owner.line(t); got != "replaced" {
						t.Fatal(got)
					}
				case "expiration":
					if point == "verified-boundary" {
						marker, err := readCompletionOwner(request.Record.Dir())
						if err != nil {
							t.Fatal(err)
						}
						time.Sleep(time.Until(marker.Deadline) + 50*time.Millisecond)
					}
				}
				if point == "verified-boundary" {
					_, _ = fmt.Fprintln(child.input, "continue")
				}
				if got := child.line(t); !strings.HasPrefix(got, "refused:") {
					t.Fatal(got)
				}
				if err := child.cmd.Wait(); err != nil {
					t.Fatal(err)
				}
				if point == "verified-gate" {
					assertCompletionHeartbeatStopped(t, root)
				}
				slice := reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0]
				if slice.VerificationAttempt == nil || slice.CommitIntent != nil || slice.Completion != nil {
					t.Fatalf("advanced orphan transaction: %+v", slice)
				}
				if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); got != slice.ExecutionStart.Head {
					t.Fatal("orphan advanced HEAD")
				}
				if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "diff", "--cached", "--name-only")); got != "" {
					t.Fatalf("orphan staged %s", got)
				}
			})
		}
	}
}

func TestSliceCompletionLifetimeExpiredCheck(t *testing.T) {
	clearSliceCompletionOwnerEnv(t)
	dir := t.TempDir()
	_, closeOwner, err := startSliceCompletionLifetime(context.Background(), dir, "001")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = closeOwner() }()
	owner, err := readCompletionOwner(dir)
	if err != nil {
		t.Fatal(err)
	}
	owner.Deadline = time.Now().Add(-time.Second)
	content, _ := json.Marshal(owner)
	if err := os.WriteFile(filepath.Join(dir, completionOwnerName), content, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sliceCompletionOwnerEnv, owner.Token)
	if _, err := BindSliceCompletionLifetime(context.Background(), dir, "001"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expired bind = %v", err)
	}
}

func TestSliceCompletionOwnerEnvMatchesRunner(t *testing.T) {
	if sliceCompletionOwnerEnv != commandrunner.SliceCompletionOwnerEnv {
		t.Fatalf("owner env %q diverged from the runner's stripped key %q", sliceCompletionOwnerEnv, commandrunner.SliceCompletionOwnerEnv)
	}
}
