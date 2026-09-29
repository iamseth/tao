package plan

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	SliceVerificationSnapshotVersion = 1
	VerificationSourceTao            = "tao"
	// Bounds apply to newly produced evidence, not historical verification rows.
	MaxVerificationIdentityBytes = 128
	MaxVerificationCommandBytes  = 4096
	MaxVerificationDetailsBytes  = 16 * 1024
	MaxSliceVerificationRuns     = 1024
)

// SliceVerificationSnapshot binds one Tao-observed attempt to its declaration
// and execution boundary. JSON names are the snake_case tags below; all fields
// are emitted within a present snapshot so replacing it cannot retain old data.
// VerificationAttempt is diagnostic until completion freezes the successful
// snapshot in an intent. Neither source labels nor events authorize recovery.
// Version 1 digests are lowercase, unprefixed SHA-256 hex strings.
// This additive contract does not itself execute gates or change completion.
type SliceVerificationSnapshot struct {
	Version             int               `json:"version"`
	AttemptID           string            `json:"attempt_id"`
	ExecutionRoot       string            `json:"execution_root"`
	StartingBranch      string            `json:"starting_branch"`
	StartingHead        string            `json:"starting_head"`
	WorktreeFingerprint string            `json:"worktree_fingerprint"`
	DeclarationDigest   string            `json:"declaration_digest"`
	Runs                []VerificationRun `json:"runs"`
	RecordedAt          time.Time         `json:"recorded_at"`
}

// Validate checks producer evidence without normalizing it or upgrading legacy
// rows. Unknown exit codes and durations remain nil, never inferred as zero.
// Declaration coverage and live Git identity must be checked by the completion
// transaction; this structural validation is not execution/recovery authority.
func (s SliceVerificationSnapshot) Validate() error {
	if s.Version != SliceVerificationSnapshotVersion {
		return fmt.Errorf("unsupported slice verification version %d", s.Version)
	}
	if !verificationIdentity(s.AttemptID) {
		return fmt.Errorf("invalid verification attempt ID")
	}
	if !verificationText(s.ExecutionRoot, MaxVerificationCommandBytes, true) || !filepath.IsAbs(s.ExecutionRoot) {
		return fmt.Errorf("invalid verification execution root")
	}
	if !verificationText(s.StartingBranch, MaxVerificationCommandBytes, true) || strings.ContainsAny(s.StartingBranch, "\r\n") || !isSHALike(s.StartingHead) {
		return fmt.Errorf("invalid verification starting branch/head")
	}
	if !verificationDigest(s.WorktreeFingerprint) || !verificationDigest(s.DeclarationDigest) {
		return fmt.Errorf("invalid verification boundary/declaration digest")
	}
	if s.RecordedAt.IsZero() {
		return fmt.Errorf("verification recorded_at is required")
	}
	if len(s.Runs) > MaxSliceVerificationRuns {
		return fmt.Errorf("too many verification runs")
	}
	for i, run := range s.Runs {
		if err := validateObservedVerificationRun(run); err != nil {
			return fmt.Errorf("verification run %d: %w", i+1, err)
		}
	}
	return nil
}

func validateObservedVerificationRun(run VerificationRun) error {
	if run.Source != VerificationSourceTao || run.CommandIndex < 1 {
		return fmt.Errorf("verification requires Tao source and one-based command index")
	}
	if !verificationText(run.Command, MaxVerificationCommandBytes, true) || !verificationText(run.OriginalCommand, MaxVerificationCommandBytes, false) {
		return fmt.Errorf("invalid or oversized command")
	}
	if !verificationText(run.CWD, MaxVerificationCommandBytes, true) || !filepath.IsAbs(run.CWD) {
		return fmt.Errorf("invalid verification cwd")
	}
	if !verificationText(run.Details, MaxVerificationDetailsBytes, false) {
		return fmt.Errorf("invalid or oversized verification details")
	}
	if !verificationDigest(run.OutputDigest) {
		return fmt.Errorf("invalid output digest")
	}
	if run.ExitCode != nil && *run.ExitCode < 0 {
		return fmt.Errorf("unknown exit code must be omitted")
	}
	if run.DurationMilliseconds != nil && *run.DurationMilliseconds < 0 {
		return fmt.Errorf("negative duration")
	}
	switch run.FailureKind {
	case "", FinalVerificationFailureKindCode, FinalVerificationFailureKindToolMissing, FinalVerificationFailureKindTimeout, FinalVerificationFailureKindCancelled, FinalVerificationFailureKindInvalidCommand:
	default:
		return fmt.Errorf("invalid failure kind")
	}
	switch run.Result {
	case "passed":
		if run.ExitCode == nil || *run.ExitCode != 0 || run.FailureKind != "" {
			return fmt.Errorf("passed run requires observed zero exit and no failure kind")
		}
	case "failed":
	default:
		return fmt.Errorf("invalid observed result")
	}
	return nil
}

func verificationText(value string, maxBytes int, required bool) bool {
	return len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0) && (!required || strings.TrimSpace(value) != "")
}

func verificationIdentity(value string) bool {
	return verificationText(value, MaxVerificationIdentityBytes, true) && strings.IndexFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

func verificationDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func markSliceVerification(detail *PlanDetail, sliceID string, expected *SliceVerificationSnapshot, snapshot SliceVerificationSnapshot) error {
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if detail.State.Status != StatusInProgress || detail.State.Plan.CurrentSlice == nil || *detail.State.Plan.CurrentSlice != sliceID || slice.Status != StatusInProgress {
		return fmt.Errorf("slice %s is not the selected in-progress slice", sliceID)
	}
	if slice.CommitIntent != nil || slice.Completion != nil {
		return fmt.Errorf("slice %s already has intent or completion", sliceID)
	}
	if slice.Approval != nil && slice.Approval.Required && !slice.Approval.Approved {
		return fmt.Errorf("slice %s requires approval", sliceID)
	}
	for _, dependency := range slice.DependsOn {
		prior := findSlice(detail, dependency)
		if prior == nil || prior.Status != StatusCompleted {
			return fmt.Errorf("slice %s dependency %s is not completed", sliceID, dependency)
		}
	}
	if slice.ExecutionRoot != "" && slice.ExecutionRoot != snapshot.ExecutionRoot {
		return fmt.Errorf("slice %s verification execution root changed", sliceID)
	}
	if boundary := slice.ExecutionStart; boundary != nil && (boundary.Branch != snapshot.StartingBranch || boundary.Head != snapshot.StartingHead) {
		return fmt.Errorf("slice %s verification starting boundary changed", sliceID)
	}
	if reflect.DeepEqual(slice.VerificationAttempt, &snapshot) {
		return nil
	}
	if !reflect.DeepEqual(slice.VerificationAttempt, expected) {
		return fmt.Errorf("slice %s verification attempt changed; reload before replacement", sliceID)
	}
	if prior := slice.VerificationAttempt; prior != nil && (prior.AttemptID == snapshot.AttemptID || !snapshot.RecordedAt.After(prior.RecordedAt)) {
		return fmt.Errorf("slice %s verification attempt is stale or conflicts with recorded identity", sliceID)
	}
	if slice.Timing.StartedAt != nil && snapshot.RecordedAt.Before(*slice.Timing.StartedAt) {
		return fmt.Errorf("slice %s verification predates slice start", sliceID)
	}
	slice.VerificationAttempt = cloneSliceVerificationSnapshot(&snapshot)
	return nil
}

// MarshalJSON bounds the new diagnostic claim fields for all event writers.
// Other event fields and historical decoding retain their existing contract.
func (e Event) MarshalJSON() ([]byte, error) {
	if e.ClaimedResult != "" && !verificationIdentity(e.ClaimedResult) {
		return nil, fmt.Errorf("invalid or oversized claimed result")
	}
	if e.VerificationAttemptID != "" && !verificationIdentity(e.VerificationAttemptID) {
		return nil, fmt.Errorf("invalid or oversized verification attempt ID")
	}
	type eventJSON Event
	return json.Marshal(eventJSON(e))
}
