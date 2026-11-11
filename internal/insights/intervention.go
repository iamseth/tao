package insights

import (
	"crypto/sha256"
	"encoding/json"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/intervention"
	"github.com/iamseth/tao/internal/plan"
)

// interventionProjection retains only bounded diagnostic evidence, separately
// from failure counters. It cannot establish absence of unlogged intervention.
type interventionProjection struct {
	planID                                         string
	marker, invalid, created, runContext, operator bool
	execution                                      bool
	mechanicalPairs                                map[[32]byte]int
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
	if event.Type == plan.EventTypeScopeExtension || event.Type == plan.EventTypeSliceAmended {
		switch event.ScopeClassification {
		case "mechanical":
			if actor != "operator" || !validMechanicalScope(event) {
				p.invalid = true
				return
			}
			// Match both journaled halves without retaining untrusted reason/path text.
			payload, _ := json.Marshal(struct {
				Slice  string
				Time   string
				Reason string
				Paths  []string
			}{event.SliceID, event.Timestamp.String(), event.Reason, event.Paths})
			key := sha256.Sum256(payload)
			if p.mechanicalPairs == nil {
				p.mechanicalPairs = make(map[[32]byte]int)
			}
			delta := 1
			if event.Type == plan.EventTypeScopeExtension {
				delta = -1
			}
			p.mechanicalPairs[key] += delta
			if p.mechanicalPairs[key] == 0 {
				delete(p.mechanicalPairs, key)
			}
			return
		case "judgement":
			if actor != "operator" {
				p.invalid = true
				return
			}
		case "":
			if event.Type == plan.EventTypeScopeExtension || len(event.Paths) != 0 {
				p.invalid = true
				return
			}
		default:
			p.invalid = true
			return
		}
	}
	if actor == "operator" {
		p.operator = true
	}
	if event.Type == plan.EventTypeRunContext {
		p.runContext = true
	}
}

func (p interventionProjection) complete(id string, data planData, started bool) bool {
	return len(p.mechanicalPairs) == 0 && p.marker && p.planID == id && p.created && !p.invalid && data.historyPresent && !data.historyMalformed && ((!started && !p.execution) || p.runContext)
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
	plan.EventTypeScopeExtension:             {"edit_amend"},
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

// Classification is diagnostic only, not proof that the operator's claim is true.
func validMechanicalScope(e plan.Event) bool {
	if e.SliceID == "" || e.Timestamp.IsZero() || strings.TrimSpace(e.Reason) == "" || !utf8.ValidString(e.Reason) || utf8.RuneCountInString(e.Reason) > 16*1024 || len(e.Paths) == 0 || len(e.ReplacedVerificationCommands) != 0 {
		return false
	}
	if e.Type == plan.EventTypeSliceAmended {
		if len(e.AmendedFields) != 1 || e.AmendedFields[0] != "expected_files" {
			return false
		}
	} else if len(e.AmendedFields) != 0 {
		return false
	}
	for _, path := range e.Paths {
		value := strings.TrimSpace(strings.ReplaceAll(path, "\\", "/"))
		if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || strings.HasPrefix(value, "/") || (len(value) >= 2 && value[1] == ':') || strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return false
		}
		for _, part := range strings.Split(value, "/") {
			if part == ".." {
				return false
			}
		}
	}
	return true
}
