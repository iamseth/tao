package insights

import (
	"github.com/iamseth/tao/internal/intervention"
	"github.com/iamseth/tao/internal/plan"
)

// interventionProjection retains only bounded diagnostic evidence, separately
// from failure counters. It cannot establish absence of unlogged intervention.
type interventionProjection struct {
	planID                                         string
	marker, invalid, created, runContext, operator bool
	execution                                      bool
}

func (p *interventionProjection) consume(event plan.Event) {
	switch event.Type {
	case plan.EventTypeSliceStarted, plan.EventTypeSliceCompleted, plan.EventTypeSliceBlocked, plan.EventTypeFinalVerification:
		p.execution = true
	}
	if event.PlanID != p.planID {
		p.invalid = true
	}
	if event.Type == "plan_created" {
		p.created = !event.Timestamp.IsZero() || p.created
	}
	triggers, relevant := interventionTriggers[event.Type]
	if !relevant {
		return
	}
	actor, trigger := intervention.Normalize(event.Actor, event.Trigger)
	supported := event.AttributionVersion == 1 && actor != "unknown"
	if event.Type != plan.EventTypeInterventionObserved {
		matched := false
		for _, allowed := range triggers {
			matched = matched || trigger == allowed
		}
		supported = supported && matched
	}
	if !supported {
		p.invalid = true
		return
	}
	if actor == "operator" {
		p.operator = true
	}
	if event.Type == plan.EventTypeRunContext {
		p.runContext = true
	}
}

func (p interventionProjection) complete(id string, data planData, started bool) bool {
	return p.marker && p.planID == id && p.created && !p.invalid && data.historyPresent && !data.historyMalformed && ((!started && !p.execution) || p.runContext)
}

// A supported pair on an unrelated operation is not attribution evidence.
// Unknown unrelated event types do not imply an actor or invalidate coverage.
var interventionTriggers = map[string][]string{
	plan.EventTypeInterventionObserved:       nil,
	plan.EventTypeRunContext:                 {"run_context"},
	plan.EventTypeSliceResumeAttempted:       {"run_resume", "transport_retry", "timeout_continue"},
	plan.EventTypeSliceResumeFailed:          {"run_resume", "transport_retry", "timeout_continue"},
	plan.EventTypeSliceRestarted:             {"run_restart"},
	plan.EventTypeSliceAmended:               {"edit_amend"},
	plan.EventTypeSliceRemoved:               {"edit_remove"},
	plan.EventTypeSliceSkipped:               {"edit_skip"},
	plan.EventTypeSlicesReordered:            {"edit_move"},
	plan.EventTypeSliceApproved:              {"approve"},
	plan.EventTypePlanReopened:               {"rework_manual", "rework_force", "rework_pr", "rework"},
	plan.EventTypeReworkRound:                {"rework", "rework_pr"},
	plan.EventTypeReworkStopped:              {"rework_stop"},
	plan.EventTypeVerificationRepairCreated:  {"verification_repair"},
	plan.EventTypeVerificationRepairStopped:  {"verification_repair_stop"},
	plan.EventTypeFinalizationFailureCleared: {"finalization_recovery"},
	plan.EventTypeSingleMergeIntentRestarted: {"merge_restart"},
	plan.EventTypeSingleMergeRolledBack:      {"merge_rollback"},
	plan.EventTypeSingleMergeRearmed:         {"merge_rearm"},
	plan.EventTypePlanMerged:                 {"merge_complete"},
}
