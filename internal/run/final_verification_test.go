package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
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
			} else if event.Command != "" || event.DurationSeconds != nil {
				t.Fatalf("unexpected skipped event: %+v", event)
			}
		})
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
		Now: advancingFinalVerificationClock(initial, 3*time.Second),
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
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error { return nil },
		Now:           advancingFinalVerificationClock(verifiedAt, 3*time.Second),
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
