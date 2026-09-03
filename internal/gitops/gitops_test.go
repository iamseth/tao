package gitops

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type call struct {
	cwd  string
	name string
	args []string
}

type fakeRunner struct {
	calls    []call
	outputs  map[string]string
	stderr   map[string]string
	failures map[string]error
}

func (r *fakeRunner) run(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
	_ = ctx
	copied := append([]string(nil), args...)
	r.calls = append(r.calls, call{cwd: cwd, name: name, args: copied})
	key := strings.Join(args, "\x00")
	_, _ = io.WriteString(stdout, r.outputs[key])
	_, _ = io.WriteString(stderr, r.stderr[key])
	if err := r.failures[key]; err != nil {
		return err
	}
	return nil
}

func TestDiffBoundedDrainsOversizedOutput(t *testing.T) {
	const (
		chunkCount = 32
		chunkSize  = 1024
		limit      = 4096
	)
	drained := 0
	runner := func(_ context.Context, _ string, name string, args []string, stdout io.Writer, _ io.Writer) error {
		if name != "git" || !reflect.DeepEqual(args, []string{"-C", "/repo", "diff", "base..head"}) {
			t.Fatalf("command = %s %#v", name, args)
		}
		chunk := strings.Repeat("x", chunkSize)
		for range chunkCount {
			n, err := io.WriteString(stdout, chunk)
			drained += n
			if err != nil {
				return err
			}
		}
		return nil
	}
	got, truncated, err := NewClient("/repo", runner).DiffBounded(context.Background(), "base..head", limit)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(got) != limit || got != strings.Repeat("x", limit) {
		t.Fatalf("bounded diff = (%d bytes, truncated=%t)", len(got), truncated)
	}
	if drained != chunkCount*chunkSize {
		t.Fatalf("drained bytes = %d, want %d", drained, chunkCount*chunkSize)
	}
}

func TestActiveOperationAndLinkedWorktreeDirectory(t *testing.T) {
	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(filepath.Join(gitDir, "rebase-merge"), 0o750); err != nil {
		t.Fatal(err)
	}
	operation, err := ActiveOperation(root)
	if err != nil {
		t.Fatal(err)
	}
	if operation != "rebase" {
		t.Fatalf("ActiveOperation() = %q, want rebase", operation)
	}

	common := filepath.Join(root, "common")
	linked, err := IsLinkedWorktreeDirectory(common, filepath.Join(common, "worktrees", "plan-a"))
	if err != nil {
		t.Fatal(err)
	}
	if !linked {
		t.Fatal("expected worktrees/plan-a metadata to identify a linked worktree")
	}
	linked, err = IsLinkedWorktreeDirectory(common, common)
	if err != nil {
		t.Fatal(err)
	}
	if linked {
		t.Fatal("common repository metadata must not identify a linked worktree")
	}
}

func TestClientRootReturnsBinding(t *testing.T) {
	if got := NewClient("/some/repo", nil).Root(); got != "/some/repo" {
		t.Fatalf("Root() = %q, want %q", got, "/some/repo")
	}
	// Empty root means Git resolves the path to the process CWD (-C "" convention).
	if got := NewClient("", nil).Root(); got != "" {
		t.Fatalf("Root() for empty binding = %q, want empty string", got)
	}
}

func TestClientConstructsTypedGitCommands(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{}, stderr: map[string]string{}, failures: map[string]error{}}
	client := NewClient("/repo", runner.run)
	ctx := context.Background()

	if _, err := client.CurrentBranch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.RevParse(ctx, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := client.Checkout(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := client.Add(ctx, "a.go", "b.go"); err != nil {
		t.Fatal(err)
	}
	if err := client.RestoreStaged(ctx, ".tao/state.json"); err != nil {
		t.Fatal(err)
	}
	if err := client.Commit(ctx, "feat: test"); err != nil {
		t.Fatal(err)
	}

	want := []call{
		{cwd: "", name: "git", args: []string{"-C", "/repo", "branch", "--show-current"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "rev-parse", "HEAD"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "checkout", "main"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "add", "--", "a.go", "b.go"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "restore", "--staged", "--", ".tao/state.json"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "commit", "-m", "feat: test"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls mismatch\nwant: %#v\n got: %#v", want, runner.calls)
	}
}

func TestIntegrationPrimitivesConstructCommands(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/repo", "merge-base", "main", "feature"): " abc123 \n",
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)
	ctx := context.Background()

	base, err := client.MergeBase(ctx, "main", "feature")
	if err != nil {
		t.Fatal(err)
	}
	if base != "abc123" {
		t.Fatalf("expected trimmed merge base, got %q", base)
	}
	if err := client.MergeFFOnly(ctx, "feature"); err != nil {
		t.Fatal(err)
	}
	if err := client.MergeSquash(ctx, "feature"); err != nil {
		t.Fatal(err)
	}
	changed, err := client.HasStagedChanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("successful diff --quiet should report no staged changes")
	}
	if err := client.CleanUntracked(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Rebase(ctx, "main"); err != nil {
		t.Fatal(err)
	}
	if err := client.RebaseAbort(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.ResetHard(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}

	want := []call{
		{cwd: "", name: "git", args: []string{"-C", "/repo", "merge-base", "main", "feature"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "merge", "--ff-only", "feature"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "merge", "--squash", "feature"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "diff", "--cached", "--quiet"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "clean", "-fdx"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "rebase", "main"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "rebase", "--abort"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "reset", "--hard", "abc123"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls mismatch\nwant: %#v\n got: %#v", want, runner.calls)
	}
}

func TestStandaloneCommitPrimitivesConstructScopedCommands(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/repo", "status", "--porcelain", "--untracked-files=all"): "?? nested/a.go\n",
			key("-C", "/repo", "diff", "HEAD", "--", "a.go"):                     "diff-a\n",
			key("-C", "/repo", "log", "--oneline", "-12"):                        "abc feat(a): change\n",
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)
	ctx := context.Background()
	if status, err := client.StatusPorcelainAllUntracked(ctx); err != nil || status != "?? nested/a.go\n" {
		t.Fatalf("StatusPorcelainAllUntracked() = %q, %v", status, err)
	}
	if diff, err := client.WorkingDiff(ctx, "a.go"); err != nil || diff != "diff-a\n" {
		t.Fatalf("WorkingDiff() = %q, %v", diff, err)
	}
	if log, err := client.RecentLog(ctx, 12); err != nil || log != "abc feat(a): change\n" {
		t.Fatalf("RecentLog() = %q, %v", log, err)
	}
	want := []call{
		{cwd: "", name: "git", args: []string{"-C", "/repo", "status", "--porcelain", "--untracked-files=all"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "diff", "HEAD", "--", "a.go"}},
		{cwd: "", name: "git", args: []string{"-C", "/repo", "log", "--oneline", "-12"}},
	}
	if !reflect.DeepEqual(runner.calls, want) {
		t.Fatalf("calls mismatch\nwant: %#v\n got: %#v", want, runner.calls)
	}
	if _, err := client.RecentLog(ctx, 0); err == nil {
		t.Fatal("RecentLog accepted a non-positive limit")
	}
}

func TestMergeFFOnlyWrapsFailureWithClearContext(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{},
		stderr: map[string]string{
			key("-C", "/repo", "merge", "--ff-only", "feature"): "fatal: Not possible to fast-forward, aborting.\n",
		},
		failures: map[string]error{
			key("-C", "/repo", "merge", "--ff-only", "feature"): errors.New("exit status 1"),
		},
	}
	client := NewClient("/repo", runner.run)

	err := client.MergeFFOnly(context.Background(), "feature")
	if err == nil {
		t.Fatal("expected error")
	}
	want := "fast-forward merge \"feature\": git merge --ff-only feature: exit status 1: fatal: Not possible to fast-forward, aborting."
	if got := err.Error(); got != want {
		t.Fatalf("error mismatch\nwant %q\n got %q", want, got)
	}
}

func TestCommitPathsCommitsOnlyGivenPaths(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	runGitCommand(t, root, "config", "user.name", "Tao Test")
	runGitCommand(t, root, "config", "user.email", "tao@example.invalid")
	writeRepoFile(t, root, "a.txt", "before\n")
	writeRepoFile(t, root, "b.txt", "before\n")
	runGitCommand(t, root, "add", "a.txt", "b.txt")
	runGitCommand(t, root, "commit", "-m", "initial")

	writeRepoFile(t, root, "a.txt", "after a\n")
	writeRepoFile(t, root, "b.txt", "after b\n")
	runGitCommand(t, root, "add", "a.txt", "b.txt")

	client := NewClient(root, nil)
	if err := client.CommitPaths(ctx, "feat: scoped", "a.txt"); err != nil {
		t.Fatal(err)
	}
	committed, err := client.rawOutput(ctx, "show", "--name-only", "--format=", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(committed), "a.txt"; got != want {
		t.Fatalf("committed paths mismatch: got %q want %q", got, want)
	}
	cached, err := client.rawOutput(ctx, "diff", "--cached", "--name-only")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(cached), "b.txt"; got != want {
		t.Fatalf("staged paths mismatch: got %q want %q", got, want)
	}
}

func TestCommitPathsRequiresPaths(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{}, stderr: map[string]string{}, failures: map[string]error{}}
	client := NewClient("/repo", runner.run)

	err := client.CommitPaths(context.Background(), "feat: empty")
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "commit paths: at least one path is required"; got != want {
		t.Fatalf("error mismatch: got %q want %q", got, want)
	}
	if len(runner.calls) != 0 {
		t.Fatalf("expected no git calls, got %#v", runner.calls)
	}
}

func TestOutputTrimmingAndRawOutput(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/repo", "branch", "--show-current"):      " feature \n",
			key("-C", "/repo", "status", "--porcelain"):         " M a.go\n?? b.go\n",
			key("-C", "/repo", "diff", "main...HEAD"):           "diff --git a/a.go b/a.go\n+line\n",
			key("-C", "/repo", "diff", "--stat", "main...HEAD"): " a.go | 1 +\n",
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)
	ctx := context.Background()

	branch, err := client.CurrentBranch(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "feature" {
		t.Fatalf("expected trimmed branch, got %q", branch)
	}
	status, err := client.StatusPorcelain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if status != " M a.go\n?? b.go\n" {
		t.Fatalf("expected raw status, got %q", status)
	}
	diff, err := client.Diff(ctx, "main...HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if diff != "diff --git a/a.go b/a.go\n+line\n" {
		t.Fatalf("expected raw diff, got %q", diff)
	}
	stat, err := client.DiffStat(ctx, "main...HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if stat != " a.go | 1 +\n" {
		t.Fatalf("expected raw diff stat, got %q", stat)
	}
}

func TestErrorFormattingIncludesGitCommandAndStderr(t *testing.T) {
	runner := &fakeRunner{
		outputs:  map[string]string{},
		stderr:   map[string]string{key("-C", "/repo", "status", "--porcelain"): "fatal: not a git repository\n"},
		failures: map[string]error{key("-C", "/repo", "status", "--porcelain"): errors.New("exit status 128")},
	}
	client := NewClient("/repo", runner.run)

	_, err := client.StatusPorcelain(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := err.Error(), "git status --porcelain: exit status 128: fatal: not a git repository"; got != want {
		t.Fatalf("error mismatch\nwant %q\n got %q", want, got)
	}
}

func TestDefaultBranchUsesOriginHeadBeforeFallbacks(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/repo", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"): "origin/trunk\n",
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)

	branch, err := client.DefaultBranch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if branch != "trunk" {
		t.Fatalf("expected origin HEAD branch, got %q", branch)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("expected no fallback calls, got %#v", runner.calls)
	}
}

func TestDefaultBranchFallsBackToMainThenMaster(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/repo", "branch", "--format=%(refname:short)", "--list", "master"): "master\n",
		},
		stderr: map[string]string{
			key("-C", "/repo", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"): "fatal: ref missing\n",
		},
		failures: map[string]error{
			key("-C", "/repo", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"): errors.New("exit status 1"),
		},
	}
	client := NewClient("/repo", runner.run)

	branch, err := client.DefaultBranch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if branch != "master" {
		t.Fatalf("expected fallback master, got %q", branch)
	}
	wantArgs := [][]string{
		{"-C", "/repo", "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"},
		{"-C", "/repo", "branch", "--format=%(refname:short)", "--list", "main"},
		{"-C", "/repo", "branch", "--format=%(refname:short)", "--list", "master"},
	}
	if len(runner.calls) != len(wantArgs) {
		t.Fatalf("expected %d calls, got %#v", len(wantArgs), runner.calls)
	}
	for i, want := range wantArgs {
		if !reflect.DeepEqual(runner.calls[i].args, want) {
			t.Fatalf("call %d args mismatch: want %#v got %#v", i, want, runner.calls[i].args)
		}
	}
}

func TestLocalBranchExistsParsesListedBranches(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/repo", "branch", "--format=%(refname:short)", "--list", "feature"): "feature\n",
			key("-C", "/repo", "branch", "--format=%(refname:short)", "--list", "missing"): "",
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)

	exists, err := client.LocalBranchExists(context.Background(), "feature")
	if err != nil || !exists {
		t.Fatalf("expected feature to exist, exists=%v err=%v", exists, err)
	}
	exists, err = client.LocalBranchExists(context.Background(), "missing")
	if err != nil || exists {
		t.Fatalf("expected missing not to exist, exists=%v err=%v", exists, err)
	}
}

func TestPullRequestRemoteAndPushOperationsConstructExactCommands(t *testing.T) {
	branch := "feature/native-pr-format"
	ref := "refs/heads/" + branch
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/repo", "config", "--get", "remote.origin.url"):             "git@github.com:owner/fetch.git\n",
			key("-C", "/repo", "remote", "get-url", "--push", "origin"):            "git@github.com:owner/push.git\n",
			key("-C", "/repo", "ls-remote", "--heads", "origin", ref):              "abc123\t" + ref + "\n",
			key("-C", "/repo", "ls-remote", "--heads", "origin", "refs/heads/new"): "",
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)
	ctx := context.Background()

	remoteURL, err := client.RemoteURL(ctx)
	if err != nil || remoteURL != "git@github.com:owner/fetch.git" {
		t.Fatalf("RemoteURL() = %q, %v", remoteURL, err)
	}
	pushURL, err := client.OriginPushURL(ctx)
	if err != nil || pushURL != "git@github.com:owner/push.git" {
		t.Fatalf("OriginPushURL() = %q, %v", pushURL, err)
	}
	head, found, err := client.OriginRemoteHead(ctx, branch)
	if err != nil || !found || head != "abc123" {
		t.Fatalf("OriginRemoteHead() = %q, %v, %v", head, found, err)
	}
	if head, found, err := client.OriginRemoteHead(ctx, "new"); err != nil || found || head != "" {
		t.Fatalf("missing OriginRemoteHead() = %q, %v, %v", head, found, err)
	}
	if err := client.PushUpstream(ctx, branch); err != nil {
		t.Fatal(err)
	}
	if err := client.PushUpstreamWithLease(ctx, "new", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.PushUpstreamWithLease(ctx, branch, "abc123"); err != nil {
		t.Fatal(err)
	}

	want := [][]string{
		{"-C", "/repo", "config", "--get", "remote.origin.url"},
		{"-C", "/repo", "remote", "get-url", "--push", "origin"},
		{"-C", "/repo", "ls-remote", "--heads", "origin", ref},
		{"-C", "/repo", "ls-remote", "--heads", "origin", "refs/heads/new"},
		{"-C", "/repo", "push", "--set-upstream", "origin", branch},
		{"-C", "/repo", "push", "--set-upstream", "--force-with-lease=refs/heads/new:", "origin", "new:refs/heads/new"},
		{"-C", "/repo", "push", "--set-upstream", "--force-with-lease=" + ref + ":abc123", "origin", branch + ":" + ref},
	}
	if len(runner.calls) != len(want) {
		t.Fatalf("calls = %#v, want %d calls", runner.calls, len(want))
	}
	for i, args := range want {
		if !reflect.DeepEqual(runner.calls[i].args, args) {
			t.Fatalf("call %d args mismatch: want %#v got %#v", i, args, runner.calls[i].args)
		}
	}
}

func TestPullRequestRemoteAndPushOperationsReportErrors(t *testing.T) {
	branch := "feature/native-pr-format"
	ref := "refs/heads/" + branch
	lookup := key("-C", "/repo", "ls-remote", "--heads", "origin", ref)
	push := key("-C", "/repo", "push", "--set-upstream", "--force-with-lease="+ref+":abc123", "origin", branch+":"+ref)
	runner := &fakeRunner{
		outputs: map[string]string{
			lookup: "abc123\trefs/heads/other\n",
		},
		stderr: map[string]string{
			push: "rejected: stale info\n",
		},
		failures: map[string]error{
			push: errors.New("exit status 1"),
		},
	}
	client := NewClient("/repo", runner.run)

	if _, _, err := client.OriginRemoteHead(context.Background(), branch); err == nil || !strings.Contains(err.Error(), "unexpected git ls-remote output") {
		t.Fatalf("OriginRemoteHead() error = %v", err)
	}
	if err := client.PushUpstreamWithLease(context.Background(), branch, "abc123"); err == nil || !strings.Contains(err.Error(), "git push --set-upstream") || !strings.Contains(err.Error(), "rejected: stale info") {
		t.Fatalf("PushUpstreamWithLease() error = %v", err)
	}
}

func TestRemoteTrackingBranchExistsMatchesExactBranchAcrossRemotes(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/repo", "for-each-ref", "--format=%(refname)", "refs/remotes"): strings.Join([]string{
				"refs/remotes/origin/feature/native-pr-format",
				"refs/remotes/upstream/fix/other",
			}, "\n"),
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)

	exists, err := client.RemoteTrackingBranchExists(context.Background(), "feature/native-pr-format")
	if err != nil || !exists {
		t.Fatalf("expected exact origin branch to exist, exists=%v err=%v", exists, err)
	}
	exists, err = client.RemoteTrackingBranchExists(context.Background(), "native-pr-format")
	if err != nil || exists {
		t.Fatalf("unexpected suffix-only match, exists=%v err=%v", exists, err)
	}
}

func TestRemoteBranchExistsQueriesConfiguredRemotesForExactRef(t *testing.T) {
	branch := "feature/native-pr-format"
	ref := "refs/heads/" + branch
	runner := &fakeRunner{
		outputs: map[string]string{
			key("-C", "/repo", "remote"):                                                          "origin\nupstream\n",
			key("-C", "/repo", "ls-remote", "--heads", "origin", ref):                             "",
			key("-C", "/repo", "ls-remote", "--heads", "upstream", ref):                           "abc123\t" + ref + "\n",
			key("-C", "/repo", "ls-remote", "--heads", "origin", "refs/heads/native-pr-format"):   "",
			key("-C", "/repo", "ls-remote", "--heads", "upstream", "refs/heads/native-pr-format"): "",
		},
		stderr:   map[string]string{},
		failures: map[string]error{},
	}
	client := NewClient("/repo", runner.run)

	exists, err := client.RemoteBranchExists(context.Background(), branch)
	if err != nil || !exists {
		t.Fatalf("expected exact upstream branch to exist, exists=%v err=%v", exists, err)
	}
	exists, err = client.RemoteBranchExists(context.Background(), "native-pr-format")
	if err != nil || exists {
		t.Fatalf("unexpected suffix-only match, exists=%v err=%v", exists, err)
	}
}

func TestRemoteBranchExistsPropagatesRemoteLookupFailure(t *testing.T) {
	lookup := key("-C", "/repo", "ls-remote", "--heads", "origin", "refs/heads/feature/native-pr-format")
	runner := &fakeRunner{
		outputs: map[string]string{key("-C", "/repo", "remote"): "origin\n"},
		stderr:  map[string]string{lookup: "authentication required"},
		failures: map[string]error{
			lookup: errors.New("exit status 128"),
		},
	}
	client := NewClient("/repo", runner.run)

	if _, err := client.RemoteBranchExists(context.Background(), "feature/native-pr-format"); err == nil || !strings.Contains(err.Error(), "authentication required") {
		t.Fatalf("RemoteBranchExists error = %v", err)
	}
}

func TestDeleteBranchForceHandling(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{}, stderr: map[string]string{}, failures: map[string]error{}}
	client := NewClient("/repo", runner.run)
	ctx := context.Background()

	if err := client.DeleteBranch(ctx, "feature", false); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteBranch(ctx, "feature", true); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"-C", "/repo", "branch", "--delete", "feature"},
		{"-C", "/repo", "branch", "--delete", "--force", "feature"},
	}
	for i, args := range want {
		if !reflect.DeepEqual(runner.calls[i].args, args) {
			t.Fatalf("call %d args mismatch: want %#v got %#v", i, args, runner.calls[i].args)
		}
	}
}

func TestBranchHelpersConstructCommandsAndPreserveBooleanFailures(t *testing.T) {
	runner := &fakeRunner{
		outputs: map[string]string{key("-C", "/repo", "rev-parse", "--verify", "feature"): "abc123\n"},
		stderr:  map[string]string{},
		failures: map[string]error{
			key("-C", "/repo", "rev-parse", "--verify", "missing"):             errors.New("exit status 1"),
			key("-C", "/repo", "merge-base", "--is-ancestor", "topic", "HEAD"): errors.New("exit status 1"),
		},
	}
	client := NewClient("/repo", runner.run)
	ctx := context.Background()

	exists, err := client.BranchExists(ctx, "feature")
	if err != nil || !exists {
		t.Fatalf("feature exists=%v err=%v, want true nil", exists, err)
	}
	exists, err = client.BranchExists(ctx, "missing")
	if err != nil || exists {
		t.Fatalf("missing exists=%v err=%v, want false nil", exists, err)
	}
	merged, err := client.BranchMerged(ctx, "main")
	if err != nil || !merged {
		t.Fatalf("main merged=%v err=%v, want true nil", merged, err)
	}
	merged, err = client.IsAncestor(ctx, "topic", "HEAD")
	if err != nil || merged {
		t.Fatalf("topic merged=%v err=%v, want false nil", merged, err)
	}

	want := [][]string{
		{"-C", "/repo", "rev-parse", "--verify", "feature"},
		{"-C", "/repo", "rev-parse", "--verify", "missing"},
		{"-C", "/repo", "merge-base", "--is-ancestor", "main", "HEAD"},
		{"-C", "/repo", "merge-base", "--is-ancestor", "topic", "HEAD"},
	}
	for i, args := range want {
		if !reflect.DeepEqual(runner.calls[i].args, args) {
			t.Fatalf("call %d args mismatch: want %#v got %#v", i, args, runner.calls[i].args)
		}
	}
}

func TestMergedIntoMechanismDistinguishesAncestryAndSquash(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	runGitCommand(t, root, "config", "user.name", "Tao Test")
	runGitCommand(t, root, "config", "user.email", "tao@example.invalid")
	writeRepoFile(t, root, "initial.txt", "initial\n")
	runGitCommand(t, root, "add", ".")
	runGitCommand(t, root, "commit", "-m", "initial")

	runGitCommand(t, root, "checkout", "-b", "ancestral")
	writeRepoFile(t, root, "ancestral.txt", "ancestral\n")
	runGitCommand(t, root, "add", ".")
	runGitCommand(t, root, "commit", "-m", "ancestral work")
	runGitCommand(t, root, "checkout", "main")
	runGitCommand(t, root, "merge", "--ff-only", "ancestral")

	runGitCommand(t, root, "checkout", "-b", "squashed")
	writeRepoFile(t, root, "squashed.txt", "squashed\n")
	runGitCommand(t, root, "add", ".")
	runGitCommand(t, root, "commit", "-m", "squashed work")
	runGitCommand(t, root, "checkout", "main")
	runGitCommand(t, root, "merge", "--squash", "squashed")
	runGitCommand(t, root, "commit", "-m", "squash merge")

	client := NewClient(root, nil)
	ancestral, err := client.MergedIntoMechanism(ctx, "ancestral", "main")
	if err != nil {
		t.Fatal(err)
	}
	if ancestral != MergeMechanismAncestry {
		t.Fatalf("ancestral mechanism = %q, want %q", ancestral, MergeMechanismAncestry)
	}
	squashed, err := client.MergedIntoMechanism(ctx, "squashed", "main")
	if err != nil {
		t.Fatal(err)
	}
	if squashed != MergeMechanismSquash {
		t.Fatalf("squashed mechanism = %q, want %q", squashed, MergeMechanismSquash)
	}
}

func writeRepoFile(t *testing.T, root string, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, path), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func key(args ...string) string {
	return strings.Join(args, "\x00")
}
