package planreport

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

var reportHeadings = map[string]struct{}{
	"## Planning Context":         {},
	"### Goal":                    {},
	"### Constraints":             {},
	"### Non-goals":               {},
	"### Decisions":               {},
	"### Risks":                   {},
	"### Open Questions":          {},
	"### Planning Effort":         {},
	"## Implementation":           {},
	"## Planned Slices":           {},
	"## Implementation Summary":   {},
	"## Review and Outcome":       {},
	"### Safety transformations":  {},
	"#### Goal":                   {},
	"#### Rationale":              {},
	"#### Dependencies":           {},
	"## Redactions and Omissions": {},
}

var reportLabels = map[string]struct{}{
	"**Verification**":          {},
	"**Execution**":             {},
	"**Tokens**":                {},
	"**Finalization recovery**": {},
}

type markdownBuilder struct {
	lines           []string
	dynamicHeadings map[string]struct{}
}

// RenderFull renders a full safe projection using the fixed v1 Markdown schema.
func RenderFull(report FullReport) ([]byte, error) {
	var b markdownBuilder
	b.frontmatter(ModeFull, report.SnapshotAt, report.Title, report.PlanID, knownStatus(report.Status))
	b.dynamicHeading("# " + safeOr(report.Title, "Untitled plan"))

	b.heading("## Planning Context")
	renderPlanning(&b, report.Planning, report.PlanningEffort)

	b.heading("## Implementation")
	if len(report.Slices) == 0 {
		b.paragraph("None recorded.")
	}
	for i, slice := range report.Slices {
		b.dynamicHeading("### Slice " + strconv.Itoa(i+1) + ": " + safeOr(slice.Title, "Untitled"))
		b.metadata(
			displayToken(knownStatus(slice.Status)),
			map[bool]string{false: "planned", true: "rework"}[slice.Rework],
			formatSliceTokens(slice.TotalTokens),
			formatSliceCommit(slice.Commit),
			formatDuration(slice.Duration),
		)
		b.heading("#### Goal")
		b.optionalParagraph(slice.Goal)
		b.heading("#### Rationale")
		b.optionalParagraph(slice.Rationale)
		b.heading("#### Dependencies")
		b.safeNumberedList(slice.Dependencies)
	}

	b.heading("## Implementation Summary")
	totalSlices := nonNegative(report.Execution.CompletedSlices) + nonNegative(report.Execution.PendingSlices)
	b.paragraph("`" + formatDuration(report.Execution.Duration) + "` · `" +
		fmt.Sprintf("%d/%d slices", nonNegative(report.Execution.CompletedSlices), totalSlices) + "` `" +
		fmt.Sprintf("%d/%d passed", nonNegative(report.Execution.Verification.Passed), nonNegative(report.Execution.Verification.Total)) + "` `" +
		formatReportedCost(report.Execution.Telemetry) + "`")

	b.label("**Verification**")
	b.item("Slices", formatCounts(report.Execution.Verification))
	if report.Execution.FinalVerification.Available {
		b.optionalItem("Final", report.Execution.FinalVerification.Result)
	} else {
		b.item("Final", "Not recorded")
	}

	b.label("**Execution**")
	renderExecutionTelemetry(&b, report.Execution.Telemetry)
	b.label("**Tokens**")
	renderTokenTelemetry(&b, report.Execution.Telemetry)

	b.heading("## Review and Outcome")
	reviewStatus, verdict, findings := "not recorded", "not recorded", 0
	if report.Review.Available {
		reviewStatus = displayToken(knownReviewStatus(report.Review.Status))
		verdict = displayToken(knownVerdict(report.Review.Verdict))
		findings = nonNegative(report.Review.FindingCount)
	}
	outcome := "not merged"
	if report.Outcome.Merged {
		outcome = "merged"
	}
	b.metadata(reviewStatus, verdict, formatFindingCount(findings), outcome)
	b.blank()
	if !report.Review.Available {
		b.paragraph("No review recorded.")
	} else {
		b.optionalParagraph(report.Review.Summary)
	}
	if report.Finalization.Available {
		b.label("**Finalization recovery**")
		b.item("Failed phase", displayToken(knownFinalizationPhaseToken(report.Finalization.Phase)))
		b.optionalItem("Category", report.Finalization.Category)
		b.item("Failed at", formatSnapshot(report.Finalization.FailedAt))
		b.optionalItem("Next action", report.Finalization.Action)
	}

	b.heading("## Redactions and Omissions")
	b.heading("### Safety transformations")
	renderDisclosures(&b, report.Disclosures)
	return b.finish()
}

// RenderPlanningOnly renders the execution-independent planning projection.
func RenderPlanningOnly(report PlanningOnlyReport) ([]byte, error) {
	var b markdownBuilder
	b.frontmatter(ModePlanningOnly, report.SnapshotAt, report.Title, report.PlanID, "planned")
	b.dynamicHeading("# " + safeOr(report.Title, "Untitled plan"))

	b.heading("## Planning Context")
	renderPlanning(&b, report.Planning, report.PlanningEffort)

	b.heading("## Planned Slices")
	if len(report.Slices) == 0 {
		b.paragraph("None recorded.")
	}
	for i, slice := range report.Slices {
		b.item("Slice "+strconv.Itoa(i+1), safeOr(slice.Title, "Untitled"))
		b.optionalItem("Goal", slice.Goal)
		b.optionalItem("Rationale", slice.Rationale)
		b.safeFieldList("Dependencies", slice.Dependencies)
	}

	b.heading("## Redactions and Omissions")
	b.heading("### Safety transformations")
	renderDisclosures(&b, report.Disclosures)
	return b.finish()
}

func renderPlanning(b *markdownBuilder, planning PlanningContext, effort PlanningEffortSummary) {
	b.heading("### Goal")
	b.optionalParagraph(planning.Goal)
	b.heading("### Constraints")
	b.safeNumberedList(planning.Constraints)
	b.heading("### Non-goals")
	b.safeNumberedList(planning.NonGoals)
	b.heading("### Decisions")
	b.safeNumberedList(planning.Decisions)
	b.heading("### Risks")
	b.safeNumberedList(planning.Risks)
	b.heading("### Open Questions")
	b.safeNumberedList(planning.Questions)
	if !effort.Available {
		return
	}
	b.heading("### Planning Effort")
	b.item("Duration", formatDuration(effort.Duration))
	b.item("Total tokens", formatOptionalInt(effort.TotalTokens))
	b.item("Messages", formatOptionalInt(effort.TotalMessages))
}

func renderExecutionTelemetry(b *markdownBuilder, telemetry TelemetrySummary) {
	if !telemetry.Available {
		b.item("Sessions", "Not recorded")
		b.item("Agents", "Not recorded")
		b.item("Agent attempts", "Not recorded")
		b.item("Messages", "Not recorded")
		b.item("Tool calls", "Not recorded")
		return
	}
	b.item("Sessions", formatOptionalInt(telemetry.Sessions))
	b.item("Agents", formatOptionalInt(telemetry.AgentCount))
	b.item("Agent attempts", fmt.Sprintf("%d (%d failed)", nonNegative(telemetry.Attempts), nonNegative(telemetry.FailedAttempts)))
	b.item("Messages", formatMetricWithQualifier(telemetry.Messages, "errored", telemetry.ErroredMessages))
	b.item("Tool calls", formatOptionalInt(telemetry.ToolCalls))
}

func renderTokenTelemetry(b *markdownBuilder, telemetry TelemetrySummary) {
	if !telemetry.Available {
		b.item("Input", "Not recorded")
		b.item("Output", "Not recorded")
		b.item("Reasoning", "Not recorded")
		b.item("Total", "Not recorded")
		return
	}
	b.item("Input", formatOptionalIntGrouped(telemetry.InputTokens))
	b.item("Output", formatOptionalIntGrouped(telemetry.OutputTokens))
	b.item("Reasoning", formatOptionalIntGrouped(telemetry.ReasoningTokens))
	b.item("Total", formatOptionalIntGrouped(telemetry.TotalTokens))
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
		if disclosure.Count <= 0 {
			continue
		}
		b.item(disclosureSection(disclosure.Section), strconv.Itoa(disclosure.Count)+" "+disclosureCategory(disclosure.Category))
		emitted = true
	}
	if !emitted {
		b.paragraph("None")
	}
}

func (b *markdownBuilder) frontmatter(mode Mode, snapshot time.Time, title, planID SafeText, status string) {
	b.lines = append(b.lines,
		"---",
		"schema: "+yamlScalar(SchemaV1),
		"mode: "+yamlScalar(string(mode)),
		"snapshot: "+yamlScalar(formatSnapshot(snapshot)),
		"plan: "+yamlScalar(safeOr(title, "Unavailable")),
		"plan-id: "+yamlScalar(safeOr(planID, "Unavailable")),
		"status: "+yamlScalar(status),
		"---",
	)
}

func yamlScalar(value string) string {
	if yamlPlainStringSafe(value) {
		return value
	}
	return strconv.Quote(value)
}

func yamlPlainStringSafe(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "\n\r#{}[],&*!|>'\"%@`") ||
		strings.Contains(value, ": ") || strings.HasSuffix(value, ":") || strings.ContainsAny(value[:1], "-?:") {
		return false
	}
	switch strings.ToLower(value) {
	case "null", "~", "true", "false", "yes", "no", "on", "off", "y", "n", ".nan", ".inf", "+.inf", "-.inf":
		return false
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return false
	}
	if value[0] >= '0' && value[0] <= '9' {
		dateLike := true
		for _, r := range value {
			if (r < '0' || r > '9') && !strings.ContainsRune("-:+.TZtz ", r) {
				dateLike = false
				break
			}
		}
		if dateLike {
			return false
		}
	}
	return true
}

func (b *markdownBuilder) heading(value string) {
	if len(b.lines) > 0 && b.lines[len(b.lines)-1] != "" {
		b.lines = append(b.lines, "")
	}
	b.lines = append(b.lines, value, "")
}

func (b *markdownBuilder) dynamicHeading(value string) {
	if b.dynamicHeadings == nil {
		b.dynamicHeadings = make(map[string]struct{})
	}
	b.dynamicHeadings[value] = struct{}{}
	b.heading(value)
}

func (b *markdownBuilder) paragraph(value string) { b.lines = append(b.lines, value) }

func (b *markdownBuilder) metadata(values ...string) {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "`"+value+"`")
	}
	b.paragraph(strings.Join(quoted, " "))
}

func (b *markdownBuilder) label(value string) {
	b.blank()
	b.lines = append(b.lines, value)
}

func (b *markdownBuilder) blank() {
	if len(b.lines) > 0 && b.lines[len(b.lines)-1] != "" {
		b.lines = append(b.lines, "")
	}
}

func (b *markdownBuilder) optionalParagraph(value OptionalText) {
	if !value.Available || inlineSafe(value.Text) == "" {
		b.paragraph("Unavailable.")
		return
	}
	b.paragraph(inlineSafe(value.Text))
}

func (b *markdownBuilder) item(label, value string) { b.lines = append(b.lines, "- "+label+": "+value) }

func (b *markdownBuilder) optionalItem(label string, value OptionalText) {
	if !value.Available || inlineSafe(value.Text) == "" {
		b.item(label, "Unavailable")
		return
	}
	b.item(label, inlineSafe(value.Text))
}

func (b *markdownBuilder) safeFieldList(label string, values []SafeText) {
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

func (b *markdownBuilder) safeNumberedList(values []SafeText) {
	n := 0
	for _, value := range values {
		if text := inlineSafe(value); text != "" {
			n++
			b.lines = append(b.lines, strconv.Itoa(n)+". "+text)
		}
	}
	if n == 0 {
		b.paragraph("None recorded.")
	}
}

func (b *markdownBuilder) finish() ([]byte, error) {
	document := []byte(strings.Join(b.lines, "\n") + "\n")
	if err := validateRenderedDocument(document, b.dynamicHeadings); err != nil {
		return nil, err
	}
	return document, nil
}

func validateRenderedDocument(document []byte, dynamicHeadings map[string]struct{}) error {
	lines := strings.Split(strings.TrimSuffix(string(document), "\n"), "\n")
	if len(lines) < 8 || lines[0] != "---" || lines[7] != "---" {
		return errUnsafeDocument
	}
	keys := []string{"schema: ", "mode: ", "snapshot: ", "plan: ", "plan-id: ", "status: "}
	var payload strings.Builder
	for i, key := range keys {
		line := lines[i+1]
		if !strings.HasPrefix(line, key) {
			return errUnsafeDocument
		}
		value, ok := parseYAMLScalar(strings.TrimPrefix(line, key))
		if !ok {
			return errUnsafeDocument
		}
		payload.WriteString(value)
		payload.WriteByte('\n')
	}
	lastHeading := ""
	for _, line := range lines[8:] {
		if line == "" {
			continue
		}
		if _, ok := reportHeadings[line]; ok {
			lastHeading = line
			continue
		}
		if _, ok := reportLabels[line]; ok {
			lastHeading = line
			continue
		}
		if _, ok := dynamicHeadings[line]; ok {
			lastHeading = line
			payload.WriteString(strings.TrimSpace(strings.TrimLeft(line, "#")))
			payload.WriteByte('\n')
			continue
		}
		switch {
		case strings.HasPrefix(line, "- "):
			if !allowsBullets(lastHeading) {
				return errUnsafeDocument
			}
			payload.WriteString(strings.TrimPrefix(line, "- "))
		case numberedLine(line):
			if !allowsNumberedValues(lastHeading) {
				return errUnsafeDocument
			}
			payload.WriteString(line[strings.Index(line, ". ")+2:])
		default:
			if !allowsParagraph(lastHeading) {
				return errUnsafeDocument
			}
			payload.WriteString(line)
		}
		payload.WriteByte('\n')
	}
	return ValidateDocument([]byte(payload.String()))
}

func parseYAMLScalar(value string) (string, bool) {
	if strings.HasPrefix(value, "\"") {
		parsed, err := strconv.Unquote(value)
		return parsed, err == nil
	}
	if strings.HasPrefix(value, "'") {
		if len(value) < 2 || !strings.HasSuffix(value, "'") {
			return "", false
		}
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'"), true
	}
	if !yamlPlainStringSafe(value) {
		return "", false
	}
	return value, true
}

func numberedLine(line string) bool {
	dot := strings.Index(line, ". ")
	if dot < 1 {
		return false
	}
	_, err := strconv.Atoi(line[:dot])
	return err == nil
}

func allowsBullets(heading string) bool {
	return heading == "### Planning Effort" || heading == "## Planned Slices" ||
		heading == "**Verification**" || heading == "**Execution**" || heading == "**Tokens**" || heading == "**Finalization recovery**" ||
		heading == "### Safety transformations"
}

func allowsNumberedValues(heading string) bool {
	switch heading {
	case "### Constraints", "### Non-goals", "### Decisions", "### Risks", "### Open Questions", "#### Dependencies":
		return true
	default:
		return false
	}
}

func allowsParagraph(heading string) bool {
	if strings.HasPrefix(heading, "### Slice ") {
		return true
	}
	switch heading {
	case "### Goal", "#### Goal", "#### Rationale", "#### Dependencies", "## Implementation", "## Implementation Summary", "## Planned Slices", "## Review and Outcome", "### Safety transformations", "### Constraints", "### Non-goals", "### Decisions", "### Risks", "### Open Questions":
		return true
	default:
		return false
	}
}

func safeOr(value SafeText, fallback string) string {
	if text := inlineSafe(value); text != "" {
		return text
	}
	return fallback
}

func inlineSafe(value SafeText) string { return strings.Join(strings.Fields(value.text), " ") }

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

func formatOptionalIntGrouped(value OptionalInt64) string {
	if !value.Available || value.Value < 0 {
		return "Not recorded"
	}
	return formatGroupedInt(value.Value)
}

func formatGroupedInt(value int64) string {
	digits := strconv.FormatInt(value, 10)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return digits
}

func formatSliceTokens(value OptionalInt64) string {
	if !value.Available || value.Value < 0 {
		return "tokens not recorded"
	}
	if value.Value < 1000 {
		return formatGroupedInt(value.Value) + " tokens"
	}
	return strconv.FormatInt((value.Value+500)/1000, 10) + "k tokens"
}

func formatFindingCount(count int) string {
	if count == 1 {
		return "1 finding"
	}
	return strconv.Itoa(count) + " findings"
}

func formatReportedCost(telemetry TelemetrySummary) string {
	if !telemetry.Available || !telemetry.Cost.Available || telemetry.Cost.Value < 0 {
		return "cost not recorded"
	}
	return "$" + strconv.FormatFloat(telemetry.Cost.Value, 'f', 2, 64)
}

func formatMetricWithQualifier(value OptionalInt64, qualifier string, qualified OptionalInt64) string {
	base := formatOptionalIntGrouped(value)
	return base + " (" + qualifier + ": " + strings.ToLower(formatOptionalIntGrouped(qualified)) + ")"
}

func formatSliceCommit(value SliceCommitSummary) string {
	switch value.Outcome {
	case "committed":
		if value.SHA.Available && inlineSafe(value.SHA.Text) != "" {
			return inlineSafe(value.SHA.Text)
		}
		return "Not recorded"
	case "no_changes":
		return "No changes"
	case "manual_uncommitted":
		return "Manual uncommitted"
	default:
		return "Not recorded"
	}
}

func displayToken(value string) string { return strings.ReplaceAll(value, "_", " ") }

func knownFinalizationPhaseToken(value string) string {
	switch value {
	case "proposal_repair", "pull_request_finalization":
		return value
	default:
		return "unavailable"
	}
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
