package plan

import (
	"fmt"
	"strings"
	"time"
)

// FinalizationFailurePhase identifies which bounded post-review operation
// failed. Failure evidence is descriptive only and never grants recovery
// authority.
type FinalizationFailurePhase string

// Failure phases identify the post-review operation recorded in finalization diagnostics.
const (
	FinalizationFailurePhaseProposalRepair FinalizationFailurePhase = "proposal_repair"
	FinalizationFailurePhasePullRequest    FinalizationFailurePhase = "pull_request_finalization"
)

// Finalization categories are persisted evidence; keep their wire values stable.
const (
	FinalizationCategoryProposalCorrectionStarted     = "proposal_correction_started"
	FinalizationCategoryProposalCorrectionUnavailable = "proposal_correction_unavailable"
	FinalizationCategoryProposalPromptFailed          = "proposal_prompt_failed"
	FinalizationCategoryProposalCorrectionFailed      = "proposal_correction_failed"
	FinalizationCategoryProposalInvalid               = "proposal_invalid"
	FinalizationCategoryProposalRecordingFailed       = "proposal_recording_failed"
	FinalizationCategoryHeadDrift                     = "head_drift"
	FinalizationCategoryWorkspaceMismatch             = "workspace_mismatch"
	FinalizationCategoryWorkspacePreflightFailed      = "workspace_preflight_failed"
	FinalizationCategoryWorkspaceDirty                = "workspace_dirty"
	FinalizationCategoryIntentMismatch                = "intent_mismatch"
)

// Recovery actions label suggested next steps in finalization diagnostics, not recovery authority.
const (
	FinalizationRecoveryResumePullRequest = "resume_pull_request"
	FinalizationRecoveryRestoreBoundary   = "restore_workspace_boundary"
	FinalizationRecoveryRerunReview       = "rerun_review"
	FinalizationRecoveryRepairIntent      = "repair_pull_request_intent"
	FinalizationRecoveryRepairIdentity    = "repair_pull_request_identity"
)

const maxFinalizationFailureValueBytes = 1024

// FinalizationFailure records a safe failure classification bound to either an
// exact review range or an exact pull-request branch/head. It deliberately has
// no provider, forge, or raw error text field.
type FinalizationFailure struct {
	Phase          FinalizationFailurePhase `json:"phase"`
	Category       string                   `json:"category"`
	Branch         string                   `json:"branch,omitempty"`
	HeadSHA        string                   `json:"head_sha,omitempty"`
	ReviewBase     string                   `json:"review_base,omitempty"`
	ReviewHead     string                   `json:"review_head,omitempty"`
	FailedAt       time.Time                `json:"failed_at"`
	RecoveryAction string                   `json:"recovery_action"`
}

// ProposalRepairRecoveryAction classifies the trustworthy next step after a
// consumed proposal correction cannot be persisted. Workspace and intent
// failures must be repaired before a replacement review can safely run.
func ProposalRepairRecoveryAction(category string) string {
	switch strings.TrimSpace(category) {
	case FinalizationCategoryHeadDrift, FinalizationCategoryWorkspaceMismatch, FinalizationCategoryWorkspacePreflightFailed, FinalizationCategoryWorkspaceDirty:
		return FinalizationRecoveryRestoreBoundary
	case FinalizationCategoryIntentMismatch:
		return FinalizationRecoveryRepairIntent
	default:
		return FinalizationRecoveryRerunReview
	}
}

// PullRequestFinalizationRecoveryAction classifies the trustworthy next step
// for a bounded pull-request failure. Unknown and operational categories retain
// exact-boundary resume behavior for forward and legacy compatibility.
func PullRequestFinalizationRecoveryAction(category string) string {
	switch strings.TrimSpace(category) {
	case "head_drift", "workspace_mismatch", "workspace_dirty":
		return FinalizationRecoveryRestoreBoundary
	case "review_head_mismatch", "review_not_approved":
		return FinalizationRecoveryRerunReview
	case "intent_mismatch":
		return FinalizationRecoveryRepairIntent
	case "identity_mismatch":
		return FinalizationRecoveryRepairIdentity
	default:
		return FinalizationRecoveryResumePullRequest
	}
}

// Validate rejects unbounded or ambiguous evidence. Category and recovery
// action are bounded machine labels, not error text and not recovery authority.
func (f FinalizationFailure) Validate() error {
	if f.Phase != FinalizationFailurePhaseProposalRepair && f.Phase != FinalizationFailurePhasePullRequest {
		return fmt.Errorf("finalization failure phase is invalid")
	}
	for label, value := range map[string]string{"category": f.Category, "recovery action": f.RecoveryAction} {
		if !validFinalizationLabel(value) {
			return fmt.Errorf("finalization failure %s must be a bounded lowercase label", label)
		}
	}
	for label, value := range map[string]string{
		"branch": f.Branch, "head SHA": f.HeadSHA, "review base": f.ReviewBase, "review head": f.ReviewHead,
	} {
		if len(value) > maxFinalizationFailureValueBytes || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("finalization failure %s is invalid", label)
		}
	}
	switch f.Phase {
	case FinalizationFailurePhaseProposalRepair:
		if f.ReviewBase == "" || f.ReviewHead == "" || f.Branch != "" || f.HeadSHA != "" {
			return fmt.Errorf("proposal repair failure requires only an exact review base and head")
		}
	case FinalizationFailurePhasePullRequest:
		if f.Branch == "" || f.HeadSHA == "" || f.ReviewBase != "" || f.ReviewHead != "" {
			return fmt.Errorf("pull request finalization failure requires only an exact branch and head")
		}
	}
	if f.FailedAt.IsZero() {
		return fmt.Errorf("finalization failure timestamp is required")
	}
	return nil
}

func validFinalizationLabel(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '_' {
			return false
		}
	}
	return true
}
