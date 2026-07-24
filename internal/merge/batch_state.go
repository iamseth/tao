package merge

import (
	"fmt"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

const (
	BatchStateSchema      = "tao.merge-batch.v1"
	BatchTransitionSchema = "tao.merge-batch-transition.v1"
)

// BatchStatus is the durable phase of a repository-wide merge transaction.
type BatchStatus string

const (
	BatchStatusPlanned     BatchStatus = "planned"
	BatchStatusIntegrating BatchStatus = "integrating"
	BatchStatusResolving   BatchStatus = "resolving"
	BatchStatusReviewing   BatchStatus = "reviewing"
	BatchStatusReadyToLand BatchStatus = "ready_to_land"
	BatchStatusLanded      BatchStatus = "landed"
	BatchStatusSettling    BatchStatus = "settling"
	BatchStatusCompleted   BatchStatus = "completed"
	BatchStatusBlocked     BatchStatus = "blocked"
)

// BatchBlockKind records whether operator intervention is required before a
// blocked batch can continue.
type BatchBlockKind string

const (
	BatchBlockKindResumable BatchBlockKind = "resumable"
	BatchBlockKindTerminal  BatchBlockKind = "terminal"
)

// BatchIntegration records the immutable source and the resulting Tao-owned
// integration commit for one candidate.
type BatchResolution struct {
	Attempt            int      `json:"attempt"`
	Kind               string   `json:"kind"`
	BaseSHA            string   `json:"base_sha,omitempty"`
	RequestedAt        string   `json:"requested_at"`
	CompletedAt        string   `json:"completed_at,omitempty"`
	Outcome            string   `json:"outcome,omitempty"`
	Summary            string   `json:"summary,omitempty"`
	CommitMessage      string   `json:"commit_message,omitempty"`
	ChangedPaths       []string `json:"changed_paths,omitempty"`
	ContentFingerprint string   `json:"content_fingerprint,omitempty"`
}

type BatchIntegration struct {
	PlanID             string            `json:"plan_id"`
	SourceHead         string            `json:"source_head"`
	IntegrationBaseSHA string            `json:"integration_base_sha,omitempty"`
	IntegrationSHA     string            `json:"integration_sha,omitempty"`
	CommitMessage      string            `json:"commit_message,omitempty"`
	Status             string            `json:"status,omitempty"`
	DeferredReason     string            `json:"deferred_reason,omitempty"`
	ConflictFiles      []string          `json:"conflict_files,omitempty"`
	VerificationOutput string            `json:"verification_output,omitempty"`
	Attempts           int               `json:"attempts,omitempty"`
	Fingerprint        string            `json:"fingerprint,omitempty"`
	Resolutions        []BatchResolution `json:"resolutions,omitempty"`
}

// BatchReviewRound records the aggregate findings needed to compare review
// progress without retaining duplicate finding details in transaction state.
type BatchReviewRound struct {
	HeadSHA              string   `json:"head_sha"`
	Fingerprint          string   `json:"fingerprint"`
	FindingFiles         []string `json:"finding_files"`
	FindingCount         int      `json:"finding_count"`
	AllFindingsHaveFiles bool     `json:"all_findings_have_files"`
}

// BatchNonConvergence records the detector output separately from its
// human-readable block reason so an attributed candidate can be ejected
// without parsing an error message.
type BatchNonConvergence struct {
	Files  []string `json:"files"`
	PlanID string   `json:"plan_id"`
	Reason string   `json:"reason"`
}

// BatchEjection is write-ahead intent for rebuilding an integration without
// one attributed candidate. Status advances from pending to reintegrating
// before the ordinary integrator settles it as completed.
type BatchEjection struct {
	PlanID string `json:"plan_id"`
	Reason string `json:"reason"`
	Status string `json:"status"`
}

// BatchAttempts contains bounded counters and the latest stable fingerprints
// needed to detect repeated conflict or review outcomes across processes.
type BatchAttempts struct {
	ConflictResolution  int                `json:"conflict_resolution,omitempty"`
	AggregateRework     int                `json:"aggregate_rework,omitempty"`
	ConflictFingerprint string             `json:"conflict_fingerprint,omitempty"`
	ReviewFingerprint   string             `json:"review_fingerprint"`
	ReviewHistory       []BatchReviewRound `json:"review_history"`
}

// BatchVerification is the durable result of verifying the staged aggregate.
type BatchVerification struct {
	Command     string `json:"command,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	Passed      bool   `json:"passed,omitempty"`
	StartedAt   string `json:"started_at,omitempty"`
	CompletedAt string `json:"completed_at,omitempty"`
	Output      string `json:"output,omitempty"`
	Error       string `json:"error,omitempty"`
}

// BatchReview is aggregate review evidence for the exact staged head.
type BatchReview struct {
	Status                string               `json:"status,omitempty"`
	Verdict               string               `json:"verdict,omitempty"`
	Summary               string               `json:"summary,omitempty"`
	Findings              []plan.ReviewFinding `json:"findings,omitempty"`
	BaseSHA               string               `json:"base_sha,omitempty"`
	HeadSHA               string               `json:"head_sha,omitempty"`
	Fingerprint           string               `json:"fingerprint,omitempty"`
	Attempts              int                  `json:"attempts,omitempty"`
	Artifact              string               `json:"artifact,omitempty"`
	ResolutionSHAs        []string             `json:"resolution_shas,omitempty"`
	CommitMessage         string               `json:"commit_message,omitempty"`
	ResolutionPaths       []string             `json:"resolution_paths,omitempty"`
	ResolutionFingerprint string               `json:"resolution_fingerprint,omitempty"`
	CompletedAt           string               `json:"completed_at,omitempty"`
}

// BatchLandingPlan binds one source plan to its Tao-owned squash commit.
type BatchLandingPlan struct {
	PlanID    string `json:"plan_id"`
	SquashSHA string `json:"squash_sha"`
}

// BatchLanding is the durable write-ahead intent for the single default-branch
// movement. It remains in state after landing so recovery never has to infer
// which commit is evidence for an individual plan.
type BatchLanding struct {
	DefaultParentSHA    string             `json:"default_parent_sha"`
	IntegrationHead     string             `json:"integration_head"`
	Plans               []BatchLandingPlan `json:"plans"`
	AggregateReviewHead string             `json:"aggregate_review_head"`
	ExpectedFastForward bool               `json:"expected_fast_forward"`
	LandedDefaultSHA    string             `json:"landed_default_sha,omitempty"`
}

// BatchSettlement tracks idempotent post-landing work for each source plan.
type BatchSettlement struct {
	PlanID                string `json:"plan_id"`
	MergeEvidenceRecorded bool   `json:"merge_evidence_recorded,omitempty"`
	WorkspaceCleaned      bool   `json:"workspace_cleaned,omitempty"`
	BranchCleaned         bool   `json:"branch_cleaned,omitempty"`
	RequiresAttention     bool   `json:"requires_attention,omitempty"`
	Completed             bool   `json:"completed,omitempty"`
	Error                 string `json:"error,omitempty"`
}

// BatchFinalization records cleanup of the batch-owned namespace. The active
// identity is cleared only after IntegrationCleaned is durable, so the retained
// batch directory is a complete audit record even if clearing the identity is
// interrupted.
type BatchFinalization struct {
	IntegrationCleaned bool   `json:"integration_cleaned,omitempty"`
	Error              string `json:"error,omitempty"`
}

// BatchState is a versioned repository-scoped merge transaction snapshot.
// Candidates and SHAs are copied from preflight and are never rediscovered on
// resume. LogSequence is the last transition represented by this state.
type BatchState struct {
	Schema          string               `json:"schema"`
	ID              string               `json:"id"`
	Status          BatchStatus          `json:"status"`
	RepoRoot        string               `json:"repo_root"`
	DefaultBranch   string               `json:"default_branch"`
	DefaultStartSHA string               `json:"default_start_sha"`
	Candidates      []BatchCandidate     `json:"candidates"`
	ChosenOrder     []string             `json:"chosen_order"`
	Integrations    []BatchIntegration   `json:"integrations,omitempty"`
	Attempts        BatchAttempts        `json:"attempts"`
	NonConvergence  *BatchNonConvergence `json:"non_convergence"`
	Ejection        *BatchEjection       `json:"ejection"`
	Verification    *BatchVerification   `json:"verification,omitempty"`
	Review          *BatchReview         `json:"review,omitempty"`
	// AggregateReviewSequence names immutable artifacts across exact-head review resets.
	AggregateReviewSequence int                `json:"aggregate_review_sequence"`
	Landing                 *BatchLanding      `json:"landing,omitempty"`
	Settlement              []BatchSettlement  `json:"settlement,omitempty"`
	Finalization            *BatchFinalization `json:"finalization,omitempty"`
	IntegrationHead         string             `json:"integration_head,omitempty"`
	LandedSHA               string             `json:"landed_sha,omitempty"`
	BlockedReason           string             `json:"blocked_reason,omitempty"`
	// BlockKind's omitempty is safe because the WAL store writes whole-file snapshots
	// plus append-log states, not mergeJSON patches that could retain an omitted value.
	BlockKind    BatchBlockKind `json:"block_kind,omitempty"`
	ResumeStatus BatchStatus    `json:"resume_status,omitempty"`
	CreatedAt    string         `json:"created_at,omitempty"`
	UpdatedAt    string         `json:"updated_at,omitempty"`
	LogSequence  uint64         `json:"log_sequence,omitempty"`
}

// BatchTransition is one complete durable state transition. Keeping the state
// in every record lets the log recover even when the snapshot is unavailable.
type BatchTransition struct {
	Schema   string      `json:"schema"`
	Sequence uint64      `json:"sequence"`
	At       string      `json:"at,omitempty"`
	From     BatchStatus `json:"from,omitempty"`
	To       BatchStatus `json:"to"`
	State    BatchState  `json:"state"`
}

func (s BatchState) validate() error {
	if s.Schema != BatchStateSchema {
		return fmt.Errorf("unsupported merge batch schema %q", s.Schema)
	}
	if strings.TrimSpace(s.ID) == "" {
		return fmt.Errorf("merge batch id is required")
	}
	if !s.Status.valid() {
		return fmt.Errorf("invalid merge batch status %q", s.Status)
	}
	if s.ResumeStatus != "" && (!s.ResumeStatus.valid() || s.ResumeStatus == BatchStatusBlocked || s.ResumeStatus == BatchStatusCompleted) {
		return fmt.Errorf("invalid merge batch resume status %q", s.ResumeStatus)
	}
	if s.BlockKind != "" && !s.BlockKind.valid() {
		return fmt.Errorf("invalid merge batch block kind %q", s.BlockKind)
	}
	if s.Ejection != nil {
		candidate := candidateByID(s.Candidates, s.Ejection.PlanID)
		if candidate == nil || candidate.Deferred == nil {
			return fmt.Errorf("ejected plan %q is not recorded as deferred", s.Ejection.PlanID)
		}
		if slices.Contains(s.ChosenOrder, s.Ejection.PlanID) {
			return fmt.Errorf("ejected plan %q remains in chosen order", s.Ejection.PlanID)
		}
		if !slices.Contains([]string{batchEjectionPending, batchEjectionReintegrating, batchEjectionCompleted}, s.Ejection.Status) {
			return fmt.Errorf("invalid batch ejection status %q", s.Ejection.Status)
		}
	}
	if len(s.ChosenOrder) != 0 {
		candidateIDs := make(map[string]bool, len(s.Candidates))
		for _, candidate := range s.Candidates {
			candidateIDs[candidate.PlanID] = true
		}
		seen := make(map[string]bool, len(s.ChosenOrder))
		for _, id := range s.ChosenOrder {
			if !candidateIDs[id] || seen[id] {
				return fmt.Errorf("chosen order contains invalid plan id %q", id)
			}
			seen[id] = true
		}
	}
	return nil
}

func effectiveBatchCandidates(state BatchState) []BatchCandidate {
	result := make([]BatchCandidate, 0, len(state.Candidates))
	for _, candidate := range state.Candidates {
		if state.Ejection == nil || candidate.PlanID != state.Ejection.PlanID {
			result = append(result, candidate)
		}
	}
	return result
}

func (s BatchStatus) valid() bool {
	return slices.Contains([]BatchStatus{BatchStatusPlanned, BatchStatusIntegrating, BatchStatusResolving, BatchStatusReviewing, BatchStatusReadyToLand, BatchStatusLanded, BatchStatusSettling, BatchStatusCompleted, BatchStatusBlocked}, s)
}

func (k BatchBlockKind) valid() bool {
	return k == BatchBlockKindResumable || k == BatchBlockKindTerminal
}

// BlockBatch records the explicit resume policy beside the operator-facing
// reason so rewording that prose cannot change batch behavior.
func BlockBatch(state *BatchState, kind BatchBlockKind, reason string) {
	phase := state.Status
	if phase == BatchStatusBlocked {
		phase = state.ResumeStatus
		if phase == "" && state.Review != nil && state.Review.Verdict == plan.ReviewVerdictApprove {
			phase = BatchStatusReadyToLand
		}
	}
	state.Status, state.BlockedReason, state.BlockKind, state.ResumeStatus = BatchStatusBlocked, reason, kind, ""
	if kind == BatchBlockKindResumable && phase != "" && phase != BatchStatusBlocked && phase != BatchStatusCompleted && phase.valid() {
		state.ResumeStatus = phase
	}
}

// ResumeBlockedBatch returns an eligible blocked batch to its recorded phase.
// The inference supports recovery files written before resume_status existed.
func ResumeBlockedBatch(state BatchState) (BatchState, bool) {
	if state.Status != BatchStatusBlocked || batchBlockIsTerminal(state) {
		return state, false
	}
	phase := state.ResumeStatus
	if phase == "" {
		switch {
		case state.Review != nil && state.Review.Verdict == plan.ReviewVerdictApprove:
			phase = BatchStatusReadyToLand
		case slices.ContainsFunc(state.Integrations, func(item BatchIntegration) bool { return item.Status == batchIntegrationDeferred }):
			phase = BatchStatusResolving
		case state.Verification != nil || state.Review != nil:
			phase = BatchStatusReviewing
		}
	}
	if phase == "" || phase == BatchStatusBlocked || phase == BatchStatusCompleted || !phase.valid() {
		return state, false
	}
	state.Status, state.BlockedReason, state.BlockKind, state.ResumeStatus = phase, "", "", ""
	return state, true
}

func batchBlockIsTerminal(state BatchState) bool {
	switch state.BlockKind {
	case BatchBlockKindTerminal:
		return true
	case BatchBlockKindResumable:
		return false
	case "":
		return terminalBatchBlock(state.BlockedReason)
	default:
		return true
	}
}

func terminalBatchBlock(reason string) bool {
	// Only legacy prose-only records written before block_kind reach this fallback.
	// Compatibility frozen 2026-07-19: do not reword these six phrases:
	// "cap exhausted", "stalled on equivalent findings",
	// "aggregate review not converging", "explicit approval required",
	// "without actionable findings", and "unsupported verdict".
	return strings.Contains(reason, "cap exhausted") ||
		strings.Contains(reason, "stalled on equivalent findings") ||
		strings.Contains(reason, "aggregate review not converging") ||
		strings.Contains(reason, "explicit approval required") ||
		strings.Contains(reason, "without actionable findings") ||
		strings.Contains(reason, "unsupported verdict")
}

// ValidateBatchTransition enforces the normal forward protocol. Blocked is a
// recoverable stop and may be entered from every non-completed phase.
func ValidateBatchTransition(from, to BatchStatus) error {
	if !to.valid() {
		return fmt.Errorf("invalid merge batch status %q", to)
	}
	if from == "" || from == to {
		return nil
	}
	if !from.valid() {
		return fmt.Errorf("invalid merge batch status %q", from)
	}
	if to == BatchStatusBlocked && from != BatchStatusCompleted {
		return nil
	}
	allowed := map[BatchStatus][]BatchStatus{
		BatchStatusPlanned:     {BatchStatusIntegrating},
		BatchStatusIntegrating: {BatchStatusResolving, BatchStatusReviewing},
		BatchStatusResolving:   {BatchStatusIntegrating, BatchStatusReviewing},
		BatchStatusReviewing:   {BatchStatusIntegrating, BatchStatusResolving, BatchStatusReadyToLand},
		BatchStatusReadyToLand: {BatchStatusLanded},
		BatchStatusLanded:      {BatchStatusSettling},
		BatchStatusSettling:    {BatchStatusCompleted},
		BatchStatusBlocked:     {BatchStatusPlanned, BatchStatusIntegrating, BatchStatusResolving, BatchStatusReviewing, BatchStatusReadyToLand, BatchStatusLanded, BatchStatusSettling},
	}
	if slices.Contains(allowed[from], to) {
		return nil
	}
	return fmt.Errorf("invalid merge batch transition %q -> %q", from, to)
}
