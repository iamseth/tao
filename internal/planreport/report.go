package planreport

import (
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

const SchemaV1 = "tao.plan-report.v1"

type Mode string

const (
	ModeFull         Mode = "full"
	ModePlanningOnly Mode = "planning-only"

	sectionIdentity  Section = "identity"
	sectionPlanning  Section = "planning_context"
	sectionSlices    Section = "slices"
	sectionExecution Section = "execution"
	sectionReview    Section = "review"
)

// OptionalText distinguishes absent source material from present, sanitized
// prose. Text can only have been constructed by this package's Sanitizer.
type OptionalText struct {
	Available bool
	Text      SafeText
}

type PlanningContext struct {
	Goal        OptionalText
	Constraints []SafeText
	NonGoals    []SafeText
	Decisions   []SafeText
	Risks       []SafeText
	Questions   []SafeText
}

// PlanningEffortSummary contains only valid aggregate legacy planning metrics.
// It deliberately excludes prompt, agent, provider, model, and session data.
type PlanningEffortSummary struct {
	Available     bool
	Duration      DurationSummary
	TotalTokens   OptionalInt64
	TotalMessages OptionalInt64
}

// PlannedSlice contains only immutable planning inputs. In particular, it has
// no lifecycle, verification, timing, result, telemetry, or attribution field.
type PlannedSlice struct {
	Title        SafeText
	Goal         OptionalText
	Rationale    OptionalText
	Dependencies []SafeText
}

// PlanningOnlyReport is deliberately a separate production type, rather than a
// filtered FullReport, so execution-derived data cannot enter this mode.
type PlanningOnlyReport struct {
	Schema         string
	Mode           Mode
	SnapshotAt     time.Time
	PlanID         SafeText
	Title          SafeText
	Synthesized    bool
	Planning       PlanningContext
	PlanningEffort PlanningEffortSummary
	Slices         []PlannedSlice
	Disclosures    []Disclosure
}

type CountSummary struct {
	Total, Passed, Failed, Other int
}

type DurationSummary struct {
	Available bool
	Seconds   int64
}

type SliceReport struct {
	Title        SafeText
	Goal         OptionalText
	Rationale    OptionalText
	Dependencies []SafeText
	Status       string
	Rework       bool
	Duration     DurationSummary
	Verification CountSummary
	TotalTokens  OptionalInt64
	Commit       SliceCommitSummary
}

// SliceCommitSummary distinguishes created commits from other completion
// outcomes. SHA is populated only for a valid committed outcome.
type SliceCommitSummary struct {
	Outcome string
	SHA     OptionalText
}

type FinalVerificationSummary struct {
	Available bool
	Result    OptionalText
}

type ReviewSummary struct {
	Available    bool
	Status       string
	Verdict      string
	Summary      OptionalText
	FindingCount int
}

type OutcomeSummary struct {
	Merged bool
}

// FinalizationSummary is an explicit share-safe projection. It excludes exact
// review ranges, branches, heads, forge data, and raw failure text.
type FinalizationSummary struct {
	Available bool
	Phase     string
	Category  OptionalText
	FailedAt  time.Time
	Action    OptionalText
}

type OptionalInt64 struct {
	Available bool
	Value     int64
}

type OptionalFloat64 struct {
	Available bool
	Value     float64
}

// TelemetrySummary contains aggregates only. It intentionally excludes agent,
// provider, model, session, event, and slice identities.
type TelemetrySummary struct {
	Available       bool
	Attempts        int
	FailedAttempts  int
	Sessions        OptionalInt64
	AgentCount      OptionalInt64
	InputTokens     OptionalInt64
	OutputTokens    OptionalInt64
	ReasoningTokens OptionalInt64
	TotalTokens     OptionalInt64
	Cost            OptionalFloat64
	Messages        OptionalInt64
	ErroredMessages OptionalInt64
	ToolCalls       OptionalInt64
}

type ExecutionSummary struct {
	Duration          DurationSummary
	CompletedSlices   int
	PendingSlices     int
	Verification      CountSummary
	FinalVerification FinalVerificationSummary
	Telemetry         TelemetrySummary
}

type FullReport struct {
	Schema         string
	Mode           Mode
	SnapshotAt     time.Time
	PlanID         SafeText
	Title          SafeText
	Status         string
	Planning       PlanningContext
	PlanningEffort PlanningEffortSummary
	Slices         []SliceReport
	Execution      ExecutionSummary
	Review         ReviewSummary
	Finalization   FinalizationSummary
	Outcome        OutcomeSummary
	Disclosures    []Disclosure
}

func ProjectPlanningOnly(detail *plan.PlanDetail, snapshotAt time.Time) PlanningOnlyReport {
	s := NewSanitizer(0)
	report := PlanningOnlyReport{Schema: SchemaV1, Mode: ModePlanningOnly, SnapshotAt: snapshotAt.UTC(), Synthesized: true}
	if detail == nil {
		report.Disclosures = s.Disclosures()
		return report
	}
	report.PlanID = s.Sanitize(sectionIdentity, detail.State.Plan.ID)
	report.Title = s.Sanitize(sectionIdentity, detail.State.Plan.Title)
	report.Planning = projectPlanning(s, detail)
	report.PlanningEffort = projectPlanningEffort(detail)
	for _, source := range detail.Slices.Slices {
		if plan.IsReworkSliceID(source.ID) {
			continue
		}
		report.Slices = append(report.Slices, projectPlannedSlice(s, source, detail.Slices.Slices))
	}
	report.Disclosures = s.Disclosures()
	return report
}

func ProjectFull(detail *plan.PlanDetail, snapshotAt time.Time) FullReport {
	s := NewSanitizer(0)
	report := FullReport{Schema: SchemaV1, Mode: ModeFull, SnapshotAt: snapshotAt.UTC()}
	if detail == nil {
		report.Disclosures = s.Disclosures()
		return report
	}
	report.PlanID = s.Sanitize(sectionIdentity, detail.State.Plan.ID)
	report.Title = s.Sanitize(sectionIdentity, detail.State.Plan.Title)
	lifecycleStatus := plan.PlanLifecycleStatus(detail)
	report.Status = knownStatus(lifecycleStatus)
	report.Planning = projectPlanning(s, detail)
	report.PlanningEffort = projectPlanningEffort(detail)
	for _, source := range detail.Slices.Slices {
		report.Slices = append(report.Slices, projectFullSlice(s, source, detail.Slices.Slices, detail.Events, snapshotAt))
	}
	derived := plan.Derive(detail, snapshotAt)
	report.Execution = ExecutionSummary{
		Duration:          durationFrom(derived.Elapsed, detail.State.Plan.Timing.StartedAt != nil),
		CompletedSlices:   derived.CompletedCount,
		PendingSlices:     derived.PendingCount,
		Verification:      verificationTotals(detail.Slices.Slices),
		FinalVerification: projectFinalVerification(s, detail.State.Plan.FinalVerification),
		Telemetry:         projectTelemetry(detail.Events),
	}
	if review := plan.CurrentReview(detail); review != nil {
		report.Review = ReviewSummary{Available: true, Status: knownReviewStatus(review.Status), Verdict: knownVerdict(review.Verdict), Summary: optional(s, sectionReview, review.Summary), FindingCount: nonNegative(review.FindingsCount)}
	}
	if recovery := derived.FinalizationRecovery; recovery != nil {
		action := derived.NextAction.Primary.Command
		if action == "" {
			action = derived.NextAction.Primary.Instruction
		}
		report.Finalization = FinalizationSummary{
			Available: true, Phase: knownFinalizationPhase(recovery.Phase), FailedAt: recovery.FailedAt.UTC(),
			Category: optional(s, sectionExecution, recovery.Category), Action: optional(s, sectionExecution, action),
		}
	}
	// Current merge evidence is authoritative. Infer a merged outcome from a
	// completed lifecycle only for legacy records without qualifying PR,
	// pending PR intent, or finalization-recovery evidence.
	report.Outcome.Merged = plan.PlanIsMerged(detail.Events) ||
		(lifecycleStatus == plan.StatusCompleted && !plan.PlanIsPullRequestComplete(detail) && detail.State.Plan.PullRequestIntent == nil && derived.FinalizationRecovery == nil)
	report.Disclosures = s.Disclosures()
	return report
}

func projectPlanning(s *Sanitizer, detail *plan.PlanDetail) PlanningContext {
	brief := markdownSections(detail.PlanningBrief.Content)
	narrative := markdownSections(detail.PlanNarrative.Content)
	goal := firstSection(brief, narrative, "user goal", "goal")
	if goal == "" {
		for _, candidate := range detail.Slices.Slices {
			if !plan.IsReworkSliceID(candidate.ID) && strings.TrimSpace(candidate.Goal) != "" {
				goal = candidate.Goal
				break
			}
		}
	}
	constraints := sectionValues(firstSection(brief, narrative, "constraints"))
	if len(constraints) == 0 {
		constraints = detail.State.GlobalInvariants
	}
	questions := sectionValues(firstSection(brief, narrative, "open questions", "questions"))
	if len(questions) == 0 {
		questions = detail.State.OpenQuestions
	}
	return PlanningContext{
		Goal:        optional(s, sectionPlanning, goal),
		Constraints: sanitizePlanningValues(s, constraints),
		NonGoals:    sanitizePlanningValues(s, sectionValues(firstSection(brief, narrative, "non-goals", "non goals"))),
		Decisions:   sanitizePlanningValues(s, sectionValues(firstSection(brief, narrative, "decisions"))),
		Risks:       sanitizePlanningValues(s, sectionValues(firstSection(brief, narrative, "risks"))),
		Questions:   sanitizePlanningValues(s, questions),
	}
}

func projectPlannedSlice(s *Sanitizer, source plan.Slice, all []plan.Slice) PlannedSlice {
	out := PlannedSlice{Title: s.Sanitize(sectionSlices, source.Title), Goal: optional(s, sectionSlices, source.Goal), Rationale: optional(s, sectionSlices, source.Context)}
	for _, dependency := range source.DependsOn {
		if title := originalSliceTitle(dependency, all); title != "" {
			out.Dependencies = append(out.Dependencies, s.Sanitize(sectionSlices, title))
		} else {
			out.Dependencies = append(out.Dependencies, s.Sanitize(sectionSlices, dependency))
		}
	}
	return out
}

func projectFullSlice(s *Sanitizer, source plan.Slice, all []plan.Slice, events []plan.Event, now time.Time) SliceReport {
	planned := projectPlannedSlice(s, source, all)
	return SliceReport{
		Title: planned.Title, Goal: planned.Goal, Rationale: planned.Rationale, Dependencies: planned.Dependencies,
		Status: knownStatus(source.Status), Rework: plan.IsReworkSliceID(source.ID), Duration: sliceDuration(source, now),
		Verification: verificationCount(source.VerificationResults), TotalTokens: sliceTotalTokens(source.ID, events),
		Commit: projectSliceCommit(s, source),
	}
}

func projectPlanningEffort(detail *plan.PlanDetail) PlanningEffortSummary {
	summary := plan.SummarizePlanningSessionMetrics(detail.PlanningSession.Stats, detail.State.CreatedAt)
	if !summary.Valid {
		return PlanningEffortSummary{}
	}
	out := PlanningEffortSummary{Available: true, Duration: durationFrom(summary.Duration, true)}
	addMetric(&out.TotalTokens, summary.TotalTokens)
	addMetric(&out.TotalMessages, summary.TotalMessages)
	return out
}

func sliceTotalTokens(sliceID string, events []plan.Event) OptionalInt64 {
	var out OptionalInt64
	for _, event := range plan.AgentMetricsEvents(events) {
		if event.SliceID == sliceID && event.Metrics.TotalTokens >= 0 && (event.Metrics.TotalTokensPresent || event.Metrics.TotalTokens > 0) {
			out.Available = true
			out.Value += event.Metrics.TotalTokens
		}
	}
	return out
}

func projectSliceCommit(s *Sanitizer, source plan.Slice) SliceCommitSummary {
	if source.Completion == nil {
		if source.Status == plan.StatusCompleted {
			return SliceCommitSummary{Outcome: "legacy"}
		}
		return SliceCommitSummary{Outcome: "not_recorded"}
	}
	out := SliceCommitSummary{Outcome: source.Completion.Outcome}
	switch source.Completion.Outcome {
	case plan.SliceCompletionCommitted:
		if sha := abbreviatedCommitSHA(source.Completion.CommitSHA); sha != "" {
			out.SHA = optional(s, sectionSlices, sha)
		}
	case plan.SliceCompletionNoChanges, plan.SliceCompletionManualUncommitted:
		// These outcomes did not create a slice commit, even if malformed legacy
		// metadata happens to contain a SHA.
	default:
		out.Outcome = "unknown"
	}
	return out
}

func abbreviatedCommitSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 7 || len(value) > 64 {
		return ""
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return ""
		}
	}
	return value[:7]
}

// Dependencies are IDs in the artifact. This helper exists to make the
// projection point explicit; IDs are sanitized and never used to reach paths.
func originalSliceTitle(id string, all []plan.Slice) string {
	for _, candidate := range all {
		if candidate.ID == id && !plan.IsReworkSliceID(candidate.ID) {
			return candidate.Title
		}
	}
	return ""
}

func projectFinalVerification(s *Sanitizer, source *plan.FinalVerification) FinalVerificationSummary {
	if source == nil {
		return FinalVerificationSummary{}
	}
	return FinalVerificationSummary{Available: true, Result: optional(s, sectionExecution, source.Result)}
}

func projectTelemetry(events []plan.Event) TelemetrySummary {
	metrics := plan.AgentMetricsEvents(events)
	if len(metrics) == 0 {
		return TelemetrySummary{}
	}
	out := TelemetrySummary{Available: true, Attempts: len(metrics)}
	sessions, agents := map[string]struct{}{}, map[string]struct{}{}
	for _, event := range metrics {
		m := event.Metrics
		if m.Status != "" && m.Status != plan.StatusCompleted || m.Result != "" && m.Result != plan.StatusCompleted {
			out.FailedAttempts++
		}
		if m.SessionID != "" {
			sessions[m.SessionID] = struct{}{}
		}
		if m.Agent != "" {
			agents[m.Agent] = struct{}{}
		}
		addMetric(&out.InputTokens, m.InputTokens)
		addMetric(&out.OutputTokens, m.OutputTokens)
		addMetric(&out.ReasoningTokens, m.ReasoningTokens)
		addMetric(&out.TotalTokens, m.TotalTokens)
		addMetric(&out.Messages, m.TotalMessages)
		addMetric(&out.ErroredMessages, m.ErroredMessages)
		addMetric(&out.ToolCalls, m.ToolCalls)
		if m.Cost > 0 {
			out.Cost.Available = true
			out.Cost.Value += m.Cost
		}
	}
	if len(sessions) > 0 {
		out.Sessions = OptionalInt64{Available: true, Value: int64(len(sessions))}
	}
	if len(agents) > 0 {
		out.AgentCount = OptionalInt64{Available: true, Value: int64(len(agents))}
	}
	return out
}

func addMetric(target *OptionalInt64, value int64) {
	if value > 0 {
		target.Available = true
		target.Value += value
	}
}

func verificationTotals(slices []plan.Slice) CountSummary {
	var out CountSummary
	for _, slice := range slices {
		addCounts(&out, verificationCount(slice.VerificationResults))
	}
	return out
}

func verificationCount(runs []plan.VerificationRun) CountSummary {
	out := CountSummary{Total: len(runs)}
	for _, run := range runs {
		switch strings.ToLower(strings.TrimSpace(run.Result)) {
		case "pass", "passed", "success", "succeeded", "ok":
			out.Passed++
		case "fail", "failed", "error":
			out.Failed++
		default:
			out.Other++
		}
	}
	return out
}

func addCounts(target *CountSummary, source CountSummary) {
	target.Total += source.Total
	target.Passed += source.Passed
	target.Failed += source.Failed
	target.Other += source.Other
}

func sliceDuration(source plan.Slice, now time.Time) DurationSummary {
	available := source.Timing.DurationSeconds != nil || source.Timing.StartedAt != nil
	return durationFrom(plan.SliceDuration(source, now), available)
}

func durationFrom(value time.Duration, available bool) DurationSummary {
	if !available || value < 0 {
		return DurationSummary{}
	}
	return DurationSummary{Available: true, Seconds: int64(value.Round(time.Second) / time.Second)}
}

func optional(s *Sanitizer, section Section, value string) OptionalText {
	if strings.TrimSpace(value) == "" {
		return OptionalText{}
	}
	text := s.Sanitize(section, value)
	if text.text == "" {
		return OptionalText{}
	}
	return OptionalText{Available: true, Text: text}
}

func sanitizePlanningValues(s *Sanitizer, values []string) []SafeText {
	out := make([]SafeText, 0, len(values))
	for _, value := range values {
		if safe := optional(s, sectionPlanning, value); safe.Available {
			out = append(out, safe.Text)
		}
	}
	return out
}

func sectionValues(value string) []string {
	var out []string
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(line, "-"), "*"), "+"))
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// markdownSections recognizes only ordinary ATX headings. Setext headings, raw
// HTML, malformed markers, and nested content outside known headings are ignored.
func markdownSections(source string) map[string]string {
	sections := make(map[string]string)
	var current string
	var body []string
	flush := func() {
		if current != "" {
			sections[current] = strings.TrimSpace(strings.Join(body, "\n"))
		}
		body = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(source, "\r", ""), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") && !strings.HasPrefix(trimmed, "### ") {
			flush()
			current = strings.ToLower(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, "## "), "#")))
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			flush()
			current = ""
			continue
		}
		if current != "" {
			body = append(body, line)
		}
	}
	flush()
	return sections
}

func firstSection(primary, secondary map[string]string, names ...string) string {
	for _, name := range names {
		if value := primary[name]; value != "" {
			return value
		}
	}
	for _, name := range names {
		if value := secondary[name]; value != "" {
			return value
		}
	}
	return ""
}

func knownStatus(value string) string {
	switch value {
	case plan.StatusPlanned, plan.StatusPending, plan.StatusInProgress, plan.StatusInReview, plan.StatusReviewed, plan.StatusChangesRequested, plan.StatusCompleted, plan.StatusSkipped, plan.StatusBlocked, plan.StatusInvalid:
		return value
	default:
		return "unavailable"
	}
}

func knownFinalizationPhase(value plan.FinalizationFailurePhase) string {
	switch value {
	case plan.FinalizationFailurePhaseProposalRepair, plan.FinalizationFailurePhasePullRequest:
		return string(value)
	default:
		return "unavailable"
	}
}

func knownReviewStatus(value string) string {
	switch value {
	case plan.ReviewStatusCompleted, plan.ReviewStatusError:
		return value
	default:
		return "not_recorded"
	}
}
func knownVerdict(value string) string {
	switch value {
	case plan.ReviewVerdictApprove, plan.ReviewVerdictChangesRequested, plan.ReviewVerdictComment:
		return value
	default:
		return "not_recorded"
	}
}
func nonNegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}
