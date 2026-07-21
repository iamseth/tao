package gitops

import (
	"context"
	"reflect"
	"testing"
)

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
