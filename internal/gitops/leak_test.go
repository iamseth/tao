package gitops

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirtyFingerprintCombinesStatusDiffAndPaths(t *testing.T) {
	runner := &fakeRunner{outputs: map[string]string{
		key("-C", "/repo", "status", "--porcelain"):                            " M dirty.go\n?? new.txt\n",
		key("-C", "/repo", "diff", "HEAD"):                                     "diff --git a/dirty.go b/dirty.go\n-old\n+new\n",
		key("-C", "/repo", "ls-files", "--stage", "-z"):                        "100644 abcdef 0\tdirty.go\x00",
		key("-C", "/repo", "diff", "--name-only", "HEAD"):                      "dirty.go\n",
		key("-C", "/repo", "ls-files", "--others", "--exclude-standard", "-z"): "",
	}, stderr: map[string]string{}, failures: map[string]error{}}

	fingerprint, err := NewClient("/repo", runner.run).DirtyFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint.Hash == "" {
		t.Fatal("expected fingerprint hash")
	}
	if got, want := strings.Join(fingerprint.Paths, ","), "dirty.go,new.txt"; got != want {
		t.Fatalf("paths mismatch: got %q want %q", got, want)
	}
}

func TestDirtyFingerprintChangesForTrackedContentOnlyEdit(t *testing.T) {
	calls := 0
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		_ = ctx
		_ = cwd
		_ = stderr
		if name != "git" {
			t.Fatalf("unexpected command %q", name)
		}
		switch strings.Join(args, "\x00") {
		case key("-C", "/repo", "status", "--porcelain"):
			_, _ = io.WriteString(stdout, " M dirty.go\n")
		case key("-C", "/repo", "diff", "HEAD"):
			calls++
			if calls == 1 {
				_, _ = io.WriteString(stdout, "diff --git a/dirty.go b/dirty.go\n-old\n+first\n")
			} else {
				_, _ = io.WriteString(stdout, "diff --git a/dirty.go b/dirty.go\n-old\n+second\n")
			}
		case key("-C", "/repo", "ls-files", "--stage", "-z"):
			_, _ = io.WriteString(stdout, "100644 abcdef 0\tdirty.go\x00")
		case key("-C", "/repo", "diff", "--name-only", "HEAD"):
			_, _ = io.WriteString(stdout, "dirty.go\n")
		case key("-C", "/repo", "ls-files", "--others", "--exclude-standard", "-z"):
		default:
			t.Fatalf("unexpected git args %#v", args)
		}
		return nil
	}
	client := NewClient("/repo", runner)

	before, err := client.DirtyFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	after, err := client.DirtyFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.Hash == after.Hash {
		t.Fatal("expected content-only edit to change fingerprint")
	}
}

func TestDirtyFingerprintChangesWhenOnlyStagedBlobChanges(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	runGitCommand(t, root, "init", "-b", "main")
	runGitCommand(t, root, "config", "user.name", "Tao Test")
	runGitCommand(t, root, "config", "user.email", "tao@example.invalid")
	writeRepoFile(t, root, "tracked.txt", "base\n")
	runGitCommand(t, root, "add", "tracked.txt")
	runGitCommand(t, root, "commit", "-m", "initial")

	writeRepoFile(t, root, "tracked.txt", "staged one\n")
	runGitCommand(t, root, "add", "tracked.txt")
	writeRepoFile(t, root, "tracked.txt", "worktree\n")

	client := NewClient(root, nil)
	beforeStatus, err := client.StatusPorcelain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	beforeDiff, err := client.rawOutput(ctx, "diff", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	beforeIndex, err := client.rawOutput(ctx, "ls-files", "--stage", "-z")
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.DirtyFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	writeRepoFile(t, root, "tracked.txt", "staged two\n")
	runGitCommand(t, root, "add", "tracked.txt")
	writeRepoFile(t, root, "tracked.txt", "worktree\n")

	afterStatus, err := client.StatusPorcelain(ctx)
	if err != nil {
		t.Fatal(err)
	}
	afterDiff, err := client.rawOutput(ctx, "diff", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	afterIndex, err := client.rawOutput(ctx, "ls-files", "--stage", "-z")
	if err != nil {
		t.Fatal(err)
	}
	after, err := client.DirtyFingerprint(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if beforeStatus != afterStatus || strings.TrimSpace(afterStatus) != "MM tracked.txt" {
		t.Fatalf("status changed: before %q after %q", beforeStatus, afterStatus)
	}
	if beforeDiff != afterDiff {
		t.Fatal("working-tree diff changed")
	}
	if beforeIndex == afterIndex {
		t.Fatal("expected staged object identity to change")
	}
	contents, err := os.ReadFile(filepath.Join(root, "tracked.txt")) // #nosec G304 -- test reads a fixed file in its temporary repository.
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != "worktree\n" {
		t.Fatalf("worktree content changed: %q", contents)
	}
	if before.Hash == after.Hash {
		t.Fatal("staged-only blob edit did not change fingerprint")
	}
}

func TestDirtyFingerprintIncludesUntrackedBytesAndMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "scratch.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := func(_ context.Context, _ string, _ string, args []string, stdout io.Writer, _ io.Writer) error {
		switch strings.Join(args, "\x00") {
		case key("-C", root, "status", "--porcelain"):
			_, _ = io.WriteString(stdout, "?? scratch.txt\n")
		case key("-C", root, "diff", "HEAD"), key("-C", root, "diff", "--name-only", "HEAD"):
		case key("-C", root, "ls-files", "--stage", "-z"):
		case key("-C", root, "ls-files", "--others", "--exclude-standard", "-z"):
			_, _ = io.WriteString(stdout, "scratch.txt\x00")
		default:
			t.Fatalf("unexpected git args %#v", args)
		}
		return nil
	}
	client := NewClient(root, runner)

	original, err := client.DirtyFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("omega\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	contentChanged, err := client.DirtyFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if original.Hash == contentChanged.Hash {
		t.Fatal("in-place untracked content edit did not change fingerprint")
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	modeChanged, err := client.DirtyFingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if contentChanged.Hash == modeChanged.Hash {
		t.Fatal("untracked mode edit did not change fingerprint")
	}
}
