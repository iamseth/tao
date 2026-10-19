package run

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRunGitFixturesDisableAutomaticMaintenance(t *testing.T) {
	t.Run("recovery", func(t *testing.T) {
		repo, _, _, _ := newRebaseRecoveryRepo(t, false)
		assertRunGitMaintenanceDisabled(t, repo)
	})
	t.Run("linked recovery", func(t *testing.T) {
		repo, worktree, _, _, _ := newLinkedRebaseRecoveryRepo(t)
		assertRunGitMaintenanceDisabled(t, repo)
		assertRunGitMaintenanceDisabled(t, worktree)
	})
	t.Run("commit completion", func(t *testing.T) {
		assertRunGitMaintenanceDisabled(t, initSliceCompletionRepo(t))
	})
	t.Run("lifecycle", func(t *testing.T) {
		fixture := newLifecycleGitFixture(t, "maintenance-test")
		assertRunGitMaintenanceDisabled(t, fixture.repoRoot)
	})
	t.Run("pull request and bare origin", func(t *testing.T) {
		fixture := newPullRequestOrchestrationFixture(t)
		assertRunGitMaintenanceDisabled(t, fixture.repoRoot)
		assertRunGitMaintenanceDisabled(t, fixture.worktreeRoot)
		assertRunGitMaintenanceDisabled(t, fixture.originRoot)
	})
}

func TestSliceRepoTemplateCopies(t *testing.T) {
	first, second := initSliceCompletionRepo(t), initSliceCompletionRepo(t)
	for _, root := range []string{first, second} {
		for _, check := range []struct {
			args []string
			want string
		}{
			{[]string{"branch", "--show-current"}, "tao/test"},
			{[]string{"branch", "--format=%(refname:short)"}, "main\ntao/test"},
			{[]string{"config", "user.name"}, "Tao Test"},
			{[]string{"config", "user.email"}, "tao@example.com"},
			{[]string{"rev-list", "--count", "HEAD"}, "1"},
			{[]string{"log", "-1", "--format=%s"}, "base"},
			{[]string{"status", "--porcelain"}, ""},
		} {
			if got := strings.TrimSpace(runCommitTestGitOutput(t, root, check.args...)); got != check.want {
				t.Fatalf("%v = %q, want %q", check.args, got, check.want)
			}
		}
		data, err := os.ReadFile(filepath.Join(root, "README.md")) // #nosec G304 -- root is a test-owned temporary fixture.
		if err != nil || string(data) != "base\n" {
			t.Fatalf("content: %q, %v", data, err)
		}
		info, err := os.Stat(filepath.Join(root, "README.md"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
		assertRunGitMaintenanceDisabled(t, root)
	}
	if canCopySliceRepo() && sharedSliceTemplate.root == "" {
		t.Fatal("compatible initializer did not use the template")
	}
	for _, name := range []string{"README.md", ".git/index", ".git/config", ".git/refs/heads/tao/test"} {
		a, err := os.Stat(filepath.Join(first, name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.Stat(filepath.Join(second, name))
		if err != nil {
			t.Fatal(err)
		}
		if os.SameFile(a, b) {
			t.Fatalf("shared file: %s", name)
		}
	}
	head := strings.TrimSpace(runCommitTestGitOutput(t, second, "rev-parse", "HEAD"))
	runCommitTestGitCommand(t, first, "config", "user.name", "Changed")
	if err := os.WriteFile(filepath.Join(first, "README.md"), []byte("changed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	runCommitTestGitCommand(t, first, "add", "README.md")
	if got := strings.TrimSpace(runCommitTestGitOutput(t, second, "status", "--porcelain")); got != "" {
		t.Fatal(got)
	}
	runCommitTestGitCommand(t, first, "commit", "-m", "changed")
	if err := os.RemoveAll(first); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{second, initSliceCompletionRepo(t)} {
		if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); got != head {
			t.Fatal("shared ref mutation")
		}
		if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "config", "user.name")); got != "Tao Test" {
			t.Fatal("shared config mutation")
		}
	}
}

func TestSliceRepoTemplateConcurrentAndFailure(t *testing.T) {
	var cache sliceRepoTemplate
	t.Cleanup(func() {
		if err := cache.cleanup(); err != nil {
			t.Error(err)
		}
	})
	parent := t.TempDir()
	sentinel := errors.New("setup failed")
	var failedRoot string
	_, err := cache.source(func(root string) error {
		failedRoot = root
		if err := os.WriteFile(filepath.Join(root, "partial"), []byte("partial"), 0600); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	if _, err := os.Stat(failedRoot); !os.IsNotExist(err) {
		t.Fatalf("failed setup left root: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			source, err := cache.source(buildSliceRepoTemplate)
			if err != nil {
				t.Error(err)
				return
			}
			dest, err := os.MkdirTemp(parent, "copy-")
			if err != nil {
				t.Error(err)
				return
			}
			if err := copySliceRepo(source, dest); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 8 {
		t.Fatalf("copies: %d", len(entries))
	}
	for _, entry := range entries {
		dest := filepath.Join(parent, entry.Name())
		if got := strings.TrimSpace(runCommitTestGitOutput(t, dest, "status", "--porcelain")); got != "" {
			t.Fatal(got)
		}
		if _, err := os.Stat(filepath.Join(dest, ".git/objects/info/alternates")); !os.IsNotExist(err) {
			t.Fatalf("alternates: %v", err)
		}
		if _, err := os.Stat(filepath.Join(dest, ".git/worktrees")); !os.IsNotExist(err) {
			t.Fatalf("worktree registrations: %v", err)
		}
	}
	root := cache.root
	if err := cache.cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("cleanup: %v", err)
	}
	if err := cache.cleanup(); err != nil {
		t.Fatal(err)
	}
}

func TestSliceRepoTemplateOverrides(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "init.defaultBranch")
	t.Setenv("GIT_CONFIG_VALUE_0", "custom")
	root := initSliceCompletionRepo(t)
	if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "branch", "--format=%(refname:short)")); got != "custom\ntao/test" {
		t.Fatal(got)
	}
}

func assertRunGitMaintenanceDisabled(t *testing.T, root string) {
	t.Helper()
	for _, setting := range []struct{ key, value string }{
		{"maintenance.auto", "false"},
		{"gc.auto", "0"},
	} {
		got := rebaseRecoveryGitOutput(t, root, "config", "--includes", "--get", setting.key)
		if got != setting.value {
			t.Errorf("%s: effective git config %s = %q, want %q", root, setting.key, got, setting.value)
		}
	}
}
