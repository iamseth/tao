package cli

import (
	"bytes"
	"context"
	"testing"

	"github.com/iamseth/tao/internal/workspace"
)

func TestDefaultCleanupRunnerExecutesCommand(t *testing.T) {
	runner := (App{}).cleanupRunner()
	var out bytes.Buffer
	if err := runner(context.Background(), "", "printf", []string{"cleanup"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "cleanup" {
		t.Fatalf("unexpected runner output %q", out.String())
	}
}

func TestCleanupItemLineFormatsTarget(t *testing.T) {
	var out bytes.Buffer
	if err := cleanupItemLine(&out, "removed", "/repo", workspace.ManagedCleanup{Branch: "tao/done", WorktreePath: "/repo/.tao/workspaces/done"}, "merged into master"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "removed /repo tao/done (worktree /repo/.tao/workspaces/done): merged into master\n" {
		t.Fatalf("unexpected worktree line %q", got)
	}

	out.Reset()
	if err := cleanupItemLine(&out, "skipped", "/repo", workspace.ManagedCleanup{Branch: "tao/wip"}, "not merged into master"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "skipped /repo tao/wip: not merged into master\n" {
		t.Fatalf("unexpected branch-only line %q", got)
	}
}
