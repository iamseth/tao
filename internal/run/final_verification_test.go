package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestVerifyCompletedBranchAppendsOutcomeEvents(t *testing.T) {
	tests := []struct {
		name       string
		hasCommand bool
		runErr     error
		wantResult string
	}{
		{name: "passed", hasCommand: true, wantResult: finalVerificationPassed},
		{name: "failed", hasCommand: true, runErr: errors.New("exit status 1"), wantResult: finalVerificationFailed},
		{name: "skipped", wantResult: finalVerificationSkipped},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.hasCommand {
				if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			detail := completedReviewPlanDetail(t.TempDir())
			var events []plan.Event
			clock := advancingFinalVerificationClock(time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC), 3*time.Second)
			runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error {
				return tt.runErr
			}
			finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
				CommandRunner:     runner,
				reviewGitFactory:  fixedReviewGit(&fakeReviewGit{head: "live-head"}),
				Now:               clock,
				PlanRecordFactory: memoryPlanRecordFactory,
				EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error {
					events = append(events, event)
					return nil
				}),
			}))

			err := finalizer.verifyCompletedBranch(context.Background(), detail, root)
			if tt.runErr == nil && err != nil {
				t.Fatal(err)
			}
			if tt.runErr != nil {
				var verificationErr *FinalVerificationError
				if !errors.As(err, &verificationErr) {
					t.Fatalf("error = %v, want FinalVerificationError", err)
				}
			}
			var event *plan.Event
			for i := range events {
				if events[i].Type == plan.EventTypeFinalVerification && events[i].PlanID == "plan-a" {
					event = &events[i]
					break
				}
			}
			if event == nil || event.Result != tt.wantResult {
				t.Fatalf("final verification event = %+v, want result %q", event, tt.wantResult)
			}
			if !strings.Contains(event.Message, root) {
				t.Fatalf("message %q does not name cwd %q", event.Message, root)
			}
			if tt.hasCommand {
				if event.Command != "make verify" || event.DurationSeconds == nil || *event.DurationSeconds != 3 {
					t.Fatalf("unexpected executed event: %+v", event)
				}
			} else if event.Command != "" || event.DurationSeconds != nil || event.FailureKind != "" || event.ExitCode != nil {
				t.Fatalf("unexpected skipped event: %+v", event)
			}
			verification := detail.State.Plan.FinalVerification
			if verification == nil || verification.Result != tt.wantResult {
				t.Fatalf("persisted final verification = %+v, want result %q", verification, tt.wantResult)
			}
			if tt.wantResult != finalVerificationFailed && (verification.FailureKind != "" || verification.ExitCode != nil) {
				t.Fatalf("non-failure was classified: %+v", verification)
			}
			if tt.wantResult == finalVerificationFailed && (verification.FailureKind != plan.FinalVerificationFailureKindCode || verification.ExitCode != nil || event.FailureKind != verification.FailureKind || event.ExitCode != nil) {
				t.Fatalf("failed verification classification was not carried to its event: verification=%+v event=%+v", verification, event)
			}
		})
	}
}

func TestClassifyFinalVerificationFailure(t *testing.T) {
	deadlineCtx, deadlineCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer deadlineCancel()
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	tests := []struct {
		name     string
		ctx      context.Context
		runErr   error
		wantKind plan.FinalVerificationFailureKind
		wantCode int
	}{
		{name: "code", ctx: context.Background(), runErr: finalVerificationProcessError(t, exec.Command("sh", "-c", "exit 1")), wantKind: plan.FinalVerificationFailureKindCode, wantCode: 1},
		{name: "tool missing", ctx: context.Background(), runErr: finalVerificationProcessError(t, exec.Command("sh", "-c", "exit 127")), wantKind: plan.FinalVerificationFailureKindToolMissing, wantCode: 127},
		{name: "timeout", ctx: deadlineCtx, runErr: finalVerificationProcessError(t, exec.Command("sh", "-c", "exit 1")), wantKind: plan.FinalVerificationFailureKindTimeout, wantCode: 1},
		{name: "cancelled", ctx: cancelledCtx, runErr: finalVerificationProcessError(t, exec.Command("sh", "-c", "exit 1")), wantKind: plan.FinalVerificationFailureKindCancelled, wantCode: 1},
		{name: "invalid command", ctx: context.Background(), runErr: finalVerificationProcessError(t, exec.Command("sh", "-c", "exit 126")), wantKind: plan.FinalVerificationFailureKindInvalidCommand, wantCode: 126},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, code := classifyFinalVerificationFailure(test.ctx, test.runErr)
			if kind != test.wantKind || code == nil || *code != test.wantCode {
				t.Fatalf("classification = %q, %v, want %q, %d", kind, code, test.wantKind, test.wantCode)
			}
		})
	}
}

func TestFinalVerificationSignalExitCodeIsOmitted(t *testing.T) {
	kind, code := classifyFinalVerificationFailure(context.Background(), finalVerificationProcessError(t, exec.Command("sh", "-c", "kill -TERM $$")))
	if kind != plan.FinalVerificationFailureKindCode || code != nil {
		t.Fatalf("signal classification = %q, %v, want code with nil exit code", kind, code)
	}
}

func TestAppendFinalVerificationEventCopiesFailureEvidence(t *testing.T) {
	exitCode := 127
	var event plan.Event
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		EventAppender: eventAppenderFunc(func(_ string, appended plan.Event) error {
			event = appended
			return nil
		}),
	}))
	detail := completedReviewPlanDetail(t.TempDir())
	verification := plan.FinalVerification{CWD: "/repo", Result: finalVerificationFailed, FailureKind: plan.FinalVerificationFailureKindToolMissing, ExitCode: &exitCode}
	finalizer.appendFinalVerificationEvent(detail, verification, time.Now(), nil)
	exitCode = 1
	if event.FailureKind != plan.FinalVerificationFailureKindToolMissing || event.ExitCode == nil || *event.ExitCode != 127 || event.ExitCode == verification.ExitCode {
		t.Fatalf("event failure evidence = %+v", event)
	}
}

func finalVerificationProcessError(t *testing.T, command *exec.Cmd) error {
	t.Helper()
	err := command.Run()
	if err == nil {
		t.Fatalf("command %q unexpectedly passed", command.String())
	}
	return err
}

func TestVerifyCompletedBranchRefusesToRunWhenLiveHeadCannotBeResolved(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandRan := false
	detail := completedReviewPlanDetail(t.TempDir())
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			commandRan = true
			return nil
		},
		reviewGitFactory:  fixedReviewGit(&fakeReviewGit{headErr: errors.New("revision unavailable")}),
		PlanRecordFactory: memoryPlanRecordFactory,
	}))

	err := finalizer.verifyCompletedBranch(context.Background(), detail, root)
	if err == nil || !strings.Contains(err.Error(), "resolve final verification HEAD") {
		t.Fatalf("error = %v, want live HEAD resolution failure", err)
	}
	if commandRan {
		t.Fatal("verification command ran without a resolved live HEAD")
	}
	if detail.State.Plan.FinalVerification != nil {
		t.Fatalf("final verification was recorded without a live HEAD: %#v", detail.State.Plan.FinalVerification)
	}
}

func TestFailedFinalVerificationAfterCleanRebaseRemainsRepairable(t *testing.T) {
	root := initSliceCompletionRepo(t)
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCommitTestGitCommand(t, root, "add", "Makefile")
	runCommitTestGitCommand(t, root, "commit", "-m", "feature before rebase")
	staleHead := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	runCommitTestGitCommand(t, root, "commit", "--amend", "-m", "feature after rebase")
	liveHead := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	if liveHead == staleHead {
		t.Fatal("amended commit did not change HEAD")
	}

	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Workspace = &plan.Workspace{Branch: "tao/test", HeadSHA: staleHead}
	factory := func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
		return plan.NewPlanRecord(detail.Dir, detail)
	}
	execution := testRunExecution(ExecutionConfig{
		ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated},
	}, RunDependencies{
		CommandRunner:     defaultCommandRunner,
		PlanRecordFactory: factory,
	})
	execution.ExecutionRoot = root
	finalizer := newFinalizer(io.Discard, execution)

	err := finalizer.verifyCompletedBranch(context.Background(), detail, root)
	var verificationErr *FinalVerificationError
	if !errors.As(err, &verificationErr) {
		t.Fatalf("error = %v, want FinalVerificationError", err)
	}
	if verificationErr.Verification.HeadSHA != liveHead {
		t.Fatalf("failed verification head = %q, want live rebased head %q", verificationErr.Verification.HeadSHA, liveHead)
	}
	if detail.State.Workspace.HeadSHA != liveHead {
		t.Fatalf("persisted workspace head = %q, want live rebased head %q", detail.State.Workspace.HeadSHA, liveHead)
	}
	if plan.CurrentFailedFinalVerification(detail) == nil {
		t.Fatal("failed verification was not current after rebased HEAD was persisted")
	}
	if err := appendVerificationRepair(context.Background(), detail, execution); err != nil {
		t.Fatalf("append verification repair after clean rebase: %v", err)
	}
	if len(detail.State.Plan.PendingSlices) != 1 || !strings.HasPrefix(detail.State.Plan.PendingSlices[0], plan.VerificationRepairSlicePrefix) {
		t.Fatalf("pending repair slices = %v", detail.State.Plan.PendingSlices)
	}
}

func TestReverifyCompletedRunReplacesEvidenceAtSameHeadWithoutAppendingSlice(t *testing.T) {
	root := initSliceCompletionRepo(t)
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCommitTestGitCommand(t, root, "add", "Makefile")
	runCommitTestGitCommand(t, root, "commit", "-m", "add verification")
	branch := strings.TrimSpace(runCommitTestGitOutput(t, root, "branch", "--show-current"))
	head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	detail := completedReviewPlanDetail(t.TempDir())
	detail.State.Repo.Root = root
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: root, Branch: branch, HeadSHA: head}
	detail.State.Plan.FinalVerification = &plan.FinalVerification{Command: "make verify", CWD: root, HeadSHA: head, Result: finalVerificationFailed, Fingerprint: "prior-failure", VerifiedAt: time.Now().Add(-time.Hour)}
	sliceCount := len(detail.Slices.Slices)
	execution := testRunExecution(ExecutionConfig{
		ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicySlice, ExecutionMode: ExecutionModeIsolated},
		Reverify:           true,
	}, RunDependencies{
		CommandRunner:     defaultCommandRunner,
		PlanRecordFactory: memoryPlanRecordFactory,
	})
	execution.ExecutionRoot = root
	finalizer := newFinalizer(io.Discard, execution)

	complete, err := finalizer.FinalizeIfComplete(context.Background(), 0, detail, plan.AnalyzeRunCapabilities(detail))
	if err != nil {
		t.Fatalf("reverify completed run: %v", err)
	}
	if !complete {
		t.Fatal("completed run was not finalized")
	}
	verification := detail.State.Plan.FinalVerification
	if verification == nil || verification.Result != finalVerificationPassed || verification.HeadSHA != head || verification.Fingerprint == "prior-failure" {
		t.Fatalf("replacement verification = %+v", verification)
	}
	if len(detail.Slices.Slices) != sliceCount || len(detail.State.Plan.PendingSlices) != 0 {
		t.Fatalf("reverify changed slices: slices=%d pending=%v", len(detail.Slices.Slices), detail.State.Plan.PendingSlices)
	}
}

func TestVerifyCompletedBranchPreservesPersistedTimestampAndUsesCompletionForEvent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	initial := time.Date(2026, 7, 14, 1, 0, 0, 0, time.UTC)
	completion := initial.Add(6 * time.Second)
	detail := completedReviewPlanDetail(t.TempDir())
	var persisted plan.State
	var event plan.Event
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			return nil
		},
		reviewGitFactory: fixedReviewGit(&fakeReviewGit{head: "live-head"}),
		Now:              advancingFinalVerificationClock(initial, 3*time.Second),
		PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return captureReviewRecord{detail: detail, wrote: &persisted}, nil
		},
		EventAppender: eventAppenderFunc(func(_ string, appended plan.Event) error {
			event = appended
			return nil
		}),
	}))

	if err := finalizer.verifyCompletedBranch(context.Background(), detail, root); err != nil {
		t.Fatal(err)
	}
	if persisted.Plan.FinalVerification == nil || !persisted.Plan.FinalVerification.VerifiedAt.Equal(initial) {
		t.Fatalf("persisted verification timestamp = %+v, want %s", persisted.Plan.FinalVerification, initial)
	}
	if !persisted.UpdatedAt.Equal(initial) {
		t.Fatalf("persisted updated_at = %s, want %s", persisted.UpdatedAt, initial)
	}
	if persisted.Plan.Timing.LastActivityAt == nil || !persisted.Plan.Timing.LastActivityAt.Equal(initial) {
		t.Fatalf("persisted last_activity_at = %v, want %s", persisted.Plan.Timing.LastActivityAt, initial)
	}
	if !event.Timestamp.Equal(completion) {
		t.Fatalf("event timestamp = %s, want command completion %s", event.Timestamp, completion)
	}
}

func TestVerifyCompletedBranchStateWriteFailureStillEmitsOutcomeEvent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	verifiedAt := time.Date(2026, 7, 18, 2, 0, 0, 0, time.UTC)
	finishedAt := verifiedAt.Add(6 * time.Second)
	detail := completedReviewPlanDetail(t.TempDir())
	detail.Events = []plan.Event{{Type: "plan_created", PlanID: "plan-a"}}
	var appended plan.Event
	persistErr := errors.New("state unavailable")
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner:    func(context.Context, string, string, []string, io.Writer, io.Writer) error { return nil },
		reviewGitFactory: fixedReviewGit(&fakeReviewGit{head: "live-head"}),
		Now:              advancingFinalVerificationClock(verifiedAt, 3*time.Second),
		PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return failingFinalVerificationRecord{detail: detail, err: persistErr}, nil
		},
		EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error {
			appended = event
			return nil
		}),
	}))

	err := finalizer.verifyCompletedBranch(context.Background(), detail, root)
	if !errors.Is(err, persistErr) {
		t.Fatalf("verification error = %v, want state persistence error", err)
	}
	verification := detail.State.Plan.FinalVerification
	if verification == nil || verification.Result != finalVerificationPassed || !verification.VerifiedAt.Equal(verifiedAt) {
		t.Fatalf("in-memory final verification = %#v", verification)
	}
	if !detail.State.UpdatedAt.Equal(verifiedAt) || detail.State.Plan.Timing.LastActivityAt == nil || !detail.State.Plan.Timing.LastActivityAt.Equal(verifiedAt) {
		t.Fatalf("in-memory state timestamps = updated:%s activity:%v", detail.State.UpdatedAt, detail.State.Plan.Timing.LastActivityAt)
	}
	if appended.Type != plan.EventTypeFinalVerification || appended.Result != finalVerificationPassed || !appended.Timestamp.Equal(finishedAt) {
		t.Fatalf("appended outcome event = %#v, want completion timestamp %s", appended, finishedAt)
	}
	for _, event := range detail.Events {
		if event.Type == plan.EventTypeFinalVerification {
			t.Fatalf("current event seam unexpectedly appended to in-memory detail: %#v", event)
		}
	}
}

func TestVerifyCompletedBranchTruncatesEventReason(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	details := strings.Repeat("界", 1001)
	var event plan.Event
	finalizer := newFinalizer(io.Discard, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: func(_ context.Context, _ string, _ string, _ []string, stdout, _ io.Writer) error {
			_, _ = io.WriteString(stdout, details)
			return nil
		},
		reviewGitFactory:  fixedReviewGit(&fakeReviewGit{head: "live-head"}),
		PlanRecordFactory: memoryPlanRecordFactory,
		EventAppender: eventAppenderFunc(func(_ string, appended plan.Event) error {
			event = appended
			return nil
		}),
	}))

	if err := finalizer.verifyCompletedBranch(context.Background(), completedReviewPlanDetail(t.TempDir()), root); err != nil {
		t.Fatal(err)
	}
	if got := len([]rune(event.Reason)); got != 1000 {
		t.Fatalf("reason length = %d, want 1000", got)
	}
	if event.Reason != strings.Repeat("界", 1000) {
		t.Fatal("reason was not truncated on a character boundary")
	}
}

func TestFinalVerificationOutputKeepsBoundedTailWithExplicitTruncation(t *testing.T) {
	prefix := strings.Repeat("successful package\n", 1000)
	tail := "FAIL\tgithub.com/iamseth/tao/internal/actionable\nfinal assertion failed"
	details, truncated := boundedFinalVerificationDetails(prefix + tail)
	if !truncated {
		t.Fatal("long output was not marked truncated")
	}
	if len(details) > maxFinalVerificationOutputBytes {
		t.Fatalf("details bytes = %d, want at most %d", len(details), maxFinalVerificationOutputBytes)
	}
	if !strings.HasSuffix(details, tail) {
		t.Fatalf("actionable output tail was not retained: %q", details[len(details)-100:])
	}
	if strings.HasPrefix(details, "successful package") {
		t.Fatal("long successful prefix was retained instead of the failure tail")
	}
}

func TestVerifyCompletedBranchEventAppendFailureIsBestEffort(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Makefile"), []byte("verify:\n\t@true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := completedReviewPlanDetail(t.TempDir())
	commandErr := errors.New("exit status 1")
	var persisted plan.State
	var out bytes.Buffer
	finalizer := newFinalizer(&out, testRunExecution(ExecutionConfig{}, RunDependencies{
		CommandRunner: func(_ context.Context, _ string, _ string, _ []string, _, stderr io.Writer) error {
			_, _ = io.WriteString(stderr, "full verification failure")
			return commandErr
		},
		reviewGitFactory: fixedReviewGit(&fakeReviewGit{head: "live-head"}),
		PlanRecordFactory: func(detail *plan.PlanDetail) (PlanMutationRecord, error) {
			return captureReviewRecord{detail: detail, wrote: &persisted}, nil
		},
		EventAppender: eventAppenderFunc(func(string, plan.Event) error {
			return errors.New("journal unavailable")
		}),
	}))

	err := finalizer.verifyCompletedBranch(context.Background(), detail, root)
	var verificationErr *FinalVerificationError
	if !errors.As(err, &verificationErr) || !errors.Is(err, commandErr) {
		t.Fatalf("error = %v, want original FinalVerificationError", err)
	}
	if verificationErr.Verification.Result != finalVerificationFailed || verificationErr.Verification.Details != "full verification failure" {
		t.Fatalf("returned verification changed: %+v", verificationErr.Verification)
	}
	if persisted.Plan.FinalVerification == nil || *persisted.Plan.FinalVerification != verificationErr.Verification {
		t.Fatalf("persisted verification = %+v, returned = %+v", persisted.Plan.FinalVerification, verificationErr.Verification)
	}
	if !strings.Contains(out.String(), "Warning: append final verification event: journal unavailable") {
		t.Fatalf("warning missing from output: %q", out.String())
	}
}

type failingFinalVerificationRecord struct {
	PlanMutationRecord
	detail *plan.PlanDetail
	err    error
}

func (r failingFinalVerificationRecord) RecordFinalVerification(verification plan.FinalVerification) error {
	if err := plan.MarkFinalVerification(r.detail, verification); err != nil {
		return err
	}
	return r.err
}

func advancingFinalVerificationClock(start time.Time, step time.Duration) func() time.Time {
	next := start
	return func() time.Time {
		current := next
		next = next.Add(step)
		return current
	}
}
