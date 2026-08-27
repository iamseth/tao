package run

import "github.com/iamseth/tao/internal/plan"

// ExecutionBoundaryDurableFacts identifies the plan state to classify. The
// controller treats the supplied detail as read-only.
type ExecutionBoundaryDurableFacts struct {
	Detail          *plan.PlanDetail
	SliceID         string
	ContinueBlocked bool
	RestartBlocked  bool
}

// ExecutionBoundaryLiveFacts is the already-inspected execution state. Keeping
// live inspection outside the controller makes action construction pure.
type ExecutionBoundaryLiveFacts struct {
	ExecutionRoot      string
	WorkspaceStrategy  string
	CommitPolicy       string
	Branch             string
	Head               string
	PorcelainStatus    string
	ActiveGitOperation string
	BaselineBranch     string
	BaselineHead       string
	BoundaryAncestor   bool
	AncestryKnown      bool
}

// ExecutionBoundaryRepairRequirement describes durable recovery that must be
// completed before ordinary execution may proceed.
type ExecutionBoundaryRepairRequirement string

const (
	ExecutionBoundaryRepairNone             ExecutionBoundaryRepairRequirement = "none"
	ExecutionBoundaryRepairSliceStart       ExecutionBoundaryRepairRequirement = "slice_start"
	ExecutionBoundaryRepairCompletion       ExecutionBoundaryRepairRequirement = "completion"
	ExecutionBoundaryRepairManualCompletion ExecutionBoundaryRepairRequirement = "manual_completion"
)

// ExecutionBoundaryDiagnostics preserves the classifier's operator-facing
// explanation and the durable/live facts on which it was based.
type ExecutionBoundaryDiagnostics struct {
	Reason string
	Facts  InterruptedSliceFacts
}

// ExecutionBoundaryAction is the typed consequence of interrupted-slice
// classification. Disposition preserves the classifier result while
// EffectiveDisposition unwraps an approved blocked continuation.
type ExecutionBoundaryAction struct {
	Disposition               InterruptedSliceDisposition
	EffectiveDisposition      InterruptedSliceDisposition
	FixedRoot                 string
	StartingBranch            string
	StartingDirtyPaths        []string
	Diagnostics               ExecutionBoundaryDiagnostics
	RepairRequirement         ExecutionBoundaryRepairRequirement
	AllowWorkspacePreparation bool
	AllowAgentHandoff         bool
	live                      ExecutionBoundaryLiveFacts
}

// ExecutionBoundaryController owns selected-slice boundary inspection and
// converts durable and live facts into an action. Classify is the effect-free
// policy seam; InspectSelected gathers facts without preparing a workspace.
type ExecutionBoundaryController struct{}

// Classify applies the pure interrupted-slice policy kernel and maps its result
// to explicit recovery permissions and requirements.
func (ExecutionBoundaryController) Classify(durable ExecutionBoundaryDurableFacts, live ExecutionBoundaryLiveFacts) ExecutionBoundaryAction {
	result := ClassifyInterruptedSlice(InterruptedSliceInput{
		Detail:             durable.Detail,
		SliceID:            durable.SliceID,
		ExecutionRoot:      live.ExecutionRoot,
		WorkspaceStrategy:  live.WorkspaceStrategy,
		CommitPolicy:       live.CommitPolicy,
		Branch:             live.Branch,
		Head:               live.Head,
		PorcelainStatus:    live.PorcelainStatus,
		ActiveGitOperation: live.ActiveGitOperation,
		ContinueBlocked:    durable.ContinueBlocked,
		RestartBlocked:     durable.RestartBlocked,
		BaselineBranch:     live.BaselineBranch,
		BaselineHead:       live.BaselineHead,
		BoundaryAncestor:   live.BoundaryAncestor,
		AncestryKnown:      live.AncestryKnown,
	})
	effective := result.EffectiveDisposition()
	action := ExecutionBoundaryAction{
		Disposition:          result.Disposition,
		EffectiveDisposition: effective,
		Diagnostics: ExecutionBoundaryDiagnostics{
			Reason: result.Reason,
			Facts:  result.Facts,
		},
		RepairRequirement: ExecutionBoundaryRepairNone,
		live:              live,
	}

	switch effective {
	case InterruptedSliceNewStart:
		action.AllowWorkspacePreparation = true
		action.AllowAgentHandoff = true
	case InterruptedSliceCleanStartRepair:
		action.FixedRoot = result.Facts.RecordedRoot
		action.RepairRequirement = ExecutionBoundaryRepairSliceStart
		action.AllowAgentHandoff = true
	case InterruptedSliceResume:
		action.FixedRoot = result.Facts.RecordedRoot
		action.AllowAgentHandoff = true
	case InterruptedSliceCompletionRecovery:
		action.FixedRoot = result.Facts.RecordedRoot
		action.RepairRequirement = ExecutionBoundaryRepairCompletion
	case InterruptedSliceManualCompletion:
		action.FixedRoot = result.Facts.RecordedRoot
		action.RepairRequirement = ExecutionBoundaryRepairManualCompletion
	case InterruptedSliceRefuse:
		// Refusal deliberately grants no root or effect permission.
	}
	if action.FixedRoot != "" {
		action.StartingBranch = live.Branch
		if durable.Detail != nil {
			action.StartingDirtyPaths = append([]string(nil), durable.Detail.State.Plan.LastRunStartingDirty...)
		}
	}
	return action
}

func (action ExecutionBoundaryAction) sameLiveBoundary(other ExecutionBoundaryAction) bool {
	return action.live == other.live
}
