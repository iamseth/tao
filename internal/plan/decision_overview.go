package plan

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	decisionOverviewTextRunes        = 240
	decisionOverviewItemRunes        = 160
	decisionOverviewPlanIDRunes      = 96
	decisionOverviewMaxCriteria      = 5
	decisionOverviewMaxRelationships = 5
)

// DecisionOverviewSource identifies whether an overview came from structured
// metadata or from one of the bounded legacy prose fallbacks.
type DecisionOverviewSource string

const (
	DecisionOverviewSourceStructured    DecisionOverviewSource = "structured"
	DecisionOverviewSourcePlanningBrief DecisionOverviewSource = "legacy_planning_brief"
	DecisionOverviewSourcePlanNarrative DecisionOverviewSource = "legacy_plan_narrative"
	DecisionOverviewSourceUnavailable   DecisionOverviewSource = "legacy_unavailable"
)

// DecisionOverview is the bounded, render-neutral decision projection shared
// by list and monitor consumers. Empty disposition and nil priority preserve
// the unranked semantics of legacy plans.
type DecisionOverview struct {
	Problem           string
	WhyNow            string
	ExpectedBenefit   string
	Readiness         DecisionReadiness
	SuccessCriteria   []string
	Disposition       DecisionDisposition
	DispositionReason string
	Priority          *Priority
	Sequence          *Sequence
	Source            DecisionOverviewSource
}

// ProjectDecisionOverview prefers structured decision metadata. Markdown is
// inspected only for plans without a decision block, and legacy prose never
// supplies disposition or priority.
func ProjectDecisionOverview(detail *PlanDetail) DecisionOverview {
	if detail == nil {
		return DecisionOverview{Source: DecisionOverviewSourceUnavailable}
	}
	if decision := detail.State.Plan.Decision; decision != nil {
		return DecisionOverview{
			Problem:           boundedOverviewText(decision.Problem, decisionOverviewTextRunes),
			WhyNow:            boundedOverviewText(decision.WhyNow, decisionOverviewTextRunes),
			ExpectedBenefit:   boundedOverviewText(decision.ExpectedBenefit, decisionOverviewTextRunes),
			Readiness:         knownDecisionReadiness(decision.Readiness),
			SuccessCriteria:   boundedOverviewList(decision.SuccessCriteria, decisionOverviewMaxCriteria),
			Disposition:       knownDecisionDisposition(decision.Disposition),
			DispositionReason: boundedOverviewText(decision.DispositionReason, decisionOverviewTextRunes),
			Priority:          projectOverviewPriority(decision.Priority),
			Sequence:          projectOverviewSequence(detail.State.Plan.Sequence),
			Source:            DecisionOverviewSourceStructured,
		}
	}

	overview := legacyDecisionOverview(detail.PlanningBrief.Content, DecisionOverviewSourcePlanningBrief)
	if !overview.hasNarrative() {
		overview = legacyDecisionOverview(detail.PlanNarrative.Content, DecisionOverviewSourcePlanNarrative)
	}
	if !overview.hasNarrative() {
		overview.Source = DecisionOverviewSourceUnavailable
	}
	overview.Sequence = projectOverviewSequence(detail.State.Plan.Sequence)
	return overview
}

func legacyDecisionOverview(markdown string, source DecisionOverviewSource) DecisionOverview {
	overview := DecisionOverview{Source: source}
	overview.Problem = boundedOverviewText(firstMarkdownSection(markdown, "Problem", "User Goal", "Goal"), decisionOverviewTextRunes)
	overview.WhyNow = boundedOverviewText(firstMarkdownSection(markdown, "Why Now"), decisionOverviewTextRunes)
	overview.ExpectedBenefit = boundedOverviewText(firstMarkdownSection(markdown, "Expected Benefit", "Benefit"), decisionOverviewTextRunes)
	criteria := markdownSectionValues(firstMarkdownSection(markdown, "Success Criteria"))
	overview.SuccessCriteria = boundedOverviewList(criteria, decisionOverviewMaxCriteria)
	return overview
}

func (o DecisionOverview) hasNarrative() bool {
	return o.Problem != "" || o.WhyNow != "" || o.ExpectedBenefit != "" || len(o.SuccessCriteria) > 0
}

func firstMarkdownSection(markdown string, headings ...string) string {
	for _, heading := range headings {
		if section, ok := extractMarkdownSection(markdown, heading); ok && strings.TrimSpace(section) != "" {
			return section
		}
	}
	return ""
}

func markdownSectionValues(section string) []string {
	var values []string
	for line := range strings.SplitSeq(section, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(line, "-"), "*"), "+"))
		if line != "" {
			values = append(values, line)
		}
	}
	return values
}

func boundedOverviewList(values []string, maxItems int) []string {
	if maxItems <= 0 {
		return nil
	}
	out := make([]string, 0, min(len(values), maxItems))
	for _, value := range values {
		value = boundedOverviewText(value, decisionOverviewItemRunes)
		if value == "" {
			continue
		}
		out = append(out, value)
		if len(out) == maxItems {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func boundedOverviewText(value string, maxRunes int) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if value == "" || maxRunes <= 0 {
		return ""
	}
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	if maxRunes == 1 {
		return "…"
	}
	return strings.TrimSpace(string(runes[:maxRunes-1])) + "…"
}

func projectOverviewPriority(source Priority) *Priority {
	return &Priority{
		Level:      knownPriorityOverallLevel(source.Level),
		Impact:     knownPriorityLevel(source.Impact),
		Urgency:    knownPriorityLevel(source.Urgency),
		Effort:     knownPriorityEffort(source.Effort),
		Risk:       knownPriorityLevel(source.Risk),
		Confidence: knownPriorityLevel(source.Confidence),
		Rationale:  boundedOverviewText(source.Rationale, decisionOverviewTextRunes),
	}
}

func projectOverviewSequence(source *Sequence) *Sequence {
	if source == nil {
		return nil
	}
	out := &Sequence{Position: source.Position, Total: source.Total}
	for _, relationship := range source.Relationships {
		out.Relationships = append(out.Relationships, PlanRelation{
			PlanID: boundedOverviewText(relationship.PlanID, decisionOverviewPlanIDRunes),
			Type:   knownPlanRelationType(relationship.Type),
			Reason: boundedOverviewText(relationship.Reason, decisionOverviewItemRunes),
		})
		if len(out.Relationships) == decisionOverviewMaxRelationships {
			break
		}
	}
	return out
}

func knownPriorityOverallLevel(value PriorityOverallLevel) PriorityOverallLevel {
	if validPriorityOverallLevel(value) {
		return value
	}
	return ""
}

func knownPriorityEffort(value PriorityEffort) PriorityEffort {
	if validPriorityEffort(value) {
		return value
	}
	return ""
}

func knownDecisionReadiness(value DecisionReadiness) DecisionReadiness {
	if validDecisionReadiness(value) {
		return value
	}
	return ""
}

func knownDecisionDisposition(value DecisionDisposition) DecisionDisposition {
	if validDecisionDisposition(value) {
		return value
	}
	return ""
}

func knownPriorityLevel(value PriorityLevel) PriorityLevel {
	if validPriorityLevel(value) {
		return value
	}
	return ""
}

func knownPlanRelationType(value PlanRelationType) PlanRelationType {
	if validPlanRelationType(value) {
		return value
	}
	return ""
}

func cloneDecisionOverview(source DecisionOverview) DecisionOverview {
	clone := source
	clone.SuccessCriteria = append([]string(nil), source.SuccessCriteria...)
	if source.Priority != nil {
		priority := *source.Priority
		clone.Priority = &priority
	}
	clone.Sequence = cloneSequence(source.Sequence)
	return clone
}
