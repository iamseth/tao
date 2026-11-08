package plan

type RunCapabilities struct {
	CanRun                 bool     `json:"can_run"`
	DisabledReason         string   `json:"disabled_reason,omitempty"`
	NeedsScopeDecision     bool     `json:"needs_scope_decision,omitempty"`
	ScopeDecisionSliceID   string   `json:"scope_decision_slice_id,omitempty"`
	ScopeDecisionPaths     []string `json:"scope_decision_paths,omitempty"`
	NeedsApproval          bool     `json:"needs_approval,omitempty"`
	ApprovalSliceID        string   `json:"approval_slice_id,omitempty"`
	ApprovalReason         string   `json:"approval_reason,omitempty"`
	CanContinue            bool     `json:"can_continue"`
	ContinueDisabledReason string   `json:"continue_disabled_reason,omitempty"`
	Complete               bool     `json:"complete"`
	Reviewed               bool     `json:"reviewed"`
	Active                 bool     `json:"active"`
}

// PlanActionKind identifies one read-only lifecycle recommendation. Commands
// remain subject to their normal dynamic gates; this projection grants no
// authority to mutate plan or repository state.
type PlanActionKind string

// Action kinds identify read-only next-step recommendations presented by plan consumers.
const (
	PlanActionRecoverSliceCompletion PlanActionKind = "recover_slice_completion"
	PlanActionRecoverRebase          PlanActionKind = "recover_rebase"
	PlanActionRecoverPullRequest     PlanActionKind = "recover_pull_request"
	PlanActionRecoverMerge           PlanActionKind = "recover_merge"
	PlanActionRebaseAndReview        PlanActionKind = "rebase_and_review"
	PlanActionRestartRework          PlanActionKind = "restart_rework"
	PlanActionApprove                PlanActionKind = "approve"
	PlanActionAmendScope             PlanActionKind = "amend_scope"
	PlanActionContinue               PlanActionKind = "continue"
	PlanActionRestartBlocked         PlanActionKind = "restart_blocked"
	PlanActionRestartMerge           PlanActionKind = "restart_merge"
	PlanActionRepairVerification     PlanActionKind = "repair_verification"
	PlanActionReverify               PlanActionKind = "reverify"
	PlanActionResolveVerification    PlanActionKind = "resolve_verification"
	PlanActionRepairReviewProposal   PlanActionKind = "repair_review_proposal"
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

// Action classes distinguish ordinary progress from recovery, exceptions, and terminal recommendations.
const (
	PlanActionClassProgress       PlanActionClass = "progress"
	PlanActionClassRecovery       PlanActionClass = "recovery"
	PlanActionClassAdministrative PlanActionClass = "administrative"
	PlanActionClassTerminal       PlanActionClass = "terminal"
)

// SingleMergeIntentRecovery is a transient read-side classification of the
// live repository boundary for an active single-plan merge intent. It is never
// persisted in plan artifacts and grants no mutation authority.
type SingleMergeIntentRecovery struct {
	Phase   string
	Verdict string
	Reason  string
}

// Recovery verdicts describe live single-merge boundaries for next-action projection only.
const (
	SingleMergeRecoveryRestartable             = "restartable"
	SingleMergeRecoveryRebaseAndReviewRequired = "rebase-and-review-required"
	SingleMergeRecoverySettleExisting          = "settle-existing-resolution"
	SingleMergeRecoveryManualOnly              = "manual-only"
	SingleMergeRecoverySatisfied               = "satisfied"
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
