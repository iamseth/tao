package plan

import (
	"fmt"
	"strings"
	"time"
)

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
	EventTypePRFeedbackTriaged          = "pr_feedback_triaged"
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

// ChangeType is the plan-level Conventional Commit type selected during planning.
type ChangeType string

const (
	ChangeTypeFeat     ChangeType = "feat"
	ChangeTypeFix      ChangeType = "fix"
	ChangeTypeDocs     ChangeType = "docs"
	ChangeTypeStyle    ChangeType = "style"
	ChangeTypeRefactor ChangeType = "refactor"
	ChangeTypePerf     ChangeType = "perf"
	ChangeTypeTest     ChangeType = "test"
	ChangeTypeBuild    ChangeType = "build"
	ChangeTypeCI       ChangeType = "ci"
	ChangeTypeChore    ChangeType = "chore"
	ChangeTypeRevert   ChangeType = "revert"
)

var supportedChangeTypes = []ChangeType{
	ChangeTypeFeat,
	ChangeTypeFix,
	ChangeTypeDocs,
	ChangeTypeStyle,
	ChangeTypeRefactor,
	ChangeTypePerf,
	ChangeTypeTest,
	ChangeTypeBuild,
	ChangeTypeCI,
	ChangeTypeChore,
	ChangeTypeRevert,
}

// SupportedChangeTypes returns the accepted planning-time change types.
func SupportedChangeTypes() []ChangeType {
	return append([]ChangeType(nil), supportedChangeTypes...)
}

// ValidateChangeType rejects non-empty values outside Tao's supported
// Conventional Commit types. Empty values remain valid for legacy plans.
func ValidateChangeType(changeType ChangeType) error {
	if changeType == "" {
		return nil
	}
	for _, supported := range supportedChangeTypes {
		if changeType == supported {
			return nil
		}
	}
	values := make([]string, len(supportedChangeTypes))
	for i, supported := range supportedChangeTypes {
		values[i] = string(supported)
	}
	return fmt.Errorf("unsupported plan change type %q (supported: %s)", changeType, strings.Join(values, ", "))
}

// Category returns the repository-facing branch and label category. Features
// use the conventional repository name; every other type keeps its own name.
func (changeType ChangeType) Category() string {
	if changeType == ChangeTypeFeat {
		return "feature"
	}
	return string(changeType)
}

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

// DecisionReadiness describes whether a plan is sufficiently resolved to act on.
type DecisionReadiness string

const (
	DecisionReadinessReady           DecisionReadiness = "ready"
	DecisionReadinessNeedsRefinement DecisionReadiness = "needs_refinement"
	DecisionReadinessBlocked         DecisionReadiness = "blocked"
)

// DecisionDisposition records the planning recommendation independently from
// runtime lifecycle gates.
type DecisionDisposition string

const (
	DecisionDispositionReady       DecisionDisposition = "ready"
	DecisionDispositionConditional DecisionDisposition = "conditional"
	DecisionDispositionDeferred    DecisionDisposition = "deferred"
	DecisionDispositionObsolete    DecisionDisposition = "obsolete"
)

// PriorityOverallLevel records whether a plan is a must, should, or could.
// Tao deliberately does not combine it with the dimensional inputs into a
// numeric score.
type PriorityOverallLevel string

const (
	PriorityOverallLevelMust   PriorityOverallLevel = "must"
	PriorityOverallLevelShould PriorityOverallLevel = "should"
	PriorityOverallLevelCould  PriorityOverallLevel = "could"
)

// PriorityLevel is a low, medium, or high dimensional priority input.
type PriorityLevel string

const (
	PriorityLevelLow    PriorityLevel = "low"
	PriorityLevelMedium PriorityLevel = "medium"
	PriorityLevelHigh   PriorityLevel = "high"
)

// PriorityEffort describes implementation size independently from priority.
type PriorityEffort string

const (
	PriorityEffortSmall  PriorityEffort = "small"
	PriorityEffortMedium PriorityEffort = "medium"
	PriorityEffortLarge  PriorityEffort = "large"
)

// PlanRelationType describes an advisory ordering relationship to another plan.
type PlanRelationType string

const (
	PlanRelationBefore  PlanRelationType = "before"
	PlanRelationAfter   PlanRelationType = "after"
	PlanRelationRelated PlanRelationType = "related"
)

// Priority records the categorical tradeoffs behind a disposition.
type Priority struct {
	Level      PriorityOverallLevel `json:"level"`
	Impact     PriorityLevel        `json:"impact"`
	Urgency    PriorityLevel        `json:"urgency"`
	Effort     PriorityEffort       `json:"effort"`
	Risk       PriorityLevel        `json:"risk"`
	Confidence PriorityLevel        `json:"confidence"`
	Rationale  string               `json:"rationale"`
}

// Decision records the planning rationale for why a plan should or should not
// be pursued. It remains descriptive and grants no execution authority.
type Decision struct {
	Problem           string              `json:"problem"`
	WhyNow            string              `json:"why_now"`
	ExpectedBenefit   string              `json:"expected_benefit"`
	Readiness         DecisionReadiness   `json:"readiness"`
	SuccessCriteria   []string            `json:"success_criteria"`
	Disposition       DecisionDisposition `json:"disposition"`
	DispositionReason string              `json:"disposition_reason"`
	Priority          Priority            `json:"priority"`
}

// PlanRelation records one advisory relationship to another plan.
type PlanRelation struct {
	PlanID string           `json:"plan_id"`
	Type   PlanRelationType `json:"type"`
	Reason string           `json:"reason"`
}

// Sequence records a plan's advisory position among a bounded group and any
// explicit cross-plan relationships.
type Sequence struct {
	Position      int            `json:"position"`
	Total         int            `json:"total"`
	Relationships []PlanRelation `json:"relationships,omitempty"`
}

// PlanDecision and PlanSequence retain explicit domain names for callers that
// prefer them while the persisted schema uses the concise model names above.
type PlanDecision = Decision
type PlanSequence = Sequence

// PlanState is the mutable queue state for a plan.
type PlanState struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	ChangeType      ChangeType `json:"change_type,omitempty"`
	Decision        *Decision  `json:"decision,omitempty"`
	Sequence        *Sequence  `json:"sequence,omitempty"`
	CurrentSlice    *string    `json:"current_slice,omitempty"`
	CompletedSlices []string   `json:"completed_slices"`
	PendingSlices   []string   `json:"pending_slices"`
	// LastRunCommitPolicy records the commit policy used by the latest run start.
	LastRunCommitPolicy string `json:"last_run_commit_policy"`
	// LastRunStartingDirty records run-start dirty paths tolerated by standalone review gates.
	LastRunStartingDirty []string               `json:"last_run_starting_dirty"`
	Timing               PlanTiming             `json:"timing"`
	PullRequest          *PullRequest           `json:"pull_request,omitempty"`
	PullRequestIntent    *PullRequest           `json:"pull_request_intent"`
	PRFeedbackTriage     PRFeedbackTriageResult `json:"pr_feedback_triage,omitempty"`
	// PRFeedbackConsumedThreadIDs is append-only conversion evidence keyed by
	// GitHub thread node ID. It is independent of the current triage snapshot so
	// resolved and later-returning threads cannot be converted twice.
	PRFeedbackConsumedThreadIDs []string                 `json:"pr_feedback_consumed_thread_ids,omitempty"`
	Review                      *PlanReview              `json:"review,omitempty"`
	MergeCommitIntent           *SingleMergeCommitIntent `json:"merge_commit_intent"`
	FinalVerification           *FinalVerification       `json:"final_verification,omitempty"`
}

// PullRequest records GitHub pull request identity. Legacy PullRequestIntent
// values may contain only branch and head, but those fields do not prove
// ownership of a remotely discovered pull request.
type PullRequest struct {
	Number    int       `json:"number"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	Branch    string    `json:"branch,omitempty"`
	HeadSHA   string    `json:"head_sha,omitempty"`
}

// PRFeedbackTriageResult is a stable pull-request review-thread classification
// keyed by GitHub thread node ID.
type PRFeedbackTriageResult map[string]PRFeedbackTriageEntry

// PRFeedbackTriageEntry records the validated disposition of one review thread.
type PRFeedbackTriageEntry struct {
	Kind      string `json:"kind"`
	Rationale string `json:"rationale"`
}

// AutomaticReworkStop is the durable evidence for a bounded automatic-rework
// decision that refused to open another round.
type AutomaticReworkStop struct {
	Round       int
	Attempts    int
	Fingerprint string
	Reason      string
	StoppedAt   time.Time
}

// Validate rejects evidence that cannot reproduce the established
// rework_stopped event contract.
func (e AutomaticReworkStop) Validate() error {
	if e.Round < 0 {
		return fmt.Errorf("automatic rework stop round cannot be negative")
	}
	if e.Attempts < 0 {
		return fmt.Errorf("automatic rework stop attempts cannot be negative")
	}
	if strings.TrimSpace(e.Fingerprint) == "" {
		return fmt.Errorf("automatic rework stop fingerprint is required")
	}
	if strings.TrimSpace(e.Reason) == "" || e.Reason != strings.TrimSpace(e.Reason) {
		return fmt.Errorf("automatic rework stop reason is required and must not have surrounding whitespace")
	}
	if e.StoppedAt.IsZero() {
		return fmt.Errorf("automatic rework stop timestamp is required")
	}
	return nil
}

// AutomaticReworkRound is the durable evidence coupled to an automatic plan
// reopen. MaxAttempts is retained to reproduce the existing event message.
type AutomaticReworkRound struct {
	Round       int
	Attempts    int
	MaxAttempts int
	Fingerprint string
	ReopenedAt  time.Time
}

// Validate rejects evidence that cannot reproduce the established rework_round
// event contract.
func (e AutomaticReworkRound) Validate() error {
	if e.Round <= 0 {
		return fmt.Errorf("automatic rework round must be positive")
	}
	if e.Attempts <= 0 {
		return fmt.Errorf("automatic rework attempts must be positive")
	}
	if e.MaxAttempts <= 0 || e.Attempts > e.MaxAttempts {
		return fmt.Errorf("automatic rework maximum attempts must include attempt %d", e.Attempts)
	}
	if strings.TrimSpace(e.Fingerprint) == "" {
		return fmt.Errorf("automatic rework round fingerprint is required")
	}
	if e.ReopenedAt.IsZero() {
		return fmt.Errorf("automatic rework round timestamp is required")
	}
	return nil
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
	DependencyFingerprint string                 `json:"dependency_fingerprint,omitempty"`
	RebaseIntent          *WorkspaceRebaseIntent `json:"rebase_intent,omitempty"`
	CleanupStatus         string                 `json:"cleanup_status,omitempty"`
}

// WorkspaceRebaseIntent records the exact pre-mutation boundary and feature
// commit series needed to prove and settle an interrupted workspace rebase.
type WorkspaceRebaseIntent struct {
	Branch                  string    `json:"branch"`
	BaseBranch              string    `json:"base_branch"`
	OldHeadSHA              string    `json:"old_head_sha"`
	OldBaseSHA              string    `json:"old_base_sha"`
	NewBaseSHA              string    `json:"new_base_sha"`
	CommitCount             int       `json:"commit_count"`
	CommitSeriesFingerprint string    `json:"commit_series_fingerprint"`
	CreatedAt               time.Time `json:"created_at"`
}

// WorkspaceRebaseSettlement is the durable workspace boundary and status
// written when an exact rebase intent has been proved complete.
type WorkspaceRebaseSettlement struct {
	Branch          string
	BaseSHA         string
	BaseCurrentSHA  string
	HeadSHA         string
	BaseStatus      string
	RefreshStatus   string
	RebaseStatus    string
	LifecycleStatus string
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
	ApprovalReason         string `json:"approval_reason,omitempty"`
	CanContinue            bool   `json:"can_continue"`
	ContinueDisabledReason string `json:"continue_disabled_reason,omitempty"`
	Complete               bool   `json:"complete"`
	Reviewed               bool   `json:"reviewed"`
	Active                 bool   `json:"active"`
}

// PlanActionKind identifies one read-only lifecycle recommendation. Commands
// remain subject to their normal dynamic gates; this projection grants no
// authority to mutate plan or repository state.
type PlanActionKind string

const (
	PlanActionRecoverSliceCompletion PlanActionKind = "recover_slice_completion"
	PlanActionRecoverRebase          PlanActionKind = "recover_rebase"
	PlanActionRecoverPullRequest     PlanActionKind = "recover_pull_request"
	PlanActionRecoverMerge           PlanActionKind = "recover_merge"
	PlanActionRestartRework          PlanActionKind = "restart_rework"
	PlanActionApprove                PlanActionKind = "approve"
	PlanActionContinue               PlanActionKind = "continue"
	PlanActionRun                    PlanActionKind = "run"
	PlanActionReview                 PlanActionKind = "review"
	PlanActionRework                 PlanActionKind = "rework"
	PlanActionMerge                  PlanActionKind = "merge"
	PlanActionNone                   PlanActionKind = "none"
)

// PlanActionClass separates ordinary progress, recovery, administrative
// exceptions, and terminal outcomes so callers never present a forced bypass as
// equivalent to the primary safe path.
type PlanActionClass string

const (
	PlanActionClassProgress       PlanActionClass = "progress"
	PlanActionClassRecovery       PlanActionClass = "recovery"
	PlanActionClassAdministrative PlanActionClass = "administrative"
	PlanActionClassTerminal       PlanActionClass = "terminal"
)

type PlanAction struct {
	Kind        PlanActionKind  `json:"kind"`
	Class       PlanActionClass `json:"class"`
	Command     string          `json:"command,omitempty"`
	Instruction string          `json:"instruction,omitempty"`
	Reason      string          `json:"reason"`
}

// PlanNextAction contains exactly one primary recommendation and any valid but
// subordinate alternatives. Alternatives are advisory and independently gated
// by the command that implements them.
type PlanNextAction struct {
	Primary      PlanAction   `json:"primary"`
	Alternatives []PlanAction `json:"alternatives"`
}

// Event is one append-only lifecycle entry from events.jsonl.
type Event struct {
	Type              string                 `json:"type"`
	Timestamp         time.Time              `json:"timestamp"`
	PlanID            string                 `json:"plan_id"`
	MutationID        string                 `json:"mutation_id,omitempty"`
	SliceID           string                 `json:"slice_id,omitempty"`
	Branch            string                 `json:"branch,omitempty"`
	MergedDefaultSHA  string                 `json:"merged_default_sha,omitempty"`
	Agent             string                 `json:"agent,omitempty"`
	DurationSeconds   *int64                 `json:"duration_seconds,omitempty"`
	Metrics           *AgentMetrics          `json:"metrics,omitempty"`
	PullRequest       *PullRequest           `json:"pull_request,omitempty"`
	PRFeedbackTriage  PRFeedbackTriageResult `json:"pr_feedback_triage,omitempty"`
	Review            *PlanReview            `json:"review,omitempty"`
	Command           string                 `json:"command,omitempty"`
	CorrectedCommand  string                 `json:"corrected_command,omitempty"`
	Result            string                 `json:"result,omitempty"`
	Round             int                    `json:"round,omitempty"`
	Attempts          int                    `json:"attempts,omitempty"`
	Fingerprint       string                 `json:"fingerprint,omitempty"`
	Reason            string                 `json:"reason,omitempty"`
	CommitPolicy      string                 `json:"commit_policy,omitempty"`
	RunPacketProvided bool                   `json:"run_packet_provided,omitempty"`
	GuardrailWarnings int                    `json:"guardrail_warnings,omitempty"`
	Metric            string                 `json:"metric,omitempty"`
	Threshold         *float64               `json:"threshold,omitempty"`
	Observed          *float64               `json:"observed,omitempty"`
	Message           string                 `json:"message"`
}
