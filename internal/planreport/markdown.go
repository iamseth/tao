package planreport

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var reportHeadings = map[string]struct{}{
	"# Plan Report":               {},
	"## Executive Summary":        {},
	"## Planning Context":         {},
	"## Slice Overview":           {},
	"## Planned Slices":           {},
	"## Execution Summary":        {},
	"## Review and Outcome":       {},
	"## Redactions and Omissions": {},
}

type markdownBuilder struct {
	lines []string
}

// RenderFull renders a full safe projection using the fixed v1 Markdown schema.
func RenderFull(report FullReport) ([]byte, error) {
	var b markdownBuilder
	b.heading("# Plan Report")
	b.heading("## Executive Summary")
	b.item("Schema", SchemaV1)
	b.item("Mode", string(ModeFull))
	b.item("Snapshot", formatSnapshot(report.SnapshotAt))
	b.safeItem("Plan", report.Title, "Unavailable")
	b.safeItem("Plan identifier", report.PlanID, "Unavailable")
	b.item("Status", displayToken(knownStatus(report.Status)))

	b.heading("## Planning Context")
	renderPlanning(&b, report.Planning)

	b.heading("## Slice Overview")
	if len(report.Slices) == 0 {
		b.item("Slices", "None recorded")
	}
	for i, slice := range report.Slices {
		b.safeItem("Slice "+strconv.Itoa(i+1), slice.Title, "Untitled")
		b.item("Kind", map[bool]string{false: "Planned", true: "Rework"}[slice.Rework])
		b.item("Status", displayToken(knownStatus(slice.Status)))
		b.optionalItem("Goal", slice.Goal)
		b.optionalItem("Rationale", slice.Rationale)
		b.safeList("Dependencies", slice.Dependencies)
		b.item("Duration", formatDuration(slice.Duration))
		b.item("Verification", formatCounts(slice.Verification))
	}

	b.heading("## Execution Summary")
	b.item("Duration", formatDuration(report.Execution.Duration))
	b.item("Completed slices", strconv.Itoa(nonNegative(report.Execution.CompletedSlices)))
	b.item("Pending slices", strconv.Itoa(nonNegative(report.Execution.PendingSlices)))
	b.item("Slice verification", formatCounts(report.Execution.Verification))
	if report.Execution.FinalVerification.Available {
		b.optionalItem("Final verification", report.Execution.FinalVerification.Result)
	} else {
		b.item("Final verification", "Not recorded")
	}
	renderTelemetry(&b, report.Execution.Telemetry)

	b.heading("## Review and Outcome")
	if !report.Review.Available {
		b.item("Review", "Not recorded")
	} else {
		b.item("Review status", displayToken(knownReviewStatus(report.Review.Status)))
		b.item("Verdict", displayToken(knownVerdict(report.Review.Verdict)))
		b.optionalItem("Review summary", report.Review.Summary)
		b.item("Finding count", strconv.Itoa(nonNegative(report.Review.FindingCount)))
	}
	b.item("Merged", yesNo(report.Outcome.Merged))

	b.heading("## Redactions and Omissions")
	renderDisclosures(&b, report.Disclosures)
	return b.finish()
}

// RenderPlanningOnly renders the execution-independent planning projection.
func RenderPlanningOnly(report PlanningOnlyReport) ([]byte, error) {
	var b markdownBuilder
	b.heading("# Plan Report")
	b.heading("## Executive Summary")
	b.item("Schema", SchemaV1)
	b.item("Mode", string(ModePlanningOnly))
	b.item("Snapshot", formatSnapshot(report.SnapshotAt))
	b.safeItem("Plan", report.Title, "Unavailable")
	b.safeItem("Plan identifier", report.PlanID, "Unavailable")
	b.item("Source notice", "Synthesized, non-verbatim planning record; not a prompt or planning-session transcript")

	b.heading("## Planning Context")
	renderPlanning(&b, report.Planning)

	b.heading("## Planned Slices")
	if len(report.Slices) == 0 {
		b.item("Slices", "None recorded")
	}
	for i, slice := range report.Slices {
		b.safeItem("Slice "+strconv.Itoa(i+1), slice.Title, "Untitled")
		b.optionalItem("Goal", slice.Goal)
		b.optionalItem("Rationale", slice.Rationale)
		b.safeList("Dependencies", slice.Dependencies)
	}

	b.heading("## Redactions and Omissions")
	renderDisclosures(&b, report.Disclosures)
	return b.finish()
}

func renderPlanning(b *markdownBuilder, planning PlanningContext) {
	b.optionalItem("Goal", planning.Goal)
	b.safeList("Constraints", planning.Constraints)
	b.safeList("Non-goals", planning.NonGoals)
	b.safeList("Decisions", planning.Decisions)
	b.safeList("Risks", planning.Risks)
	b.safeList("Open questions", planning.Questions)
}

func renderTelemetry(b *markdownBuilder, telemetry TelemetrySummary) {
	if !telemetry.Available {
		b.item("Agent metrics", "Not recorded")
		return
	}
	b.item("Agent attempts", strconv.Itoa(nonNegative(telemetry.Attempts)))
	b.item("Failed agent attempts", strconv.Itoa(nonNegative(telemetry.FailedAttempts)))
	b.item("Sessions", formatOptionalInt(telemetry.Sessions))
	b.item("Agents", formatOptionalInt(telemetry.AgentCount))
	b.item("Input tokens", formatOptionalInt(telemetry.InputTokens))
	b.item("Output tokens", formatOptionalInt(telemetry.OutputTokens))
	b.item("Reasoning tokens", formatOptionalInt(telemetry.ReasoningTokens))
	b.item("Total tokens", formatOptionalInt(telemetry.TotalTokens))
	b.item("Messages", formatOptionalInt(telemetry.Messages))
	b.item("Errored messages", formatOptionalInt(telemetry.ErroredMessages))
	b.item("Tool calls", formatOptionalInt(telemetry.ToolCalls))
	if telemetry.Cost.Available && telemetry.Cost.Value >= 0 {
		b.item("Reported cost", strconv.FormatFloat(telemetry.Cost.Value, 'f', 2, 64))
	} else {
		b.item("Reported cost", "Not recorded")
	}
}

func renderDisclosures(b *markdownBuilder, disclosures []Disclosure) {
	items := append([]Disclosure(nil), disclosures...)
	sort.SliceStable(items, func(i, j int) bool {
		left := disclosureSection(items[i].Section) + "\x00" + disclosureCategory(items[i].Category)
		right := disclosureSection(items[j].Section) + "\x00" + disclosureCategory(items[j].Category)
		return left < right
	})
	emitted := false
	for _, disclosure := range items {
		if disclosure.Count > 0 {
			b.item("Safety transformation", disclosureSection(disclosure.Section)+"; "+disclosureCategory(disclosure.Category)+"; count "+strconv.Itoa(disclosure.Count))
			emitted = true
		}
	}
	if !emitted {
		b.item("Safety transformations", "None recorded")
	}
}

func (b *markdownBuilder) heading(value string) {
	if len(b.lines) > 0 {
		b.lines = append(b.lines, "")
	}
	b.lines = append(b.lines, value)
}

func (b *markdownBuilder) item(label, value string) {
	b.lines = append(b.lines, "- "+label+": "+value)
}

func (b *markdownBuilder) safeItem(label string, value SafeText, fallback string) {
	text := inlineSafe(value)
	if text == "" {
		text = fallback
	}
	b.item(label, text)
}

func (b *markdownBuilder) optionalItem(label string, value OptionalText) {
	if !value.Available || inlineSafe(value.Text) == "" {
		b.item(label, "Unavailable")
		return
	}
	b.item(label, inlineSafe(value.Text))
}

func (b *markdownBuilder) safeList(label string, values []SafeText) {
	emitted := false
	for _, value := range values {
		if text := inlineSafe(value); text != "" {
			b.item(label, text)
			emitted = true
		}
	}
	if !emitted {
		b.item(label, "None recorded")
	}
}

func (b *markdownBuilder) finish() ([]byte, error) {
	document := []byte(strings.Join(b.lines, "\n") + "\n")
	if err := validateRenderedDocument(document); err != nil {
		return nil, err
	}
	return document, nil
}

func validateRenderedDocument(document []byte) error {
	var payload strings.Builder
	for _, line := range strings.Split(strings.TrimSuffix(string(document), "\n"), "\n") {
		if _, ok := reportHeadings[line]; ok || line == "" {
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			return errUnsafeDocument
		}
		payload.WriteString(strings.TrimPrefix(line, "- "))
		payload.WriteByte('\n')
	}
	return ValidateDocument([]byte(payload.String()))
}

func inlineSafe(value SafeText) string {
	return strings.Join(strings.Fields(value.text), " ")
}

func formatSnapshot(value time.Time) string {
	if value.IsZero() {
		return "Unavailable"
	}
	return value.UTC().Format(time.RFC3339)
}

func formatDuration(value DurationSummary) string {
	if !value.Available || value.Seconds < 0 {
		return "Not recorded"
	}
	seconds := value.Seconds
	hours := seconds / 3600
	minutes := seconds % 3600 / 60
	remaining := seconds % 60
	if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, remaining)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, remaining)
	}
	return fmt.Sprintf("%ds", remaining)
}

func formatCounts(value CountSummary) string {
	return fmt.Sprintf("%d total; %d passed; %d failed; %d other", nonNegative(value.Total), nonNegative(value.Passed), nonNegative(value.Failed), nonNegative(value.Other))
}

func formatOptionalInt(value OptionalInt64) string {
	if !value.Available || value.Value < 0 {
		return "Not recorded"
	}
	return strconv.FormatInt(value.Value, 10)
}

func displayToken(value string) string {
	return strings.ReplaceAll(value, "_", " ")
}

func yesNo(value bool) string {
	if value {
		return "Yes"
	}
	return "No"
}

func disclosureSection(section Section) string {
	switch section {
	case sectionIdentity:
		return "Identity"
	case sectionPlanning:
		return "Planning context"
	case sectionSlices:
		return "Slices"
	case sectionExecution:
		return "Execution"
	case sectionReview:
		return "Review"
	default:
		return "Other"
	}
}

func disclosureCategory(category DisclosureCategory) string {
	switch category {
	case DisclosureNormalized:
		return "normalized"
	case DisclosureOmitted:
		return "omitted"
	case DisclosureRedacted:
		return "redacted"
	case DisclosureTruncated:
		return "truncated"
	default:
		return "transformed"
	}
}
