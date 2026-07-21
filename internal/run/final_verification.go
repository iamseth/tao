package run

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/verifydetect"
)

const (
	finalVerificationPassed  = "passed"
	finalVerificationFailed  = "failed"
	finalVerificationSkipped = "skipped"
)

// FinalVerificationError reports a repository-wide gate failure with enough
// context to reproduce it from the plan execution root.
type FinalVerificationError struct {
	Verification plan.FinalVerification
	Cause        error
}

func (e *FinalVerificationError) Error() string {
	message := fmt.Sprintf("final repository verification failed in %s", e.Verification.CWD)
	if e.Verification.Command != "" {
		message += fmt.Sprintf(" for %q", e.Verification.Command)
	}
	if details := strings.TrimSpace(e.Verification.Details); details != "" {
		message += ":\n" + details
	}
	return message
}

func (e *FinalVerificationError) Unwrap() error { return e.Cause }

// verifyCompletedBranch runs repository-owned broad verification in the plan
// execution root and persists its command and result before review begins.
func (f Finalizer) verifyCompletedBranch(ctx context.Context, detail *plan.PlanDetail, executionRoot string) error {
	command := verifydetect.DetectCommand(executionRoot)
	verification := plan.FinalVerification{
		Command:    command,
		CWD:        executionRoot,
		Result:     finalVerificationSkipped,
		VerifiedAt: now(f.execution).UTC(),
	}
	if command == "" {
		recordErr := f.recordFinalVerification(detail, verification)
		f.appendFinalVerificationEvent(detail, verification, verification.VerifiedAt, nil)
		return recordErr
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	startedAt := now(f.execution)
	runErr := f.execution.Dependencies.CommandRunner(ctx, executionRoot, "sh", []string{"-c", command}, &stdout, &stderr)
	finishedAt := now(f.execution)
	durationSeconds := max(int64(finishedAt.Sub(startedAt)/time.Second), 0)
	verification.Details = combineFinalVerificationOutput(stdout.String(), stderr.String())
	if runErr != nil {
		verification.Result = finalVerificationFailed
		recordErr := f.recordFinalVerification(detail, verification)
		f.appendFinalVerificationEvent(detail, verification, finishedAt.UTC(), &durationSeconds)
		if recordErr != nil {
			return fmt.Errorf("record failed final repository verification: %w (verification error: %w)", recordErr, runErr)
		}
		return &FinalVerificationError{Verification: verification, Cause: runErr}
	}
	verification.Result = finalVerificationPassed
	recordErr := f.recordFinalVerification(detail, verification)
	f.appendFinalVerificationEvent(detail, verification, finishedAt.UTC(), &durationSeconds)
	if recordErr != nil {
		return recordErr
	}
	return writef(f.outputWriter(), "Final verification: passed (%s)\n", command)
}

func (f Finalizer) appendFinalVerificationEvent(detail *plan.PlanDetail, verification plan.FinalVerification, timestamp time.Time, durationSeconds *int64) {
	appender := f.execution.Dependencies.EventAppender
	if detail == nil || appender == nil {
		return
	}
	reason := []rune(verification.Details)
	if len(reason) > 1000 {
		reason = reason[:1000]
	}
	event := plan.Event{
		Type:            plan.EventTypeFinalVerification,
		Timestamp:       timestamp,
		PlanID:          detail.State.Plan.ID,
		DurationSeconds: durationSeconds,
		Command:         verification.Command,
		Result:          verification.Result,
		Reason:          string(reason),
		Message:         fmt.Sprintf("Final verification %s in %s", verification.Result, verification.CWD),
	}
	if err := appender.AppendEvent(detail.Dir, event); err != nil {
		_, _ = fmt.Fprintf(f.outputWriter(), "Warning: append final verification event: %v\n", err)
	}
}

func (f Finalizer) recordFinalVerification(detail *plan.PlanDetail, verification plan.FinalVerification) error {
	if detail == nil {
		return fmt.Errorf("record final repository verification: plan detail is nil")
	}
	record, err := planMutationRecord(f.execution, detail)
	if err != nil {
		return fmt.Errorf("record final repository verification: %w", err)
	}
	if err := record.RecordFinalVerification(verification); err != nil {
		return fmt.Errorf("record final repository verification: %w", err)
	}
	return nil
}

func combineFinalVerificationOutput(stdout, stderr string) string {
	stdout = strings.TrimSpace(stdout)
	stderr = strings.TrimSpace(stderr)
	switch {
	case stdout == "":
		return stderr
	case stderr == "":
		return stdout
	default:
		return stdout + "\n" + stderr
	}
}
