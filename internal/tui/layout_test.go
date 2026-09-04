package tui

import (
	"strconv"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/term/cells"
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
			if got := cells.Width(row); got != width {
				t.Fatalf("row width at %d = %d, want %d: %q", width, got, width, row)
			}
		})
	}
}

func TestFitColumnsUsesDeclaredPriorityAndKeepsRequiredOrder(t *testing.T) {
	columns := []column{
		{name: "CONTEXT", width: 8, priority: 20},
		{name: "CORE", width: 20, flex: true, required: true, minimum: 6},
		{name: "LOW", width: 8, priority: 10},
		{name: "END", width: 3, required: true, priority: 30},
	}
	if got := columnNames(fitColumns(columns, 22)); strings.Join(got, ",") != "CONTEXT,CORE,END" {
		t.Fatalf("22-cell columns = %v, want lowest-priority optional column removed", got)
	}
	if got := columnNames(fitColumns(columns, 12)); strings.Join(got, ",") != "CORE,END" {
		t.Fatalf("12-cell columns = %v, want only required columns in original order", got)
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
			got := sectionRule(ProfileNone, RolePlanNow, "NOW", 12, width)
			if visible := cells.Width(got); visible != width {
				t.Fatalf("section rule width = %d, want %d: %q", visible, width, got)
			}
			if !strings.HasPrefix(got, "▌ NOW ") {
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
	if cells.Width(got) != 70 {
		t.Fatalf("styled section rule width = %d, want 70", cells.Width(got))
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
	if cells.Width(got) != 70 {
		t.Fatalf("column section rule width = %d, want 70", cells.Width(got))
	}
}

func TestMoreIndicatorIsDim(t *testing.T) {
	got := moreIndicator(ProfileANSI16, 4)
	if visible := cells.Width(got); visible != cells.Width("+ 4 more  ↓") {
		t.Fatalf("indicator width = %d: %q", visible, got)
	}
	if !strings.Contains(got, colorSequence(N2(ProfileANSI16), false)) {
		t.Fatalf("indicator does not use dim neutral role: %q", got)
	}
}
