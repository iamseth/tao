package planreport

import (
	"bytes"
	"regexp"
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
	want := `# Plan Report

## Executive Summary
- Schema: tao.plan-report.v1
- Mode: planning-only
- Snapshot: 2026-08-04T22:00:00Z
- Plan: Leadership Report
- Plan identifier: leadership-report
- Source notice: Synthesized, non-verbatim planning record; not a prompt or planning-session transcript

## Planning Context
- Goal: Share a leadership snapshot.
- Constraints: Keep it safe
- Non-goals: Raw export
- Decisions: Use typed projections
- Risks: Sensitive text
- Open questions: Who receives it?

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
- Safety transformations: None recorded
`
	if string(first) != want {
		t.Fatalf("planning Markdown differs:\n--- got ---\n%s--- want ---\n%s", first, want)
	}
}

func TestRenderFullUsesFixedSectionOrderAcrossPhases(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	statuses := []string{plan.StatusPlanned, plan.StatusInProgress, plan.StatusBlocked, plan.StatusInReview, plan.StatusReviewed, plan.StatusChangesRequested, plan.StatusCompleted}
	sections := []string{"## Executive Summary", "## Planning Context", "## Slice Overview", "## Execution Summary", "## Review and Outcome", "## Redactions and Omissions"}
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
			if bytes.Count(got, []byte("## ")) != len(sections) {
				t.Fatalf("unexpected dynamic heading in:\n%s", got)
			}
		})
	}
}

func TestRenderFullEmptyRedactedAndMeasuredSections(t *testing.T) {
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
		"- Goal: Unavailable",
		"- Slices: None recorded",
		"- Agent metrics: Not recorded",
		"- Review: Not recorded",
		"- Safety transformation: Identity; redacted; count 1",
		"- Safety transformation: Slices; truncated; count 2",
	} {
		if !bytes.Contains(got, []byte(text)) {
			t.Errorf("missing %q in:\n%s", text, got)
		}
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
	allowedLine := regexp.MustCompile(`^(?:$|# Plan Report|## (?:Executive Summary|Planning Context|Planned Slices|Redactions and Omissions)|- [^\r\n]+)$`)
	for _, line := range strings.Split(strings.TrimSuffix(string(got), "\n"), "\n") {
		if !allowedLine.MatchString(line) {
			t.Fatalf("line is outside static heading/paragraph bullet subset: %q", line)
		}
	}
}

func TestRenderPlanningOnlyStructurallyExcludesExecutionData(t *testing.T) {
	now := time.Date(2026, 8, 4, 15, 0, 0, 0, time.UTC)
	detail := reportFixture(now)
	detail.Slices.Slices[0].Notes = "execution-only-secret"
	detail.Slices.Slices[0].VerificationResults = []plan.VerificationRun{{Result: "failed", Details: "execution-only-secret"}}
	detail.Events = []plan.Event{{Type: plan.EventTypeAgentMetrics, Metrics: &plan.AgentMetrics{OutputTokens: 999}}}
	got, err := RenderPlanningOnly(ProjectPlanningOnly(detail, now))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"Execution Summary", "Review and Outcome", "verification", "tokens", "execution-only-secret", "Rework"} {
		if bytes.Contains(bytes.ToLower(got), bytes.ToLower([]byte(forbidden))) {
			t.Fatalf("planning-only output contains %q:\n%s", forbidden, got)
		}
	}
}
