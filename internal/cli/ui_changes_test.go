package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plandelta"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/tui"
)

func TestUIChangesSnapshotScopes(t *testing.T) {
	const head = "1111111111111111111111111111111111111111"
	const base = "2222222222222222222222222222222222222222"
	for _, mode := range []string{"branch", "worktree", "current", "missing"} {
		t.Run(mode, func(t *testing.T) {
			root, work := t.TempDir(), t.TempDir()
			detail := &plan.PlanDetail{State: plan.State{Plan: plan.PlanState{ID: "p"}, Repo: plan.Repo{Root: root, BaseCommit: base}, Workspace: &plan.Workspace{Strategy: "worktree", Path: work, Branch: "feature/a", BaseSHA: base, BaseBranch: "main"}}}
			scope := ""
			wantScope := "worktree"
			if mode == "branch" {
				scope = "branch"
				wantScope = "branch"
			}
			if mode == "current" {
				detail.State.Workspace.Strategy = "current"
				work = root
			}
			if mode == "missing" {
				detail.State.Workspace.Path = filepath.Join(work, "missing")
				wantScope = "branch"
			}
			loader := newUIDetailChangesLoader(func(_ context.Context, cwd, name string, args []string, out, _ io.Writer) error {
				if cwd != "" || name != "git" || (args[1] != root && args[1] != work) {
					t.Fatalf("unsafe probe %q %s %v", cwd, name, args)
				}
				result := ""
				switch args[10] {
				case "rev-parse":
					result = head
					if args[len(args)-1] == base+"^{commit}" {
						result = base
					}
				case "symbolic-ref":
					result = "origin/main"
				case "merge-base":
					result = base
				case "branch":
					result = "main"
				case "status":
				case "diff":
					joined := strings.Join(args, " ")
					switch {
					case strings.Contains(joined, "--name-status"):
						result = "M\x00a\x00"
					case strings.Contains(joined, "--numstat"):
						result = "2\t1\ta\x00"
					default:
						result = "@@ -1 +1 @@\n-old\n+new\n"
					}
				default:
					t.Fatalf("unexpected command %v", args)
				}
				_, err := io.WriteString(out, result)
				return err
			})
			s, err := loader.Snapshot(context.Background(), detail, scope)
			if err != nil || s.Availability != "ready" || s.Scope != wantScope || s.WorktreeMissing != (mode == "missing") || len(s.Files) != 1 || s.Files[0].Path != "a" || s.Signature == "" || s.CollectedAt.IsZero() {
				t.Fatalf("snapshot %+v %v", s, err)
			}
			wantStrategy := "isolated"
			if mode == "current" {
				wantStrategy = "current"
			}
			if s.RepoRoot != root || s.Strategy != wantStrategy || s.Base.SHA != base {
				t.Fatalf("identity %+v", s)
			}
			d, err := loader.FileDiff(context.Background(), s, "a")
			if err != nil || d.Signature != s.Signature || len(d.Lines) != 3 {
				t.Fatalf("diff %+v %v", d, err)
			}
		})
	}
}

func TestUIChangesAppWiring(t *testing.T) {
	t.Setenv("PATH", "")
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	dir := writeRunPlan(t, t.TempDir(), "changes-plan", plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	input, keys := io.Pipe()
	defer func() { _ = input.Close() }()
	defer func() { _ = keys.Close() }()
	reached := make(chan struct{}, 1)
	var output bytes.Buffer
	app := App{
		In: input, Out: &output, Err: io.Discard,
		MonitorIsTerminal: func(io.Writer) bool { return true },
		MonitorCollector:  &monitorCollectorStub{snapshots: []monitor.Snapshot{{Rows: []monitor.Row{{Kind: monitor.RowKindPlan, PlanID: "changes-plan", PlanDir: dir, PlanTitle: "Changes plan", Status: plan.StatusInProgress}}}}},
		MonitorTicker: func(time.Duration) MonitorTicker {
			return &monitorTickerStub{ch: make(chan time.Time), stopped: make(chan struct{})}
		},
		UITerminal: &uiTerminalStub{size: term.Size{Width: 120, Height: 30}, resizes: make(chan struct{})},
		Registry:   func() NoteRegistry { return monitorRegistryStub{} },
		CommandRunner: func(_ context.Context, _, _ string, args []string, _, _ io.Writer) error {
			if strings.Contains(strings.Join(args, " "), "--no-optional-locks") {
				select {
				case reached <- struct{}{}:
				default:
				}
			}
			return errors.New("test probe unavailable")
		},
		RepoHealthCheck: func(context.Context, taodata.Repo) taodata.RepoHealth { return taodata.RepoHealth{} },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.ui(ctx, nil) }()
	go func() { _, _ = io.WriteString(keys, "\r\t\t\t") }()
	select {
	case <-reached:
	case <-ctx.Done():
		<-done
		t.Fatalf("Changes never reached injected runner: %s", output.String())
	}
	_, _ = io.WriteString(keys, "q")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUIChangesProjectionRoundTrip(t *testing.T) {
	merged := true
	s := plandelta.Snapshot{
		Target: plandelta.Target{PlanID: "p", RepoRoot: "/repo", WorktreePath: "/work", Branch: "feature/a", Strategy: "current", Separate: true, BranchIsNew: true},
		Scope:  plandelta.ScopeWorktree, Base: plandelta.Base{SHA: "base", Source: plandelta.BaseSourceWorkspace, DefaultBranch: "main", PlanBranch: "feature/a"},
		Head: "head", DefaultHead: "default", WorktreeMissing: true, ActiveOperation: "rebase", RebaseIntent: true, Dirty: true, Merged: &merged,
		Review:         plandelta.ReviewParity{Recorded: true, Verdict: "approve", Base: "review-base", Head: "review-head", BaseMatches: true, HeadMatches: true, Superseded: true},
		Files:          []plandelta.FileChange{{Path: "raw\tpath", OldPath: "old", Status: 'M', Added: 2, Deleted: 3, Binary: true, Untracked: true, Uncommitted: true, RevertsCommitted: true}},
		FilesTruncated: true, Total: plandelta.Stat{Files: 4, Added: 5, Deleted: 6}, Uncommitted: plandelta.Stat{Files: 1, Added: 2, Deleted: 3}, UntrackedCount: 1, Signature: "signature", Warnings: []string{"warning"}, Availability: plandelta.AvailabilityReady, Reason: "reason", CollectedAt: time.Unix(42, 0),
	}
	view := uiChangesSnapshot(s)
	view.Files[0].Display = "not the lookup path"
	if got := domainChangesSnapshot(view); !reflect.DeepEqual(got, s) {
		t.Fatalf("round trip = %+v, want %+v", got, s)
	}
}

func TestUIChangesDiffKinds(t *testing.T) {
	kinds := []plandelta.LineKind{plandelta.Context, plandelta.Add, plandelta.Del, plandelta.Hunk, plandelta.FileHeader, plandelta.Meta, plandelta.NoNewline, plandelta.Marker}
	want := []tui.DetailDiffLineKind{tui.DetailDiffContext, tui.DetailDiffAdd, tui.DetailDiffDel, tui.DetailDiffHunk, tui.DetailDiffFileHeader, tui.DetailDiffMeta, tui.DetailDiffNoNewline, tui.DetailDiffMarker}
	d := plandelta.FileDiff{Path: "raw\tpath", Signature: "sig", Binary: true, Truncated: true, Hunks: []int{3}}
	for _, k := range kinds {
		d.Lines = append(d.Lines, plandelta.Line{Kind: k, Text: " text"})
	}
	got := uiChangesFileDiff(d)
	if got.Path != d.Path || got.Signature != d.Signature || !got.Binary || !got.Truncated || !reflect.DeepEqual(got.Hunks, d.Hunks) {
		t.Fatalf("diff metadata: %+v", got)
	}
	for i, k := range want {
		if got.Lines[i].Kind != k || got.Lines[i].Text != " text" {
			t.Fatalf("line %d: %+v", i, got.Lines[i])
		}
	}
}

func TestUIChangesFileDiffWithoutSnapshot(t *testing.T) {
	for _, mode := range []string{"branch", "worktree", "current", "revert", "untracked"} {
		t.Run(mode, func(t *testing.T) {
			root, work := t.TempDir(), t.TempDir()
			path := ":(glob)*\tfile"
			s := tui.DetailChangesSnapshot{RepoRoot: root, WorktreePath: work, Branch: "feature/a", Head: "verified-head", Scope: "branch", Base: tui.DetailChangesBase{SHA: "base"}, Signature: "sig", Files: []tui.DetailChangedFile{{Path: path, Display: "display only"}}}
			wantRoot, revisions := root, []string{"base", "verified-head"}
			switch mode {
			case "worktree", "revert", "untracked":
				s.Scope = "worktree"
				wantRoot = work
				revisions = []string{"base"}
			case "current":
				s.Strategy = "current"
			}
			if mode == "revert" {
				s.Files[0].RevertsCommitted = true
				revisions = []string{"HEAD"}
			}
			if mode == "untracked" {
				s.Files[0].Untracked = true
				if err := os.WriteFile(filepath.Join(work, path), []byte("new\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			loader := newUIDetailChangesLoader(func(_ context.Context, cwd, name string, args []string, out, _ io.Writer) error {
				calls++
				if cwd != "" || name != "git" || args[1] != wantRoot {
					t.Fatalf("command %q %q %v", cwd, name, args)
				}
				tail := append(append([]string{}, revisions...), "--", path)
				if !reflect.DeepEqual(args[len(args)-len(tail):], tail) || !strings.Contains(strings.Join(args, " "), "--literal-pathspecs") {
					t.Fatalf("args %v", args)
				}
				_, err := io.WriteString(out, "@@ -1 +1 @@\n-old\n+new\n")
				return err
			})
			if calls != 0 {
				t.Fatal("construction performed I/O")
			}
			d, err := loader.FileDiff(context.Background(), s, path)
			if err != nil || d.Path != path || d.Signature != "sig" || len(d.Lines) == 0 {
				t.Fatalf("diff %+v %v", d, err)
			}
			if mode == "untracked" {
				if calls != 0 {
					t.Fatal("untracked used Git")
				}
			} else if calls != 1 {
				t.Fatalf("calls %d", calls)
			}
			if mode == "revert" && !strings.Contains(d.Lines[0].Text, "relative to HEAD") {
				t.Fatalf("lost revert flag: %+v", d)
			}
		})
	}
}

func TestUIChangesFailureAndCancellation(t *testing.T) {
	loader := newUIDetailChangesLoader(func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		return errors.New("failure\x1b[31m\nunsafe")
	})
	s := tui.DetailChangesSnapshot{RepoRoot: t.TempDir(), Branch: "feature/a", Head: "verified-head", Scope: "branch", Base: tui.DetailChangesBase{SHA: "base"}, Files: []tui.DetailChangedFile{{Path: "a"}}}
	_, err := loader.FileDiff(context.Background(), s, "a")
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\n") {
		t.Fatalf("unsafe failure: %v", err)
	}
	d := &plan.PlanDetail{State: plan.State{Repo: plan.Repo{Root: s.RepoRoot}, Plan: plan.PlanState{ID: "p"}}}
	snap, err := loader.Snapshot(context.Background(), d, "")
	if err != nil || snap.Availability != "git_error" || strings.ContainsAny(snap.Reason, "\x1b\n") {
		t.Fatalf("snapshot %+v %v", snap, err)
	}
	for _, operation := range []string{"snapshot", "diff"} {
		ctx, cancel := context.WithCancel(context.Background())
		inFlight := newUIDetailChangesLoader(func(got context.Context, _, _ string, _ []string, _, _ io.Writer) error {
			if got != ctx {
				t.Error("adapter replaced context")
			}
			cancel()
			return got.Err()
		})
		if operation == "snapshot" {
			_, err = inFlight.Snapshot(ctx, d, "")
		} else {
			_, err = inFlight.FileDiff(ctx, s, "a")
		}
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("%s cancellation: %v", operation, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = loader.Snapshot(ctx, d, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err = loader.FileDiff(ctx, s, "a"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
