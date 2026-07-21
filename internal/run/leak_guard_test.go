package run

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
)

func TestAgentSessionLeakGuardFailsForNewUntrackedControlFile(t *testing.T) {
	planDir := writeLeakGuardState(t)
	git := &scriptedGitRunner{
		Statuses:  []string{"", "?? leaked.txt\n"},
		Diffs:     []string{"", ""},
		DiffNames: []string{"", ""},
	}
	runner := newLeakGuardTestRunner(planDir, git.Run)

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: "/worktree", LogAction: "running 001-a", Prompt: "go"})
	var leak ControlCheckoutLeakError
	if !errors.As(err, &leak) {
		t.Fatalf("expected leak error, got %v", err)
	}
	if leak.ControlRoot != "/control" || !strings.Contains(err.Error(), "leaked.txt") {
		t.Fatalf("expected control root and leaked path in error, got %#v / %v", leak, err)
	}
}

func TestAgentSessionLeakGuardFailsForAlreadyDirtyContentOnlyEdit(t *testing.T) {
	planDir := writeLeakGuardState(t)
	git := &scriptedGitRunner{
		Statuses:  []string{" M dirty.go\n", " M dirty.go\n"},
		Diffs:     []string{"diff --git a/dirty.go b/dirty.go\n-old\n+first\n", "diff --git a/dirty.go b/dirty.go\n-old\n+second\n"},
		DiffNames: []string{"dirty.go\n", "dirty.go\n"},
	}
	runner := newLeakGuardTestRunner(planDir, git.Run)

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: "/worktree", LogAction: "reviewing plan plan-a", Prompt: "go"})
	if err == nil || !strings.Contains(err.Error(), "dirty.go") {
		t.Fatalf("expected dirty.go leak error, got %v", err)
	}
}

func TestAgentSessionLeakGuardCurrentModeDoesNotCallGit(t *testing.T) {
	planDir := writeLeakGuardState(t)
	var calls []string
	runner := newLeakGuardTestRunner(planDir, func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" {
			calls = append(calls, runGitKey(args))
		}
		return nil
	})

	if _, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: "/control", LogAction: "running 001-a", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 0 {
		t.Fatalf("expected no git calls in current mode, got %#v", calls)
	}
}

func TestAgentSessionLeakGuardAllowsPreExistingUnchangedDirt(t *testing.T) {
	planDir := writeLeakGuardState(t)
	git := &scriptedGitRunner{
		Statuses:  []string{" M dirty.go\n?? existing.txt\n", " M dirty.go\n?? existing.txt\n"},
		Diffs:     []string{"diff --git a/dirty.go b/dirty.go\n-old\n+new\n", "diff --git a/dirty.go b/dirty.go\n-old\n+new\n"},
		DiffNames: []string{"dirty.go\n", "dirty.go\n"},
	}
	runner := newLeakGuardTestRunner(planDir, git.Run)

	if _, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: "/worktree", LogAction: "running 001-a", Prompt: "go"}); err != nil {
		t.Fatal(err)
	}
}

func TestAgentSessionLeakGuardCoversPullRequestBodySession(t *testing.T) {
	action := "drafting pull request body for plan plan-a"
	planDir := writeLeakGuardState(t)
	git := &scriptedGitRunner{
		Statuses:  []string{"", "?? " + strings.ReplaceAll(action, " ", "-") + ".txt\n"},
		Diffs:     []string{"", ""},
		DiffNames: []string{"", ""},
	}
	runner := newLeakGuardTestRunner(planDir, git.Run)

	_, err := runner.RunAgentSession(context.Background(), AgentSessionRequest{PlanDir: planDir, RepoRoot: "/worktree", LogAction: action, Prompt: "go"})
	if err == nil || !strings.Contains(err.Error(), "/control") {
		t.Fatalf("expected leak guard error for %s, got %v", action, err)
	}
}

func writeLeakGuardState(t *testing.T) string {
	t.Helper()
	planDir := t.TempDir()
	record, err := plan.NewPlanRecord(planDir, &plan.PlanDetail{Dir: planDir, State: plan.State{Repo: plan.Repo{Root: "/control"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistState(); err != nil {
		t.Fatal(err)
	}
	return planDir
}

func newLeakGuardTestRunner(planDir string, commandRunner CommandRunner) agentSessionRunner {
	_ = planDir
	runtime := &leakGuardRuntime{}
	return newAgentSessionRunner(agentSessionRunnerConfig{
		descriptor:    agent.Descriptor{Label: "test", NewRuntime: func(agent.RuntimeDeps) agent.Runtime { return runtime }},
		logAppender:   plan.NewFileRepository(""),
		commandRunner: commandRunner,
	})
}

type leakGuardRuntime struct{}

func (r *leakGuardRuntime) RunSession(ctx context.Context, session agent.Session) (agent.SessionResult, error) {
	return agent.SessionResult{Output: "done"}, ctx.Err()
}
