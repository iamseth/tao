package plan

import (
	"fmt"
	"slices"
	"time"
)

// SingleMergeResolutionPhase identifies the durable boundary reached by an
// ordinary squash-conflict transaction. Each phase adds exact recovery
// evidence; phases never infer authority from the worktree.
type SingleMergeResolutionPhase string

// Resolution phases mark durable boundaries in single-merge intents and resolution events.
const (
	SingleMergeResolutionPhaseRequested  SingleMergeResolutionPhase = "requested"
	SingleMergeResolutionPhaseResolved   SingleMergeResolutionPhase = "resolved"
	SingleMergeResolutionPhaseCommitted  SingleMergeResolutionPhase = "committed"
	SingleMergeResolutionPhaseReviewed   SingleMergeResolutionPhase = "reviewed"
	SingleMergeResolutionPhaseRolledBack SingleMergeResolutionPhase = "rolled_back"
)

type SingleMergeResolutionOutcome string

const SingleMergeResolutionOutcomeResolved SingleMergeResolutionOutcome = "resolved"

// SingleMergeStartupCapability is the stable launch stage that prevented an
// attributed resolver prompt from being accepted. These labels are diagnostic
// only and never grant retry authority by themselves.
type SingleMergeStartupCapability string

// Startup capabilities identify failed launch stages in single-merge startup evidence.
const (
	SingleMergeStartupExecutable        SingleMergeStartupCapability = "executable"
	SingleMergeStartupConfinement       SingleMergeStartupCapability = "confinement"
	SingleMergeStartupConfigProjection  SingleMergeStartupCapability = "configuration_projection"
	SingleMergeStartupRPCInitialization SingleMergeStartupCapability = "rpc_initialization"
	SingleMergeStartupSelectedModel     SingleMergeStartupCapability = "selected_model"
	SingleMergeStartupLocalCredentials  SingleMergeStartupCapability = "local_credentials" //nolint:gosec // capability label, never credential material.
	SingleMergeStartupPromptAcceptance  SingleMergeStartupCapability = "prompt_acceptance"
)

// SingleMergeStartupFailure is bounded append-only evidence that Tao restored
// the exact provisional request boundary and rearmed it. Error text and
// credentials are deliberately excluded.
type SingleMergeStartupFailure struct {
	Capability       SingleMergeStartupCapability `json:"capability"`
	PromptAcceptance string                       `json:"prompt_acceptance"`
	FailedAt         time.Time                    `json:"failed_at"`
}

func (f SingleMergeStartupFailure) Validate() error {
	if !slices.Contains([]SingleMergeStartupCapability{
		SingleMergeStartupExecutable, SingleMergeStartupConfinement,
		SingleMergeStartupConfigProjection, SingleMergeStartupRPCInitialization,
		SingleMergeStartupSelectedModel, SingleMergeStartupLocalCredentials,
		SingleMergeStartupPromptAcceptance,
	}, f.Capability) {
		return fmt.Errorf("single-merge startup capability %q is invalid", f.Capability)
	}
	if f.PromptAcceptance != "not_transmitted" && f.PromptAcceptance != "rejected" {
		return fmt.Errorf("single-merge startup failure requires proven pre-acceptance")
	}
	if f.FailedAt.IsZero() || f.FailedAt.Location() != time.UTC {
		return fmt.Errorf("single-merge startup failure requires a non-zero UTC timestamp")
	}
	return nil
}

type SingleMergeResolutionRollbackReason string

// Rollback reasons classify why a single-merge resolution transaction was restored.
const (
	SingleMergeResolutionRollbackVerificationFailed    SingleMergeResolutionRollbackReason = "verification_failed"
	SingleMergeResolutionRollbackReviewUnavailable     SingleMergeResolutionRollbackReason = "review_unavailable"
	SingleMergeResolutionRollbackReviewNotApproved     SingleMergeResolutionRollbackReason = "review_not_approved"
	SingleMergeResolutionRollbackMergeRecordingFailed  SingleMergeResolutionRollbackReason = "merge_recording_failed"
	SingleMergeResolutionRollbackRecoveredInterruption SingleMergeResolutionRollbackReason = "recovered_interruption"
)

// SingleMergeResolutionReview is an independent fresh-session review of the
// exact integration commit. It is deliberately separate from PlanReview,
// which continues to describe the source-plan diff.
type SingleMergeResolutionReview struct {
	Status        string          `json:"status"`
	Verdict       string          `json:"verdict"`
	Summary       string          `json:"summary"`
	FindingsCount int             `json:"findings_count"`
	Findings      []ReviewFinding `json:"findings"`
	Base          string          `json:"base"`
	Head          string          `json:"head"`
	Agent         string          `json:"agent"`
	ReviewedAt    time.Time       `json:"reviewed_at"`
}

func (r *SingleMergeResolutionReview) IsApproved() bool {
	return r != nil && r.Status == ReviewStatusCompleted && r.Verdict == ReviewVerdictApprove
}

// SingleMergeResolution records the request before an untrusted resolver runs,
// then the exact validated output, Tao-owned commit, and independent review.
// Fields are intentionally non-omitempty once the optional resolution object
// exists so phase advancement cannot retain stale known fields through the
// merge-preserving state writer.
type SingleMergeResolution struct {
	Phase              SingleMergeResolutionPhase          `json:"phase"`
	ConflictFiles      []string                            `json:"conflict_files"`
	RequestedAt        time.Time                           `json:"requested_at"`
	Outcome            SingleMergeResolutionOutcome        `json:"outcome"`
	Summary            string                              `json:"summary"`
	ChangedPaths       []string                            `json:"changed_paths"`
	ContentFingerprint string                              `json:"content_fingerprint"`
	CommitMessage      string                              `json:"commit_message"`
	ResolvedAt         time.Time                           `json:"resolved_at"`
	IntegrationHead    string                              `json:"integration_head"`
	CommittedAt        time.Time                           `json:"committed_at"`
	Review             *SingleMergeResolutionReview        `json:"review"`
	RollbackReason     SingleMergeResolutionRollbackReason `json:"rollback_reason"`
	RolledBackAt       time.Time                           `json:"rolled_back_at"`
}

// SingleMergeResolutionEvent is the bounded append-only projection of
// resolution evidence. Counts preserve the complete diagnostic cardinality
// when path or finding details are capped. CommitMessage remains decode-only
// compatibility for historical events; new events omit it because exact commit
// authority belongs to the state transaction, not event diagnostics.
type SingleMergeResolutionEvent struct {
	Phase                SingleMergeResolutionPhase          `json:"phase"`
	ConflictFiles        []string                            `json:"conflict_files"`
	ConflictFilesCount   int                                 `json:"conflict_files_count,omitempty"`
	RequestedAt          time.Time                           `json:"requested_at"`
	Outcome              SingleMergeResolutionOutcome        `json:"outcome"`
	Summary              string                              `json:"summary"`
	ChangedPaths         []string                            `json:"changed_paths"`
	ChangedPathsCount    int                                 `json:"changed_paths_count,omitempty"`
	ContentFingerprint   string                              `json:"content_fingerprint"`
	CommitMessage        string                              `json:"commit_message,omitempty"`
	ResolvedAt           time.Time                           `json:"resolved_at"`
	IntegrationHead      string                              `json:"integration_head"`
	CommittedAt          time.Time                           `json:"committed_at"`
	Review               *SingleMergeResolutionReview        `json:"review"`
	RollbackReason       SingleMergeResolutionRollbackReason `json:"rollback_reason"`
	RolledBackAt         time.Time                           `json:"rolled_back_at"`
	DiagnosticsTruncated bool                                `json:"diagnostics_truncated,omitempty"`
}

// SingleMergeCommitIntent binds the exact trusted squash message to the Git
// refs Tao validated before mutating the default worktree. Resolution is
// optional so historical intents retain their original JSON shape and meaning.
type SingleMergeCommitIntent struct {
	Message       string                 `json:"message"`
	PlanID        string                 `json:"plan_id"`
	SourceHead    string                 `json:"source_head"`
	DefaultBranch string                 `json:"default_branch"`
	DefaultParent string                 `json:"default_parent"`
	CreatedAt     time.Time              `json:"created_at"`
	Resolution    *SingleMergeResolution `json:"resolution,omitempty"`
}

// IsActive reports whether the intent still owns an unsettled single-merge
// transaction. Exact rollback settlement retains diagnostics without retaining
// transaction authority; legacy and all pre-rollback intents remain active.
func (i *SingleMergeCommitIntent) IsActive() bool {
	return i != nil && (i.Resolution == nil || i.Resolution.Phase != SingleMergeResolutionPhaseRolledBack)
}
