package plandelta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestWorktreeOptionalStatusAndCancellation(t *testing.T) {
	for _, failure := range []error{errors.New("optional failure"), context.Canceled, context.DeadlineExceeded} {
		d, c := snapshotFixture(t)
		d.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Branch: "feature/a"}
		original := c.Runner
		c.Runner = func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
			if args[10] == "status" {
				if cwd != "" || !strings.Contains(strings.Join(args, " "), "--no-optional-locks") || !strings.Contains(strings.Join(args, " "), "core.fsmonitor=false") {
					t.Fatalf("unsafe %v", args)
				}
				return failure
			}
			return original(ctx, cwd, name, args, out, errout)
		}
		s, err := c.Snapshot(context.Background(), d, ScopeWorktree)
		if errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded) {
			if !errors.Is(err, failure) {
				t.Fatalf("lost cancellation %v", err)
			}
		} else if err != nil || s.Availability != AvailabilityReady || len(s.Warnings) == 0 {
			t.Fatalf("%+v %v", s, err)
		}
	}
}

func TestWorktreeLocalGitSignaturesAndReverts(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git unavailable")
	}
	root := t.TempDir()
	git := localDeltaGit(t, root)
	put := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-b", "main")
	put("a", "base\n")
	put("deleted", "delete\n")
	put("binary", "\x00base")
	git("add", ".")
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("checkout", "-b", "feature/a")
	put("a", "committed\n")
	git("commit", "-am", "feature")
	d := &plan.PlanDetail{}
	d.State.Plan.ID = "plan-a"
	d.State.Repo.Root, d.State.Repo.BaseCommit = root, base
	d.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Branch: "feature/a", BaseBranch: "main", BaseSHA: base}
	c := NewCollector(nil)
	snap := func() Snapshot {
		t.Helper()
		s, err := c.Snapshot(context.Background(), d, ScopeWorktree)
		if err != nil || s.Availability != AvailabilityReady {
			t.Fatalf("%+v %v", s, err)
		}
		return s
	}
	clean := snap()
	// Stat-only changes must not invalidate content signatures or dirty labels.
	if err := os.Chtimes(filepath.Join(root, "a"), time.Now().Add(time.Hour), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	touched := snap()
	if touched.Signature != clean.Signature || touched.Dirty {
		t.Fatalf("stat noise: %+v", touched)
	}
	put("a", "modified1\n")
	edited := snap()
	put("a", "modified2\n")
	sameCounts := snap()
	if edited.Signature == clean.Signature || sameCounts.Signature == edited.Signature {
		t.Fatal("content edit did not invalidate")
	}
	git("add", "a")
	staged := snap()
	if staged.Signature == sameCounts.Signature {
		t.Fatal("staging did not invalidate")
	}
	put(".tao/noise", "ignored")
	plan.SetPersistedReview(d, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: base, Head: git("rev-parse", "HEAD")})
	if got := snap(); got.Signature != staged.Signature {
		t.Fatal("presentation/metadata changed signature")
	}
	put("untracked", "new\n")
	untracked := snap()
	if untracked.Signature == staged.Signature || untracked.UntrackedCount != 1 {
		t.Fatalf("%+v", untracked)
	}
	put("a", "base\n")
	git("add", "a")
	if err := os.Remove(filepath.Join(root, "deleted")); err != nil {
		t.Fatal(err)
	}
	put("binary", "\x00changed")
	reverted := snap()
	var revert FileChange
	for _, f := range reverted.Files {
		if f.Path == "a" {
			revert = f
		}
	}
	if !revert.RevertsCommitted || !revert.Uncommitted || revert.Status != 'M' || reverted.Uncommitted.Files != 4 {
		t.Fatalf("%+v", reverted)
	}
	diff, err := c.FileDiff(context.Background(), reverted, "a")
	if err != nil {
		t.Fatal(err)
	}
	text := ""
	for _, line := range diff.Lines {
		text += line.Text + "\n"
	}
	if !strings.Contains(text, "-committed") || !strings.Contains(text, "+base") {
		t.Fatalf("not HEAD diff: %s", text)
	}
	// Hardened probes must not execute drivers/fsmonitor or change any bytes,
	// even when the index lock is held.
	sentinel := filepath.Join(root, "sentinel")
	script := filepath.Join(root, "driver")
	if err := os.WriteFile(script, []byte("#!/bin/sh\ntouch '"+sentinel+"'\n"), 0700); err != nil { // #nosec G306 -- executable sentinel in a private test directory.
		t.Fatal(err)
	}
	git("config", "core.fsmonitor", script)
	git("config", "diff.external", script)
	git("config", "diff.test.textconv", script)
	put(".gitattributes", "a diff=test\n")
	put(".git/index.lock", "held")
	before := snapshotTreeState(t, root)
	_ = snap()
	if !reflect.DeepEqual(before, snapshotTreeState(t, root)) {
		t.Fatal("collector modified repository or ran a driver")
	}
	if err := os.Remove(filepath.Join(root, ".git/index.lock")); err != nil {
		t.Fatal(err)
	}
	// Move the default ref without checking out or disturbing the worktree.
	previous := snap()
	git("branch", "-f", "main", "feature/a")
	if next := snap(); next.Signature == previous.Signature {
		t.Fatal("default advance did not invalidate")
	}
}

func TestWorktreeUntrackedCapAndMalformedStatus(t *testing.T) {
	d, c := snapshotFixture(t)
	d.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Branch: "feature/a"}
	original := c.Runner
	status := ""
	for i := 0; i < MaxFiles+1; i++ {
		status += fmt.Sprintf("? untracked-%04d\x00", i)
	}
	c.Runner = func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
		switch args[10] {
		case "status":
			_, _ = io.WriteString(out, status)
			return nil
		case "diff":
			return nil
		default:
			return original(ctx, cwd, name, args, out, errout)
		}
	}
	s, err := c.Snapshot(context.Background(), d, ScopeWorktree)
	if err != nil || !s.FilesTruncated || s.Dirty || s.Total.Files != MaxFiles || s.UntrackedCount != MaxFiles || s.Uncommitted.Files != MaxFiles {
		t.Fatalf("%+v %v", s, err)
	}
	status = "? ../escape\x00"
	s, err = c.Snapshot(context.Background(), d, ScopeWorktree)
	if err != nil || s.Availability != AvailabilityGitError {
		t.Fatalf("malformed status accepted: %+v %v", s, err)
	}
	status = strings.Repeat("? .tao/noise\x00", MaxListBytes/13+10)
	s, err = c.Snapshot(context.Background(), d, ScopeWorktree)
	if err != nil || !s.FilesTruncated || len(s.Files) != 0 || len(s.Warnings) == 0 {
		t.Fatalf("status cap: %+v %v", s, err)
	}
}

func TestWorktreeResolvesHEADBeforeRejectingBranchBase(t *testing.T) {
	d, c := snapshotFixture(t)
	d.State.Repo.BaseCommit = ""
	d.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Branch: "feature/a"}
	original := c.Runner
	c.Runner = func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
		switch args[10] {
		case "merge-base":
			if args[len(args)-1] == "feature/a" {
				return snapshotExit(1)
			}
		case "rev-parse":
			if args[len(args)-1] == "HEAD^{commit}" {
				_, _ = io.WriteString(out, snapshotBase+"\n")
				return nil
			}
		case "diff", "status":
			return nil
		}
		return original(ctx, cwd, name, args, out, errout)
	}
	s, err := c.Snapshot(context.Background(), d, ScopeWorktree)
	if err != nil || s.Availability != AvailabilityReady || s.Base.SHA != snapshotBase || s.Base.Source != BaseSource("merge-base (worktree HEAD)") {
		t.Fatalf("worktree HEAD base should survive unavailable branch base: %+v %v", s, err)
	}
}

func localDeltaGit(t *testing.T, root string) func(...string) string {
	t.Helper()
	return func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...) // #nosec G204 -- local test-owned Git fixture.
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
}
