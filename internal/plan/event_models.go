package plan

import "time"

// Event types identify lifecycle transitions and diagnostics appended to events.jsonl.
const (
	EventTypeSliceStarted               = "slice_started"
	EventTypeSliceCompleted             = "slice_completed"
	EventTypeSliceBlocked               = "slice_blocked"
	EventTypeSliceResumeAttempted       = "slice_resume_attempted"
	EventTypeSliceResumeFailed          = "slice_resume_failed"
	EventTypeSliceRestarted             = "slice_restarted"
	EventTypeSliceRemoved               = "slice_removed"
	EventTypeSliceSkipped               = "slice_skipped"
	EventTypeSlicesReordered            = "slices_reordered"
	EventTypeSliceAmended               = "slice_amended"
	EventTypeScopeExtension             = "scope_extension"
	EventTypeSliceApproved              = "slice_approved"
	EventTypePullRequestCreated         = "pull_request_created"
	EventTypeFinalizationFailed         = "finalization_failed"
	EventTypeFinalizationFailureCleared = "finalization_failure_cleared"
	EventTypePRFeedbackTriaged          = "pr_feedback_triaged"
	EventTypePlanReviewed               = "plan_reviewed"
	EventTypePlanReopened               = "plan_reopened"
	EventTypePlanMerged                 = "plan_merged"
	EventTypePlanAbandoned              = "plan_abandoned"
	EventTypePlanParked                 = "plan_parked"
	EventTypePlanUnparked               = "plan_unparked"
	EventTypeVerificationCommandInvalid = "verification_command_invalid"
	EventTypeVerificationClaimMismatch  = "verification_claim_mismatch"
	EventTypeRunContext                 = "run_context"
	EventTypeSessionTimeout             = "session_timeout"
	EventTypeRunAborted                 = "run_aborted"
	EventTypeControlCheckoutChanged     = "control_checkout_changed"
	EventTypeBudgetExceeded             = "budget_exceeded"
	EventTypeReworkRound                = "rework_round"
	EventTypeReworkStopped              = "rework_stopped"
	EventTypeFinalVerification          = "final_verification"
	EventTypeFinalVerificationFlake     = "final_verification_flake"
	EventTypeVerificationRepairCreated  = "verification_repair_created"
	EventTypeVerificationRepairStopped  = "verification_repair_stopped"
	EventTypeMergeVerification          = "merge_verification"
	EventTypeSingleMergeRolledBack      = "single_merge_resolution_rolled_back"
	EventTypeSingleMergeRearmed         = "single_merge_resolution_rearmed"
	EventTypeSingleMergeIntentRestarted = "single_merge_intent_restarted"
	EventTypePlanCommitFallback         = "plan_commit_fallback"
	EventTypePlanCommitGuard            = "plan_commit_guard"
)

// Event is one append-only lifecycle entry from events.jsonl.
type Event struct {
	ParkingOperationID           string                           `json:"parking_operation_id,omitempty"`
	Actor                        string                           `json:"actor,omitempty"`
	Trigger                      string                           `json:"trigger,omitempty"`
	AttributionVersion           int                              `json:"attribution_version,omitempty"`
	Via                          string                           `json:"via,omitempty"`
	Type                         string                           `json:"type"`
	Timestamp                    time.Time                        `json:"timestamp"`
	PlanID                       string                           `json:"plan_id"`
	MutationID                   string                           `json:"mutation_id,omitempty"`
	SliceID                      string                           `json:"slice_id,omitempty"`
	Branch                       string                           `json:"branch,omitempty"`
	PriorRoot                    string                           `json:"prior_root,omitempty"`
	PriorBranch                  string                           `json:"prior_branch,omitempty"`
	PriorHead                    string                           `json:"prior_head,omitempty"`
	BaselineBranch               string                           `json:"baseline_branch,omitempty"`
	BaselineHead                 string                           `json:"baseline_head,omitempty"`
	MergedDefaultSHA             string                           `json:"merged_default_sha,omitempty"`
	Agent                        string                           `json:"agent,omitempty"`
	Model                        string                           `json:"model,omitempty"`
	DurationSeconds              *int64                           `json:"duration_seconds,omitempty"`
	Metrics                      *AgentMetrics                    `json:"metrics,omitempty"`
	PullRequest                  *PullRequest                     `json:"pull_request,omitempty"`
	PRFeedbackTriage             PRFeedbackTriageResult           `json:"pr_feedback_triage,omitempty"`
	Review                       *PlanReview                      `json:"review,omitempty"`
	SingleMergeResolution        *SingleMergeResolutionEvent      `json:"single_merge_resolution,omitempty"`
	SingleMergeStartup           *SingleMergeStartupFailure       `json:"single_merge_startup,omitempty"`
	FinalizationFailure          *FinalizationFailure             `json:"finalization_failure,omitempty"`
	Command                      string                           `json:"command,omitempty"`
	Paths                        []string                         `json:"paths,omitempty"`
	BlockerCategory              string                           `json:"blocker_category,omitempty"`
	BlockerClassification        string                           `json:"blocker_classification,omitempty"`
	HeadSHA                      string                           `json:"head_sha,omitempty"`
	ClaimedResult                string                           `json:"claimed_result,omitempty"`
	VerificationAttemptID        string                           `json:"verification_attempt_id,omitempty"`
	CorrectedCommand             string                           `json:"corrected_command,omitempty"`
	Result                       string                           `json:"result,omitempty"`
	FailureKind                  FinalVerificationFailureKind     `json:"failure_kind,omitempty"`
	Baseline                     *FinalVerificationBaseline       `json:"baseline,omitempty"`
	ExitCode                     *int                             `json:"exit_code,omitempty"`
	Round                        int                              `json:"round,omitempty"`
	Attempts                     int                              `json:"attempts,omitempty"`
	Fingerprint                  string                           `json:"fingerprint,omitempty"`
	ScopeClassification          string                           `json:"scope_classification,omitempty"`
	Reason                       string                           `json:"reason,omitempty"`
	AbortKind                    string                           `json:"abort_kind,omitempty"`
	AmendedFields                []string                         `json:"amended_fields,omitempty"`
	ReplacedVerificationCommands []VerificationCommandReplacement `json:"replaced_verification_commands,omitempty"`
	CommitPolicy                 string                           `json:"commit_policy,omitempty"`
	RunPacketProvided            bool                             `json:"run_packet_provided,omitempty"`
	GuardrailWarnings            int                              `json:"guardrail_warnings,omitempty"`
	Metric                       string                           `json:"metric,omitempty"`
	Threshold                    *float64                         `json:"threshold,omitempty"`
	Observed                     *float64                         `json:"observed,omitempty"`
	Message                      string                           `json:"message"`
}
