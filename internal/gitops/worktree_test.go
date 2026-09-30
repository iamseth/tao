package gitops

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMainWorktreeRoot(t *testing.T) {
	discovery := key("-C", "/launch", "worktree", "list", "--porcelain", "-z")
	fallback := key("-C", "/launch", "rev-parse", "--show-toplevel")
	failed := errors.New("git failed")
	for _, tt := range []struct {
		name, output, fallback, want            string
		candidate, checkout                     string
		discoveryErr, fallbackErr, candidateErr error
	}{
		{name: "ordinary", output: "worktree /main\x00HEAD abc\x00\x00", candidate: "/main", checkout: "/main\n", want: "/main"},
		{name: "linked", output: "worktree /main\x00HEAD abc\x00\x00worktree /launch\x00HEAD def\x00\x00", candidate: "/main", checkout: "/main\n", want: "/main"},
		{name: "quoting characters", output: "worktree /main \"tab\tline\nslash\\é \x00HEAD abc\x00\x00", candidate: "/main \"tab\tline\nslash\\é ", checkout: "/main \"tab\tline\nslash\\é \n", want: "/main \"tab\tline\nslash\\é "},
		{name: "submodule", output: "worktree /super/.git/modules/sub\x00HEAD abc\x00\x00", candidate: "/super/.git/modules/sub", checkout: "/super/sub\n", want: "/super/sub"},
		{name: "unusable candidate", output: "worktree /missing\x00HEAD abc\x00\x00", candidate: "/missing", candidateErr: failed, fallback: "/launch\n", want: "/launch"},
		{name: "empty candidate checkout", output: "worktree /missing\x00HEAD abc\x00\x00", candidate: "/missing", fallback: "/launch\n", want: "/launch"},
		{name: "empty", fallback: "/launch\n", want: "/launch"},
		{name: "discovery error", discoveryErr: failed, fallback: "/launch\n", want: "/launch"},
		{name: "fallback error", fallbackErr: failed},
		{name: "empty fallback"},
		{name: "bare", output: "worktree /bare.git\x00bare\x00\x00", fallbackErr: failed},
		{name: "bare with linked checkout", output: "worktree /bare.git\x00bare\x00\x00worktree /launch\x00HEAD abc\x00\x00", fallback: "/launch\n", want: "/launch"},
		{name: "fallback whitespace", fallback: "/launch \t\n\n", want: "/launch \t\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := &fakeRunner{outputs: map[string]string{discovery: tt.output, fallback: tt.fallback}, failures: map[string]error{discovery: tt.discoveryErr, fallback: tt.fallbackErr}}
			if tt.candidate != "" {
				command := key("-C", tt.candidate, "rev-parse", "--show-toplevel")
				runner.outputs[command] = tt.checkout
				runner.failures[command] = tt.candidateErr
			}
			got, err := NewClient("/launch", runner.run).MainWorktreeRoot(context.Background())
			if got != tt.want || (err != nil) != (tt.want == "") {
				t.Fatalf("MainWorktreeRoot() = %q, %v; want %q", got, err, tt.want)
			}
			if tt.fallbackErr != nil && !errors.Is(err, tt.fallbackErr) {
				t.Fatalf("lost fallback error: %v", err)
			}
			wantCalls := 2
			if tt.candidate != "" && tt.fallback != "" {
				wantCalls = 3
			}
			if len(runner.calls) != wantCalls {
				t.Fatalf("calls = %v", runner.calls)
			}
		})
	}
}

func TestMainWorktreeRootGitDirOverride(t *testing.T) {
	launch := t.TempDir()
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, launch, "init", "-b", "main")
	runGitCommand(t, other, "init", "-b", "main")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	got, err := NewClient(launch, nil).MainWorktreeRoot(context.Background())
	if err != nil || got != other {
		t.Fatalf("GIT_DIR override: got %q, %v; want %q", got, err, other)
	}

	bare := t.TempDir()
	runGitCommand(t, launch, "init", "--bare", bare)
	t.Setenv("GIT_DIR", bare)
	got, err = NewClient(launch, nil).MainWorktreeRoot(context.Background())
	if err == nil || got != "" {
		t.Fatalf("bare GIT_DIR override: got %q, %v", got, err)
	}
}

func TestMainWorktreeRootCancellation(t *testing.T) {
	for _, before := range []bool{true, false} {
		ctx, cancel := context.WithCancel(context.Background())
		calls := 0
		runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			calls++
			cancel()
			return errors.New("interrupted")
		}
		if before {
			cancel()
		}
		_, err := NewClient("/launch", runner).MainWorktreeRoot(ctx)
		cancel()
		if !errors.Is(err, context.Canceled) || calls > 1 || (before && calls != 0) {
			t.Fatalf("before=%v: error=%v calls=%d", before, err, calls)
		}
	}
}

func TestWorktreeCommandConstruction(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{}, stderr: map[string]string{}, failures: map[string]error{}}
	client := NewClient("/repo", runner.run)
	ctx := context.Background()

	if err := client.AddWorktree(ctx, "/repo/.tao/workspaces/plan-a", "tao/plan-a", "main", true); err != nil {
		t.Fatal(err)
	}
	if err := client.AddWorktree(ctx, "/repo/.tao/workspaces/plan-b", "tao/plan-b", "", false); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveWorktree(ctx, "/repo/.tao/workspaces/plan-a", false); err != nil {
		t.Fatal(err)
	}
	if err := client.RemoveWorktree(ctx, "/repo/.tao/workspaces/plan-b", true); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"-C", "/repo", "worktree", "add", "-b", "tao/plan-a", "/repo/.tao/workspaces/plan-a", "main"},
		{"-C", "/repo", "worktree", "add", "/repo/.tao/workspaces/plan-b", "tao/plan-b"},
		{"-C", "/repo", "worktree", "remove", "/repo/.tao/workspaces/plan-a"},
		{"-C", "/repo", "worktree", "remove", "--force", "/repo/.tao/workspaces/plan-b"},
	}
	for i, args := range want {
		if !reflect.DeepEqual(runner.calls[i].args, args) {
			t.Fatalf("call %d args mismatch: want %#v got %#v", i, args, runner.calls[i].args)
		}
	}
}

func TestWorktreeStatusCommandConstructionAndDirtyParsing(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/worktree", "branch", "--show-current"): "tao/plan-a\n",
			key("-C", "/worktree", "rev-parse", "HEAD"):        "abc123\n",
			key("-C", "/worktree", "status", "--porcelain"):    " M a.go\n",
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)

	status, err := client.WorktreeStatus(context.Background(), "/worktree")
	if err != nil {
		t.Fatal(err)
	}
	wantStatus := WorktreeStatus{Branch: "tao/plan-a", HEAD: "abc123", Dirty: true}
	if status != wantStatus {
		t.Fatalf("status mismatch: want %#v got %#v", wantStatus, status)
	}
	wantArgs := [][]string{
		{"-C", "/worktree", "branch", "--show-current"},
		{"-C", "/worktree", "rev-parse", "HEAD"},
		{"-C", "/worktree", "status", "--porcelain"},
	}
	for i, args := range wantArgs {
		if !reflect.DeepEqual(runner.calls[i].args, args) {
			t.Fatalf("call %d args mismatch: want %#v got %#v", i, args, runner.calls[i].args)
		}
	}
}

func TestWorktreePorcelainParsing(t *testing.T) {
	input := "worktree /repo\nHEAD abc123\nbranch refs/heads/master\n\nworktree /repo/.tao/workspaces/plan-a\nHEAD def456\nbranch refs/heads/tao/plan-a\n"
	want := []Worktree{
		{Path: "/repo", HEAD: "abc123", Branch: "master"},
		{Path: "/repo/.tao/workspaces/plan-a", HEAD: "def456", Branch: "tao/plan-a"},
	}
	if got := ParseWorktreePorcelain(input); !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed worktrees mismatch\nwant %#v\n got %#v", want, got)
	}
}

func TestWorktreesUsesPorcelainList(t *testing.T) {
	runner := &fakeRunner{
		outputs:  map[string]string{key("-C", "/repo", "worktree", "list", "--porcelain"): "worktree /repo\nHEAD abc123\nbranch refs/heads/master\n"},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)
	worktrees, err := client.Worktrees(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(worktrees) != 1 || worktrees[0].Path != "/repo" || worktrees[0].Branch != "master" || worktrees[0].HEAD != "abc123" {
		t.Fatalf("unexpected worktrees: %#v", worktrees)
	}
}
