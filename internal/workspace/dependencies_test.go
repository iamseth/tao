package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPrepareDependenciesSelectsCommandAndRunsInWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "pnpm-lock.yaml"))
	var gotCwd, gotName string
	var gotArgs []string
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		gotCwd = cwd
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil
	}

	metadata, err := PrepareDependencies(context.Background(), root, DefaultConfig(), runner, fixedDependencyClock())
	if err != nil {
		t.Fatalf("PrepareDependencies failed: %v", err)
	}
	if gotCwd != root {
		t.Fatalf("expected cwd %q, got %q", root, gotCwd)
	}
	if gotName != "pnpm" || !reflect.DeepEqual(gotArgs, []string{"install", "--frozen-lockfile"}) {
		t.Fatalf("unexpected command: %s %v", gotName, gotArgs)
	}
	if metadata.Status != "ready" || metadata.Command != "pnpm install --frozen-lockfile" || metadata.StartedAt == nil || metadata.CompletedAt == nil {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestPrepareDependenciesSkipsWhenAutoAndNoLockfile(t *testing.T) {
	called := false
	metadata, err := PrepareDependencies(context.Background(), t.TempDir(), DefaultConfig(), func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		called = true
		return nil
	}, fixedDependencyClock())
	if err != nil {
		t.Fatalf("PrepareDependencies failed: %v", err)
	}
	if called {
		t.Fatal("runner should not be called")
	}
	if metadata.Status != "skipped" || metadata.FailureReason == "" {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestPrepareDependenciesRecordsFailure(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "package-lock.json"))
	runnerErr := errors.New("exit status 1")
	metadata, err := PrepareDependencies(context.Background(), root, DefaultConfig(), func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "install failed")
		return runnerErr
	}, fixedDependencyClock())
	if !errors.Is(err, runnerErr) {
		t.Fatalf("expected runner error, got %v", err)
	}
	if metadata.Status != "failed" || metadata.Command != "npm ci" || metadata.FailureReason != "install failed" || metadata.CompletedAt == nil {
		t.Fatalf("unexpected metadata: %#v", metadata)
	}
}

func TestPrepareDependenciesSupportsExplicitCommand(t *testing.T) {
	config := DefaultConfig()
	config.DependencyInstallBehavior = DependencyInstallCommand
	config.DependencyInstallCommand = "make deps"
	var gotName string
	var gotArgs []string
	_, err := PrepareDependencies(context.Background(), t.TempDir(), config, func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		gotName = name
		gotArgs = append([]string(nil), args...)
		return nil
	}, fixedDependencyClock())
	if err != nil {
		t.Fatalf("PrepareDependencies failed: %v", err)
	}
	if gotName != "make" || !reflect.DeepEqual(gotArgs, []string{"deps"}) {
		t.Fatalf("unexpected command: %s %v", gotName, gotArgs)
	}
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("lock\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write %s: %v", path, err)
	}
}

func fixedDependencyClock() func() time.Time {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return now }
}
