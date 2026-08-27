package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/verifydetect"
)

const (
	finalVerificationPassed         = "passed"
	finalVerificationFailed         = "failed"
	finalVerificationSkipped        = "skipped"
	maxFinalVerificationOutputBytes = 8 * 1024
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
	command := finalVerificationCommand(detail, executionRoot)
	verification := plan.FinalVerification{
		Command:    command,
		CWD:        executionRoot,
		Result:     finalVerificationSkipped,
		VerifiedAt: now(f.execution).UTC(),
	}
	if detail != nil && detail.State.Workspace != nil {
		verification.HeadSHA = strings.TrimSpace(detail.State.Workspace.HeadSHA)
	}
	if command == "" {
		recordErr := f.recordFinalVerification(detail, verification)
		f.appendFinalVerificationEvent(detail, verification, verification.VerifiedAt, nil)
		return recordErr
	}

	headSHA, err := f.execution.Dependencies.reviewGitFactory(executionRoot).RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("resolve final verification HEAD: %w", err)
	}
	verification.HeadSHA = strings.TrimSpace(headSHA)
	if verification.HeadSHA == "" {
		return fmt.Errorf("resolve final verification HEAD: git returned an empty revision")
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	startedAt := now(f.execution)
	runErr := f.execution.Dependencies.CommandRunner(ctx, executionRoot, "sh", []string{"-c", command}, &stdout, &stderr)
	finishedAt := now(f.execution)
	durationSeconds := max(int64(finishedAt.Sub(startedAt)/time.Second), 0)
	combined := combineFinalVerificationOutput(stdout.String(), stderr.String())
	if runErr != nil {
		verification.Result = finalVerificationFailed
		verification.Details, verification.OutputTruncated = boundedFinalVerificationDetails(combined)
		verification.Fingerprint = finalVerificationFingerprint(verification)
		recordErr := f.recordFinalVerification(detail, verification)
		f.appendFinalVerificationEvent(detail, verification, finishedAt.UTC(), &durationSeconds)
		if recordErr != nil {
			return fmt.Errorf("record failed final repository verification: %w (verification error: %w)", recordErr, runErr)
		}
		return &FinalVerificationError{Verification: verification, Cause: runErr}
	}
	verification.Result = finalVerificationPassed
	verification.Details, verification.OutputTruncated = boundedFinalVerificationDetails(combined)
	verification.Fingerprint = finalVerificationFingerprint(verification)
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

func finalVerificationCommand(detail *plan.PlanDetail, executionRoot string) string {
	if detail != nil {
		for i := len(detail.Slices.Slices) - 1; i >= 0; i-- {
			slice := detail.Slices.Slices[i]
			if slice.VerificationRepair != nil && slice.Status == plan.StatusCompleted {
				return slice.VerificationRepair.Command
			}
		}
	}
	return verifydetect.DetectCommand(executionRoot)
}

func boundedFinalVerificationDetails(output string) (string, bool) {
	output = strings.TrimSpace(output)
	truncated := len(output) > maxFinalVerificationOutputBytes
	if truncated {
		output = output[len(output)-maxFinalVerificationOutputBytes:]
		for len(output) > 0 && !utf8.ValidString(output) {
			output = output[1:]
		}
	}
	return output, truncated
}

func finalVerificationFingerprint(verification plan.FinalVerification) string {
	sum := sha256.Sum256([]byte(verification.Command + "\x00" + verification.HeadSHA + "\x00" + verification.Result + "\x00" + verification.Details))
	return hex.EncodeToString(sum[:])
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
