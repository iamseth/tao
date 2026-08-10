package agentsession

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/iamseth/tao/internal/agent"
)

func TestRunnerDetectsControlCheckoutLeak(t *testing.T) {
	controlRoot := t.TempDir()
	runGit(t, controlRoot, "init")
	runGit(t, controlRoot, "config", "user.email", "test@example.com")
	runGit(t, controlRoot, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(controlRoot, "tracked.txt"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, controlRoot, "add", "tracked.txt")
	runGit(t, controlRoot, "commit", "-m", "base")

	calls := 0
	runtime := runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		calls++
		if err := os.WriteFile(filepath.Join(controlRoot, "tracked.txt"), []byte("leaked\n"), 0o600); err != nil {
			return agent.SessionResult{}, err
		}
		return agent.SessionResult{Output: "partial"}, nil
	})
	runner := New(Config{Descriptor: agent.Descriptor{NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }}})
	result, err := runner.Run(context.Background(), Request{ControlRoot: controlRoot, RepoRoot: t.TempDir()})
	var leak ControlCheckoutLeakError
	if !errors.As(err, &leak) {
		t.Fatalf("error = %v, want ControlCheckoutLeakError", err)
	}
	if calls != 1 || result.Output != "partial" || len(leak.Paths) != 1 || leak.Paths[0] != "tracked.txt" {
		t.Fatalf("calls/result/leak = %d, %+v, %+v", calls, result, leak)
	}
}

func TestRunnerSkipsLeakFingerprintForControlCheckoutSession(t *testing.T) {
	calls := 0
	runtime := runtimeFunc(func(context.Context, agent.Session) (agent.SessionResult, error) {
		calls++
		return agent.SessionResult{}, nil
	})
	runner := New(Config{Descriptor: agent.Descriptor{NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }}, CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		t.Fatal("same-root session invoked git")
		return nil
	}})
	if _, err := runner.Run(context.Background(), Request{ControlRoot: "/same", RepoRoot: "/same"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("provider calls = %d, want 1", calls)
	}
}

func runGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- test helper invokes fixed git with test-owned arguments.
	cmd.Dir = cwd
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
}
