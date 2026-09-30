package run

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/plan"
)

func TestSliceVerifierOrderAndEvidence(t *testing.T) {
	root := sliceVerifierRoot(t)
	sub := filepath.Join(root, "pkg")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	var calls []string
	clock := time.Unix(100, 0)
	v := SliceVerifier{Now: func() time.Time { clock = clock.Add(25 * time.Millisecond); return clock }, CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 10*time.Minute || time.Until(deadline) < 9*time.Minute {
			t.Fatal("missing fixed command deadline")
		}
		if name != "sh" || len(args) != 2 || args[0] != "-c" {
			t.Fatal("not the shell seam")
		}
		calls = append(calls, cwd+":"+args[1])
		_, _ = io.WriteString(stdout, "out")
		_, _ = io.WriteString(stderr, "err")
		return nil
	}}
	runs, err := v.Verify(context.Background(), SliceVerificationRequest{root, plan.Verification{Commands: []string{"first", "again", "again"}, Steps: []plan.VerificationStep{{Command: "again", CWD: "pkg"}, {Command: "again", CWD: sub}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{root + ":first", sub + ":again", sub + ":again"}) {
		t.Fatalf("calls = %v", calls)
	}
	for i, run := range runs {
		if run.CommandIndex != i+1 || run.Source != "tao" || run.Result != "passed" || run.ExitCode == nil || *run.ExitCode != 0 || run.DurationMilliseconds == nil || *run.DurationMilliseconds != 25 || len(run.OutputDigest) != 64 {
			t.Fatalf("run = %+v", run)
		}
	}
}

func TestSliceVerifierRejectsDeclarations(t *testing.T) {
	root := sliceVerifierRoot(t)
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name         string
		verification plan.Verification
	}{
		{"empty", plan.Verification{Commands: []string{"true", " "}}},
		{"nul", plan.Verification{Commands: []string{"true\x00"}}},
		{"step-only", plan.Verification{Steps: []plan.VerificationStep{{Command: "extra"}}}},
		{"extra", plan.Verification{Commands: []string{"true"}, Steps: []plan.VerificationStep{{Command: "extra"}}}},
		{"conflict", plan.Verification{Commands: []string{"true"}, Steps: []plan.VerificationStep{{Command: "true"}, {Command: "true", CWD: "pkg"}}}},
		{"missing", plan.Verification{Commands: []string{"true"}, Steps: []plan.VerificationStep{{Command: "true", CWD: "missing"}}}},
		{"escape", plan.Verification{Commands: []string{"true"}, Steps: []plan.VerificationStep{{Command: "true", CWD: "escape"}}}},
		{"parent", plan.Verification{Commands: []string{"true"}, Steps: []plan.VerificationStep{{Command: "true", CWD: ".."}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := SliceVerifier{CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
				t.Fatal("invalid declaration executed")
				return nil
			}}
			runs, err := v.Verify(context.Background(), SliceVerificationRequest{root, tc.verification})
			var gateErr *SliceVerificationError
			if !errors.As(err, &gateErr) || len(runs) != 0 {
				t.Fatalf("runs = %+v, error = %v", runs, err)
			}
		})
	}
}

func TestSliceVerifierFailureKinds(t *testing.T) {
	root := sliceVerifierRoot(t)
	for _, tc := range []struct {
		command string
		kind    plan.FinalVerificationFailureKind
		code    int
	}{
		{"exit 2", plan.FinalVerificationFailureKindCode, 2},
		{"tao_nonexistent_verification_executable", plan.FinalVerificationFailureKindToolMissing, 127},
		{"exit 126", plan.FinalVerificationFailureKindInvalidCommand, 126},
		{"printf 'No test files found' >&2; exit 1", plan.FinalVerificationFailureKindInvalidCommand, 1},
		{"kill -TERM $$", plan.FinalVerificationFailureKindCode, -1},
	} {
		t.Run(tc.command, func(t *testing.T) {
			runs, err := (SliceVerifier{}).Verify(context.Background(), SliceVerificationRequest{root, plan.Verification{Commands: []string{tc.command, "true"}}})
			var gateErr *SliceVerificationError
			if !errors.As(err, &gateErr) || len(runs) != 1 {
				t.Fatalf("runs=%+v err=%v", runs, err)
			}
			run := runs[0]
			if run.FailureKind != tc.kind || run.Result != "failed" {
				t.Fatalf("run=%+v", run)
			}
			if tc.code < 0 {
				if run.ExitCode != nil {
					t.Fatal("invented exit code")
				}
			} else if run.ExitCode == nil || *run.ExitCode != tc.code {
				t.Fatalf("exit=%v", run.ExitCode)
			}
		})
	}
}

func TestSliceVerifierCorrection(t *testing.T) {
	root := sliceVerifierRoot(t)
	sub := filepath.Join(root, "pkg")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "example_test.go"), []byte("package example"), 0600); err != nil {
		t.Fatal(err)
	}
	exitErr := exec.Command("sh", "-c", "exit 1").Run()
	for _, tc := range []struct {
		name, command  string
		correctedFails bool
		wantCalls      int
		wantError      bool
	}{
		{"success", "go test pkg/example_test.go", false, 3, false},
		{"corrected failure", "go test pkg/example_test.go", true, 2, true},
		{"rejected", "go test pkg/example_test.go && true", false, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			v := SliceVerifier{CommandRunner: func(_ context.Context, cwd, _ string, args []string, _, stderr io.Writer) error {
				calls = append(calls, args[1])
				if len(calls) == 1 {
					_, _ = io.WriteString(stderr, "No test files found")
					return exitErr
				}
				if len(calls) == 2 {
					if cwd != sub || args[1] != "go test example_test.go" {
						t.Fatalf("correction = %s %v", cwd, args)
					}
					if tc.correctedFails {
						_, _ = io.WriteString(stderr, "No test files found")
						return exitErr
					}
				}
				return nil
			}}
			runs, err := v.Verify(context.Background(), SliceVerificationRequest{root, plan.Verification{Commands: []string{tc.command, "next"}, Steps: []plan.VerificationStep{{Command: tc.command, CWD: "pkg"}}}})
			if (err != nil) != tc.wantError || len(calls) != tc.wantCalls || len(runs) != tc.wantCalls {
				t.Fatalf("runs=%+v err=%v", runs, err)
			}
			if len(runs) > 1 && (runs[1].OriginalCommand != tc.command || runs[1].CommandIndex != 1) {
				t.Fatalf("correction provenance = %+v", runs[1])
			}
			if len(runs) == 3 && runs[2].CommandIndex != 2 {
				t.Fatal("skipped subsequent gate")
			}
		})
	}
}

func TestSliceVerifierTimeoutAndCancellation(t *testing.T) {
	root := sliceVerifierRoot(t)
	for _, cancelParent := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelParent {
			cancel()
		}
		runs, err := (SliceVerifier{}).verify(ctx, SliceVerificationRequest{root, plan.Verification{Commands: []string{"sleep 30"}}}, 30*time.Millisecond)
		cancel()
		kind := plan.FinalVerificationFailureKindTimeout
		if cancelParent {
			kind = plan.FinalVerificationFailureKindCancelled
		}
		if err == nil {
			t.Fatal("expected failure")
		}
		// A pre-cancelled request does not manufacture an observed attempt.
		if cancelParent {
			if len(runs) != 0 || !errors.Is(err, context.Canceled) {
				t.Fatalf("runs=%+v err=%v", runs, err)
			}
			continue
		}
		if len(runs) != 1 || runs[0].FailureKind != kind || runs[0].ExitCode != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("runs=%+v err=%v", runs, err)
		}
	}
}

func TestSliceVerifierUnknownErrorAndBoundedDiagnostics(t *testing.T) {
	v := SliceVerifier{CommandRunner: func(_ context.Context, _, _ string, _ []string, stdout, stderr io.Writer) error {
		_, _ = io.WriteString(stdout, strings.Repeat("x", 100000))
		_, _ = stderr.Write([]byte{0xff, 0, 27})
		return errors.New(strings.Repeat("bad\x1b", 10000))
	}}
	runs, err := v.Verify(context.Background(), SliceVerificationRequest{sliceVerifierRoot(t), plan.Verification{Commands: []string{"gate"}}})
	if err == nil || len(err.Error()) > 20*1024 || strings.ContainsAny(err.Error(), "\x00\x1b") || len(runs) != 1 || runs[0].ExitCode != nil || !runs[0].OutputTruncated {
		t.Fatalf("unbounded or invented evidence: %+v", runs)
	}
}

func TestSliceVerifierRechecksCWD(t *testing.T) {
	root := sliceVerifierRoot(t)
	sub := filepath.Join(root, "pkg")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	calls := 0
	v := SliceVerifier{CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		calls++
		if calls == 1 {
			if err := os.Remove(sub); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, sub); err != nil {
				t.Fatal(err)
			}
		}
		return nil
	}}
	runs, err := v.Verify(context.Background(), SliceVerificationRequest{root, plan.Verification{Commands: []string{"first", "second"}, Steps: []plan.VerificationStep{{Command: "second", CWD: "pkg"}}}})
	if err == nil || calls != 1 || len(runs) != 1 {
		t.Fatalf("changed cwd executed: calls=%d runs=%+v err=%v", calls, runs, err)
	}
}

func TestSliceVerifierLargeRealOutput(t *testing.T) {
	runs, err := (SliceVerifier{}).Verify(context.Background(), SliceVerificationRequest{sliceVerifierRoot(t), plan.Verification{Commands: []string{`i=0; while [ "$i" -lt 10000 ]; do printf 'stdout chunk\n'; printf 'stderr chunk\n' >&2; i=$((i+1)); done; printf '\377\000\033'`}}})
	if err != nil || len(runs) != 1 || !runs[0].OutputTruncated || runs[0].Result != "passed" {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	var stdout, stderr verificationTail
	for range 10000 {
		_, _ = io.WriteString(&stdout, "stdout chunk\n")
		_, _ = io.WriteString(&stderr, "stderr chunk\n")
	}
	_, _ = stdout.Write([]byte{0xff, 0, 27})
	if runs[0].OutputDigest != verificationOutputDigest(&stdout, &stderr) {
		t.Fatal("real streams not fully drained or hashed raw")
	}
}

func TestSliceVerifierInheritedCancellation(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
		}
		v := SliceVerifier{CommandRunner: func(ctx context.Context, _, _ string, _ []string, _, _ io.Writer) error {
			if !deadline {
				cancel()
			}
			<-ctx.Done()
			return ctx.Err()
		}}
		runs, err := v.Verify(ctx, SliceVerificationRequest{sliceVerifierRoot(t), plan.Verification{Commands: []string{"gate", "never"}}})
		cancel()
		kind, cause := plan.FinalVerificationFailureKindCancelled, context.Canceled
		if deadline {
			kind, cause = plan.FinalVerificationFailureKindTimeout, context.DeadlineExceeded
		}
		if !errors.Is(err, cause) || len(runs) != 1 || runs[0].FailureKind != kind || runs[0].ExitCode != nil {
			t.Fatalf("runs=%+v err=%v", runs, err)
		}
	}
}

func TestSliceVerifierMissingShellAndEmptyDeclaration(t *testing.T) {
	root := sliceVerifierRoot(t)
	v := SliceVerifier{CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error { return exec.ErrNotFound }}
	runs, err := v.Verify(context.Background(), SliceVerificationRequest{root, plan.Verification{Commands: []string{"gate"}}})
	if err == nil || len(runs) != 1 || runs[0].FailureKind != plan.FinalVerificationFailureKindToolMissing || runs[0].ExitCode != nil {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	runs, err = v.Verify(context.Background(), SliceVerificationRequest{root, plan.Verification{}})
	if err != nil || len(runs) != 0 {
		t.Fatalf("empty declaration: runs=%+v err=%v", runs, err)
	}
}

func TestSliceVerifierCacheAcrossWorktreesAndSteps(t *testing.T) {
	shared := t.TempDir()
	t.Setenv("GOLANGCI_LINT_CACHE", shared)
	repo := initSliceCompletionRepo(t)
	for _, branch := range []string{"cache-one", "cache-two"} {
		root := filepath.Join(sliceVerifierRoot(t), "worktree with spaces")
		runCommitTestGitCommand(t, repo, "worktree", "add", "-b", branch, root)
		sub := filepath.Join(root, "pkg")
		if err := os.Mkdir(sub, 0700); err != nil {
			t.Fatal(err)
		}
		command := `printf '%s\n' "$GOLANGCI_LINT_CACHE"; printf 'cache data\n' >> "$GOLANGCI_LINT_CACHE/entries"`
		rootCommand := `printf '%s\n' "$GOLANGCI_LINT_CACHE"`
		calls := 0
		v := SliceVerifier{CommandRunner: func(ctx context.Context, cwd, name string, args []string, stdout, stderr io.Writer) error {
			wantCWD, wantCommand := root, rootCommand
			if calls > 0 {
				wantCWD, wantCommand = sub, command
			}
			calls++
			if cwd != wantCWD || name != "sh" || !reflect.DeepEqual(args, []string{"-c", wantCommand}) {
				t.Fatalf("changed dispatch: %s %s %v", cwd, name, args)
			}
			return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
		}}
		runs, err := v.Verify(context.Background(), SliceVerificationRequest{root, plan.Verification{
			Commands: []string{rootCommand, command, command},
			Steps:    []plan.VerificationStep{{Command: command, CWD: "pkg"}},
		}})
		if err != nil || len(runs) != 3 {
			t.Fatalf("runs=%+v err=%v", runs, err)
		}
		cache := filepath.Join(root, ".tao", "cache", "golangci-lint")
		for _, run := range runs {
			if run.Result != "passed" || !strings.Contains(run.Details, "\n"+cache+"\n") {
				t.Fatalf("wrong cache: %+v; want %s", run, cache)
			}
		}
		contents, err := os.ReadFile(filepath.Join(cache, "entries")) //nolint:gosec // G304: fixed file in a test-owned temporary worktree.
		if err != nil || string(contents) != "cache data\ncache data\n" {
			t.Fatalf("repeated cache use: %q, %v", contents, err)
		}
		if _, err := os.Stat(filepath.Join(sub, ".tao")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("prepared subdirectory cache: %v", err)
		}
	}
	if os.Getenv("GOLANGCI_LINT_CACHE") != shared {
		t.Fatal("changed parent environment")
	}
	entries, err := os.ReadDir(shared)
	if err != nil || len(entries) != 0 {
		t.Fatalf("wrote shared cache: %v %v", entries, err)
	}
}

func TestSliceVerifierCacheMechanicalCorrection(t *testing.T) {
	root := sliceVerifierRoot(t)
	sub := filepath.Join(root, "pkg")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "example_test.go"), []byte("package example"), 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	// A deterministic tool failure exercises the real shell and correction classifier.
	tool := "#!/bin/sh\nprintf '%s\\n' \"$GOLANGCI_LINT_CACHE\"\nif [ \"$2\" = pkg/example_test.go ]; then echo 'No test files found' >&2; exit 1; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte(tool), 0700); err != nil { //nolint:gosec // G306: owner-only executable permission for the test tool.
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GOLANGCI_LINT_CACHE", t.TempDir())
	command := "go test pkg/example_test.go"
	v := SliceVerifier{CommandRunner: commandrunner.DefaultLocal}
	runs, err := v.Verify(context.Background(), SliceVerificationRequest{root, plan.Verification{Commands: []string{command}, Steps: []plan.VerificationStep{{Command: command, CWD: "pkg"}}}})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	cache := filepath.Join(root, ".tao", "cache", "golangci-lint")
	for _, run := range runs {
		if run.CWD != sub || !strings.Contains(run.Details, cache) {
			t.Fatalf("correction lost root cache: %+v", run)
		}
	}
	if runs[0].Command != command || runs[0].Result != "failed" || runs[1].Command != "go test example_test.go" || runs[1].OriginalCommand != command || runs[1].Result != "passed" {
		t.Fatalf("changed correction evidence: %+v", runs)
	}
}

func TestSliceVerifierCacheSetupFailure(t *testing.T) {
	root := sliceVerifierRoot(t)
	if err := os.WriteFile(filepath.Join(root, ".tao"), []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	runs, err := (SliceVerifier{}).Verify(context.Background(), SliceVerificationRequest{root, plan.Verification{Commands: []string{"touch executed"}}})
	if err == nil || len(runs) != 1 || runs[0].Result != "failed" || !strings.Contains(err.Error(), "prepare verification cache") {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
	if _, err := os.Stat(filepath.Join(root, "executed")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command executed after setup failure: %v", err)
	}
}

func sliceVerifierRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}
