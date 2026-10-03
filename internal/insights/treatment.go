// Planner treatment normalization uses a deliberately explicit, low-confidence
// table of observed plan_created.agent labels, not provider or model inference.
// Additions require listing the exact observed label; only the documented runtime
// prefix forms generalize. Reasoning effort comes only from planning metrics.
package insights

import (
	"strings"
	"unicode"

	"github.com/iamseth/tao/internal/plan"
)

type TreatmentConfidence string

const (
	TreatmentConfidenceHigh      TreatmentConfidence = "high"
	TreatmentConfidenceLow       TreatmentConfidence = "low"
	TreatmentConfidenceAmbiguous TreatmentConfidence = "ambiguous"
)

const maxTreatmentLabelRunes = 64

type PlannerTreatment struct {
	Runtime, Provider, Model, ReasoningEffort string
	Confidence                                TreatmentConfidence
	Label                                     string
	Source                                    string
}

// NormalizePlannerLabel preserves model spellings rather than merging aliases.
// Provider-like prefixes in free-form labels are not provider evidence.
func NormalizePlannerLabel(label string) PlannerTreatment {
	label = sanitizeTreatmentLabel(label)
	t := PlannerTreatment{Label: label, Confidence: TreatmentConfidenceAmbiguous, Source: "plan_created"}
	if label == "" {
		t.Source = "none"
		return t
	}
	switch label {
	case "pi", "claude":
		t.Runtime, t.Confidence = label, TreatmentConfidenceHigh
		return t
	case "claude-code":
		t.Runtime = "claude"
	case "claude-opus-5", "claude-opus-5[1m]", "claude-fable-5":
		t.Runtime, t.Model = "claude", label
	case "claude-code/fable-5":
		t.Runtime, t.Model = "claude", "fable-5"
	case "build", "gpt-5.6-sol", "openai-codex/gpt-5.6-sol":
		return t
	default:
		if model, ok := strings.CutPrefix(label, "claude-code/"); ok && model != "" {
			t.Runtime = "claude"
		} else {
			// Most specific first: pi/openai-codex/gpt-5.6-sol,
			// pi/gpt-5.6-sol, and pi:gpt-5.6-sol.
			for _, prefix := range []string{"pi/openai-codex/", "pi/", "pi:"} {
				if model, ok := strings.CutPrefix(label, prefix); ok {
					if model != "" {
						t.Runtime, t.Model = "pi", model
					}
					break
				}
			}
		}
	}
	if t.Runtime != "" {
		t.Confidence = TreatmentConfidenceLow
	}
	return t
}

// ResolvePlannerTreatment uses the last qualifying planning event in input
// order, without mixing identities from separate sessions. Legacy role-less or
// non-planning metrics never supply planner evidence. High confidence requires
// agreement on the runtime and, when present in the label, the exact model.
func ResolvePlannerTreatment(label string, planning []plan.AgentMetrics) PlannerTreatment {
	t := NormalizePlannerLabel(label)
	for i := len(planning) - 1; i >= 0; i-- {
		metrics := planning[i]
		if metrics.Role != plan.AgentRolePlanning {
			continue
		}
		provider, model := sanitizeTreatmentLabel(metrics.ProviderID), sanitizeTreatmentLabel(metrics.ModelID)
		if provider == "" && model == "" {
			continue
		}
		agent := NormalizePlannerLabel(metrics.Agent)
		agrees := t.Runtime != "" && t.Runtime == agent.Runtime && (t.Model == "" || t.Model == model)
		if t.Confidence == TreatmentConfidenceAmbiguous {
			t.Runtime = agent.Runtime
		}
		t.Provider = provider
		t.ReasoningEffort = sanitizeTreatmentLabel(metrics.ReasoningEffort)
		if model != "" {
			t.Model = model
		}
		t.Source = "planning_metrics"
		switch {
		case t.Runtime == "":
			t.Confidence = TreatmentConfidenceAmbiguous
		case agrees:
			t.Confidence = TreatmentConfidenceHigh
		default:
			t.Confidence = TreatmentConfidenceLow
		}
		return t
	}
	return t
}

// CohortKey excludes unidentified treatments and bounds each key component.
func (t PlannerTreatment) CohortKey() string {
	if t.Confidence != TreatmentConfidenceHigh && t.Confidence != TreatmentConfidenceLow {
		return ""
	}
	runtime := treatmentKeyPart(t.Runtime)
	if runtime == "" {
		return ""
	}
	if model := treatmentKeyPart(t.Model); model != "" {
		return runtime + "/" + model
	}
	return runtime
}

func treatmentKeyPart(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_/:[]", r) {
			return r
		}
		return '_'
	}, sanitizeTreatmentLabel(value))
}

func sanitizeTreatmentLabel(value string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(value) {
		if n == maxTreatmentLabelRunes {
			break
		}
		r = unicode.ToLower(r)
		// Replace rather than delete controls so they cannot join fragments
		// into a recognized runtime label (for example, p\x00i into pi).
		if !unicode.IsPrint(r) {
			r = '_'
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
