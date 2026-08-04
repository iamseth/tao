package planreport

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestRenderPlanningOnlyGoldenAndFixedClockStability(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.FixedZone("local", -7*60*60))
	report := ProjectPlanningOnly(reportFixture(now), now)
	first, err := RenderPlanningOnly(report)
	if err != nil {
		t.Fatal(err)
	}
	second, err := RenderPlanningOnly(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("repeated rendering was not deterministic")
	}
	want := `---
schema: tao.plan-report.v1
mode: planning-only
snapshot: "2026-08-04T22:00:00Z"
plan: Leadership Report
plan-id: leadership-report
status: planned
---

# Leadership Report

## Planning Context

### Goal

Share a leadership snapshot.

### Constraints

1. Keep it safe

### Non-goals

1. Raw export

### Decisions

1. Use typed projections

### Risks

1. Sensitive text

### Open Questions

1. Who receives it?

## Planned Slices

- Slice 1: Build projection
- Goal: Create safe models
- Rationale: Renderers need an allowlist
- Dependencies: None recorded
- Slice 2: Ship report
- Goal: Expose the report
- Rationale: Leaders need snapshots
- Dependencies: Build projection

## Redactions and Omissions

### Safety transformations

None
`
	if string(first) != want {
		t.Fatalf("planning Markdown differs:\n--- got ---\n%s--- want ---\n%s", first, want)
	}
}

func TestRenderFullGolden(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	report := ProjectFull(reportFixture(now), now)
	got, err := RenderFull(report)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(`---
schema: tao.plan-report.v1
mode: full
snapshot: "2026-08-04T15:00:00Z"
plan: Leadership Report
plan-id: leadership-report
status: planned
---

# Leadership Report

## Planning Context

### Goal

Share a leadership snapshot.

### Constraints

1. Keep it safe

### Non-goals

1. Raw export

### Decisions

1. Use typed projections

### Risks

1. Sensitive text

### Open Questions

1. Who receives it?

## Implementation

### Slice 1: Build projection

~pending~ ~planned~ ~tokens not recorded~ ~Not recorded~ ~Not recorded~

#### Goal

Create safe models

#### Rationale

Renderers need an allowlist

#### Dependencies

None recorded.

### Slice 2: Ship report

~pending~ ~planned~ ~tokens not recorded~ ~Not recorded~ ~Not recorded~

#### Goal

Expose the report

#### Rationale

Leaders need snapshots

#### Dependencies

1. Build projection

## Implementation Summary

~2m 0s~ · ~0/2 slices~ ~0/0 passed~ ~cost not recorded~

**Verification**
- Slices: 0 total; 0 passed; 0 failed; 0 other
- Final: Not recorded

**Execution**
- Sessions: Not recorded
- Agents: Not recorded
- Agent attempts: Not recorded
- Messages: Not recorded
- Tool calls: Not recorded

**Tokens**
- Input: Not recorded
- Output: Not recorded
- Reasoning: Not recorded
- Total: Not recorded

## Review and Outcome

~not recorded~ ~not recorded~ ~0 findings~ ~not merged~

No review recorded.

## Redactions and Omissions

### Safety transformations

None
`, "~", "`")
	if string(got) != want {
		t.Fatalf("full Markdown differs:\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestRenderFullUsesTargetSectionsAcrossPhases(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	statuses := []string{plan.StatusPlanned, plan.StatusInProgress, plan.StatusBlocked, plan.StatusInReview, plan.StatusReviewed, plan.StatusChangesRequested, plan.StatusCompleted}
	sections := []string{"## Planning Context", "## Implementation", "## Implementation Summary", "## Review and Outcome", "## Redactions and Omissions"}
	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			detail := reportFixture(now)
			detail.State.Status = status
			got, err := RenderFull(ProjectFull(detail, now))
			if err != nil {
				t.Fatal(err)
			}
			previous := -1
			for _, section := range sections {
				index := bytes.Index(got, []byte(section))
				if index <= previous {
					t.Fatalf("section %q missing or out of order in:\n%s", section, got)
				}
				previous = index
			}
			if !bytes.HasPrefix(got, []byte("---\nschema: tao.plan-report.v1\nmode: full")) || !bytes.Contains(got, []byte("\nplan: Leadership Report\nplan-id: leadership-report\n")) || !bytes.Contains(got, []byte("\n# Leadership Report\n")) {
				t.Fatalf("frontmatter or title missing in:\n%s", got)
			}
		})
	}
}

func TestRenderFrontmatterQuotesAmbiguousYAMLStrings(t *testing.T) {
	s := NewSanitizer(0)
	for _, title := range []string{"Release:", "? question", "yes", "null", "2026-08-04"} {
		t.Run(title, func(t *testing.T) {
			report := PlanningOnlyReport{Title: s.Sanitize(sectionIdentity, title)}
			got, err := RenderPlanningOnly(report)
			if err != nil {
				t.Fatal(err)
			}
			want := "plan: " + strconv.Quote(title) + "\n"
			if !bytes.Contains(got, []byte(want)) {
				t.Fatalf("frontmatter did not quote ambiguous title %q:\n%s", title, got)
			}
			if _, ok := parseYAMLScalar(title); ok {
				t.Fatalf("validator accepted ambiguous plain scalar %q", title)
			}
		})
	}
}

func TestRenderFullSliceMetadataAndCompactOutcome(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	seconds := int64(61)
	detail.Slices.Slices[0].Status = plan.StatusCompleted
	detail.Slices.Slices[0].Timing.DurationSeconds = &seconds
	detail.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "abcdef1234567890"}
	detail.Slices.Slices[0].VerificationResults = []plan.VerificationRun{{Result: "passed"}, {Result: "failed"}}
	detail.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, SliceID: "001-build", Metrics: &plan.AgentMetrics{Status: plan.StatusCompleted, TotalTokens: 42}}}

	got, err := RenderFull(ProjectFull(detail, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"### Slice 1: Build projection", "`completed` `planned` `42 tokens` `abcdef1` `1m 1s`",
		"#### Goal\n\nCreate safe models", "#### Rationale\n\nRenderers need an allowlist", "#### Dependencies\n\nNone recorded.",
		"## Implementation Summary", "**Verification**", "**Execution**", "**Tokens**", "## Review and Outcome", "`not recorded` `not recorded` `0 findings` `not merged`",
	} {
		if !bytes.Contains(got, []byte(text)) {
			t.Errorf("missing %q in:\n%s", text, got)
		}
	}
}

func TestRenderFullDoesNotPresentNonCommitOutcomesAsSHAs(t *testing.T) {
	s := NewSanitizer(0)
	for _, tc := range []struct {
		name    string
		outcome string
		want    string
	}{
		{name: "no changes", outcome: plan.SliceCompletionNoChanges, want: "No changes"},
		{name: "manual", outcome: plan.SliceCompletionManualUncommitted, want: "Manual uncommitted"},
		{name: "unknown", outcome: "unknown", want: "Not recorded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := FullReport{Slices: []SliceReport{{
				Title: s.Sanitize(sectionSlices, "Slice"),
				Commit: SliceCommitSummary{Outcome: tc.outcome, SHA: OptionalText{
					Available: true, Text: s.Sanitize(sectionSlices, "abcdef1"),
				}},
			}}}
			got, err := RenderFull(report)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Contains(got, []byte("`"+tc.want+"`")) || bytes.Contains(got, []byte("`abcdef1`")) {
				t.Fatalf("outcome rendered as a created commit:\n%s", got)
			}
		})
	}
}

func TestRenderFullMissingValuesAndGroupedDisclosures(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	report := ProjectFull(nil, now)
	report.Disclosures = []Disclosure{
		{Section: sectionSlices, Category: DisclosureTruncated, Count: 2},
		{Section: sectionIdentity, Category: DisclosureRedacted, Count: 1},
	}
	got, err := RenderFull(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{
		"### Goal\n\nUnavailable.",
		"## Implementation\n\nNone recorded.",
		"**Execution**\n- Sessions: Not recorded",
		"**Tokens**\n- Input: Not recorded",
		"### Safety transformations\n\n- Identity: 1 redacted",
		"- Slices: 2 truncated",
	} {
		if !bytes.Contains(got, []byte(text)) {
			t.Errorf("missing %q in:\n%s", text, got)
		}
	}
}

func TestRenderPlanningEffortInBothModes(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	report := PlanningEffortSummary{
		Available: true, Duration: DurationSummary{Available: true, Seconds: 75},
		TotalTokens: OptionalInt64{Available: true, Value: 321}, TotalMessages: OptionalInt64{Available: true, Value: 7},
	}
	full := ProjectFull(reportFixture(now), now)
	full.PlanningEffort = report
	planning := ProjectPlanningOnly(reportFixture(now), now)
	planning.PlanningEffort = report
	for name, render := range map[string]func() ([]byte, error){
		"full":     func() ([]byte, error) { return RenderFull(full) },
		"planning": func() ([]byte, error) { return RenderPlanningOnly(planning) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := render()
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"### Planning Effort", "- Duration: 1m 15s", "- Total tokens: 321", "- Messages: 7"} {
				if !bytes.Contains(got, []byte(value)) {
					t.Errorf("missing %q in:\n%s", value, got)
				}
			}
		})
	}
}

func TestRenderLongSlicesRemainConverterSafe(t *testing.T) {
	s := NewSanitizer(0)
	report := PlanningOnlyReport{
		SnapshotAt: time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC),
		Title:      s.Sanitize(sectionIdentity, "# <Leadership> [report](relative.md)"),
		Slices: []PlannedSlice{{
			Title: s.Sanitize(sectionSlices, strings.Repeat("wide column content ", 100)),
			Goal:  OptionalText{Available: true, Text: s.Sanitize(sectionSlices, "first line\n## injected\n<img src=x> ![asset](file.png)\nDocs https://docs.example.com/project at /srv/company/plan.md")},
		}},
	}
	got, err := RenderPlanningOnly(report)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasSuffix(got, []byte("\n")) || bytes.HasSuffix(got, []byte("\n\n")) || bytes.Contains(got, []byte("\r")) {
		t.Fatalf("output does not use canonical LF/final-newline form: %q", got)
	}
	for _, forbidden := range []string{"\n<img", "\n## injected", "|---", "\x1b"} {
		if bytes.Contains(got, []byte(forbidden)) {
			t.Fatalf("converter-unsafe syntax %q survived:\n%s", forbidden, got)
		}
	}
	for _, retained := range []string{`https://docs.example.com/project`, `/srv/company/plan.md`} {
		if !bytes.Contains(got, []byte(retained)) {
			t.Fatalf("coworker-accessible context was removed %q:\n%s", retained, got)
		}
	}
	for _, escaped := range []string{`\<img src=x\>`, `\!\[asset\](file.png)`, `\## injected`} {
		if !bytes.Contains(got, []byte(escaped)) {
			t.Fatalf("dynamic Markdown was not escaped as text %q:\n%s", escaped, got)
		}
	}
	if regexp.MustCompile(`(?m)^#{1,4} injected$|(?m)^<`).Match(got) {
		t.Fatalf("active source structure survived:\n%s", got)
	}
}

func TestRenderPlanningOnlyStructurallyExcludesImplementationData(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.Slices.Slices[0].Notes = "execution-only-secret"
	detail.Slices.Slices[0].Completion = &plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionCommitted, CommitSHA: "abcdef1234567890"}
	detail.Slices.Slices[0].VerificationResults = []plan.VerificationRun{{Result: "failed", Details: "execution-only-secret"}}
	detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: "r101-fix", Title: "generated rework"})
	detail.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, Metrics: &plan.AgentMetrics{OutputTokens: 999}}}
	got, err := RenderPlanningOnly(ProjectPlanningOnly(detail, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Implementation Summary", "Review and Outcome", "verification", "commit", "duration: 0", "execution-only-secret", "generated rework", "- Status:", "- Kind:", "### Slice"} {
		if bytes.Contains(bytes.ToLower(got), bytes.ToLower([]byte(forbidden))) {
			t.Fatalf("planning-only output contains %q:\n%s", forbidden, got)
		}
	}
}

func TestRenderedDocumentValidationRejectsNonSchemaStructure(t *testing.T) {
	s := NewSanitizer(0)
	report := PlanningOnlyReport{Title: s.Sanitize(sectionIdentity, "Safe title")}
	got, err := RenderPlanningOnly(report)
	if err != nil {
		t.Fatal(err)
	}
	headings := map[string]struct{}{"# Safe title": {}}
	for _, mutation := range [][]byte{
		bytes.Replace(got, []byte("## Planning Context"), []byte("## Injected"), 1),
		bytes.Replace(got, []byte("# Safe title\n"), []byte("# Safe title\nInjected paragraph\n"), 1),
		bytes.Replace(got, []byte("None recorded."), []byte("<img src=x>"), 1),
		bytes.Replace(got, []byte("plan-id: "), []byte("source-key: "), 1),
	} {
		if err := validateRenderedDocument(mutation, headings); err == nil {
			t.Fatalf("accepted non-schema mutation:\n%s", mutation)
		}
	}
}
