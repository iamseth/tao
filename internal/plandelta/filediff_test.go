package plandelta

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

func TestParseUnifiedDiff(t *testing.T) {
	raw := "diff --git a/x b/x\nold mode 100644\nnew mode 100755\nindex abc..def\n--- a/x\n+++ b/x\n@@ -1 +1 @@\n----payload\n+++payload\n\\ No newline at end of file\n"
	got := ParseUnifiedDiff(raw, false)
	want := []LineKind{Meta, Meta, Hunk, Del, Add, NoNewline}
	var kinds []LineKind
	for _, l := range got.Lines {
		kinds = append(kinds, l.Kind)
	}
	if !reflect.DeepEqual(kinds, want) || !reflect.DeepEqual(got.Hunks, []int{2}) {
		t.Fatalf("%+v", got)
	}
	if got.Lines[3].Text != "----payload" || got.Lines[4].Text != "+++payload" {
		t.Fatal(got)
	}
	if d := ParseUnifiedDiff("Binary files a/x and b/x differ\n", false); !d.Binary || len(d.Lines) != 1 {
		t.Fatal(d)
	}
	capped := ParseUnifiedDiff("@@ -1 +1 @@\n"+strings.Repeat("+x\n", MaxFileDiffLines+1), true)
	if !capped.Truncated || len(capped.Lines) != MaxFileDiffLines+2 || capped.Lines[MaxFileDiffLines].Text != "[showing first 4000 lines]" || capped.Lines[MaxFileDiffLines+1].Text != "[diff truncated at 512 KiB]" {
		t.Fatalf("lines=%d tail=%+v", len(capped.Lines), capped.Lines[len(capped.Lines)-2:])
	}
}

func TestParseUnifiedDiffByteBound(t *testing.T) {
	d := ParseUnifiedDiff("@@ -1 +1 @@\n+"+strings.Repeat("x", MaxFileDiffBytes), false)
	if !d.Truncated || len(d.Lines) != 3 || d.Lines[2].Text != "[diff truncated at 512 KiB]" {
		t.Fatalf("%+v", d)
	}
	// Header-looking lines without a real preamble are retained as metadata.
	d = ParseUnifiedDiff("index not a preamble\n--- not paired\n", false)
	if len(d.Lines) != 2 || d.Lines[0].Text != "index not a preamble" {
		t.Fatalf("%+v", d)
	}
}

func TestFileDiffSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		scope    Scope
		strategy string
		revert   bool
		root     string
		refs     []string
	}{
		{"branch", ScopeBranch, workspace.StrategyWorktree, false, "/repo", []string{"base", "verified-head"}},
		{"current", ScopeBranch, workspace.StrategyCurrent, false, "/repo", []string{"base", "verified-head"}},
		{"worktree", ScopeWorktree, workspace.StrategyWorktree, false, "/worktree", []string{"base"}},
		{"revert", ScopeWorktree, workspace.StrategyWorktree, true, "/worktree", []string{"HEAD"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := ":(glob)*\tfile"
			called := false
			c := NewCollector(func(_ context.Context, cwd, name string, args []string, stdout, _ io.Writer) error {
				called = true
				if cwd != "" || name != "git" || args[1] != tc.root {
					t.Fatalf("%s %s %v", cwd, name, args)
				}
				tail := append(append([]string{}, tc.refs...), "--", path)
				if !reflect.DeepEqual(args[len(args)-len(tail):], tail) || !strings.Contains(strings.Join(args, " "), "--literal-pathspecs") {
					t.Fatal(args)
				}
				_, err := io.WriteString(stdout, "@@ -1 +1 @@\n-a\n+b\n")
				return err
			})
			s := Snapshot{Target: Target{RepoRoot: "/repo", WorktreePath: "/worktree", Branch: "feature", Strategy: tc.strategy}, Scope: tc.scope, Base: Base{SHA: "base"}, Head: "verified-head", Signature: "sig", Files: []FileChange{{Path: path, RevertsCommitted: tc.revert}}}
			d, err := c.FileDiff(context.Background(), s, path)
			if err != nil || !called || d.Signature != "sig" || d.Path != path {
				t.Fatalf("%+v %v", d, err)
			}
			if tc.revert && (d.Lines[0].Kind != Meta || d.Hunks[0] != 1) {
				t.Fatal(d)
			}
			called = false
			if _, err := c.FileDiff(context.Background(), s, "missing"); err == nil || called {
				t.Fatal("lookup not refused")
			}
			for _, invalid := range []string{"../escape", "/absolute", "a/../b", "nul\x00", ".git/config", ".tao/state"} {
				s.Files = []FileChange{{Path: invalid}}
				if _, err := c.FileDiff(context.Background(), s, invalid); err == nil || called {
					t.Fatalf("invalid path %q accepted", invalid)
				}
			}
		})
	}
}

func TestFileDiffBranchWithSameNamedTag(t *testing.T) {
	root := t.TempDir()
	git := localDeltaGit(t, root)
	git("init", "-b", "main")
	path := "changed.txt"
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write("base\n")
	git("add", path)
	git("commit", "-m", "base")
	base := git("rev-parse", "HEAD")
	git("tag", "feature", base)
	write("branch content\n")
	git("commit", "-am", "branch change")
	head := git("rev-parse", "HEAD")
	git("branch", "feature", head)
	git("reset", "--hard", base)

	detail := &plan.PlanDetail{}
	detail.State.Plan.ID = "plan-a"
	detail.State.Repo.Root = root
	detail.State.Repo.BaseCommit = base
	detail.State.Workspace = &plan.Workspace{Branch: "feature", BaseBranch: "main", BaseSHA: base, Strategy: plan.WorkspaceStrategyWorktree}
	c := NewCollector(nil)
	s, err := c.Snapshot(context.Background(), detail, ScopeBranch)
	if err != nil || s.Availability != AvailabilityReady || s.Head != head || len(s.Files) != 1 || s.Files[0].Path != path || s.Total != (Stat{1, 1, 1}) {
		t.Fatalf("FILES: %+v, %v", s, err)
	}
	d, err := c.FileDiff(context.Background(), s, path)
	if err != nil || d.Signature != s.Signature || d.Path != path {
		t.Fatalf("DIFF: %+v, %v", d, err)
	}
	want := []Line{{Hunk, "@@ -1 +1 @@"}, {Del, "-base"}, {Add, "+branch content"}}
	if !reflect.DeepEqual(d.Lines, want) {
		t.Fatalf("DIFF does not match branch FILES: got %+v, want %+v", d.Lines, want)
	}
}

func TestFileDiffFailure(t *testing.T) {
	c := NewCollector(func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		return errors.New("\x1bunsafe\n" + strings.Repeat("x", 500))
	})
	s := Snapshot{Target: Target{RepoRoot: "/repo", Branch: "feature"}, Scope: ScopeBranch, Base: Base{SHA: "base"}, Head: "verified-head", Files: []FileChange{{Path: "x"}}}
	_, err := c.FileDiff(context.Background(), s, "x")
	if err == nil || strings.ContainsAny(err.Error(), "\x1b\n") || len(err.Error()) > MaxReasonChars*3 {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.FileDiff(ctx, s, "x"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func FuzzParseUnifiedDiff(f *testing.F) {
	for _, seed := range []string{"", "@@ -1 +1 @@\n+++data\n----data", "diff --git a/x b/x\nBinary files a/x and b/x differ", "\xff\x1b\u202e"} {
		f.Add(seed, false)
	}
	f.Fuzz(func(t *testing.T, text string, truncated bool) {
		d := ParseUnifiedDiff(text, truncated)
		if len(d.Lines) > MaxFileDiffLines+2 {
			t.Fatal(len(d.Lines))
		}
		for _, h := range d.Hunks {
			if h < 0 || h >= len(d.Lines) || d.Lines[h].Kind != Hunk {
				t.Fatal(h)
			}
		}
		for _, l := range d.Lines {
			if strings.ContainsAny(l.Text, "\x1b\r\n\t") {
				t.Fatalf("unsafe %q", l.Text)
			}
		}
	})
}
