package tui

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
)

func TestRenderFrameStylesTabsRuleSummaryAndContext(t *testing.T) {
	model := Model{
		Page: PageNotes, Width: 70, Profile: ProfileANSI16,
		FocusRepositoryID: "repo", FocusRepositoryName: "alpha",
		DebugSnapshot: DebugSnapshot{SelectedAgent: "pi"},
	}
	summary := &frameSummary{primary: "4 open notes", attentionCount: 2, attentionNoun: "warnings"}
	lines := renderFrame(model, PageNotes, summary)
	if len(lines) != 3 {
		t.Fatalf("frame lines = %d, want 3: %q", len(lines), lines)
	}
	for _, want := range []string{
		Paint(ProfileANSI16, RoleNeutral5, "tao"),
		Paint(ProfileANSI16, RoleNeutral1, "│"),
		Paint(ProfileANSI16, RoleAccent, "notes"),
		Paint(ProfileANSI16, RoleNeutral2, "plans"),
		Paint(ProfileANSI16, RoleWarn, "2 warnings"),
	} {
		if !strings.Contains(strings.Join(lines, "\n"), want) {
			t.Fatalf("styled frame missing %q: %q", want, lines)
		}
	}

	_, activeEnd := renderTabStrip(ProfileNone, PageNotes)
	wantRule := Paint(ProfileANSI16, RoleAccent, strings.Repeat("─", activeEnd)) +
		Paint(ProfileANSI16, RoleNeutral0, strings.Repeat("─", model.Width-activeEnd))
	if lines[1] != wantRule {
		t.Fatalf("active rule extent mismatch\nwant: %q\n got: %q", wantRule, lines[1])
	}
	if width := visibleWidth(lines[0]); width != model.Width {
		t.Fatalf("context line width = %d, want %d: %q", width, model.Width, lines[0])
	}
}

func TestRenderFrameCompactsLongFocusedRepositoryAtSeventyColumns(t *testing.T) {
	model := Model{
		Page: PagePlans, Width: 70,
		FocusRepositoryID:   "repo",
		FocusRepositoryName: "a-very-long-focused-repository-name-that-cannot-fit",
		DebugSnapshot:       DebugSnapshot{SelectedAgent: "pi"},
	}

	lines := renderFrame(model, PagePlans, nil)
	if got := visibleWidth(lines[0]); got != model.Width {
		t.Fatalf("context line width = %d, want %d: %q", got, model.Width, lines[0])
	}
	if !strings.Contains(lines[0], "  repo ") || !strings.Contains(lines[0], "…") {
		t.Fatalf("focused repository is not visibly compacted: %q", lines[0])
	}
	if !strings.HasSuffix(lines[0], "  agent pi  ●") {
		t.Fatalf("compacted context lost agent or health marker: %q", lines[0])
	}
	if strings.Contains(lines[0], model.FocusRepositoryName) {
		t.Fatalf("focused repository name was not truncated: %q", lines[0])
	}
}

func TestRenderTabStripMarksEveryActivePageWithoutColor(t *testing.T) {
	wantStrips := map[PageID]string{
		PagePlans:    "tao │▸plans  notes  settings  debug",
		PageNotes:    "tao │ plans ▸notes  settings  debug",
		PageSettings: "tao │ plans  notes ▸settings  debug",
		PageDebug:    "tao │ plans  notes  settings ▸debug",
	}
	for _, tab := range dashboardTabs {
		t.Run(string(tab.ID), func(t *testing.T) {
			strip, activeEnd := renderTabStrip(ProfileNone, tab.ID)
			if strip != wantStrips[tab.ID] {
				t.Fatalf("tab strip = %q, want %q", strip, wantStrips[tab.ID])
			}
			markedLabel := "▸" + tab.Label
			markerAt := strings.Index(strip, markedLabel)
			if markerAt < 0 {
				t.Fatalf("tab strip has no active marker before %q: %q", tab.Label, strip)
			}
			wantActiveEnd := visibleWidth(strip[:markerAt+len(markedLabel)])
			if activeEnd != wantActiveEnd {
				t.Fatalf("active rule end = %d, want %d for %q", activeEnd, wantActiveEnd, strip)
			}
		})
	}
}

func TestRenderFrameSkeletonAndContentAtSupportedSizes(t *testing.T) {
	pages := []struct {
		page    PageID
		content string
	}{
		{page: PagePlans, content: "  repo"},
		{page: PageNotes, content: "> repo"},
		{page: PageSettings, content: "> repo (repo)"},
		{page: PageDebug, content: "UI"},
	}
	for _, width := range []int{199, 120, 100, 80, 70} {
		for _, test := range pages {
			t.Run(fmt.Sprintf("%s-%dx20", test.page, width), func(t *testing.T) {
				model := frameFixtureModel(test.page, width, 20)
				frame := Render(model)
				lines := renderedLines(frame)
				if len(lines) > model.Height {
					t.Fatalf("frame has %d lines, want at most %d", len(lines), model.Height)
				}
				wantStrip, _ := renderTabStrip(ProfileNone, test.page)
				if !strings.HasPrefix(lines[0], wantStrip) {
					t.Fatalf("tab strip is not first: %q", lines)
				}
				if !strings.Contains(lines[1], "─") {
					t.Fatalf("tab rule is not second: %q", lines)
				}
				switch test.page {
				case PagePlans, PageNotes:
					if !strings.Contains(lines[2], map[PageID]string{PagePlans: "plan", PageNotes: "note"}[test.page]) {
						t.Fatalf("summary is not third for %s: %q", test.page, lines)
					}
				case PageSettings:
					sectionIndex := -1
					for index := 2; index < len(lines); index++ {
						if strings.TrimSpace(lines[index]) != "" {
							sectionIndex = index
							break
						}
					}
					if sectionIndex < 0 || !strings.Contains(lines[sectionIndex], "▌ GLOBAL RUNTIME DEFAULTS ") {
						t.Fatalf("first Settings section does not follow the tab rule: %q", lines)
					}
					if strings.Contains(frame, "1 repository") || strings.Contains(frame, "need attention") {
						t.Fatalf("Settings frame unexpectedly contains a summary:\n%s", frame)
					}
				}
				if !strings.Contains(frame, test.content) {
					t.Fatalf("%s frame has no visible content row at %dx20:\n%s", test.page, width, frame)
				}
				wantFooter := renderKeyHintsFooter(ProfileNone, test.page, width)
				if got := strings.TrimRight(lines[len(lines)-1], " "); got != wantFooter {
					t.Fatalf("%s key hints are not the final frame element\nwant: %q\n got: %q", test.page, wantFooter, got)
				}
				for _, line := range lines {
					if got := visibleWidth(line); got != model.Width {
						t.Fatalf("line width = %d, want %d: %q", got, model.Width, line)
					}
				}
			})
		}
	}
}

func TestRenderStressPlanRowsKeepCompleteResponsiveColumns(t *testing.T) {
	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-time.Hour)
	row := monitor.Row{
		RepositoryName: "very-long-repository-name-alpha",
		PlanID:         strings.Repeat("plan-", 12),
		Status:         plan.StatusBlocked,
		NextAction:     "RESOLVE",
		Liveness:       monitor.LivenessLive,
		Phase:          "running_slice",
		SliceID:        strings.Repeat("slice-", 6),
		UpdatedAt:      &updated,
		AttentionReasons: []monitor.AttentionReason{
			monitor.AttentionRunCrashed,
		},
	}
	values := tableRowValues(row, now, "")
	expectedNames := map[int]string{
		199: "REPO,NEXT,PLAN,SLICES,RUN,AGE,ATTENTION",
		120: "REPO,NEXT,PLAN,SLICES,RUN,AGE,ATTENTION",
		100: "REPO,NEXT,PLAN,SLICES,ATTENTION",
		80:  "REPO,NEXT,PLAN,ATTENTION",
		70:  "REPO,NEXT,PLAN,ATTENTION",
	}
	valueForColumn := func(name string) string {
		switch name {
		case "REPO":
			return values.repo
		case "NEXT":
			return values.next
		case "PLAN":
			return values.plan
		case "SLICES":
			return renderSlicesValue(ProfileNone, row)
		case "RUN":
			return values.run
		case "AGE":
			return values.age
		case "ATTENTION":
			return values.attention
		default:
			return ""
		}
	}

	widths := measureTable([]Section{{Kind: SectionAttention, Rows: []monitor.Row{row}}}, now, nil)
	for _, width := range []int{199, 120, 100, 80, 70} {
		t.Run(fmt.Sprintf("%d", width), func(t *testing.T) {
			columns := planTableColumns(widths, true, width)
			names := make([]string, len(columns))
			for index, item := range columns {
				names[index] = item.name
				if item.flex != (item.name == "PLAN") {
					t.Errorf("column %q flex = %t, want only PLAN flexible", item.name, item.flex)
				}
			}
			if got := strings.Join(names, ","); got != expectedNames[width] {
				t.Fatalf("columns = %q, want %q", got, expectedNames[width])
			}

			frame := Render(Model{Snapshot: monitor.Snapshot{CollectedAt: now, Rows: []monitor.Row{row}}, Width: width, Height: 20})
			lines := renderedLines(frame)
			var header, content string
			for _, line := range lines {
				if strings.HasPrefix(line, "  REPO") {
					header = line
				}
				if strings.Contains(line, " RESOLVE ") && strings.Contains(line, "very-long-repository-name-alpha") {
					content = line
				}
				if got := visibleWidth(line); got != width {
					t.Fatalf("line width = %d, want %d: %q", got, width, line)
				}
			}
			if header == "" || content == "" {
				t.Fatalf("rendered table is incomplete:\n%s", frame)
			}

			resolved := resolveColumns(columns, planTablePaneWidth(width, columns))
			offset := visibleWidth("  ")
			for index, item := range resolved {
				if index > 0 {
					offset += columnGapWidth
				}
				headerRunes := []rune(header)
				contentRunes := []rune(content)
				headerCell := strings.TrimSpace(string(headerRunes[offset : offset+item.width]))
				contentCell := strings.TrimSpace(string(contentRunes[offset : offset+item.width]))
				if headerCell != item.name {
					t.Errorf("column %q rendered ambiguous header %q", item.name, headerCell)
				}
				wantValue := strings.TrimSpace(valueForColumn(item.name))
				if item.name == "PLAN" {
					if item.width < minimumPlanColumnWidth || !strings.HasPrefix(wantValue, contentCell) || visibleWidth(contentCell) < minimumPlanColumnWidth {
						t.Errorf("PLAN rendered without a meaningful value width: width=%d value=%q", item.width, contentCell)
					}
				} else if contentCell != wantValue {
					t.Errorf("column %q value = %q, want complete %q", item.name, contentCell, wantValue)
				}
				offset += item.width
			}
		})
	}
}

func TestDashboardPagesRenderSharedSectionRules(t *testing.T) {
	tests := []struct {
		page PageID
		role Role
		want []string
	}{
		{page: PagePlans, role: RoleInfo, want: []string{"▌ PLANNED ", "REPO", "NEXT", "PLAN", "SLICES", "AGE"}},
		{page: PageNotes, role: RoleAccent, want: []string{"▌ OPEN NOTES ", "REPO", "NOTE", "STATUS", "PREVIEW"}},
		{page: PageSettings, role: RoleAccent, want: []string{"▌ GLOBAL RUNTIME DEFAULTS ", "VALUE", "SOURCE", "▌ REPOSITORY DEFAULTS ", "PULL_REQUEST"}},
		{page: PageDebug, role: RoleAccent, want: []string{"▌ UI ", " 10 ─", "▌ DOCTOR ", "▌ RUNTIME DEFAULTS "}},
	}
	for _, test := range tests {
		t.Run(string(test.page), func(t *testing.T) {
			model := frameFixtureModel(test.page, 120, 30)
			plain := Render(model)
			for _, want := range test.want {
				if !strings.Contains(plain, want) {
					t.Fatalf("%s frame missing section-rule content %q:\n%s", test.page, want, plain)
				}
			}

			model.Profile = ProfileANSI16
			styled := Render(model)
			if !strings.Contains(styled, Paint(ProfileANSI16, test.role, test.want[0])) {
				t.Fatalf("%s frame does not apply the section's semantic role: %q", test.page, styled)
			}
		})
	}
}

func TestRenderKeyHintsFooterIsStyledBoundedAndHonest(t *testing.T) {
	for _, page := range []PageID{PagePlans, PageNotes, PageSettings, PageDebug} {
		t.Run(string(page), func(t *testing.T) {
			const width = 70
			hints := footerHintsForPage(page, width)
			if len(hints) == 0 {
				t.Fatal("footer has no hints")
			}
			footer := renderKeyHintsFooter(ProfileANSI16, page, width)
			if got := visibleWidth(footer); got > width {
				t.Fatalf("footer width = %d, want <= %d: %q", got, width, footer)
			}

			entries := shortcutsForPage(page)
			for _, hint := range hints {
				honest := false
				for _, entry := range entries {
					if shortcutFooterKey(entry.key) == hint.key && entry.footerLabel == hint.label {
						honest = true
						break
					}
				}
				if !honest {
					t.Errorf("rendered hint %q %q does not come from shortcutsForPage", hint.key, hint.label)
				}
				for _, want := range []string{
					Paint(ProfileANSI16, RoleAccent, hint.key),
					Paint(ProfileANSI16, RoleNeutral2, hint.label),
				} {
					if !strings.Contains(footer, want) {
						t.Errorf("footer missing semantic style %q: %q", want, footer)
					}
				}
				for _, unavailable := range []string{"n", "e", "a", "t", "d", "g"} {
					if hint.key == unavailable {
						t.Errorf("footer advertises unavailable key %q", unavailable)
					}
				}
				if hint.key == "Enter" && hint.label == "edit" {
					t.Error("footer advertises unavailable Enter edit action")
				}
			}
		})
	}
}

func TestViewportReportsTruncatedRows(t *testing.T) {
	model := frameFixtureModel(PageNotes, 70, 8)
	items := make([]note.CatalogNote, 12)
	for index := range items {
		items[index] = note.CatalogNote{RepositoryID: "repo", RepositoryName: "repo", ID: fmt.Sprintf("note-%02d", index), Text: "preview"}
	}
	model.NoteSnapshot.Notes = items
	model.Selected = 8
	frame := Render(model)
	if !strings.Contains(frame, "+ ") || !strings.Contains(frame, " more  ↓") || !strings.Contains(frame, "note-08") {
		t.Fatalf("truncated viewport lacks indicator or selection:\n%s", frame)
	}
}

func TestNotesViewportKeepsSectionContextAndCountsOnlyHiddenNotes(t *testing.T) {
	model := frameFixtureModel(PageNotes, 70, 20)
	items := make([]note.CatalogNote, 30)
	for index := range items {
		items[index] = note.CatalogNote{RepositoryID: "repo", RepositoryName: "repo", ID: fmt.Sprintf("note-%02d", index), Text: "preview"}
	}
	model.NoteSnapshot.Notes = items
	model.Selected = len(items) - 1

	frame := Render(model)
	for _, want := range []string{"▌ OPEN NOTES ", "REPO", "NOTE", "> repo  note-29", "+ 17 more  ↓"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("selected-last Notes viewport missing %q:\n%s", want, frame)
		}
	}
	if visible := strings.Count(frame, "note-"); visible != 13 {
		t.Fatalf("visible note rows = %d, want 13:\n%s", visible, frame)
	}
	if strings.Contains(frame, "+ 20 more  ↓") {
		t.Fatalf("hidden count includes section structure:\n%s", frame)
	}
}

func TestNotesViewportKeepsWarningsVisibleWithManyNotes(t *testing.T) {
	model := frameFixtureModel(PageNotes, 70, 20)
	model.NoteSnapshot.Notes = make([]note.CatalogNote, 30)
	for index := range model.NoteSnapshot.Notes {
		model.NoteSnapshot.Notes[index] = note.CatalogNote{
			RepositoryID: "repo", RepositoryName: "repo",
			ID: fmt.Sprintf("note-%02d", index), Text: "preview",
		}
	}
	model.NoteSnapshot.Warnings = []note.CatalogWarning{{
		RepositoryID: "repo", RepositoryName: "repo", Err: errors.New("catalog damaged"),
	}}
	model.Selected = len(model.NoteSnapshot.Notes) - 1

	frame := Render(model)
	for _, want := range []string{"▌ OPEN NOTES ", "> repo  note-29", "▌ Warnings ", "repo: catalog damaged", "+ 19 more  ↓"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("constrained Notes viewport missing %q:\n%s", want, frame)
		}
	}
}

func TestSettingsViewportKeepsGlobalDefaultsVisibleWithManyRepositories(t *testing.T) {
	model := frameFixtureModel(PageSettings, 70, 20)
	model.SettingsSnapshot.RuntimeDefaults = []SettingsRuntimeDefault{
		{Name: "TAO_AGENT", Value: "pi", Source: "default"},
		{Name: "TAO_AUTO_REWORK", Value: "true", Source: "default"},
		{Name: "TAO_SESSION_TIMEOUT", Value: "20m", Source: "default"},
	}
	model.SettingsSnapshot.Repositories = make([]RepositorySetting, 30)
	for index := range model.SettingsSnapshot.Repositories {
		model.SettingsSnapshot.Repositories[index] = RepositorySetting{
			ID: fmt.Sprintf("repo-%02d", index), Name: "repo", Health: "ok", Root: "/repo",
		}
	}
	model.Selected = len(model.SettingsSnapshot.Repositories) - 1

	frame := Render(model)
	for _, want := range []string{"▌ GLOBAL RUNTIME DEFAULTS ", "TAO_AGENT", "TAO_AUTO_REWORK", "TAO_SESSION_TIMEOUT", "▌ REPOSITORY DEFAULTS ", "> repo (repo-29)", "+ 19 more  ↓"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("constrained Settings viewport missing %q:\n%s", want, frame)
		}
	}
}

func frameFixtureModel(page PageID, width, height int) Model {
	return Model{
		Page: page, Width: width, Height: height,
		Snapshot:     monitor.Snapshot{Rows: []monitor.Row{{RepositoryID: "repo", RepositoryName: "repo", PlanID: "plan", Status: plan.StatusPlanned}}},
		NoteSnapshot: note.Snapshot{Notes: []note.CatalogNote{{RepositoryID: "repo", RepositoryName: "repo", ID: "note", Text: "preview"}}},
		SettingsSnapshot: SettingsSnapshot{
			RuntimeDefaults: []SettingsRuntimeDefault{{Name: "TAO_AGENT", Value: "pi", Source: "default"}},
			Repositories:    []RepositorySetting{{ID: "repo", Name: "repo", Health: "ok", Root: "/repo"}},
		},
		DebugSnapshot: DebugSnapshot{SelectedAgent: "pi"},
	}
}
