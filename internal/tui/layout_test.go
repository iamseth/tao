package tui

import (
	"strconv"
	"strings"
	"testing"
)

func TestJoinRowFlexArithmeticAtFrameWidths(t *testing.T) {
	columns := []column{
		{name: "REPO", width: 12},
		{name: "PLAN", flex: true},
		{name: "STATUS", width: 8},
	}
	for _, width := range []int{199, 120, 100, 80, 70} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			resolved := resolveColumns(columns, width)
			wantFlex := width - 12 - 8 - 2*columnGapWidth
			if got := resolved[1].width; got != wantFlex {
				t.Fatalf("flex width at %d = %d, want %d", width, got, wantFlex)
			}
			row := joinRow(columns, []string{"repo", "plan", "running"}, width)
			if got := visibleWidth(row); got != width {
				t.Fatalf("row width at %d = %d, want %d: %q", width, got, width, row)
			}
		})
	}
}

func TestResolveColumnsClampsFlexWidth(t *testing.T) {
	columns := []column{
		{name: "ONE", width: 40},
		{name: "TWO", flex: true},
		{name: "THREE", width: 40},
	}
	resolved := resolveColumns(columns, 70)
	if got := resolved[1].width; got != minimumFlexWidth {
		t.Fatalf("flex width = %d, want minimum %d", got, minimumFlexWidth)
	}
	if resolved[1].width < 0 {
		t.Fatalf("flex width is negative: %d", resolved[1].width)
	}
}

func TestSectionRuleAtFrameWidths(t *testing.T) {
	for _, width := range []int{199, 120, 100, 80, 70} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			got := sectionRule(ProfileNone, RoleWarn, "NEEDS ATTENTION", 12, width)
			if visible := visibleWidth(got); visible != width {
				t.Fatalf("section rule width = %d, want %d: %q", visible, width, got)
			}
			if !strings.HasPrefix(got, "▌ NEEDS ATTENTION ") {
				t.Fatalf("section rule prefix = %q", got)
			}
			if !strings.HasSuffix(got, " 12 ─") {
				t.Fatalf("section rule suffix = %q", got)
			}
		})
	}
}

func TestSectionRuleUsesSemanticRoles(t *testing.T) {
	got := sectionRule(ProfileANSI16, RoleWarn, "RUNNING", 3, 70)
	warnSequence := colorSequence(Warn(ProfileANSI16), false)
	neutralSequence := colorSequence(N0(ProfileANSI16), false)
	if strings.Count(got, warnSequence) != 2 {
		t.Fatalf("section rule does not paint title and count with caller role: %q", got)
	}
	if strings.Count(got, neutralSequence) != 2 {
		t.Fatalf("section rule does not paint both rule runs neutral: %q", got)
	}
	if visibleWidth(got) != 70 {
		t.Fatalf("styled section rule width = %d, want 70", visibleWidth(got))
	}
}

func TestSectionRuleColumnsReplaceCount(t *testing.T) {
	columns := []column{
		{name: "USED", width: 6},
		{name: "LIMIT", width: 6},
	}
	got := sectionRuleColumns(ProfileNone, RoleAccent, "BUDGETS", columns, 70)
	if !strings.HasSuffix(got, " USED    LIMIT  ─") {
		t.Fatalf("column section rule suffix = %q", got)
	}
	if visibleWidth(got) != 70 {
		t.Fatalf("column section rule width = %d, want 70", visibleWidth(got))
	}
}

func TestMoreIndicatorIsDim(t *testing.T) {
	got := moreIndicator(ProfileANSI16, 4)
	if visible := visibleWidth(got); visible != visibleWidth("+ 4 more  ↓") {
		t.Fatalf("indicator width = %d: %q", visible, got)
	}
	if !strings.Contains(got, colorSequence(N2(ProfileANSI16), false)) {
		t.Fatalf("indicator does not use dim neutral role: %q", got)
	}
}

func TestBorderedPaneWidthLabelsAndSelectionColor(t *testing.T) {
	selected := borderedPane(ProfileANSI16, 30, "Selected plan", "tao", true, []string{"body"})
	if len(selected) != 3 {
		t.Fatalf("pane has %d lines, want 3", len(selected))
	}
	for index, line := range selected {
		if got := visibleWidth(line); got != 30 {
			t.Fatalf("pane line %d width = %d, want 30: %q", index, got, line)
		}
	}
	if !strings.Contains(selected[0], "Selected plan") || !strings.Contains(selected[0], "tao") {
		t.Fatalf("top border omits labels: %q", selected[0])
	}
	if !strings.Contains(selected[0], colorSequence(Accent(ProfileANSI16), false)) {
		t.Fatalf("selected pane border is not accented: %q", selected[0])
	}

	unselected := borderedPane(ProfileANSI16, 30, "Plan", "", false, nil)
	if !strings.Contains(unselected[0], colorSequence(N1(ProfileANSI16), false)) {
		t.Fatalf("unselected pane border is not neutral: %q", unselected[0])
	}
}
