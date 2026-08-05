package plan

import "time"

const (
	StatusPlanned          = "planned"
	StatusPending          = "pending"
	StatusInProgress       = "in_progress"
	StatusInReview         = "in_review"
	StatusReviewed         = "reviewed"
	StatusChangesRequested = "changes_requested"
	StatusCompleted        = "completed"
	StatusSkipped          = "skipped"
	StatusBlocked          = "blocked"
	StatusInvalid          = "invalid"
)

const (
	EventTypeSliceStarted               = "slice_started"
	EventTypeSliceCompleted             = "slice_completed"
	EventTypeSliceBlocked               = "slice_blocked"
	EventTypeSliceResumeAttempted       = "slice_resume_attempted"
	EventTypeSliceResumeFailed          = "slice_resume_failed"
	EventTypeSliceRemoved               = "slice_removed"
	EventTypeSliceSkipped               = "slice_skipped"
	EventTypeSlicesReordered            = "slices_reordered"
	EventTypeSliceApproved              = "slice_approved"
	EventTypePullRequestCreated         = "pull_request_created"
	EventTypePlanReviewed               = "plan_reviewed"
	EventTypePlanReopened               = "plan_reopened"
	EventTypePlanMerged                 = "plan_merged"
	EventTypeVerificationCommandInvalid = "verification_command_invalid"
	EventTypeRunContext                 = "run_context"
	EventTypeSessionTimeout             = "session_timeout"
	EventTypeBudgetExceeded             = "budget_exceeded"
	EventTypeReworkRound                = "rework_round"
	EventTypeReworkStopped              = "rework_stopped"
	EventTypeFinalVerification          = "final_verification"
	EventTypeMergeVerification          = "merge_verification"
	EventTypePlanCommitFallback         = "plan_commit_fallback"
	EventTypePlanCommitGuard            = "plan_commit_guard"
)

const (
	ReviewStatusCompleted = "completed"
	ReviewStatusError     = "error"
)

const (
	ReviewVerdictApprove          = "approve"
	ReviewVerdictChangesRequested = "changes_requested"
	ReviewVerdictComment          = "comment"
)

const (
	WorkspaceStrategyWorktree = "worktree"
	WorkspaceStrategyCurrent  = "current"

	WorkspaceStatusPending     = "pending"
	WorkspaceStatusPreparing   = "preparing"
	WorkspaceStatusReady       = "ready"
	WorkspaceStatusFailed      = "failed"
	WorkspaceStatusCleaning    = "cleaning"
	WorkspaceStatusCleaned     = "cleaned"
	WorkspaceStatusCleanupHeld = "cleanup_held"

	DependencyPreparationStatusPending = "pending"
	DependencyPreparationStatusRunning = "running"
	DependencyPreparationStatusReady   = "ready"
	DependencyPreparationStatusFailed  = "failed"
	DependencyPreparationStatusSkipped = "skipped"

	WorkspaceCleanupStatusPending = "pending"
	WorkspaceCleanupStatusHeld    = "held"
	WorkspaceCleanupStatusRunning = "running"
	WorkspaceCleanupStatusDone    = "done"
	WorkspaceCleanupStatusFailed  = "failed"

	WorkspaceBaseStatusUnknown = "unknown"
	WorkspaceBaseStatusCurrent = "current"
	WorkspaceBaseStatusStale   = "stale"

	WorkspaceRefreshStatusUnknown   = "unknown"
	WorkspaceRefreshStatusNotNeeded = "not_needed"
	WorkspaceRefreshStatusNeeded    = "needed"

	WorkspaceRebaseStatusUnknown   = "unknown"
	WorkspaceRebaseStatusNotNeeded = "not_needed"
	WorkspaceRebaseStatusNeeded    = "needed"
)

// State is the mutable top-level state.json artifact for a plan.
type State struct {
	Schema           string     `json:"schema"`
	Status           string     `json:"status"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	Repo             Repo       `json:"repo"`
	Plan             PlanState  `json:"plan"`
	Workspace        *Workspace `json:"workspace,omitempty"`
	GlobalInvariants []string   `json:"global_invariants"`
	OpenQuestions    []string   `json:"open_questions"`
	Extra            any        `json:"-"`
}

// Repo records the repository context captured when a plan is created.
type Repo struct {
	Name       string `json:"name"`
	Root       string `json:"root"`
	Branch     string `json:"branch"`
	BaseCommit string `json:"base_commit,omitempty"`
}

// PlanState is the mutable queue state for a plan.
type PlanState struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	CurrentSlice    *string  `json:"current_slice,omitempty"`
	CompletedSlices []string `json:"completed_slices"`
	PendingSlices   []string `json:"pending_slices"`
	// LastRunCommitPolicy records the commit policy used by the latest run start.
	LastRunCommitPolicy string `json:"last_run_commit_policy"`
	// LastRunStartingDirty records run-start dirty paths tolerated by standalone review gates.
	LastRunStartingDirty []string                 `json:"last_run_starting_dirty"`
	Timing               PlanTiming               `json:"timing"`
	PullRequest          *PullRequest             `json:"pull_request,omitempty"`
	Review               *PlanReview              `json:"review,omitempty"`
	MergeCommitIntent    *SingleMergeCommitIntent `json:"merge_commit_intent"`
	FinalVerification    *FinalVerification       `json:"final_verification,omitempty"`
}

// PullRequest records a successfully created GitHub pull request for a plan.
type PullRequest struct {
	Number    int       `json:"number"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	Branch    string    `json:"branch,omitempty"`
	HeadSHA   string    `json:"head_sha,omitempty"`
}

// FinalVerification records broad repository verification performed after all
// slices settle and before a completed branch is reviewed.
type FinalVerification struct {
	Command    string    `json:"command,omitempty"`
	CWD        string    `json:"cwd"`
	Result     string    `json:"result"`
	Details    string    `json:"details,omitempty"`
	VerifiedAt time.Time `json:"verified_at"`
}

// ReviewFinding records one structured issue from a persisted review.
type ReviewFinding struct {
	Severity   string `json:"severity,omitempty"`
	File       string `json:"file,omitempty"`
	Line       int    `json:"line,omitempty"`
	Message    string `json:"message,omitempty"`
	Suggestion string `json:"suggestion,omitempty"`
}

// ReviewCommitMessage records the untrusted commit proposal produced while
// reviewing an exact base/head diff. Tao adds evidence trailers at commit time.
type ReviewCommitMessage struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// SingleMergeCommitIntent binds the exact trusted squash message to the Git
// refs Tao validated before mutating the default worktree.
type SingleMergeCommitIntent struct {
	Message       string    `json:"message"`
	PlanID        string    `json:"plan_id"`
	SourceHead    string    `json:"source_head"`
	DefaultBranch string    `json:"default_branch"`
	DefaultParent string    `json:"default_parent"`
	CreatedAt     time.Time `json:"created_at"`
}

// PlanReview records the persisted fresh-session review for a completed plan.
type PlanReview struct {
	Status        string               `json:"status,omitempty"`
	Verdict       string               `json:"verdict,omitempty"`
	Summary       string               `json:"summary,omitempty"`
	FindingsCount int                  `json:"findings_count,omitempty"`
	Findings      []ReviewFinding      `json:"findings,omitempty"`
	CommitMessage *ReviewCommitMessage `json:"commit_message,omitempty"`
	Base          string               `json:"base,omitempty"`
	Head          string               `json:"head,omitempty"`
	Agent         string               `json:"agent,omitempty"`
	ReviewedAt    time.Time            `json:"reviewed_at,omitempty,omitzero"`
}

func (r *PlanReview) IsApproved() bool {
	return r != nil && r.Status == ReviewStatusCompleted && r.Verdict == ReviewVerdictApprove
}

// PlanTiming records lifecycle timestamps for the whole plan.
type PlanTiming struct {
	StartedAt      *time.Time `json:"started_at"`
	CompletedAt    *time.Time `json:"completed_at"`
	LastActivityAt *time.Time `json:"last_activity_at"`
}

// Workspace records optional execution workspace lifecycle metadata for a plan.
// Legacy plans may omit it; runtime config supplies compatibility behavior.
type Workspace struct {
	Strategy              string          `json:"strategy"`
	Root                  string          `json:"root,omitempty"`
	Path                  string          `json:"path,omitempty"`
	Branch                string          `json:"branch,omitempty"`
	BaseBranch            string          `json:"base_branch,omitempty"`
	BaseSHA               string          `json:"base_sha,omitempty"`
	BaseCurrentSHA        string          `json:"base_current_sha,omitempty"`
	BaseStatus            string          `json:"base_status,omitempty"`
	HeadSHA               string          `json:"head_sha,omitempty"`
	PushedSHA             string          `json:"pushed_sha,omitempty"`
	RefreshStatus         string          `json:"refresh_status,omitempty"`
	RebaseStatus          string          `json:"rebase_status,omitempty"`
	LifecycleStatus       string          `json:"lifecycle_status,omitempty"`
	Timing                WorkspaceTiming `json:"timing"`
	DependencyPreparation string          `json:"dependency_preparation_status,omitempty"`
	DependencyCommand     string          `json:"dependency_preparation_command,omitempty"`
	DependencyStartedAt   *time.Time      `json:"dependency_preparation_started_at,omitempty"`
	DependencyCompletedAt *time.Time      `json:"dependency_preparation_completed_at,omitempty"`
	DependencyFailure     string          `json:"dependency_preparation_failure,omitempty"`
	// DependencyFingerprint is the SHA-256 of the lockfile behind the last successful dependency install; empty when unknown.
	DependencyFingerprint string `json:"dependency_fingerprint,omitempty"`
	CleanupStatus         string `json:"cleanup_status,omitempty"`
}

// WorkspaceTiming records lifecycle timestamps for a plan workspace.
type WorkspaceTiming struct {
	CreatedAt      *time.Time `json:"created_at,omitempty"`
	PreparedAt     *time.Time `json:"prepared_at,omitempty"`
	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	CleanedAt      *time.Time `json:"cleaned_at,omitempty"`
}

// RunCapabilities describes plan-owned action eligibility for starting a run.
// SliceCompletionPendingError reports an automatic slice transaction whose
// durable intent has not yet been settled in slices.json.
type SliceCompletionPendingError struct {
	SliceID string
	Reason  string
}

func (e *SliceCompletionPendingError) Error() string {
	return "slice " + e.SliceID + " automatic completion transaction is unsettled: " + e.Reason + "; rerun tao slice-complete to recover it"
}

type RunCapabilities struct {
	CanRun                 bool   `json:"can_run"`
	DisabledReason         string `json:"disabled_reason,omitempty"`
	NeedsApproval          bool   `json:"needs_approval,omitempty"`
	ApprovalSliceID        string `json:"approval_slice_id,omitempty"`
	CanContinue            bool   `json:"can_continue"`
	ContinueDisabledReason string `json:"continue_disabled_reason,omitempty"`
	Complete               bool   `json:"complete"`
	Reviewed               bool   `json:"reviewed"`
	Active                 bool   `json:"active"`
}

// Event is one append-only lifecycle entry from events.jsonl.
type Event struct {
	Type              string        `json:"type"`
	Timestamp         time.Time     `json:"timestamp"`
	PlanID            string        `json:"plan_id"`
	MutationID        string        `json:"mutation_id,omitempty"`
	SliceID           string        `json:"slice_id,omitempty"`
	Branch            string        `json:"branch,omitempty"`
	MergedDefaultSHA  string        `json:"merged_default_sha,omitempty"`
	Agent             string        `json:"agent,omitempty"`
	DurationSeconds   *int64        `json:"duration_seconds,omitempty"`
	Metrics           *AgentMetrics `json:"metrics,omitempty"`
	PullRequest       *PullRequest  `json:"pull_request,omitempty"`
	Review            *PlanReview   `json:"review,omitempty"`
	Command           string        `json:"command,omitempty"`
	CorrectedCommand  string        `json:"corrected_command,omitempty"`
	Result            string        `json:"result,omitempty"`
	Round             int           `json:"round,omitempty"`
	Attempts          int           `json:"attempts,omitempty"`
	Fingerprint       string        `json:"fingerprint,omitempty"`
	Reason            string        `json:"reason,omitempty"`
	CommitPolicy      string        `json:"commit_policy,omitempty"`
	RunPacketProvided bool          `json:"run_packet_provided,omitempty"`
	GuardrailWarnings int           `json:"guardrail_warnings,omitempty"`
	Metric            string        `json:"metric,omitempty"`
	Threshold         *float64      `json:"threshold,omitempty"`
	Observed          *float64      `json:"observed,omitempty"`
	Message           string        `json:"message"`
}
