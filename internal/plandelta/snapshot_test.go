package plandelta

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

const snapshotSHA = "1111111111111111111111111111111111111111"
const snapshotBase = "2222222222222222222222222222222222222222"

type snapshotExit int

func (e snapshotExit) Error() string { return "exit" }
func (e snapshotExit) ExitCode() int { return int(e) }

func snapshotFixture(t *testing.T) (*plan.PlanDetail, Collector) {
	t.Helper()
	d := &plan.PlanDetail{}
	d.State.Plan.ID = "plan-a"
	d.State.Repo.Root = t.TempDir()
	d.State.Repo.BaseCommit = snapshotBase
	c := NewCollector(func(_ context.Context, cwd, name string, args []string, out, _ io.Writer) error {
		if cwd != "" || name != "git" || len(args) < 12 || args[0] != "-C" || args[1] != d.State.Repo.Root || strings.Join(args[2:10], " ") != "--no-optional-locks -c diff.autoRefreshIndex=false -c core.fsmonitor=false -c core.quotePath=false --literal-pathspecs" {
			t.Fatalf("unsafe probe: %s %v", cwd, args)
		}
		for _, pin := range []string{"--no-optional-locks", "diff.autoRefreshIndex=false", "core.fsmonitor=false", "core.quotePath=false", "--literal-pathspecs"} {
			if !strings.Contains(strings.Join(args, " "), pin) {
				t.Fatalf("missing pin %s: %v", pin, args)
			}
		}
		switch args[10] {
		case "rev-parse":
			sha := snapshotSHA
			if args[len(args)-1] == snapshotBase+"^{commit}" {
				sha = snapshotBase
			}
			_, _ = io.WriteString(out, sha+"\n")
		case "symbolic-ref":
			_, _ = io.WriteString(out, "origin/main\n")
		case "merge-base":
			_, _ = io.WriteString(out, snapshotBase+"\n")
		case "branch":
			_, _ = io.WriteString(out, "main\n")
		case "diff":
			joined := strings.Join(args, " ")
			if !strings.Contains(joined, "--no-renames") || !strings.Contains(joined, "--no-textconv") || !strings.Contains(joined, "--no-ext-diff") {
				t.Fatalf("unsafe diff %v", args)
			}
			if strings.Contains(joined, "--name-status") {
				_, _ = io.WriteString(out, "M\x00z\x00A\x00a\x00M\x00noise\x00M\x00.tao/state\x00")
			} else {
				_, _ = io.WriteString(out, "2\t1\tz\x001\t0\ta\x009\t9\t.tao/state\x00")
			}
		default:
			t.Fatalf("forbidden probe %v", args)
		}
		return nil
	})
	c.Now = func() time.Time { return time.Unix(42, 0) }
	return d, c
}

func TestSnapshotBranch(t *testing.T) {
	d, c := snapshotFixture(t)
	s, err := c.Snapshot(context.Background(), d, ScopeBranch)
	if err != nil || s.Availability != AvailabilityReady || len(s.Files) != 2 || s.Files[0].Path != "a" || s.Total != (Stat{2, 3, 1}) || !s.WorktreeMissing || s.Signature == "" || !s.CollectedAt.Equal(c.Now()) {
		t.Fatalf("snapshot %+v, %v", s, err)
	}
}

func TestSnapshotAvailability(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*plan.PlanDetail, *Collector)
		scope  Scope
		want   Availability
	}{
		{"missing root", func(d *plan.PlanDetail, _ *Collector) { d.State.Repo.Root = "" }, ScopeBranch, AvailabilityNoRepoRoot},
		{"inaccessible", func(d *plan.PlanDetail, _ *Collector) { d.State.Repo.Root += "/missing" }, ScopeBranch, AvailabilityRepoInaccessible},
		{"unsupported", func(_ *plan.PlanDetail, _ *Collector) {}, Scope("unsupported"), AvailabilityGitError},
		{"missing branch", func(_ *plan.PlanDetail, c *Collector) {
			c.Runner = func(context.Context, string, string, []string, io.Writer, io.Writer) error { return snapshotExit(1) }
		}, ScopeBranch, AvailabilityNoBranch},
		{"git error", func(_ *plan.PlanDetail, c *Collector) {
			c.Runner = func(context.Context, string, string, []string, io.Writer, io.Writer) error {
				return errors.New("bad\x1b[31m\nsecret")
			}
		}, ScopeBranch, AvailabilityGitError},
		{"no base", func(d *plan.PlanDetail, _ *Collector) { d.State.Repo.BaseCommit = "" }, ScopeBranch, AvailabilityNoBase},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, c := snapshotFixture(t)
			tt.mutate(d, &c)
			s, err := c.Snapshot(context.Background(), d, tt.scope)
			if err != nil || s.Availability != tt.want || strings.ContainsAny(s.Reason, "\x1b\n") {
				t.Fatalf("%+v %v", s, err)
			}
		})
	}
}

func TestSnapshotFallbacksAndMetadata(t *testing.T) {
	for _, strategy := range []string{plan.WorkspaceStrategyCurrent, plan.WorkspaceStrategyWorktree} {
		t.Run(strategy, func(t *testing.T) {
			d, c := snapshotFixture(t)
			d.State.Workspace = &plan.Workspace{Strategy: strategy, Branch: "feature/a", BaseSHA: snapshotBase, BaseBranch: "main", Path: t.TempDir(), RebaseIntent: &plan.WorkspaceRebaseIntent{}}
			root := d.State.Workspace.Path
			if strategy == plan.WorkspaceStrategyCurrent {
				root = d.State.Repo.Root
			}
			if err := os.MkdirAll(filepath.Join(root, ".git", "rebase-merge"), 0700); err != nil {
				t.Fatal(err)
			}
			plan.SetPersistedReview(d, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: snapshotBase, Head: snapshotSHA})
			original := c.Runner
			c.Runner = func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
				if args[10] == "merge-base" {
					return errors.New("optional merge-base failure")
				}
				return original(ctx, cwd, name, args, out, errout)
			}
			s, err := c.Snapshot(context.Background(), d, ScopeBranch)
			if err != nil || s.Availability != AvailabilityReady || s.Base.Source != BaseSourceWorkspace || s.ActiveOperation != "rebase" || !s.RebaseIntent || !s.Review.Recorded || !s.Review.BaseMatches || !s.Review.HeadMatches || s.Merged != nil || len(s.Warnings) == 0 {
				t.Fatalf("%+v %v", s, err)
			}
		})
	}
}

func TestSnapshotTypedBranchExistence(t *testing.T) {
	for _, exists := range []bool{false, true} {
		d, c := snapshotFixture(t)
		d.State.Plan.ID = "20261001-140136-plan-changes"
		d.State.Plan.ChangeType = "feat"
		original := c.Runner
		c.Runner = func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
			if args[10] == "rev-parse" && args[len(args)-1] == "refs/heads/feature/plan-changes^{commit}" && !exists {
				return snapshotExit(1)
			}
			return original(ctx, cwd, name, args, out, errout)
		}
		s, err := c.Snapshot(context.Background(), d, ScopeBranch)
		if err != nil || !s.Target.BranchIsNew {
			t.Fatalf("%+v %v", s, err)
		}
		if exists && s.Availability != AvailabilityReady || !exists && (s.Availability != AvailabilityNoBranch || !strings.Contains(s.Reason, "not been created")) {
			t.Fatalf("%+v", s)
		}
	}
}

func TestSnapshotProbeFailures(t *testing.T) {
	for _, tt := range []struct {
		name, command, match string
		failure              error
		want                 Availability
		cancel               bool
	}{
		{"required diff", "diff", "", errors.New("failed"), AvailabilityGitError, false},
		{"optional default", "symbolic-ref", "", errors.New("failed"), AvailabilityReady, false},
		{"optional default head", "rev-parse", "refs/heads/main", errors.New("failed"), AvailabilityReady, false},
		{"required base missing", "rev-parse", snapshotBase, snapshotExit(1), AvailabilityNoBase, false},
		{"required head", "rev-parse", "HEAD^", errors.New("failed"), AvailabilityGitError, false},
		{"swallowed cancellation", "merge-base", "", context.DeadlineExceeded, AvailabilityReady, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, c := snapshotFixture(t)
			d.State.Workspace = &plan.Workspace{Branch: "feature/a", Strategy: plan.WorkspaceStrategyCurrent}
			original := c.Runner
			c.Runner = func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
				if args[10] == tt.command && strings.Contains(strings.Join(args, " "), tt.match) {
					return tt.failure
				}
				return original(ctx, cwd, name, args, out, errout)
			}
			s, err := c.Snapshot(context.Background(), d, ScopeBranch)
			if tt.cancel {
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("lost cancellation: %v", err)
				}
				return
			}
			if err != nil || s.Availability != tt.want {
				t.Fatalf("%+v %v", s, err)
			}
		})
	}
}

func TestJoinBranchLists(t *testing.T) {
	names := []gitops.NameStatusEntry{{Status: "M", Path: "b"}, {Status: "A", Path: "a\n\t"}, {Status: "M", Path: "noise"}, {Status: "M", Path: ".git/config"}, {Status: "M", Path: ".tao/state"}}
	stats := []gitops.NumstatEntry{{Path: "b", Binary: true}, {Path: "a\n\t", Added: 2}, {Path: ".git/config", Added: 3}, {Path: ".tao/state", Added: 3}}
	files, err := joinBranchLists(names, stats, false)
	if err != nil || len(files) != 2 || files[0].Path != "a\n\t" || !files[1].Binary {
		t.Fatalf("%+v %v", files, err)
	}
	s := Snapshot{Scope: ScopeBranch}
	signature := snapshotSignature(s, files)
	names = append(names, gitops.NameStatusEntry{Status: "M", Path: "more-noise"})
	stats = append(stats, gitops.NumstatEntry{Path: ".tao/other", Added: 12})
	files, err = joinBranchLists(names, stats, false)
	if err != nil || snapshotSignature(s, files) != signature {
		t.Fatalf("noise altered signature: %v", err)
	}
	partial, err := joinBranchLists(names, stats, true)
	if err != nil || len(partial) != 4 {
		t.Fatalf("truncated stats: %+v %v", partial, err)
	}
	for _, bad := range []gitops.NameStatusEntry{{Status: "M", Path: "../bad"}, {Status: "R100", Path: "a", OldPath: "b"}, {Status: "M", Path: "b"}} {
		if _, err := joinBranchLists(append(names, bad), stats, false); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestSnapshotFileCap(t *testing.T) {
	d, c := snapshotFixture(t)
	original := c.Runner
	c.Runner = func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
		if args[10] != "diff" {
			return original(ctx, cwd, name, args, out, errout)
		}
		for i := range MaxFiles + 1 {
			if strings.Contains(strings.Join(args, " "), "--name-status") {
				_, _ = fmt.Fprintf(out, "M\x00file-%04d\x00", i)
			} else {
				_, _ = fmt.Fprintf(out, "1\t2\tfile-%04d\x00", i)
			}
		}
		return nil
	}
	s, err := c.Snapshot(context.Background(), d, ScopeBranch)
	if err != nil || len(s.Files) != MaxFiles || !s.FilesTruncated || s.Total != (Stat{MaxFiles + 1, MaxFiles + 1, 2 * (MaxFiles + 1)}) {
		t.Fatalf("%+v %v", s, err)
	}
}

func TestSnapshotTruncatedAndMalformedLists(t *testing.T) {
	for _, truncated := range []bool{true, false} {
		d, c := snapshotFixture(t)
		original := c.Runner
		c.Runner = func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
			if args[10] != "diff" {
				return original(ctx, cwd, name, args, out, errout)
			}
			if strings.Contains(strings.Join(args, " "), "--name-status") {
				_, _ = io.WriteString(out, "M\x00a\x00M\x00")
			} else {
				_, _ = io.WriteString(out, "1\t0\ta\x002\t1\t")
			}
			if truncated {
				_, _ = io.WriteString(out, strings.Repeat("x", MaxListBytes))
			} else {
				_, _ = io.WriteString(out, "incomplete")
			}
			return nil
		}
		s, err := c.Snapshot(context.Background(), d, ScopeBranch)
		if err != nil {
			t.Fatal(err)
		}
		if truncated {
			if s.Availability != AvailabilityReady || !s.FilesTruncated || len(s.Files) != 1 || s.Files[0].Path != "a" {
				t.Fatalf("%+v", s)
			}
		} else if s.Availability != AvailabilityGitError {
			t.Fatalf("%+v", s)
		}
	}
}

func TestSnapshotOptionalOperationAndSupersededReview(t *testing.T) {
	d, c := snapshotFixture(t)
	d.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Branch: "feature/a"}
	if err := os.WriteFile(filepath.Join(d.State.Repo.Root, ".git"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	plan.SetPersistedReview(d, plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: snapshotBase, Head: snapshotSHA})
	d.Events = []plan.Event{{Type: "plan_reopened"}}
	s, err := c.Snapshot(context.Background(), d, ScopeBranch)
	if err != nil || s.Availability != AvailabilityReady || !s.Review.Superseded || len(s.Warnings) == 0 {
		t.Fatalf("%+v %v", s, err)
	}
	s, err = c.Snapshot(context.Background(), nil, ScopeBranch)
	if err != nil || s.Availability != AvailabilityNoRepoRoot {
		t.Fatalf("nil detail: %+v %v", s, err)
	}
}

func TestSnapshotCancellation(t *testing.T) {
	d, c := snapshotFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := c.Runner
	c.Runner = func(ctx context.Context, cwd, name string, args []string, out, errout io.Writer) error {
		if args[10] == "symbolic-ref" {
			cancel()
			return ctx.Err()
		}
		return original(ctx, cwd, name, args, out, errout)
	}
	_, err := c.Snapshot(ctx, d, ScopeBranch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}
