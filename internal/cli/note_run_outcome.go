package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/planning"
)

// Note-run exit codes are intentionally separate from ordinary run semantics.
const (
	NoteRunCompleted = iota
	NoteRunInfrastructure
	NoteRunUsage
	NoteRunBlocked
	NoteRunDecision
	NoteRunPlanningRefusal
	NoteRunNothingToDo
)

// NoteRunOutcome reports an observation, never permission to mutate or recover.
type NoteRunOutcome struct {
	Code  int
	Cause error
}

func (e *NoteRunOutcome) Error() string {
	if e.Cause == nil {
		return fmt.Sprintf("note run outcome %d", e.Code)
	}
	return e.Cause.Error()
}
func (e *NoteRunOutcome) Unwrap() error { return e.Cause }

// ExitCode preserves the historical nil/error contract for unrelated commands.
// Every independent branch of a joined error must agree on a typed outcome;
// an infrastructure failure must not be hidden by another branch.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := err.(*NoteRunOutcome); ok { //nolint:errorlint // Inspect this node only; recursive traversal below preserves independent joined failures.
		return e.Code
	}
	if e, ok := err.(interface{ Unwrap() []error }); ok {
		code := -1
		for _, child := range e.Unwrap() {
			if child == nil {
				continue
			}
			next := ExitCode(child)
			if code != -1 && code != next {
				return NoteRunInfrastructure
			}
			code = next
		}
		if code >= 0 {
			return code
		}
	}
	if e, ok := err.(interface{ Unwrap() error }); ok {
		if cause := e.Unwrap(); cause != nil {
			return ExitCode(cause)
		}
	}
	return NoteRunInfrastructure
}

func noteRunOutcome(code int, err error) error { return &NoteRunOutcome{Code: code, Cause: err} }

func classifyNoteRunError(err error) error {
	if err == nil {
		return nil
	}
	return noteRunOutcome(noteRunErrorCode(err), err)
}

func noteRunErrorCode(err error) int {
	switch e := err.(type) { //nolint:errorlint // Inspect each error-tree node separately so errors.As cannot hide a joined infrastructure failure.
	case *NoteRunOutcome:
		return e.Code
	case *plan.ApprovalRequiredError, *plan.ReworkScopeDecisionError, *plan.ApprovalEvidenceError:
		return NoteRunDecision
	case *planning.GenerationError:
		if e.CleanupErr != nil || errors.Is(e.Err, context.Canceled) || errors.Is(e.Err, context.DeadlineExceeded) {
			return NoteRunInfrastructure
		}
		switch e.Stage {
		case planning.GenerationStageWroteElsewhere, planning.GenerationStageRequiredArtifact, planning.GenerationStagePlanID, planning.GenerationStageVerification, planning.GenerationStageOpenQuestions, planning.GenerationStageValidation:
			return NoteRunPlanningRefusal
		}
		return NoteRunInfrastructure
	case interface{ Unwrap() []error }:
		code := -1
		for _, child := range e.Unwrap() {
			if child == nil {
				continue
			}
			next := noteRunErrorCode(child)
			if code != -1 && code != next {
				return NoteRunInfrastructure
			}
			code = next
		}
		if code >= 0 {
			return code
		}
	case interface{ Unwrap() error }:
		return noteRunErrorCode(e.Unwrap())
	}
	return NoteRunInfrastructure
}

// projectNoteRunOutcome reads freshly loaded artifacts after a nil handoff or
// admission refusal. Only blocked/decision projections refine admission refusals;
// actual execution failures take precedence over all durable projections.
func projectNoteRunOutcome(detail *plan.PlanDetail, alreadyComplete bool) error {
	if detail == nil {
		return noteRunOutcome(NoteRunInfrastructure, fmt.Errorf("note run: outcome loader returned no plan"))
	}
	if plan.PlanLifecycleStatus(detail) == plan.StatusCompleted {
		if !alreadyComplete {
			return nil
		}
		return noteRunOutcome(NoteRunNothingToDo, fmt.Errorf("note run: linked plan is already completed"))
	}
	derived := plan.Derive(detail, time.Time{})
	if derived.Capabilities.NeedsApproval || derived.Capabilities.NeedsScopeDecision || plan.IsPostSliceStatus(plan.PlanLifecycleStatus(detail)) {
		return noteRunOutcome(NoteRunDecision, fmt.Errorf("note run: plan requires an approval, review, merge, or scope decision"))
	}
	if detail.State.Status == plan.StatusBlocked || derived.CurrentSlice != nil && derived.CurrentSlice.Status == plan.StatusBlocked || derived.NextSlice != nil && derived.NextSlice.Status == plan.StatusBlocked {
		return noteRunOutcome(NoteRunBlocked, fmt.Errorf("note run: slice is blocked"))
	}
	return noteRunOutcome(NoteRunNothingToDo, fmt.Errorf("note run: no further work executed; plan is not completed (partial progress is possible)"))
}
