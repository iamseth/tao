package workspace

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type testRepo struct {
	path string
}

func newTestRepo(t *testing.T) testRepo {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-b", "master")
	runGit(t, dir, "config", "user.name", "Tao Test")
	runGit(t, dir, "config", "user.email", "tao@example.com")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write README: %v", err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
	return testRepo{path: dir}
}

func runGit(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- tests invoke fixed git commands with test-controlled args.
	cmd.Dir = cwd
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, stderr.String())
	}
	return stdout.String()
}

func TestManagerUsesCommandRunnerSeam(t *testing.T) {
	var calls []string
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		key := workspaceGitKey(args)
		first, _, _ := strings.Cut(key, " ")
		calls = append(calls, name+" "+first)
		switch first {
		case "branch":
			_, _ = io.WriteString(stdout, "master\n")
		case "rev-parse":
			_, _ = io.WriteString(stdout, "abc123\n")
		case "worktree":
			if strings.HasPrefix(key, "worktree list ") {
				_, _ = io.WriteString(stdout, "worktree /repo\nHEAD abc123\nbranch refs/heads/master\n")
			}
		case "status":
		}
		return nil
	}
	manager, err := NewManager(Options{RepoRoot: t.TempDir(), Runner: runner})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	if _, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master"}); err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if len(calls) == 0 {
		t.Fatalf("expected runner to be called")
	}
}

func TestPrepareDetectsStaleRecordedBaseWithFakeGitOutput(t *testing.T) {
	var calls []string
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		key := workspaceGitKey(args)
		calls = append(calls, name+" "+key)
		switch key {
		case "rev-parse master":
			_, _ = io.WriteString(stdout, "newbase\n")
		case "worktree list --porcelain":
			_, _ = io.WriteString(stdout, "worktree /repo\nHEAD newbase\nbranch refs/heads/master\n")
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "tao/plan-a\n")
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "head123\n")
		case "status --porcelain":
		}
		return nil
	}
	manager, err := NewManager(Options{RepoRoot: t.TempDir(), Runner: runner})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	metadata, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master", BaseSHA: "oldbase"})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if metadata.BaseSHA != "oldbase" || metadata.BaseCurrentSHA != "newbase" || metadata.BaseStatus != "stale" || metadata.RebaseStatus != "needed" || metadata.RefreshStatus != "needed" {
		t.Fatalf("expected stale base metadata, got %#v", metadata)
	}
	if metadata.HeadSHA != "head123" {
		t.Fatalf("expected head metadata, got %#v", metadata)
	}
}

func TestPrepareCurrentStrategyFallbackRecordsGitMetadata(t *testing.T) {
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		switch workspaceGitKey(args) {
		case "branch --show-current":
			_, _ = io.WriteString(stdout, "feature\n")
		case "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "head123\n")
		case "rev-parse main":
			_, _ = io.WriteString(stdout, "newbase\n")
		case "status --porcelain":
		}
		return nil
	}
	config := DefaultConfig()
	config.Strategy = StrategyCurrent
	manager, err := NewManager(Options{RepoRoot: t.TempDir(), Config: config, Runner: runner})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}

	metadata, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "main", BaseSHA: "oldbase"})
	if err != nil {
		t.Fatalf("Prepare failed: %v", err)
	}
	if metadata.Branch != "feature" || metadata.HeadSHA != "head123" || metadata.BaseSHA != "oldbase" || metadata.BaseCurrentSHA != "newbase" || metadata.BaseStatus != "stale" {
		t.Fatalf("expected current strategy fallback metadata, got %#v", metadata)
	}
}

func commitTestFile(t *testing.T, cwd string, name string, content string, message string) {
	t.Helper()
	path := filepath.Join(cwd, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: test fixture dirs
		t.Fatalf("create fixture dir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write fixture file: %v", err)
	}
	runGit(t, cwd, "add", name)
	runGit(t, cwd, "commit", "-m", message)
}

func gitHead(t *testing.T, cwd string) string {
	t.Helper()
	return strings.TrimSpace(runGit(t, cwd, "rev-parse", "HEAD"))
}

func gitIsAncestor(t *testing.T, cwd string, ancestor string, descendant string) bool {
	t.Helper()
	cmd := exec.Command("git", "merge-base", "--is-ancestor", ancestor, descendant) // #nosec G204 -- tests invoke fixed git command with test-controlled refs.
	cmd.Dir = cwd
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); ok {
			return false
		}
		t.Fatalf("git merge-base --is-ancestor failed: %v\n%s", err, stderr.String())
	}
	return true
}

func workspaceGitKey(args []string) string {
	if len(args) >= 2 && args[0] == "-C" {
		args = args[2:]
	}
	return strings.Join(args, " ")
}
