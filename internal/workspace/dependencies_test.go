package workspace

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	if metadata.Status != "skipped" || metadata.FailureReason != "no supported lockfile found" {
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

func TestPrepareDependenciesSubmodulePipeline(t *testing.T) {
	const gitCommand = "git submodule update --init --recursive"
	const statusCommand = "git submodule status --recursive"
	for _, tc := range []struct {
		name     string
		js       bool
		fail     string
		stderr   string
		commands []string
	}{
		{name: "submodules only", commands: []string{gitCommand, statusCommand}},
		{name: "before JS", js: true, commands: []string{gitCommand, statusCommand, "npm ci"}},
		{name: "status failure stops JS", js: true, fail: statusCommand, stderr: "cannot inspect submodules", commands: []string{gitCommand, statusCommand}},
		{name: "submodule failure stops JS", js: true, fail: "git", stderr: "transport denied", commands: []string{gitCommand}},
		{name: "stderr fallback", js: true, fail: "git", commands: []string{gitCommand}},
		{name: "JS failure identifies step", js: true, fail: "npm", stderr: "registry unavailable", commands: []string{gitCommand, statusCommand, "npm ci"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, ".gitmodules"))
			if tc.js {
				writeFile(t, filepath.Join(root, "package-lock.json"))
			}
			var commands []string
			failure := errors.New("runner failed")
			runner := func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
				if cwd != root {
					t.Fatalf("cwd = %q, want %q", cwd, root)
				}
				commands = append(commands, strings.Join(append([]string{name}, args...), " "))
				if name == tc.fail || commands[len(commands)-1] == tc.fail {
					_, _ = io.WriteString(stderr, tc.stderr)
					return failure
				}
				return nil
			}
			start := time.Date(2026, 9, 30, 12, 0, 0, 0, time.FixedZone("test", 3600))
			clockCalls := 0
			clock := func() time.Time {
				value := start.Add(time.Duration(clockCalls) * time.Second)
				clockCalls++
				return value
			}
			metadata, err := PrepareDependencies(context.Background(), root, DefaultConfig(), runner, clock)
			if !reflect.DeepEqual(commands, tc.commands) {
				t.Fatalf("commands = %v, want %v", commands, tc.commands)
			}
			if metadata.Command != strings.Join(tc.commands, " && ") || metadata.StartedAt == nil || metadata.CompletedAt == nil {
				t.Fatalf("metadata = %#v", metadata)
			}
			if !metadata.StartedAt.Equal(start) || metadata.StartedAt.Location() != time.UTC || !metadata.CompletedAt.Equal(start.Add(time.Duration(2*len(commands)-1)*time.Second)) {
				t.Fatalf("incorrect attempt timestamps: %#v", metadata)
			}
			if tc.fail == "" {
				if err != nil || metadata.Status != "ready" || metadata.FailureReason != "" {
					t.Fatalf("success metadata = %#v, err = %v", metadata, err)
				}
			} else {
				diagnostic := tc.stderr
				if diagnostic == "" {
					diagnostic = failure.Error()
				}
				if !errors.Is(err, failure) || metadata.Status != "failed" || !strings.Contains(metadata.FailureReason, diagnostic) || !strings.Contains(metadata.FailureReason, tc.commands[len(tc.commands)-1]) {
					t.Fatalf("failure metadata = %#v, err = %v", metadata, err)
				}
			}
		})
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
