package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareSubmodulesAbsentAndInspectionError(t *testing.T) {
	root := t.TempDir()
	runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		t.Fatal("runner must not be called")
		return nil
	}
	metadata, err := PrepareSubmodules(context.Background(), root, runner, nil)
	if err != nil || metadata.Status != "skipped" || metadata.Command != "" || metadata.StartedAt != nil || metadata.CompletedAt != nil {
		t.Fatalf("absent metadata = %#v, err = %v", metadata, err)
	}
	file := filepath.Join(root, "not-a-directory")
	writeFile(t, file)
	metadata, err = PrepareSubmodules(context.Background(), file, runner, nil)
	if err == nil || metadata.Status != "failed" || !strings.Contains(metadata.FailureReason, "inspect workspace .gitmodules") || metadata.Command != "" || metadata.StartedAt == nil || metadata.CompletedAt == nil {
		t.Fatalf("inspection failure = %#v, err = %v", metadata, err)
	}
}

func TestPrepareSubmodulesCancellationAndRetry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".gitmodules"))
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		calls++
		if calls == 1 {
			cancel()
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	metadata, err := PrepareSubmodules(ctx, root, runner, fixedDependencyClock())
	if !errors.Is(err, context.Canceled) || metadata.Status != "failed" || !strings.Contains(metadata.FailureReason, context.Canceled.Error()) || metadata.CompletedAt == nil {
		t.Fatalf("cancellation = %#v, err = %v", metadata, err)
	}
	metadata, err = PrepareSubmodules(context.Background(), root, runner, nil)
	if err != nil || calls != 3 || metadata.Status != "ready" || metadata.FailureReason != "" || metadata.StartedAt == nil || metadata.CompletedAt == nil {
		t.Fatalf("retry = %#v, calls = %d, err = %v", metadata, calls, err)
	}
}

func TestPrepareSubmodulesDefaultRunnerAndClock(t *testing.T) {
	repo := newTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo.path, ".gitmodules"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, err := PrepareSubmodules(context.Background(), repo.path, nil, nil)
	if err != nil || metadata.Status != "ready" || metadata.Command != "git submodule update --init --recursive && git submodule status --recursive" || metadata.StartedAt == nil || metadata.CompletedAt == nil || metadata.CompletedAt.Before(*metadata.StartedAt) {
		t.Fatalf("default runner metadata = %#v, err = %v", metadata, err)
	}
}

// File transport is allowed only in these test subprocesses, never in Git's
// persistent configuration or the production runner. Other transports are denied.
func localSubmoduleRunner(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- test-controlled commands and local repository paths.
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL=file")
	cmd.Stdout, cmd.Stderr = stdout, stderr
	return cmd.Run()
}

func addLocalSubmodule(t *testing.T, root, source, path string) {
	t.Helper()
	var stderr strings.Builder
	if err := localSubmoduleRunner(context.Background(), root, "git", []string{"submodule", "add", source, path}, io.Discard, &stderr); err != nil {
		t.Fatalf("add local submodule: %v: %s", err, &stderr)
	}
}

type nestedSubmoduleFixture struct {
	root     string
	oldChild string
	newChild string
	oldLeaf  string
	newLeaf  string
}

func newNestedSubmoduleFixture(t *testing.T) nestedSubmoduleFixture {
	t.Helper()
	leaf := newTestRepo(t)
	commitTestFile(t, leaf.path, "payload.txt", "one\n", "first payload")
	oldLeaf := gitHead(t, leaf.path)
	commitTestFile(t, leaf.path, "payload.txt", "two\n", "second payload")
	newLeaf := gitHead(t, leaf.path)
	child := newTestRepo(t)
	addLocalSubmodule(t, child.path, leaf.path, "nested")
	runGit(t, filepath.Join(child.path, "nested"), "checkout", oldLeaf)
	runGit(t, child.path, "add", ".gitmodules", "nested")
	runGit(t, child.path, "commit", "-m", "pin old nested revision")
	oldChild := gitHead(t, child.path)
	runGit(t, filepath.Join(child.path, "nested"), "checkout", newLeaf)
	runGit(t, child.path, "add", "nested")
	runGit(t, child.path, "commit", "-m", "pin new nested revision")
	newChild := gitHead(t, child.path)
	parent := newTestRepo(t)
	addLocalSubmodule(t, parent.path, child.path, "modules/child")
	runGit(t, filepath.Join(parent.path, "modules/child"), "checkout", oldChild)
	runGit(t, parent.path, "add", ".gitmodules", "modules/child")
	runGit(t, parent.path, "commit", "-m", "pin old child revision")
	root := filepath.Join(t.TempDir(), "workspace")
	runGit(t, parent.path, "worktree", "add", "--detach", root)
	return nestedSubmoduleFixture{root: root, oldChild: oldChild, newChild: newChild, oldLeaf: oldLeaf, newLeaf: newLeaf}
}

func TestPrepareSubmodulesNestedPinnedRevisionsAndDirtyContent(t *testing.T) {
	fixture := newNestedSubmoduleFixture(t)
	child := filepath.Join(fixture.root, "modules/child")
	leaf := filepath.Join(child, "nested")
	prepare := func() {
		t.Helper()
		metadata, err := PrepareSubmodules(context.Background(), fixture.root, localSubmoduleRunner, nil)
		if err != nil || metadata.Status != "ready" {
			t.Fatalf("prepare = %#v, err = %v", metadata, err)
		}
	}
	prepare()
	prepare()
	if gitHead(t, child) != fixture.oldChild || gitHead(t, leaf) != fixture.oldLeaf {
		t.Fatal("initialization followed remote tips rather than recorded gitlinks")
	}
	payload := filepath.Join(leaf, "payload.txt")
	if err := os.WriteFile(payload, []byte("local edits\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepare()
	// Change only the recorded gitlink. Updating its nested pin would overwrite
	// local edits, so Git must refuse rather than force/reset the nested checkout.
	runGit(t, fixture.root, "update-index", "--cacheinfo", "160000,"+fixture.newChild+",modules/child")
	metadata, err := PrepareSubmodules(context.Background(), fixture.root, localSubmoduleRunner, nil)
	if err == nil || metadata.Status != "failed" || !strings.Contains(metadata.FailureReason, "git submodule update") {
		t.Fatalf("dirty update = %#v, err = %v", metadata, err)
	}
	contents, err := os.ReadFile(payload) //nolint:gosec // test-owned payload in a temporary local submodule fixture
	if err != nil || string(contents) != "local edits\n" || gitHead(t, leaf) != fixture.oldLeaf {
		t.Fatalf("dirty content was overwritten: %q, err = %v", contents, err)
	}
	// Resolve the fixture's local edit explicitly, then retry the same pinned update.
	if err := os.WriteFile(payload, []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepare()
	prepare()
	if gitHead(t, child) != fixture.newChild || gitHead(t, leaf) != fixture.newLeaf {
		t.Fatal("retry did not initialize changed pinned revisions recursively")
	}
}

func TestPrepareSubmodulesConfiguredUpdateModes(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mode        string
		nested      bool
		initialized bool
		pinned      bool
		wantReady   bool
	}{
		{name: "uninitialized none", mode: "none"},
		{name: "nested uninitialized none", mode: "none", nested: true},
		{name: "non-pinned none", mode: "none", initialized: true},
		{name: "non-pinned merge", mode: "merge", initialized: true},
		{name: "non-pinned rebase", mode: "rebase", initialized: true},
		{name: "non-pinned custom no-op", mode: "!true", initialized: true},
		{name: "nested non-pinned merge", mode: "merge", nested: true, initialized: true},
		{name: "checkout restores pin", mode: "checkout", initialized: true, wantReady: true},
		{name: "already pinned none", mode: "none", initialized: true, pinned: true, wantReady: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newNestedSubmoduleFixture(t)
			child := filepath.Join(fixture.root, "modules/child")
			root, key, path, newer := fixture.root, "submodule.modules/child.update", child, fixture.newChild
			if tc.initialized || tc.nested {
				if _, err := PrepareSubmodules(context.Background(), fixture.root, localSubmoduleRunner, nil); err != nil {
					t.Fatal(err)
				}
			}
			if tc.nested {
				root, key, path, newer = child, "submodule.nested.update", filepath.Join(child, "nested"), fixture.newLeaf
				if !tc.initialized {
					runGit(t, child, "submodule", "deinit", "nested")
				}
			}
			if tc.initialized && !tc.pinned {
				runGit(t, path, "checkout", newer)
			}
			runGit(t, root, "config", key, tc.mode)
			// Ordinary status ignore settings must not conceal prerequisite drift.
			runGit(t, root, "config", strings.TrimSuffix(key, "update")+"ignore", "all")
			metadata, err := PrepareSubmodules(context.Background(), fixture.root, localSubmoduleRunner, nil)
			if tc.wantReady {
				if err != nil || metadata.Status != "ready" || gitHead(t, child) != fixture.oldChild {
					t.Fatalf("pinned checkout = %#v, err = %v", metadata, err)
				}
				return
			}
			if err == nil || metadata.Status != "failed" || !strings.Contains(metadata.FailureReason, "git submodule status --recursive") || !strings.Contains(metadata.FailureReason, "update configuration") || metadata.CompletedAt == nil {
				t.Fatalf("unsafe readiness after successful update = %#v, err = %v", metadata, err)
			}
			if tc.initialized && gitHead(t, path) != newer {
				t.Fatal("validation changed the configured non-pinned checkout")
			}
		})
	}
}

func TestPrepareSubmodulesStatusValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		fail   error
	}{
		{name: "missing", output: "-012345 child with spaces\n"},
		{name: "divergent nested", output: " 012345 child\n+678901 child/nested\n"},
		{name: "conflicted", output: "U000000 child\n"},
		{name: "unexpected output", output: "unexpected\n"},
		{name: "canceled status", fail: context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, ".gitmodules"))
			writeFile(t, filepath.Join(root, "package-lock.json"))
			runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if name != "git" {
					t.Fatal("JS must not run after status validation fails")
				}
				if strings.Join(args, " ") == "submodule status --recursive" {
					_, _ = io.WriteString(stdout, tc.output)
					return tc.fail
				}
				return nil
			}
			metadata, err := PrepareDependencies(context.Background(), root, DefaultConfig(), runner, nil)
			if err == nil || metadata.Status != "failed" || !strings.Contains(metadata.FailureReason, "git submodule status --recursive") || metadata.CompletedAt == nil {
				t.Fatalf("status failure = %#v, err = %v", metadata, err)
			}
			if tc.fail != nil && !errors.Is(err, tc.fail) {
				t.Fatalf("lost status command error: %v", err)
			}
		})
	}
}

func TestPrepareSubmodulesRespectsTransportPolicy(t *testing.T) {
	fixture := newNestedSubmoduleFixture(t)
	t.Setenv("GIT_ALLOW_PROTOCOL", "none")
	metadata, err := PrepareSubmodules(context.Background(), fixture.root, nil, nil)
	if err == nil || metadata.Status != "failed" || !strings.Contains(metadata.FailureReason, "transport 'file' not allowed") {
		t.Fatalf("transport policy was bypassed: %#v, err = %v", metadata, err)
	}
}

func TestPrepareSubmodulesDefaultRunnerCancellation(t *testing.T) {
	repo := newTestRepo(t)
	writeFile(t, filepath.Join(repo.path, ".gitmodules"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	metadata, err := PrepareSubmodules(ctx, repo.path, nil, nil)
	if !errors.Is(err, context.Canceled) || metadata.Status != "failed" || metadata.CompletedAt == nil {
		t.Fatalf("default runner ignored cancellation: %#v, err = %v", metadata, err)
	}
}

func TestPrepareDependenciesFallsBackToRunnerErrorWhenStderrEmpty(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/yarn.lock")
	metadata, err := PrepareDependencies(context.Background(), root, DefaultConfig(), func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		return context.Canceled
	}, fixedDependencyClock())
	if err == nil || metadata.Status != "failed" || metadata.FailureReason != context.Canceled.Error() {
		t.Fatalf("expected runner error failure metadata, got %#v err=%v", metadata, err)
	}
}
