package merge

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

type verifyRunnerCall struct {
	cwd  string
	name string
	args []string
}

func TestBoundMergeVerifyOutputRepairsSplitRuneAtTailBoundary(t *testing.T) {
	const suffix = "original trailing bytes"
	trailing := strings.Repeat("z", mergeVerifyOutputLimit-2-len(suffix)) + suffix
	output := "x€" + trailing
	if len(output) <= mergeVerifyOutputLimit || output[len(output)-mergeVerifyOutputLimit]&0xc0 != 0x80 {
		t.Fatal("test input does not split a multi-byte rune at the tail boundary")
	}

	got := boundMergeVerifyOutput(output)
	if !utf8.ValidString(got) {
		t.Fatalf("bounded output is not valid UTF-8: %q", got[:16])
	}
	if strings.ContainsRune(got, utf8.RuneError) {
		t.Fatalf("bounded output contains U+FFFD: %q", got[:16])
	}
	if len(got) > mergeVerifyOutputLimit {
		t.Fatalf("bounded output length = %d, want at most %d", len(got), mergeVerifyOutputLimit)
	}
	if !strings.HasSuffix(got, suffix) {
		t.Fatalf("bounded output does not retain original trailing bytes: %q", got[len(got)-32:])
	}
}

func TestMergeRunsPassingVerifyAfterSquash(t *testing.T) {
	git := mergeVerifyGit()
	detail := mergeVerifyDetail()
	var calls []verifyRunnerCall
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		_ = ctx
		_ = stderr
		if len(git.calls) == 0 || !strings.HasPrefix(git.calls[len(git.calls)-1], "commit feat(merge): use approved review message") {
			t.Fatalf("verify should run after squash commit, git calls: %#v", git.calls)
		}
		calls = append(calls, verifyRunnerCall{cwd: cwd, name: name, args: append([]string(nil), args...)})
		_, _ = stdout.Write([]byte("ok\n"))
		return nil
	}

	events := &fakeEventAppender{}
	err := (Service{Git: git, Runner: runner, Cleaner: successfulCleanup(), Events: events}).Merge(context.Background(), detail, Options{VerifyCommand: "go test ./internal/merge"})
	if err != nil {
		t.Fatal(err)
	}

	verification := events.requireSingle(t, plan.EventTypeMergeVerification)
	events.requireSingle(t, plan.EventTypePlanMerged)
	if verification.PlanID != "plan-a" || verification.Command != "go test ./internal/merge" || verification.Result != "passed" {
		t.Fatalf("unexpected passed verification event: %#v", verification)
	}
	wantCalls := []verifyRunnerCall{{cwd: "/repo/root", name: "sh", args: []string{"-c", "go test ./internal/merge"}}}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("runner calls mismatch\nwant: %#v\n got: %#v", wantCalls, calls)
	}
	if hasGitCall(git.calls, "reset-hard pre123") {
		t.Fatalf("passing verify should not roll back, git calls: %#v", git.calls)
	}
}

func TestMergeDetectsDefaultVerifyCommand(t *testing.T) {
	unsetMergeVerifyCommandEnv(t)

	tests := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{
			name:  "go module",
			files: map[string]string{"go.mod": "module example.com/project\n"},
			want:  "go build ./... && go test ./...",
		},
		{
			name: "make verify target",
			files: map[string]string{
				"Makefile": ".PHONY: verify build test\nverify: build test\n\t@echo verified\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\n",
			},
			want: "make verify",
		},
		{
			name: "make build and test targets",
			files: map[string]string{
				"Makefile": ".PHONY: build test\nbuild:\n\tgo build ./...\ntest:\n\tgo test ./...\n",
			},
			want: "make build && make test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := mergeVerifyGit()
			detail := mergeVerifyFixtureDetail(t, tt.files)
			var calls []verifyRunnerCall
			runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
				_ = ctx
				_ = stderr
				calls = append(calls, verifyRunnerCall{cwd: cwd, name: name, args: append([]string(nil), args...)})
				_, _ = stdout.Write([]byte("ok\n"))
				return nil
			}

			err := (Service{Git: git, Runner: runner, Cleaner: successfulCleanup(), Events: &fakeEventAppender{}}).Merge(context.Background(), detail, Options{})
			if err != nil {
				t.Fatal(err)
			}

			wantCalls := []verifyRunnerCall{{cwd: detail.State.Repo.Root, name: "sh", args: []string{"-c", tt.want}}}
			if !reflect.DeepEqual(calls, wantCalls) {
				t.Fatalf("runner calls mismatch\nwant: %#v\n got: %#v", wantCalls, calls)
			}
		})
	}
}

func TestMergeVerifyUsesExplicitIntegrationRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolution := resolveMergeVerifyCommandAtRoot(root, Options{})
	if resolution.command != "make verify" || resolution.repoRoot != root {
		t.Fatalf("unexpected resolution: %#v", resolution)
	}
	var gotRoot string
	service := Service{Runner: func(_ context.Context, cwd, _ string, _ []string, _, _ io.Writer) error {
		gotRoot = cwd
		return nil
	}}
	if _, err := service.runMergeVerifyAtRoot(context.Background(), root, resolution.command); err != nil {
		t.Fatal(err)
	}
	if gotRoot != root {
		t.Fatalf("verification root = %q, want %q", gotRoot, root)
	}
}

func TestMergeSkipsVerifyAndLogsWhenNoBuildSystemDetected(t *testing.T) {
	unsetMergeVerifyCommandEnv(t)

	git := mergeVerifyGit()
	detail := mergeVerifyFixtureDetail(t, map[string]string{"README.md": "# project\n"})
	called := false
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		_ = ctx
		_ = cwd
		_ = name
		_ = args
		_ = stdout
		_ = stderr
		called = true
		return errors.New("runner should not be called")
	}
	var logs []string
	logf := func(format string, args ...any) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}

	events := &fakeEventAppender{}
	if err := (Service{Git: git, Runner: runner, Cleaner: successfulCleanup(), Events: events, Logf: logf}).Merge(context.Background(), detail, Options{}); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("unrecognized build system should skip the runner")
	}
	wantLogs := []string{"merge verification skipped: no supported build system detected in " + detail.State.Repo.Root}
	if !reflect.DeepEqual(logs, wantLogs) {
		t.Fatalf("logs mismatch\nwant: %#v\n got: %#v", wantLogs, logs)
	}
	verification := events.requireSingle(t, plan.EventTypeMergeVerification)
	events.requireSingle(t, plan.EventTypePlanMerged)
	wantReason := "no supported build system detected in " + detail.State.Repo.Root
	if verification.Result != "skipped" || verification.Reason != wantReason {
		t.Fatalf("unexpected skipped verification event: %#v", verification)
	}
	if hasGitCall(git.calls, "reset-hard pre123") {
		t.Fatalf("skipped verify should not roll back, git calls: %#v", git.calls)
	}
}

func TestMergeVerifyFailureRollsBackDefault(t *testing.T) {
	git := mergeVerifyGit()
	detail := mergeVerifyDetail()
	runnerErr := errors.New("tests failed")
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		_ = ctx
		_ = cwd
		_ = name
		_ = args
		if len(git.calls) == 0 || !strings.HasPrefix(git.calls[len(git.calls)-1], "commit feat(merge): use approved review message") {
			t.Fatalf("verify should run after squash commit, git calls: %#v", git.calls)
		}
		_, _ = stdout.Write([]byte("build failed\n"))
		_, _ = stderr.Write([]byte("test failed\n"))
		return runnerErr
	}

	cleaner := successfulCleanup()
	events := &fakeEventAppender{}
	err := (Service{Git: git, Runner: runner, Cleaner: cleaner, Events: events}).Merge(context.Background(), detail, Options{VerifyCommand: "make test"})
	if err == nil {
		t.Fatal("expected verify failure")
	}
	if !errors.Is(err, ErrVerifyFailed) {
		t.Fatalf("expected ErrVerifyFailed, got %v", err)
	}
	var verifyErr *VerifyFailedError
	if !errors.As(err, &verifyErr) {
		t.Fatalf("expected VerifyFailedError, got %#v", err)
	}
	if verifyErr.Command != "make test" || verifyErr.RepoRoot != "/repo/root" || !errors.Is(verifyErr.Cause, runnerErr) {
		t.Fatalf("unexpected verify error details: %#v", verifyErr)
	}
	if !strings.Contains(verifyErr.Output, "build failed") || !strings.Contains(verifyErr.Output, "test failed") {
		t.Fatalf("verify output missing captured stdout/stderr: %q", verifyErr.Output)
	}
	if len(cleaner.calls) != 0 {
		t.Fatalf("cleanup should not run after failed verify, calls=%#v", cleaner.calls)
	}
	verification := events.requireSingle(t, plan.EventTypeMergeVerification)
	if events.count(plan.EventTypePlanMerged) != 0 {
		t.Fatalf("failed verification must not record a merge: %#v", events.events)
	}
	if verification.Command != "make test" || verification.Result != "failed" || !strings.Contains(verification.Message, "rolled back") {
		t.Fatalf("unexpected failed verification event: %#v", verification)
	}
	wantSuffix := []string{"reset-hard pre123", "checkout main"}
	gotSuffix := git.calls[len(git.calls)-len(wantSuffix):]
	if !reflect.DeepEqual(gotSuffix, wantSuffix) {
		t.Fatalf("rollback calls mismatch\nwant suffix: %#v\n got calls: %#v", wantSuffix, git.calls)
	}
}

func TestMergeVerificationEventAppendFailureIsBestEffort(t *testing.T) {
	appendErr := errors.New("event store unavailable")
	newEvents := func() *fakeEventAppender {
		events := &fakeEventAppender{err: appendErr}
		calls := 0
		events.onCall = func(string) {
			calls++
			if calls > 1 {
				events.err = nil
			}
		}
		return events
	}

	t.Run("passed", func(t *testing.T) {
		git := mergeVerifyGit()
		events := newEvents()
		var logs []string
		runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error { return nil }
		err := (Service{
			Git: git, Runner: runner, Cleaner: successfulCleanup(), Events: events,
			Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		}).Merge(context.Background(), mergeVerifyDetail(), Options{VerifyCommand: "true"})
		if err != nil {
			t.Fatalf("event append failure changed successful merge outcome: %v", err)
		}
		events.requireSingle(t, plan.EventTypePlanMerged)
		if len(logs) != 1 || !strings.Contains(logs[0], appendErr.Error()) {
			t.Fatalf("expected append warning, got %#v", logs)
		}
	})

	t.Run("failed", func(t *testing.T) {
		git := mergeVerifyGit()
		runnerErr := errors.New("tests failed")
		runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error { return runnerErr }
		events := newEvents()
		err := (Service{Git: git, Runner: runner, Cleaner: successfulCleanup(), Events: events}).Merge(context.Background(), mergeVerifyDetail(), Options{VerifyCommand: "make test"})
		if !errors.Is(err, runnerErr) || !errors.Is(err, ErrVerifyFailed) {
			t.Fatalf("event append failure changed verify error: %v", err)
		}
		wantSuffix := []string{"reset-hard pre123", "checkout main"}
		if got := git.calls[len(git.calls)-len(wantSuffix):]; !reflect.DeepEqual(got, wantSuffix) {
			t.Fatalf("event append failure changed rollback\nwant: %#v\n got: %#v", wantSuffix, git.calls)
		}
	})

	t.Run("skipped", func(t *testing.T) {
		unsetMergeVerifyCommandEnv(t)
		git := mergeVerifyGit()
		detail := mergeVerifyFixtureDetail(t, map[string]string{"README.md": "# project\n"})
		events := newEvents()
		err := (Service{Git: git, Cleaner: successfulCleanup(), Events: events}).Merge(context.Background(), detail, Options{})
		if err != nil {
			t.Fatalf("event append failure changed skipped merge outcome: %v", err)
		}
		events.requireSingle(t, plan.EventTypePlanMerged)
	})
}

func TestMergeIntentionalVerifySkipsEmitEventsWithoutLogging(t *testing.T) {
	tests := []struct {
		name       string
		options    Options
		envSet     bool
		wantReason string
	}{
		{
			name:       "no verify",
			options:    Options{NoVerify: true, VerifyCommand: "exit 1"},
			wantReason: "verification disabled",
		},
		{
			name:       "whitespace explicit command",
			options:    Options{VerifyCommand: " \t"},
			wantReason: "configured verification command is empty",
		},
		{
			name:       "empty environment command",
			envSet:     true,
			wantReason: "TAO_MERGE_VERIFY_COMMAND is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unsetMergeVerifyCommandEnv(t)
			if tt.envSet {
				t.Setenv(runtimeconfig.EnvMergeVerifyCommand, "")
			}
			git := mergeVerifyGit()
			called := false
			runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error {
				called = true
				return errors.New("runner should not be called")
			}
			var logs []string
			events := &fakeEventAppender{}

			err := (Service{
				Git: git, Runner: runner, Cleaner: successfulCleanup(), Events: events,
				Logf: func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
			}).Merge(context.Background(), mergeVerifyDetail(), tt.options)
			if err != nil {
				t.Fatal(err)
			}
			if called {
				t.Fatal("empty verification command should skip the runner")
			}
			if len(logs) != 0 {
				t.Fatalf("intentional skip should not log skipped detection, logs=%#v", logs)
			}
			verification := events.requireSingle(t, plan.EventTypeMergeVerification)
			events.requireSingle(t, plan.EventTypePlanMerged)
			if verification.Result != "skipped" || verification.Reason != tt.wantReason {
				t.Fatalf("unexpected skipped verification event: %#v", verification)
			}
			if hasGitCall(git.calls, "reset-hard pre123") {
				t.Fatalf("skipped verify should not roll back, git calls: %#v", git.calls)
			}
		})
	}
}

// mergeVerifyRoot is the repo root path used by verify and cleanup fake tests.
const mergeVerifyRoot = "/repo/root"

// mergeVerifyGit returns a fake git client pre-seeded in a registry bound to
// mergeVerifyRoot. Callers that need to inspect per-root call logs or set
// additional state (e.g. revParseSequence) may do so directly on the returned
// *fakeGitClient; the registry association is established at construction time.
func mergeVerifyGit() *fakeGitClient {
	return newFakeGitRegistry().seed(mergeVerifyRoot, &fakeGitClient{
		defaultBranch: "main",
		mergeBase:     "base123",
		revParse:      map[string]string{"main": "pre123", "tao/plan-a": "head123"},
		ancestors:     map[string]bool{"main..tao/plan-a": true},
	}).client(mergeVerifyRoot)
}

func mergeVerifyDetail() *plan.PlanDetail {
	detail := mergeReadyDetail("base123")
	detail.Dir = "/plans/plan-a"
	detail.State.Repo.Root = "/repo/root"
	detail.State.Plan.Review.Head = "head123"
	return detail
}

func mergeVerifyFixtureDetail(t *testing.T, files map[string]string) *plan.PlanDetail {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	detail := mergeVerifyDetail()
	detail.State.Repo.Root = root
	return detail
}

func unsetMergeVerifyCommandEnv(t *testing.T) {
	t.Helper()
	original, ok := os.LookupEnv(runtimeconfig.EnvMergeVerifyCommand)
	if err := os.Unsetenv(runtimeconfig.EnvMergeVerifyCommand); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ok {
			if err := os.Setenv(runtimeconfig.EnvMergeVerifyCommand, original); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := os.Unsetenv(runtimeconfig.EnvMergeVerifyCommand); err != nil {
			t.Fatal(err)
		}
	})
}

func hasGitCall(calls []string, want string) bool {
	return slices.Contains(calls, want)
}
