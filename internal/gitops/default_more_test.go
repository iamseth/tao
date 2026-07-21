package gitops

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestDefaultRunnerWithRealGitAndErrorWithoutStderr(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	client := NewClient(root, nil)
	branch, err := client.CurrentBranch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if branch != "main" {
		t.Fatalf("expected main branch, got %q", branch)
	}
	formatted := commandError([]string{"status"}, errors.New("boom"), "")
	if formatted.Error() != "git status: boom" {
		t.Fatalf("unexpected command error %q", formatted.Error())
	}
}

func runGitCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // G204: test invokes fixed git command with test-controlled args
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
}
