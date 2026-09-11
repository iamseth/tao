package plan

import (
	"fmt"
	"strings"
	"time"
)

const (
	VerificationRepairSlicePrefix = "vr01-final-verification-"
	VerificationRepairAttemptCap  = 2
)

// VerificationRecoveryDecision is the centralized read-side interpretation of
// current final-verification evidence and the plan's generated repair history.
type VerificationRecoveryDecision struct {
	Kind              PlanActionKind
	Command           string
	Instruction       string
	Reason            string
	AttemptCapReached bool
}

// VerificationRepairAttemptCount returns the lifetime number of generated
// verification-repair slices. Completed attempts remain part of the count.
func VerificationRepairAttemptCount(detail *PlanDetail) int {
	if detail == nil {
		return 0
	}
	count := 0
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].VerificationRepair != nil {
			count++
		}
	}
	return count
}

// DeriveVerificationRecovery classifies the safe recovery for current failed
// final-verification evidence. It grants no mutation authority.
func DeriveVerificationRecovery(detail *PlanDetail) VerificationRecoveryDecision {
	failure := CurrentFailedFinalVerification(detail)
	if failure == nil {
		return VerificationRecoveryDecision{Kind: PlanActionNone, Reason: "no current failed final-verification evidence authorizes repair"}
	}

	id := "<plan>"
	if strings.TrimSpace(detail.State.Plan.ID) != "" {
		id = strings.TrimSpace(detail.State.Plan.ID)
	}
	attempts := VerificationRepairAttemptCount(detail)
	decision := VerificationRecoveryDecision{AttemptCapReached: attempts >= VerificationRepairAttemptCap}
	stopReason := ""
	if decision.AttemptCapReached {
		if stopped := currentVerificationRepairStop(detail, *failure); stopped != nil {
			stopReason = strings.TrimSpace(stopped.Reason)
		}
	}
	switch failure.FailureKind {
	case FinalVerificationFailureKindCode:
		if decision.AttemptCapReached {
			decision.Kind = PlanActionResolveVerification
			decision.Instruction = "Repair the repository verification failure manually before explicitly reverifying"
			decision.Reason = "verification repair attempt cap reached"
			if stopReason != "" {
				decision.Reason = stopReason
			}
			return decision
		}
		decision.Kind = PlanActionRepairVerification
		decision.Command = "tao run --repair-verification " + id
		decision.Reason = "current code-classified final repository verification failed on the completed branch"
	case FinalVerificationFailureKindToolMissing:
		decision.Kind = PlanActionResolveVerification
		decision.Instruction = "Restore the tool required by the repository verification command before explicitly reverifying the unchanged head"
		decision.Reason = "tool-missing verification evidence does not authorize code repair"
	case FinalVerificationFailureKindTimeout:
		decision.Kind = PlanActionResolveVerification
		decision.Instruction = "Resolve the repository verification timeout before explicitly reverifying the unchanged head"
		decision.Reason = "timed-out verification evidence does not authorize code repair"
	case FinalVerificationFailureKindCancelled:
		decision.Kind = PlanActionResolveVerification
		decision.Instruction = "Resolve the repository verification cancellation before explicitly reverifying the unchanged head"
		decision.Reason = "cancelled verification evidence does not authorize code repair"
	case FinalVerificationFailureKindInvalidCommand:
		decision.Kind = PlanActionResolveVerification
		decision.Instruction = "Correct the repository verification command before explicitly reverifying the unchanged head"
		decision.Reason = "invalid-command verification evidence does not authorize code repair"
	default:
		decision.Kind = PlanActionReverify
		decision.Command = "tao run --reverify " + id
		decision.Reason = "unclassified verification evidence does not authorize code repair"
	}
	if stopReason != "" {
		decision.Reason = stopReason
	}
	return decision
}

func currentVerificationRepairStop(detail *PlanDetail, failure FinalVerification) *Event {
	if detail == nil {
		return nil
	}
	for i := len(detail.Events) - 1; i >= 0; i-- {
		event := &detail.Events[i]
		if event.Type == EventTypeVerificationRepairStopped && event.Command == failure.Command && event.HeadSHA == failure.HeadSHA && event.Fingerprint == failure.Fingerprint {
			return event
		}
	}
	return nil
}

// VerificationRepairRequest carries live Git evidence checked by run before the
// journaled mutation appends an evidence-bound generated repair slice.
type VerificationRepairRequest struct {
	Binding   VerificationRepairBinding
	CreatedAt time.Time
}

func (r VerificationRepairRequest) Validate() error {
	if strings.TrimSpace(r.Binding.Command) == "" || strings.TrimSpace(r.Binding.HeadSHA) == "" || strings.TrimSpace(r.Binding.Fingerprint) == "" {
		return fmt.Errorf("verification repair requires command, head, and fingerprint")
	}
	if r.CreatedAt.IsZero() {
		return fmt.Errorf("verification repair requires creation timestamp")
	}
	return nil
}

// AppendVerificationRepair atomically checks current failed evidence and adds
// one generated slice for that exact failure. A completed repair may be
// followed by another repair only when final verification records new evidence.
func (r *PlanRecord) AppendVerificationRepair(request VerificationRepairRequest) error {
	if err := request.Validate(); err != nil {
		return err
	}
	return r.apply(func(detail *PlanDetail) (lifecycleMutation, error) {
		if detail == nil || detail.State.Plan.FinalVerification == nil {
			return lifecycleMutation{}, fmt.Errorf("verification repair requires failed final-verification evidence")
		}
		failed := detail.State.Plan.FinalVerification
		if failed.Result != "failed" {
			return lifecycleMutation{}, fmt.Errorf("verification repair requires a failed result; current result is %q", failed.Result)
		}
		if failed.Command != request.Binding.Command || failed.HeadSHA != request.Binding.HeadSHA || failed.Fingerprint != request.Binding.Fingerprint {
			return lifecycleMutation{}, fmt.Errorf("verification repair evidence changed before mutation")
		}
		decision := DeriveVerificationRecovery(detail)
		if decision.Kind != PlanActionRepairVerification {
			return lifecycleMutation{}, fmt.Errorf("verification repair refused: %s", decision.Reason)
		}
		repairNumber := 1
		for i := range detail.Slices.Slices {
			slice := &detail.Slices.Slices[i]
			if slice.VerificationRepair == nil {
				continue
			}
			repairNumber++
			if slice.Completion == nil {
				return lifecycleMutation{}, fmt.Errorf("verification repair slice already exists and is not complete: %s", slice.ID)
			}
			if *slice.VerificationRepair == request.Binding {
				return lifecycleMutation{}, fmt.Errorf("verification repair already exists for current evidence: %s", slice.ID)
			}
		}
		if !sliceWorkSettled(detail) {
			return lifecycleMutation{}, fmt.Errorf("verification repair requires settled slice work")
		}
		if detail.State.Plan.MergeCommitIntent != nil || detail.State.Plan.PullRequestIntent != nil {
			return lifecycleMutation{}, fmt.Errorf("verification repair refuses unsettled post-slice intent")
		}

		short := request.Binding.Fingerprint
		if len(short) > 12 {
			short = short[:12]
		}
		sliceID := VerificationRepairSlicePrefix + short
		if repairNumber > 1 {
			sliceID = fmt.Sprintf("vr%02d-final-verification-%s", repairNumber, short)
		}
		binding := request.Binding
		truncation := "no"
		if failed.OutputTruncated {
			truncation = "yes; only the output tail was retained"
		}
		failureContext := fmt.Sprintf("Command: %s\nResult: %s\nHead: %s\nFingerprint: %s\nOutput truncated: %s\nOutput tail:\n%s", failed.Command, failed.Result, failed.HeadSHA, failed.Fingerprint, truncation, failed.Details)
		slice := Slice{
			ID: sliceID, Title: "Repair final verification", Status: StatusPending,
			Tags:      []string{"generated", "verification-repair"},
			DependsOn: append([]string(nil), detail.State.Plan.CompletedSlices...),
			Timing:    SliceTiming{CreatedAt: request.CreatedAt, UpdatedAt: request.CreatedAt},
			Goal:      "Repair the exact failed repository-wide verification command without expanding scope.",
			Context:   failureContext,
			Tasks: []string{
				"Diagnose and repair only the bounded final-verification failure context.",
				"Use the normal structured commit proposal and Tao-owned slice completion path.",
			},
			Verification: Verification{Commands: []string{request.Binding.Command}, Source: "exact failed repository-owned final-verification command", ManualChecks: []string{}},
			Approval:     &Approval{Required: false}, VerificationRepair: &binding,
		}
		detail.Slices.Slices = append(detail.Slices.Slices, slice)
		detail.State.Plan.PendingSlices = append(detail.State.Plan.PendingSlices, sliceID)
		detail.State.Plan.CurrentSlice = nil
		detail.State.Plan.Timing.CompletedAt = nil
		detail.State.Plan.Timing.LastActivityAt = new(request.CreatedAt)
		detail.State.Status = StatusInProgress
		detail.State.UpdatedAt = request.CreatedAt
		event := Event{Type: EventTypeVerificationRepairCreated, Timestamp: request.CreatedAt, PlanID: detail.State.Plan.ID, SliceID: sliceID, Command: binding.Command, Fingerprint: binding.Fingerprint, Reason: "failed final verification at " + binding.HeadSHA, Message: "Generated final-verification repair slice"}
		return lifecycleMutation{State: detail.State, Slices: detail.Slices, Events: []Event{event}}, nil
	})
}

// CurrentFailedFinalVerification reports failure evidence that still describes
// the durable completed workspace head.
func CurrentFailedFinalVerification(detail *PlanDetail) *FinalVerification {
	if detail == nil || detail.State.Plan.FinalVerification == nil || detail.State.Plan.FinalVerification.Result != "failed" || detail.State.Workspace == nil {
		return nil
	}
	verification := detail.State.Plan.FinalVerification
	if strings.TrimSpace(verification.HeadSHA) == "" || verification.HeadSHA != detail.State.Workspace.HeadSHA || strings.TrimSpace(verification.Fingerprint) == "" {
		return nil
	}
	return verification
}
