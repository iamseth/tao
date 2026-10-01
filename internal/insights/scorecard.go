package insights

import (
	"cmp"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

// Fixed defaults chosen during planning, not execution policy or configuration.
const scorecardMaturityWindow = 14 * 24 * time.Hour

// scorecardMinimumSamples is the fixed minimum cohort size chosen during planning.
const scorecardMinimumSamples = 5

// Leave room for fixed reliability counters without allowing untrusted blocked
// reasons and finalization categories to grow the observation without bound.
const scorecardDynamicReliabilityKeys = 48

// Scorecard contains read-time advisory planner evidence, never lifecycle authority.
type Scorecard struct {
	Coverage       ScorecardCoverage  `json:"coverage"`
	Cohorts        []TreatmentCohort  `json:"cohorts"`
	Strata         []StratumBreakdown `json:"strata"`
	Inversions     []Inversion        `json:"inversions"`
	ExcludedLabels []ExcludedLabel    `json:"excluded_labels"`
}

type LabelCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type RoleTotals struct {
	Role         string  `json:"role"`
	Sessions     int     `json:"sessions"`
	OutputTokens int64   `json:"output_tokens"`
	TotalTokens  int64   `json:"total_tokens"`
	Cost         float64 `json:"cost"`
	ToolCalls    int64   `json:"tool_calls"`
}

type EfficiencyMedians struct {
	Plans                        int            `json:"plans"`
	Sessions                     MedianEstimate `json:"sessions"`
	OutputTokens                 MedianEstimate `json:"output_tokens"`
	TotalTokens                  MedianEstimate `json:"total_tokens"`
	Cost                         MedianEstimate `json:"cost"`
	ToolCalls                    MedianEstimate `json:"tool_calls"`
	CostPerOriginalSlice         MedianEstimate `json:"cost_per_original_slice"`
	OutputTokensPerOriginalSlice MedianEstimate `json:"output_tokens_per_original_slice"`
	HoursToApproval              MedianEstimate `json:"hours_to_approval"`
	PlanningCost                 MedianEstimate `json:"planning_cost"`
	PlanningOutputTokens         MedianEstimate `json:"planning_output_tokens"`
}

type EfficiencyOutcomes struct {
	MaturedCompleted    EfficiencyMedians `json:"matured_completed"`
	MaturedAll          EfficiencyMedians `json:"matured_all"`
	ByRole              []RoleTotals      `json:"by_role"`
	RoleAttributedRatio float64           `json:"role_attributed_ratio"`
}

type QualityOutcomes struct {
	Started                RateEstimate   `json:"started"`
	FirstReviewApproved    RateEstimate   `json:"first_review_approved"`
	ExactApproval          RateEstimate   `json:"exact_approval"`
	Completed              RateEstimate   `json:"completed"`
	Merged                 RateEstimate   `json:"merged"`
	PullRequestCompleted   RateEstimate   `json:"pull_request_completed"`
	Abandoned              RateEstimate   `json:"abandoned"`
	ReworkStopped          RateEstimate   `json:"rework_stopped"`
	AnyRework              RateEstimate   `json:"any_rework"`
	AnyVerificationRepair  RateEstimate   `json:"any_verification_repair"`
	ReviewRounds           MedianEstimate `json:"review_rounds"`
	OriginalSlices         MedianEstimate `json:"original_slices"`
	FindingSeverities      []LabelCount   `json:"finding_severities"`
	ValidationWarningPlans int            `json:"validation_warning_plans"`
}

type ReliabilityOutcomes struct {
	Infrastructure []LabelCount `json:"infrastructure"`
	Quality        []LabelCount `json:"quality"`
}

type CohortOutcomes struct {
	Plans        int                 `json:"plans"`
	Matured      int                 `json:"matured"`
	Censored     int                 `json:"censored"`
	NeverStarted int                 `json:"never_started"`
	Quality      QualityOutcomes     `json:"quality"`
	Efficiency   EfficiencyOutcomes  `json:"efficiency"`
	Reliability  ReliabilityOutcomes `json:"reliability"`
}

type TreatmentCohort struct {
	Key        string              `json:"key"`
	Runtime    string              `json:"runtime"`
	Provider   string              `json:"provider"`
	Model      string              `json:"model"`
	Confidence TreatmentConfidence `json:"confidence"`
	Outcomes   CohortOutcomes      `json:"outcomes"`
}

type StratumBreakdown struct {
	Stratum string            `json:"stratum"`
	Key     string            `json:"key"`
	Cohorts []TreatmentCohort `json:"cohorts"`
}

type ExcludedLabel struct {
	Label  string `json:"label"`
	Plans  int    `json:"plans"`
	Reason string `json:"reason"`
}

// ScorecardCoverage exposes missing evidence and censoring alongside sample counts.
type ScorecardCoverage struct {
	Plans                   int `json:"plans"`
	NeverStarted            int `json:"never_started"`
	Active                  int `json:"active"`
	Terminal                int `json:"terminal"`
	Matured                 int `json:"matured"`
	Censored                int `json:"censored"`
	TreatmentHigh           int `json:"treatment_high"`
	TreatmentLow            int `json:"treatment_low"`
	TreatmentAmbiguous      int `json:"treatment_ambiguous"`
	TreatmentMissing        int `json:"treatment_missing"`
	PlanningMetricsPlans    int `json:"planning_metrics_plans"`
	RoleAttributedSessions  int `json:"role_attributed_sessions"`
	UnattributedSessions    int `json:"unattributed_sessions"`
	ReasoningEffortRecorded int `json:"reasoning_effort_recorded"`
	MaturityWindowDays      int `json:"maturity_window_days"`
	MinimumSamples          int `json:"minimum_samples"`
}

type observationClass string

const (
	observationNeverStarted observationClass = "never_started"
	observationActive       observationClass = "active"
	observationTerminal     observationClass = "terminal"
)

type roleTotals struct {
	sessions                                                   int
	outputTokens, totalTokens, toolCalls                       int64
	cost                                                       float64
	outputPresent, totalPresent, costPresent, toolCallsPresent bool
	availability                                               plan.AgentMetricsAvailabilityCounts
}

type planObservation struct {
	repository                                                        sourceIdentity
	planID                                                            string
	treatment                                                         PlannerTreatment
	repositoryKey, effort, risk, changeType, era                      string
	class                                                             observationClass
	matured                                                           bool
	started                                                           bool
	firstReviewVerdict                                                string
	reviewRounds                                                      int
	severities                                                        map[string]int
	reworkSlices, verificationRepairs, reworkStops                    int
	abandoned, exactApproval, completed, merged, pullRequestCompleted bool
	validationWarnings                                                int
	roles                                                             map[string]roleTotals
	originalSlices                                                    int
	hoursToApproval                                                   float64
	hasApproval                                                       bool
	planningMetrics                                                   bool
	reliability                                                       map[string]int
}

func observePlan(repository sourceIdentity, summary plan.PlanSummary, data planData, now time.Time) planObservation {
	o := planObservation{
		repository: repository, planID: summary.ID,
		treatment:     NormalizePlannerLabel(""),
		repositoryKey: scorecardLabel(repository.id), effort: "unknown", risk: "unknown",
		changeType: scorecardLabel(string(summary.ChangeType)), era: "unknown",
		started: summary.StartedAt != nil, completed: summary.Complete,
		abandoned:    summary.Status == plan.StatusAbandoned || summary.Abandonment != nil,
		merged:       plan.PlanIsMerged(data.lifecycle),
		reworkSlices: summary.ReworkTotalCount, originalSlices: summary.OriginalTotalCount,
		validationWarnings: len(summary.Warnings),
		severities:         make(map[string]int), roles: make(map[string]roleTotals), reliability: make(map[string]int),
		planningMetrics: len(data.planningMetrics) > 0,
	}
	// Missing/malformed creation evidence remains missing even if metrics exist.
	if !data.createdAt.IsZero() {
		o.treatment = ResolvePlannerTreatment(data.createdAgent, data.planningMetrics)
	}
	if summary.Overview.Priority != nil {
		o.effort = scorecardLabel(string(summary.Overview.Priority.Effort))
		o.risk = scorecardLabel(string(summary.Overview.Priority.Risk))
	}
	if prefix, _, ok := strings.Cut(summary.ID, "-"); ok {
		if date, err := time.Parse("20060102", prefix); err == nil {
			o.era = date.Format("2006-01")
		}
	}
	rounds, _ := plan.ProjectReviewRounds(data.lifecycle)
	o.reviewRounds = len(rounds)
	start := data.createdAt
	if start.IsZero() {
		start = data.earliestAt
	}
	var currentReview *plan.PlanReview
	var approvalAt time.Time
	approvalSeen := false
	for _, event := range data.lifecycle {
		switch event.Type {
		case plan.EventTypeSliceStarted:
			o.started = true
		case plan.EventTypePlanReopened:
			currentReview = nil
		case plan.EventTypePlanReviewed:
			currentReview = event.Review
			if event.Review == nil || event.Review.Status != plan.ReviewStatusCompleted {
				continue
			}
			if o.firstReviewVerdict == "" {
				o.firstReviewVerdict = scorecardLabel(event.Review.Verdict)
			}
			for _, finding := range event.Review.Findings {
				incrementBounded(o.severities, scorecardLabel(finding.Severity), 16)
			}
			if event.Review.Verdict == plan.ReviewVerdictChangesRequested {
				o.reliability["changes_requested"]++
			}
			if event.Review.IsApproved() && !approvalSeen {
				approvalSeen = true
				approvalAt = event.Timestamp
			}
		case plan.EventTypeVerificationRepairCreated:
			o.verificationRepairs++
		case plan.EventTypeReworkStopped:
			o.reworkStops++
		case plan.EventTypeFinalVerification:
			if event.Result == "failed" {
				switch event.FailureKind {
				case plan.FinalVerificationFailureKindCode, plan.FinalVerificationFailureKindToolMissing,
					plan.FinalVerificationFailureKindTimeout, plan.FinalVerificationFailureKindCancelled,
					plan.FinalVerificationFailureKindInvalidCommand:
					o.reliability["final_verification_"+string(event.FailureKind)]++
				}
			}
		case plan.EventTypeFinalizationFailed:
			category := "unknown"
			if event.FinalizationFailure != nil {
				category = scorecardLabel(event.FinalizationFailure.Category)
			}
			incrementBounded(o.reliability, "finalization_"+category, scorecardDynamicReliabilityKeys)
		case plan.EventTypeBudgetExceeded:
			o.reliability["budget_exceeded"]++
		}
	}
	// Exact means a recorded base/head approval, not validation against live Git.
	o.exactApproval = summary.Reviewed && summary.ReviewVerdict == plan.ReviewVerdictApprove && currentReview.IsApproved() && strings.TrimSpace(currentReview.Base) != "" && strings.TrimSpace(currentReview.Head) != ""
	if !start.IsZero() && !approvalAt.IsZero() && !approvalAt.Before(start) {
		o.hasApproval = true
		o.hoursToApproval = approvalAt.Sub(start).Hours()
	}
	for _, reason := range data.blockedReasons {
		incrementBounded(o.reliability, "blocked_"+scorecardLabel(reason.category), scorecardDynamicReliabilityKeys)
	}
	for _, signal := range data.signals {
		switch signal.typeName {
		case plan.EventTypeSessionTimeout, plan.EventTypeSliceResumeFailed, plan.EventTypeVerificationCommandInvalid:
			o.reliability[signal.typeName]++
		}
	}
	o.observeMetrics(data)
	o.pullRequestCompleted = summary.Complete && !o.merged && summary.PullRequest != nil
	switch {
	case summary.Status == plan.StatusCompleted || summary.Status == plan.StatusAbandoned || o.merged || o.pullRequestCompleted:
		o.class = observationTerminal
	case !o.started && summary.Status == plan.StatusPlanned:
		o.class = observationNeverStarted
	default:
		o.class = observationActive
	}
	// Unlike routing's terminal-only maturity, scorecard maturity also admits
	// inactive history after a fixed window. Compare against one UTC day boundary.
	now = now.UTC().Truncate(24 * time.Hour)
	o.matured = o.class == observationTerminal || (summary.LastActivityAt != nil && now.Sub(*summary.LastActivityAt) >= scorecardMaturityWindow)
	return o
}

func (o *planObservation) observeMetrics(data planData) {
	keys := make([]string, 0, len(data.sessions))
	for key := range data.sessions {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		byRole := make(map[string][]plan.AgentMetricEvent)
		for _, event := range data.sessions[key] {
			role := scorecardMetricRole(event.Metrics.Role)
			byRole[role] = append(byRole[role], event)
			if event.Metrics.Status == "failed" {
				o.reliability["provider_failure"]++
			}
		}
		for role, events := range byRole {
			metrics := plan.SummarizeAgentMetrics(events).Totals
			totals := o.roles[role]
			// The scanner gives anonymous events distinct session keys. Repeated
			// metrics for a named session contribute usage but only one session/role.
			totals.sessions++
			totals.outputTokens += metrics.OutputTokens
			totals.totalTokens += metrics.TotalTokens
			totals.cost += metrics.Cost
			totals.toolCalls += metrics.ToolCalls
			totals.outputPresent = totals.outputPresent || metrics.OutputTokensPresent
			totals.totalPresent = totals.totalPresent || metrics.TotalTokensPresent
			totals.costPresent = totals.costPresent || metrics.CostPresent
			totals.toolCallsPresent = totals.toolCallsPresent || data.toolCallsPresent[role] || metrics.ToolCalls != 0
			totals.availability.Reported += metrics.Availability.Reported
			totals.availability.Partial += metrics.Availability.Partial
			totals.availability.Unavailable += metrics.Availability.Unavailable
			totals.availability.Unknown += metrics.Availability.Unknown
			o.roles[role] = totals
		}
	}
}

func scorecardMetricRole(role plan.AgentRole) string {
	if role == "" {
		return "unattributed"
	}
	return string(role.Normalized())
}

func scorecardLabel(value string) string {
	if value = sanitizeTreatmentLabel(value); value != "" {
		return value
	}
	return "unknown"
}

// Reserve one slot for overflow, including when "other" is itself recorded.
func incrementBounded(histogram map[string]int, key string, limit int) {
	if _, exists := histogram[key]; !exists && len(histogram) >= limit-1 {
		key = "other"
	}
	histogram[key]++
}

func buildScorecard(observations []planObservation) Scorecard {
	scorecard := Scorecard{Coverage: scorecardCoverage(observations)}
	var included []planObservation
	excluded := make(map[ExcludedLabel]int)
	keys := make(map[string]string)
	for _, o := range observations {
		key := o.treatment.CohortKey()
		if key == "" {
			reason := "ambiguous"
			if o.treatment.Source == "none" {
				reason = "missing"
			}
			excluded[ExcludedLabel{Label: o.treatment.Label, Reason: reason}]++
			continue
		}
		keys[key] = key
		included = append(included, o)
	}
	for label, count := range excluded {
		label.Plans = count
		scorecard.ExcludedLabels = append(scorecard.ExcludedLabels, label)
	}
	slices.SortFunc(scorecard.ExcludedLabels, func(a, b ExcludedLabel) int {
		return cmp.Or(cmp.Compare(b.Plans, a.Plans), cmp.Compare(a.Label, b.Label), cmp.Compare(a.Reason, b.Reason))
	})
	// Use the same overflow membership globally and locally: otherwise an
	// inversion could compare different treatments under the same "other" key.
	if len(keys) > 32 {
		for _, key := range slices.Sorted(maps.Keys(keys))[31:] {
			keys[key] = "other"
		}
	}
	cohorts := func(group []planObservation) []TreatmentCohort {
		groups := groupScorecardObservations(group, func(o planObservation) string { return keys[o.treatment.CohortKey()] })
		var result []TreatmentCohort
		for _, key := range slices.Sorted(maps.Keys(groups)) {
			result = append(result, buildTreatmentCohort(key, groups[key]))
		}
		return result
	}
	scorecard.Cohorts = cohorts(included)
	for _, stratum := range []struct {
		name string
		key  func(planObservation) string
	}{
		{"repository", func(o planObservation) string { return o.repositoryKey }},
		{"effort", func(o planObservation) string { return o.effort }},
		{"risk", func(o planObservation) string { return o.risk }},
		{"change_type", func(o planObservation) string { return o.changeType }},
		{"era", func(o planObservation) string { return o.era }},
	} {
		groups := groupScorecardObservations(included, stratum.key)
		for _, key := range slices.Sorted(maps.Keys(groups)) {
			scorecard.Strata = append(scorecard.Strata, StratumBreakdown{Stratum: stratum.name, Key: key, Cohorts: cohorts(groups[key])})
		}
	}
	scorecard.Inversions = scorecardInversions(scorecard)
	return scorecard
}

// Keep the lexical prefix and reserve a slot for overflow. Real "other"
// observations join overflow rather than taking another slot or being lost.
func groupScorecardObservations(observations []planObservation, key func(planObservation) string) map[string][]planObservation {
	groups := make(map[string][]planObservation)
	for _, o := range observations {
		k := key(o)
		groups[k] = append(groups[k], o)
	}
	if len(groups) > 32 {
		keys := slices.Sorted(maps.Keys(groups))
		keys = slices.DeleteFunc(keys, func(k string) bool { return k == "other" })
		for _, k := range keys[31:] {
			groups["other"] = append(groups["other"], groups[k]...)
			delete(groups, k)
		}
	}
	return groups
}

func buildTreatmentCohort(key string, observations []planObservation) TreatmentCohort {
	t := observations[0].treatment
	cohort := TreatmentCohort{Key: key, Runtime: t.Runtime, Provider: t.Provider, Model: t.Model, Confidence: t.Confidence}
	for _, o := range observations {
		// A key may span confidence levels or provider identities. Do not
		// arbitrarily display the first observation as authoritative evidence.
		if cohort.Runtime != o.treatment.Runtime {
			cohort.Runtime = ""
		}
		if cohort.Provider != o.treatment.Provider {
			cohort.Provider = ""
		}
		if cohort.Model != o.treatment.Model {
			cohort.Model = ""
		}
		if o.treatment.Confidence == TreatmentConfidenceLow {
			cohort.Confidence = TreatmentConfidenceLow
		}
	}
	if key == "other" {
		cohort.Runtime, cohort.Provider, cohort.Model = "", "", ""
		cohort.Confidence = TreatmentConfidenceAmbiguous
	}
	cohort.Outcomes = buildCohortOutcomes(observations)
	return cohort
}

func buildCohortOutcomes(observations []planObservation) CohortOutcomes {
	out := CohortOutcomes{Plans: len(observations)}
	q := &out.Quality
	var matured, completed []planObservation
	var rounds, original []float64
	severities, infrastructure, quality := make(map[string]int), make(map[string]int), make(map[string]int)
	for _, o := range observations {
		if o.class == observationNeverStarted {
			out.NeverStarted++
		}
		if o.started {
			q.Started.Numerator++
		}
		if o.validationWarnings > 0 {
			q.ValidationWarningPlans++
		}
		for label, count := range o.severities {
			severities[label] += count
		}
		// Histograms retain all observed evidence (including censored plans).
		// Operational failures never offset code/review failures.
		for label, count := range o.reliability {
			switch label {
			case "changes_requested", "final_verification_code":
				quality[label] += count
			default:
				infrastructure[label] += count
			}
		}
		quality["rework_stopped"] += o.reworkStops
		quality["verification_repair_created"] += o.verificationRepairs
		if !o.matured {
			out.Censored++
			continue
		}
		out.Matured++
		matured = append(matured, o)
		if o.completed {
			completed = append(completed, o)
		}
		rounds = append(rounds, float64(o.reviewRounds))
		original = append(original, float64(o.originalSlices))
		for _, item := range []struct {
			yes  bool
			rate *RateEstimate
		}{
			{o.firstReviewVerdict == plan.ReviewVerdictApprove, &q.FirstReviewApproved},
			{o.exactApproval, &q.ExactApproval}, {o.completed, &q.Completed},
			{o.merged, &q.Merged}, {o.pullRequestCompleted, &q.PullRequestCompleted},
			{o.abandoned, &q.Abandoned}, {o.reworkStops > 0, &q.ReworkStopped},
			{o.reworkSlices > 0, &q.AnyRework}, {o.verificationRepairs > 0, &q.AnyVerificationRepair},
		} {
			if item.yes {
				item.rate.Numerator++
			}
		}
	}
	q.Started = EstimateRate(q.Started.Numerator, out.Plans, scorecardMinimumSamples)
	for _, rate := range []*RateEstimate{&q.FirstReviewApproved, &q.ExactApproval, &q.Completed, &q.Merged, &q.PullRequestCompleted, &q.Abandoned, &q.ReworkStopped, &q.AnyRework, &q.AnyVerificationRepair} {
		*rate = EstimateRate(rate.Numerator, out.Matured, scorecardMinimumSamples)
	}
	q.ReviewRounds = EstimateMedian(rounds, scorecardMinimumSamples)
	q.OriginalSlices = EstimateMedian(original, scorecardMinimumSamples)
	q.FindingSeverities = scorecardHistogram(severities)
	out.Reliability = ReliabilityOutcomes{Infrastructure: scorecardHistogram(infrastructure), Quality: scorecardHistogram(quality)}
	out.Efficiency.MaturedAll = efficiencyMedians(matured)
	out.Efficiency.MaturedCompleted = efficiencyMedians(completed)
	out.Efficiency.ByRole, out.Efficiency.RoleAttributedRatio = scorecardRoleTotals(observations)
	return out
}

func efficiencyMedians(observations []planObservation) EfficiencyMedians {
	result := EfficiencyMedians{Plans: len(observations)}
	var sessions, outputs, tokens, costs, calls, sliceCosts, sliceOutputs, hours, planningCosts, planningOutputs []float64
	for _, o := range observations {
		var total roleTotals
		// Stable role order avoids floating-point variation from map iteration.
		for _, role := range slices.Sorted(maps.Keys(o.roles)) {
			r := o.roles[role]
			total.sessions += r.sessions
			total.outputTokens += r.outputTokens
			total.totalTokens += r.totalTokens
			total.cost += r.cost
			total.toolCalls += r.toolCalls
			total.outputPresent = total.outputPresent || r.outputPresent
			total.totalPresent = total.totalPresent || r.totalPresent
			total.costPresent = total.costPresent || r.costPresent
			total.toolCallsPresent = total.toolCallsPresent || r.toolCallsPresent
		}
		if total.sessions > 0 {
			sessions = append(sessions, float64(total.sessions))
		}
		if total.outputPresent {
			outputs = append(outputs, float64(total.outputTokens))
			if o.originalSlices > 0 {
				sliceOutputs = append(sliceOutputs, float64(total.outputTokens)/float64(o.originalSlices))
			}
		}
		if total.totalPresent {
			tokens = append(tokens, float64(total.totalTokens))
		}
		if total.costPresent {
			costs = append(costs, total.cost)
			if o.originalSlices > 0 {
				sliceCosts = append(sliceCosts, total.cost/float64(o.originalSlices))
			}
		}
		if total.toolCallsPresent {
			calls = append(calls, float64(total.toolCalls))
		}
		if o.hasApproval {
			hours = append(hours, o.hoursToApproval)
		}
		planning := o.roles["planning"]
		if planning.costPresent {
			planningCosts = append(planningCosts, planning.cost)
		}
		if planning.outputPresent {
			planningOutputs = append(planningOutputs, float64(planning.outputTokens))
		}
	}
	for _, metric := range []struct {
		values []float64
		result *MedianEstimate
	}{
		{sessions, &result.Sessions}, {outputs, &result.OutputTokens}, {tokens, &result.TotalTokens},
		{costs, &result.Cost}, {calls, &result.ToolCalls}, {sliceCosts, &result.CostPerOriginalSlice},
		{sliceOutputs, &result.OutputTokensPerOriginalSlice}, {hours, &result.HoursToApproval},
		{planningCosts, &result.PlanningCost}, {planningOutputs, &result.PlanningOutputTokens},
	} {
		*metric.result = EstimateMedian(metric.values, scorecardMinimumSamples)
	}
	return result
}

func scorecardRoleTotals(observations []planObservation) ([]RoleTotals, float64) {
	totals := make(map[string]RoleTotals)
	costs := make(map[string][]float64)
	for _, o := range observations {
		for role, r := range o.roles {
			total := totals[role]
			total.Role = role
			total.Sessions += r.sessions
			total.OutputTokens += r.outputTokens
			total.TotalTokens += r.totalTokens
			total.ToolCalls += r.toolCalls
			totals[role] = total
			costs[role] = append(costs[role], r.cost)
		}
	}
	var result []RoleTotals
	var sessions, attributed int
	for role, total := range totals {
		// Sorted summation makes totals independent of source/plan order.
		slices.Sort(costs[role])
		for _, cost := range costs[role] {
			total.Cost += cost
		}
		result = append(result, total)
		sessions += total.Sessions
		if role != "unattributed" {
			attributed += total.Sessions
		}
	}
	slices.SortFunc(result, func(a, b RoleTotals) int {
		if a.Role == "unattributed" && b.Role != "unattributed" {
			return 1
		}
		if b.Role == "unattributed" && a.Role != "unattributed" {
			return -1
		}
		return cmp.Compare(a.Role, b.Role)
	})
	ratio := 0.0
	if sessions > 0 {
		ratio = float64(attributed) / float64(sessions)
	}
	return result, ratio
}

func scorecardHistogram(counts map[string]int) []LabelCount {
	var result []LabelCount
	for label, count := range counts {
		if count > 0 {
			result = append(result, LabelCount{Label: label, Count: count})
		}
	}
	less := func(a, b LabelCount) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Label, b.Label))
	}
	slices.SortFunc(result, less)
	if len(result) > 16 {
		other := LabelCount{Label: "other"}
		result = slices.DeleteFunc(result, func(item LabelCount) bool {
			if item.Label == "other" {
				other.Count += item.Count
				return true
			}
			return false
		})
		for _, item := range result[15:] {
			other.Count += item.Count
		}
		result = append(result[:15], other)
		slices.SortFunc(result, less)
	}
	return result
}

func scorecardInversions(scorecard Scorecard) []Inversion {
	var result []Inversion
	for _, metric := range []struct {
		name  string
		point func(CohortOutcomes) ComparisonPoint
	}{
		{"completed_rate", func(o CohortOutcomes) ComparisonPoint {
			return ComparisonPoint{Value: o.Quality.Completed.Rate, Samples: o.Quality.Completed.Denominator}
		}},
		{"exact_approval_rate", func(o CohortOutcomes) ComparisonPoint {
			return ComparisonPoint{Value: o.Quality.ExactApproval.Rate, Samples: o.Quality.ExactApproval.Denominator}
		}},
		{"median_cost_matured_completed", func(o CohortOutcomes) ComparisonPoint {
			return ComparisonPoint{Value: o.Efficiency.MaturedCompleted.Cost.Median, Samples: o.Efficiency.MaturedCompleted.Cost.Samples}
		}},
		{"median_hours_to_approval", func(o CohortOutcomes) ComparisonPoint {
			return ComparisonPoint{Value: o.Efficiency.MaturedAll.HoursToApproval.Median, Samples: o.Efficiency.MaturedAll.HoursToApproval.Samples}
		}},
		{"rework_stopped_rate", func(o CohortOutcomes) ComparisonPoint {
			return ComparisonPoint{Value: o.Quality.ReworkStopped.Rate, Samples: o.Quality.ReworkStopped.Denominator}
		}},
	} {
		points := func(cohorts []TreatmentCohort) []ComparisonPoint {
			var values []ComparisonPoint
			for _, cohort := range cohorts {
				point := metric.point(cohort.Outcomes)
				point.Cohort = cohort.Key
				values = append(values, point)
			}
			return values
		}
		var strata []StratumComparison
		for _, stratum := range scorecard.Strata {
			strata = append(strata, StratumComparison{Stratum: stratum.Stratum, Key: stratum.Key, Points: points(stratum.Cohorts)})
		}
		result = append(result, DetectInversions(metric.name, points(scorecard.Cohorts), strata, scorecardMinimumSamples)...)
	}
	return result
}

func scorecardCoverage(observations []planObservation) ScorecardCoverage {
	c := ScorecardCoverage{MaturityWindowDays: int(scorecardMaturityWindow / (24 * time.Hour)), MinimumSamples: scorecardMinimumSamples}
	for _, o := range observations {
		c.Plans++
		switch o.class {
		case observationNeverStarted:
			c.NeverStarted++
		case observationActive:
			c.Active++
		case observationTerminal:
			c.Terminal++
		}
		if o.matured {
			c.Matured++
		} else {
			c.Censored++
		}
		switch {
		case o.treatment.Source == "none":
			c.TreatmentMissing++
		case o.treatment.Confidence == TreatmentConfidenceHigh:
			c.TreatmentHigh++
		case o.treatment.Confidence == TreatmentConfidenceLow:
			c.TreatmentLow++
		default:
			c.TreatmentAmbiguous++
		}
		if o.planningMetrics {
			c.PlanningMetricsPlans++
		}
		for role, totals := range o.roles {
			if role == "unattributed" {
				c.UnattributedSessions += totals.sessions
			} else {
				c.RoleAttributedSessions += totals.sessions
			}
		}
	}
	return c
}
