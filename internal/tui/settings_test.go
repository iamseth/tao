package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
	"github.com/iamseth/tao/internal/theme"
)

func TestSettingsRejectedDiagnosticsStayVisibleAtNarrowWidths(t *testing.T) {
	for _, width := range []int{40, 70, 120} {
		model := Model{Page: PageSettings, Width: width, Height: 80, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: []SettingsRuntimeDefault{
			{Name: "TAO_AGENT", Value: "pi", Source: "invalid", Warning: "invalid provider\n\t\x1b[31mconfiguration; rejected"},
			{Name: "TAO_THEME", Value: "tokyonight", Source: "default", Warning: "invalid theme; using default"},
			{Name: "TAO_BUDGET_PLAN_COST_WARN", Value: "20", Source: "invalid", Warning: "invalid budget; rejected"},
		}}}
		frame := strings.TrimPrefix(Render(model), "\x1b[H\x1b[2J")
		for _, want := range []string{"TAO_AGENT", "(rejected)", "rejected on consumption:", "using default", "invalid budget"} {
			if !strings.Contains(strings.Join(strings.Fields(frame), " "), want) {
				t.Errorf("width %d missing %q:\n%s", width, want, frame)
			}
		}
		if strings.Contains(frame, "\x1b") || strings.Contains(frame, "\t") {
			t.Fatalf("unsanitized diagnostic: %q", frame)
		}
		if got := settingsBudgetValue(model.SettingsSnapshot.RuntimeDefaults[2], true); got != "(rejected)" {
			t.Fatalf("rejected budget displayed as accepted placeholder: %s", got)
		}
	}
}

func TestSettingsInvalidBaselineDoesNotHideExplicitRepositoryDefault(t *testing.T) {
	model := Model{Page: PageSettings, Width: 100, SettingsSnapshot: SettingsSnapshot{
		InheritedPullRequest: false, InheritedPullRequestInvalid: true,
		RuntimeDefaults: []SettingsRuntimeDefault{{Name: "TAO_PULL_REQUEST", Value: "false", Source: "invalid", Warning: "invalid boolean; rejected"}},
		Repositories:    []RepositorySetting{{ID: "repo-a", Name: "alpha", PullRequest: new(false)}},
	}}
	lines, _ := renderSettingsOverrides(model)
	if !lineContainsAll(lines, "TAO_PULL_REQUEST", "false", "← alpha") || !lineContainsAll(lines, "TAO_PULL_REQUEST", "(rejected)") {
		t.Fatalf("invalid placeholder suppressed accepted repository default: %s", strings.Join(lines, "\n"))
	}
}

func TestRenderSettingsShowsGlobalAndRepositoryDefaults(t *testing.T) {
	explicit := true
	frame := Render(Model{
		Page: PageSettings, Selected: 1, Width: 120, Height: 30,
		SettingsSnapshot: SettingsSnapshot{
			InheritedPullRequest: false,
			RuntimeDefaults: []SettingsRuntimeDefault{
				{Name: "TAO_AGENT", Value: "pi", Source: "default"},
				{Name: "TAO_PULL_REQUEST", Value: "false", Source: "env", Warning: "example warning"},
			},
			Repositories: []RepositorySetting{
				{ID: "alpha-123", Name: "alpha", Root: "/repos/alpha", Health: "ok", Finding: "ok", PullRequest: &explicit},
				{ID: "beta-456", Name: "beta", Root: "/repos/beta", Health: "missing_root", Finding: "repo root does not exist"},
			},
		},
	})
	for _, want := range []string{
		"tao │ notes  plans ▸settings  debug", "OVERRIDES", "EXECUTION · all default", "Agent", "← env", "← alpha", "warning: example warning",
		"REPOSITORY DEFAULTS", "alpha", "● ok", "pr=on", "/repos/alpha", "> beta", "● missing root", "pr=inherit", "finding: repo root does not exist",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("settings frame missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "2 repositories") || strings.Contains(frame, "need attention") {
		t.Fatalf("settings frame unexpectedly contains a summary:\n%s", frame)
	}
}

func TestSettingsSectionsUseMutedHeadingColor(t *testing.T) {
	explicit := true
	model := Model{
		Page: PageSettings, Width: 120, Height: 60, Profile: theme.ProfileTrueColor,
		SettingsSnapshot: SettingsSnapshot{
			InheritedPullRequest: false,
			RuntimeDefaults: []SettingsRuntimeDefault{
				{Name: "TAO_AGENT", Value: "pi", Source: "default"},
				{Name: "TAO_PULL_REQUEST", Value: "true", Source: "env"},
				{Name: "TAO_BUDGET_PLAN_COST_WARN", Value: "20"},
			},
			Repositories: []RepositorySetting{{ID: "repo", Name: "repo", Health: "missing_root", PullRequest: &explicit}},
		},
	}
	frame := Render(model)
	for _, title := range []string{"OVERRIDES", "EXECUTION · all default", "BUDGET", "REPOSITORY DEFAULTS"} {
		want := theme.Default().Palette(theme.ProfileTrueColor).Paint(theme.RoleSettingsSection, "▌ "+title+" ")
		if !strings.Contains(frame, want) {
			t.Errorf("settings section %q does not use the settings section color: %q", title, frame)
		}
	}
	if !strings.Contains(frame, theme.Default().Palette(theme.ProfileTrueColor).Paint(theme.RoleWarn, "●")) {
		t.Errorf("settings warning content lost its warning color: %q", frame)
	}
}

func TestRenderSettingsDefaultsUsesResponsivePairGrid(t *testing.T) {
	rows := []SettingsRuntimeDefault{
		{Name: "TAO_COMMIT_POLICY", Value: "slice", Source: "default"},
		{Name: "TAO_EXECUTION_MODE", Value: "isolated", Source: "default"},
		{Name: "TAO_AGENT", Value: "pi", Source: "default"},
		{Name: "TAO_SESSION_TIMEOUT", Value: "20m", Source: "default"},
		{Name: "TAO_SESSION_WARN_PERCENT", Value: "80", Source: "default"},
		{Name: "TAO_PULL_REQUEST", Value: "false", Source: "default"},
	}
	wide, _ := renderSettingsDefaultGroups(Model{Page: PageSettings, Width: 120, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: rows}})
	wideText := strings.Join(wide, "\n")
	if !strings.Contains(wideText, "EXECUTION · all default") || !strings.Contains(wideText, "WORKFLOW · all default") {
		t.Fatalf("group default annotations are not truthful:\n%s", wideText)
	}
	if !lineContainsAll(wide, "Commit policy", "Execution mode") || !lineContainsAll(wide, "Agent", "Session timeout") {
		t.Fatalf("wide Settings defaults do not render paired rows:\n%s", wideText)
	}

	narrow, _ := renderSettingsDefaultGroups(Model{Page: PageSettings, Width: 35, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: rows}})
	for _, lines := range [][]string{wide, narrow} {
		if text := strings.Join(lines, "\n"); strings.Count(text, "Session warn percent") != 1 || !strings.Contains(text, "80") {
			t.Fatalf("warning percentage missing or duplicated: %s", text)
		}
	}
	if got, _ := settingsDefaultGroupForName("TAO_SESSION_WARN_PERCENT"); got != settingsGroupExecution {
		t.Fatalf("warning percentage group = %q", got)
	}
	for _, labels := range [][2]string{{"Commit policy", "Execution mode"}, {"Agent", "Session timeout"}} {
		if lineContainsAll(narrow, labels[0], labels[1]) {
			t.Fatalf("narrow Settings defaults kept pair %q/%q on one line:\n%s", labels[0], labels[1], strings.Join(narrow, "\n"))
		}
	}
}

func TestRenderSettingsUnavailableRuntimeViewportKeepsSectionContext(t *testing.T) {
	explicit := true
	model := Model{
		Page: PageSettings, Width: 70, Height: 14,
		SettingsSnapshot: SettingsSnapshot{
			CollectionError:      "runtime status collection failed",
			InheritedPullRequest: false,
			Repositories: []RepositorySetting{{
				ID: "override", Name: "override-repo", Health: "ok", Root: "/override", PullRequest: &explicit,
			}},
		},
	}
	for range 29 {
		model.SettingsSnapshot.Repositories = append(model.SettingsSnapshot.Repositories, RepositorySetting{
			ID: "repo", Name: "repo", Health: "ok", Root: "/repo",
		})
	}

	frame := Render(model)
	for _, want := range []string{
		"OVERRIDES", "RUNTIME DEFAULTS", "Runtime defaults unavailable.",
		"REPOSITORY DEFAULTS", "runtime status collection failed", "> override-repo", "+ 24 more  ↓",
	} {
		if !strings.Contains(frame, want) {
			t.Errorf("constrained Settings viewport missing %q:\n%s", want, frame)
		}
	}
	if count := strings.Count(frame, "OVERRIDES"); count != 1 {
		t.Errorf("constrained Settings viewport rendered OVERRIDES %d times, want once:\n%s", count, frame)
	}
}

func lineContainsAll(lines []string, values ...string) bool {
	for _, line := range lines {
		matches := true
		for _, value := range values {
			matches = matches && strings.Contains(line, value)
		}
		if matches {
			return true
		}
	}
	return false
}

func TestSettingsDefaultsClassifyAndRenderEveryRuntimeStatusOnce(t *testing.T) {
	statuses := runtimeconfig.LoadEnv(nil).Status()
	rows := make([]SettingsRuntimeDefault, 0, len(statuses))
	for _, status := range statuses {
		rows = append(rows, SettingsRuntimeDefault{Name: status.Name, Value: status.Value, Source: "default"})
	}
	groups := settingsDefaultGroups(rows)
	counts := make(map[string]int)
	for _, group := range groups {
		for _, row := range group.rows {
			counts[row.Name]++
		}
	}
	renderedLines, _ := renderSettingsDefaultGroups(Model{Page: PageSettings, Width: 200, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: rows}})
	rendered := strings.Join(renderedLines, "\n")
	for _, status := range statuses {
		if strings.HasPrefix(status.Name, "TAO_BUDGET_") || strings.HasPrefix(status.Name, "TAO_MAX_SLICE_") {
			if counts[status.Name] != 0 {
				t.Errorf("budget %s rendered in defaults", status.Name)
			}
			continue
		}
		if _, known := settingsDefaultGroupForName(status.Name); !known {
			t.Errorf("runtime setting %s is not explicitly classified", status.Name)
		}
		if counts[status.Name] != 1 {
			t.Errorf("runtime setting %s grouped %d times, want once", status.Name, counts[status.Name])
		}
		label := humanizeSettingsName(status.Name)
		got := strings.Count(rendered, label)
		for _, other := range statuses {
			otherLabel := humanizeSettingsName(other.Name)
			if otherLabel != label && strings.Contains(otherLabel, label) {
				got -= strings.Count(rendered, otherLabel)
			}
		}
		if got != 1 {
			t.Errorf("runtime setting %s rendered %d times, want once:\n%s", status.Name, got, rendered)
		}
	}
}

func TestRenderSettingsShowsRuntimeOverridesExactlyOnce(t *testing.T) {
	frame := Render(Model{
		Page: PageSettings, Width: 120, Height: 40,
		SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: []SettingsRuntimeDefault{
			{Name: "TAO_AGENT", Value: "pi", Source: "default"},
			{Name: "TAO_PULL_REQUEST", Value: "false", Source: "env"},
			{Name: "TAO_REVIEW", Value: "true", Source: "default"},
			{Name: "TAO_UPDATE", Value: "warn", Source: "default", Warning: "invalid value; using fallback"},
		}},
	})

	for _, name := range []string{"TAO_PULL_REQUEST", "TAO_UPDATE"} {
		got := strings.Count(frame, name) + strings.Count(frame, humanizeSettingsName(name))
		if got != 1 {
			t.Errorf("runtime setting %s rendered %d times, want once:\n%s", name, got, frame)
		}
	}
	if !strings.Contains(frame, "WORKFLOW") || strings.Contains(frame, "WORKFLOW · all default") {
		t.Errorf("Workflow heading does not reflect its environment override:\n%s", frame)
	}
	if got := strings.Count(frame, "warning: invalid value; using fallback"); got != 1 {
		t.Errorf("warning rendered %d times, want once:\n%s", got, frame)
	}
	if got := strings.Count(frame, "Agent"); got != 1 {
		t.Errorf("remaining default Agent rendered %d times, want once:\n%s", got, frame)
	}
}

func settingsBudgetWarnRows() []SettingsRuntimeDefault {
	return []SettingsRuntimeDefault{
		{Name: "TAO_BUDGET_PLAN_TOOL_CALLS_WARN", Value: "400", Source: "default"},
		{Name: "TAO_BUDGET_SLICE_ERRORED_MESSAGES_WARN", Value: "0", Source: "default"},
		{Name: "TAO_BUDGET_PLAN_OUTPUT_TOKENS_WARN", Value: "150000", Source: "default"},
		{Name: "TAO_BUDGET_SLICE_COST_WARN", Value: "5.00", Source: "default"},
		{Name: "TAO_BUDGET_PLAN_ASSISTANT_MESSAGES_WARN", Value: "300", Source: "default"},
		{Name: "TAO_BUDGET_SLICE_OUTPUT_TOKENS_WARN", Value: "40000", Source: "default"},
		{Name: "TAO_BUDGET_PLAN_ERRORED_MESSAGES_WARN", Value: "2", Source: "default"},
		{Name: "TAO_BUDGET_SLICE_TOOL_CALLS_WARN", Value: "120", Source: "default"},
		{Name: "TAO_BUDGET_PLAN_COST_WARN", Value: "20.000", Source: "default"},
		{Name: "TAO_BUDGET_SLICE_ASSISTANT_MESSAGES_WARN", Value: "80", Source: "default"},
	}
}

func settingsBudgetRows() []SettingsRuntimeDefault {
	return append(settingsBudgetWarnRows(),
		SettingsRuntimeDefault{Name: "TAO_BUDGET_SLICE_OUTPUT_TOKENS_STOP", Value: "60000", Source: "env"},
		SettingsRuntimeDefault{Name: "TAO_BUDGET_SLICE_COST_STOP", Value: "disabled", Source: "default"},
	)
}

func settingsBudgetLineIndex(lines []string, label string) int {
	for index, line := range lines {
		if strings.Contains(line, label) {
			return index
		}
	}
	return -1
}

func TestRenderSettingsBudgetsPairsScopesAndKeepsZeroTruthful(t *testing.T) {
	lines, _ := renderSettingsBudgets(Model{Page: PageSettings, Width: 120, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: settingsBudgetRows()}})
	if len(lines) < 2 {
		t.Fatalf("budget section missing heading:\n%s", strings.Join(lines, "\n"))
	}
	heading := lines[1]
	position := 0
	for _, header := range []string{"BUDGET", "SLICE WARN", "SLICE STOP", "PLAN WARN", "PLAN STOP"} {
		next := strings.Index(heading[position:], header)
		if next < 0 {
			t.Fatalf("budget heading missing %q in order:\n%s", header, heading)
		}
		position += next + len(header)
	}
	if strings.Contains(strings.Join(lines, "\n"), "BUDGET WARNINGS") {
		t.Fatalf("budget section kept the old title:\n%s", strings.Join(lines, "\n"))
	}
	for _, want := range []string{
		"Output tokens 40 000 60 000 150 000 -",
		"Cost 5.00 disabled 20.000 -",
		"Tool calls 120 - 400 -",
		"Assistant messages 80 - 300 -",
		"Errored messages 0 - 2 -",
	} {
		if !lineContainsAll(collapseSpaces(lines), want) {
			t.Errorf("budget row missing exact cells %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	joined := strings.ToLower(strings.Join(lines, "\n"))
	if strings.Contains(joined, "unlimited") || strings.Contains(joined, "none") {
		t.Fatalf("zero budget rendered as a sentinel:\n%s", joined)
	}
}

// collapseSpaces joins each line's fields with one space so cell expectations
// stay independent of column padding.
func collapseSpaces(lines []string) []string {
	collapsed := make([]string, 0, len(lines))
	for _, line := range lines {
		collapsed = append(collapsed, strings.Join(strings.Fields(line), " "))
	}
	return collapsed
}

func TestRenderSettingsBudgetsRendersDisabledForUnsetSliceStop(t *testing.T) {
	lines, _ := renderSettingsBudgets(Model{Page: PageSettings, Width: 120, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: settingsBudgetWarnRows()}})
	for _, want := range []string{
		"Output tokens 40 000 disabled 150 000 -",
		"Cost 5.00 disabled 20.000 -",
		"Tool calls 120 - 400 -",
	} {
		if !lineContainsAll(collapseSpaces(lines), want) {
			t.Errorf("unset slice STOP not rendered as disabled %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
}

func TestRenderSettingsBudgetsShowsSetAliasAsDeprecatedDiagnostic(t *testing.T) {
	rows := append(settingsBudgetWarnRows(),
		SettingsRuntimeDefault{Name: "TAO_BUDGET_SLICE_COST", Value: "7", Source: "env", Warning: "deprecated; use TAO_BUDGET_SLICE_COST_WARN"},
		SettingsRuntimeDefault{Name: "TAO_MAX_SLICE_COST", Value: "10", Source: "env", Warning: "deprecated; use TAO_BUDGET_SLICE_COST_STOP"},
		SettingsRuntimeDefault{Name: "TAO_BUDGET_PLAN_COST_WARN", Value: "bad", Source: "invalid", Warning: "invalid budget; rejected"},
	)
	lines, _ := renderSettingsBudgets(Model{Page: PageSettings, Width: 160, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: rows}})
	costIndex := settingsBudgetLineIndex(lines, "Cost")
	if costIndex < 0 {
		t.Fatalf("cost row missing:\n%s", strings.Join(lines, "\n"))
	}
	toolIndex := settingsBudgetLineIndex(lines, "Tool calls")
	if toolIndex < 0 {
		t.Fatalf("tool calls row missing:\n%s", strings.Join(lines, "\n"))
	}
	between := strings.Join(lines[costIndex+1:toolIndex], "\n")
	for _, want := range []string{
		"plan warn rejected on consumption: invalid budget; rejected",
		"deprecated TAO_BUDGET_SLICE_COST: deprecated; use TAO_BUDGET_SLICE_COST_WARN",
		"deprecated TAO_MAX_SLICE_COST: deprecated; use TAO_BUDGET_SLICE_COST_STOP",
	} {
		if !strings.Contains(between, want) {
			t.Errorf("cost diagnostics missing %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	if got := strings.Count(strings.Join(lines, "\n"), "Cost"); got != 1 {
		t.Errorf("alias rows produced %d Cost rows, want 1:\n%s", got, strings.Join(lines, "\n"))
	}
}

func TestSettingsDefaultGroupsExcludeBudgetRows(t *testing.T) {
	rows := []SettingsRuntimeDefault{
		{Name: "TAO_UPDATE", Value: "warn", Source: "default"},
		{Name: "TAO_DANGEROUSLY_SKIP_PERMISSIONS", Value: "false", Source: "default"},
		{Name: "TAO_MAX_SLICE_COST", Value: "10", Source: "env", Warning: "deprecated; use TAO_BUDGET_SLICE_COST_STOP"},
		{Name: "TAO_MAX_SLICE_OUTPUT_TOKENS", Value: "70000", Source: "env", Warning: "deprecated; use TAO_BUDGET_SLICE_OUTPUT_TOKENS_STOP"},
		{Name: "TAO_BUDGET_SLICE_COST_STOP", Value: "10", Source: "env"},
		{Name: "TAO_BUDGET_SLICE_COST_WARN", Value: "5", Source: "default"},
	}
	for _, group := range settingsDefaultGroups(rows) {
		if group.key == settingsGroupSafety && group.hasOverride {
			t.Errorf("budget alias marked the Safety group as overridden")
		}
		for _, row := range group.rows {
			if strings.HasPrefix(row.Name, "TAO_MAX_SLICE_") || strings.HasPrefix(row.Name, "TAO_BUDGET_") {
				t.Errorf("budget row %s grouped under %s", row.Name, group.key)
			}
		}
	}
	for _, name := range []string{"TAO_MAX_SLICE_COST", "TAO_MAX_SLICE_OUTPUT_TOKENS"} {
		if _, known := settingsDefaultGroupForName(name); known {
			t.Errorf("%s still classified as a default group setting", name)
		}
	}
	lines, _ := renderSettingsDefaultGroups(Model{Page: PageSettings, Width: 120, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: rows}})
	rendered := strings.Join(lines, "\n")
	for _, absent := range []string{"Slice cost cap", "Slice output cap", "Max slice cost", "TAO_MAX_SLICE_COST"} {
		if strings.Contains(rendered, absent) {
			t.Errorf("Safety group still renders %q:\n%s", absent, rendered)
		}
	}
	if !strings.Contains(rendered, "SAFETY / UPDATE · all default") {
		t.Errorf("Safety group lost its all-default heading:\n%s", rendered)
	}
}

func TestRenderSettingsBudgetsDropsStopColumnsBeforeWrapping(t *testing.T) {
	tests := []struct {
		width   int
		present []string
		absent  []string
	}{
		{width: 120, present: []string{"SLICE WARN", "SLICE STOP", "PLAN WARN", "PLAN STOP"}},
		{width: 60, present: []string{"SLICE WARN", "SLICE STOP", "PLAN WARN"}, absent: []string{"PLAN STOP"}},
		{width: 40, present: []string{"SLICE WARN", "PLAN WARN"}, absent: []string{"SLICE STOP", "PLAN STOP"}},
	}
	for _, test := range tests {
		lines, section := renderSettingsBudgets(Model{Page: PageSettings, Width: test.width, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: settingsBudgetRows()}})
		heading := lines[1]
		for _, want := range test.present {
			if !strings.Contains(heading, want) {
				t.Errorf("width %d heading missing %q:\n%s", test.width, want, heading)
			}
		}
		for _, absent := range test.absent {
			if strings.Contains(heading, absent) {
				t.Errorf("width %d heading kept %q instead of dropping it:\n%s", test.width, absent, heading)
			}
		}
		if len(section.contentLines) != 5 {
			t.Errorf("width %d wrapped budget rows: %d content lines, want 5:\n%s", test.width, len(section.contentLines), strings.Join(lines, "\n"))
		}
		for _, line := range lines {
			if cells.Width(line) > test.width {
				t.Errorf("width %d line overflows: %q", test.width, line)
			}
		}
		if test.width == 60 && !lineContainsAll(collapseSpaces(lines), "Output tokens 40 000 60 000 150 000") {
			t.Errorf("width 60 lost the slice STOP value:\n%s", strings.Join(lines, "\n"))
		}
	}
}

func TestSettingsRepositoryRowsUseCellAlignmentAndHomeAbbreviation(t *testing.T) {
	model := Model{Page: PageSettings, Width: 100, Selected: 1, SettingsSnapshot: SettingsSnapshot{
		DisplayHome: "/Users/example",
		Repositories: []RepositorySetting{
			{ID: "beta", Name: "βeta", Root: "/Users/example/src/βeta", Health: "ok"},
			{ID: "nihongo", Name: "日本語", Root: "/Users/example/src/日本語", Health: "missing_root"},
		},
	}}
	lines, selected, _ := renderSettingsPage(model)
	if selected < 0 || !strings.HasPrefix(lines[selected], "> ") || !strings.Contains(lines[selected], "日本語") {
		t.Fatalf("selected repository line = %d %q", selected, lines[selected])
	}
	var healthOffsets []int
	for _, line := range lines {
		if strings.Contains(line, "βeta") || strings.Contains(line, "日本語") {
			byteOffset := strings.Index(line, "●")
			if byteOffset < 0 {
				t.Fatalf("repository row lacks semantic health dot: %q", line)
			}
			healthOffsets = append(healthOffsets, cells.Width(line[:byteOffset]))
			if !strings.Contains(line, "~/src/") {
				t.Errorf("repository root is not home-abbreviated: %q", line)
			}
		}
	}
	if len(healthOffsets) != 2 || healthOffsets[0] != healthOffsets[1] {
		t.Fatalf("Unicode repository health columns are not cell-aligned: %v\n%s", healthOffsets, strings.Join(lines, "\n"))
	}
}

func TestSettingsRepositoryColumnsKeepIdentityHealthAndEditablePR(t *testing.T) {
	model := Model{Page: PageSettings, SettingsSnapshot: SettingsSnapshot{Repositories: []RepositorySetting{{
		ID: "repository-alpha-long", Name: "repository-alpha-long", Health: "missing_root",
		Root: "/long/repository/root/optional-context",
	}}}}
	for _, width := range []int{100, 80, 70, 44} {
		model.Width = width
		lines, _, _ := renderSettingsPage(model)
		joined := strings.Join(lines, "\n")
		for _, want := range []string{"REPOSITORY DEFAULTS", "repository-alpha-long", "HEALTH", "PR", "pr=inherit", "●"} {
			if !strings.Contains(joined, want) {
				t.Errorf("width %d Settings rows missing %q:\n%s", width, want, joined)
			}
		}
		if width == 100 {
			if !strings.Contains(joined, "/long/repository") {
				t.Errorf("wide Settings row shed root prematurely:\n%s", joined)
			}
		} else if strings.Contains(joined, "ROOT") || strings.Contains(joined, "/long/repository") {
			t.Errorf("width %d Settings row retained optional root:\n%s", width, joined)
		}
		if width == 44 && strings.Contains(joined, "missing root") {
			t.Errorf("44-cell Settings row retained health detail instead of its semantic indicator:\n%s", joined)
		}
	}
}

func TestSettingsRepositoryRootAbbreviationIsBoundarySafe(t *testing.T) {
	for _, test := range []struct {
		name string
		root string
		home string
		want string
	}{
		{name: "home", root: "/Users/example", home: "/Users/example", want: "~"},
		{name: "descendant", root: "/Users/example/src/tao", home: "/Users/example", want: "~/src/tao"},
		{name: "sibling prefix", root: "/Users/example-other/src", home: "/Users/example", want: "/Users/example-other/src"},
		{name: "unavailable home", root: "/Users/example/src", want: "/Users/example/src"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := settingsRepositoryRoot(test.root, test.home); got != test.want {
				t.Fatalf("settingsRepositoryRoot(%q, %q) = %q, want %q", test.root, test.home, got, test.want)
			}
		})
	}
}

func TestSettingsRepositoryRowsUseSemanticStyles(t *testing.T) {
	repository := RepositorySetting{ID: "beta", Name: "βeta", Health: "ok"}
	if got := settingsStyledRepositoryName(theme.Default().Palette(theme.ProfileANSI16), repository); got != theme.Default().Palette(theme.ProfileANSI16).Paint(theme.RepoColor("beta"), "βeta") {
		t.Fatalf("styled repository name = %q", got)
	}
	if got := settingsRepositoryHealth(theme.Default().Palette(theme.ProfileANSI16), "missing_root"); !strings.Contains(got, theme.Default().Palette(theme.ProfileANSI16).Paint(theme.RoleWarn, "●")) || !strings.Contains(got, "missing root") {
		t.Fatalf("styled unhealthy status = %q", got)
	}
}

func TestSettingsDefaultPairsUseSemanticStyles(t *testing.T) {
	labelWidth := cells.Width("Pull request")
	for _, test := range []struct {
		row  SettingsRuntimeDefault
		role theme.Role
	}{
		{row: SettingsRuntimeDefault{Name: "TAO_PULL_REQUEST", Value: "true"}, role: theme.RoleSuccess},
		{row: SettingsRuntimeDefault{Name: "TAO_PULL_REQUEST", Value: "false"}, role: theme.RoleNeutral2},
		{row: SettingsRuntimeDefault{Name: "TAO_PULL_REQUEST", Value: "none"}, role: theme.RoleNeutral2},
	} {
		got := settingsDefaultPair(theme.Default().Palette(theme.ProfileANSI16), test.row, labelWidth, false)
		if !strings.Contains(got, theme.Default().Palette(theme.ProfileANSI16).Paint(theme.RoleNeutral2, "Pull request")) || !strings.Contains(got, theme.Default().Palette(theme.ProfileANSI16).Paint(test.role, test.row.Value)) {
			t.Errorf("styled default pair = %q", got)
		}
	}
}

func TestRenderSettingsOverridesIncludesOnlyTruthfulOverridesAndWarnings(t *testing.T) {
	explicitFalse := false
	explicitTrue := true
	tests := []struct {
		name     string
		snapshot SettingsSnapshot
		want     []string
		absent   []string
	}{
		{
			name: "environment",
			snapshot: SettingsSnapshot{RuntimeDefaults: []SettingsRuntimeDefault{
				{Name: "TAO_AGENT", Value: "claude", Source: "env"},
				{Name: "TAO_REVIEW", Value: "true", Source: "default"},
			}},
			want:   []string{"OVERRIDES", "TAO_AGENT", "claude", "← env"},
			absent: []string{"TAO_REVIEW"},
		},
		{
			name: "differing repository",
			snapshot: SettingsSnapshot{
				InheritedPullRequest: false,
				Repositories: []RepositorySetting{
					{ID: "alpha", Name: "alpha", PullRequest: &explicitFalse},
					{ID: "beta", Name: "βeta", PullRequest: &explicitTrue},
				},
			},
			want:   []string{"OVERRIDES", "TAO_PULL_REQUEST", "true", "← βeta"},
			absent: []string{"← alpha", "explicit"},
		},
		{
			name: "warning only",
			snapshot: SettingsSnapshot{RuntimeDefaults: []SettingsRuntimeDefault{
				{Name: "TAO_UPDATE", Value: "warn", Source: "default", Warning: "invalid value; using fallback"},
			}},
			want: []string{"OVERRIDES", "TAO_UPDATE", "warn", "← default", "warning: invalid value; using fallback"},
		},
		{
			name: "empty",
			snapshot: SettingsSnapshot{
				InheritedPullRequest: false,
				RuntimeDefaults:      []SettingsRuntimeDefault{{Name: "TAO_AGENT", Value: "pi", Source: "default"}},
				Repositories:         []RepositorySetting{{ID: "alpha", Name: "alpha", PullRequest: &explicitFalse}},
			},
			absent: []string{"OVERRIDES"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines, _ := renderSettingsOverrides(Model{Page: PageSettings, Width: 120, SettingsSnapshot: test.snapshot})
			got := strings.Join(lines, "\n")
			for _, want := range test.want {
				if !strings.Contains(got, want) {
					t.Errorf("overrides missing %q:\n%s", want, got)
				}
			}
			for _, absent := range test.absent {
				if strings.Contains(got, absent) {
					t.Errorf("overrides unexpectedly contain %q:\n%s", absent, got)
				}
			}
		})
	}
}

func TestRenderSettingsOverridesUsesSemanticStyles(t *testing.T) {
	explicitTrue := true
	model := Model{
		Page: PageSettings, Width: 120, Profile: theme.ProfileANSI16,
		SettingsSnapshot: SettingsSnapshot{
			InheritedPullRequest: false,
			RuntimeDefaults: []SettingsRuntimeDefault{
				{Name: "TAO_AGENT", Value: "claude", Source: "env", Warning: "fallback warning"},
			},
			Repositories: []RepositorySetting{{ID: "beta", Name: "βeta", PullRequest: &explicitTrue}},
		},
	}
	lines, _ := renderSettingsOverrides(model)
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		theme.Default().Palette(theme.ProfileANSI16).Paint(theme.RoleNeutral5, "claude"),
		theme.Default().Palette(theme.ProfileANSI16).Paint(theme.RoleWarn, "warning: fallback warning"),
		theme.Default().Palette(theme.ProfileANSI16).Paint(theme.RepoColor("beta"), "← βeta"),
	} {
		if !strings.Contains(got, want) {
			t.Errorf("styled overrides missing %q: %q", want, got)
		}
	}
}

func TestPullRequestSettingCycleIncludesInheritedState(t *testing.T) {
	first := nextPullRequestSetting(nil)
	second := nextPullRequestSetting(first)
	third := nextPullRequestSetting(second)
	if first == nil || !*first || second == nil || *second || third != nil {
		t.Fatalf("setting cycle = %#v -> %#v -> %#v, want true -> false -> nil", first, second, third)
	}
}

func TestSettingsUpdateRequiresConfirmationAndRefreshesSnapshot(t *testing.T) {
	service := &fakeSettingsService{snapshot: SettingsSnapshot{
		Repositories: []RepositorySetting{{ID: "repo-a", Name: "alpha", Health: "ok"}},
	}}
	state := loopState{page: PageSettings, settingsSnapshot: service.snapshot}
	app := App{Settings: service}

	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'p'}); quit || state.confirm == nil || service.calls != 0 {
		t.Fatalf("settings edit did not open confirmation: quit=%t confirm=%#v calls=%d", quit, state.confirm, service.calls)
	}
	if !strings.Contains(state.confirm.message, "pr=inherit to pr=on") {
		t.Fatalf("settings confirmation = %q", state.confirm.message)
	}
	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'y'}); quit || state.confirm != nil || service.calls != 1 {
		t.Fatalf("settings confirmation did not update: quit=%t confirm=%#v calls=%d", quit, state.confirm, service.calls)
	}
	value := state.settingsSnapshot.Repositories[0].PullRequest
	if value == nil || !*value || !strings.Contains(state.settingsMessage, "pr=on") {
		t.Fatalf("updated settings = value=%v message=%q", value, state.settingsMessage)
	}
}

func TestSettingsShortcutsAreContextAware(t *testing.T) {
	frame := Render(Model{Page: PageSettings, Width: 68, Height: 14, ShowShortcuts: true})
	for _, want := range []string{"Keyboard shortcuts", "Select repository", "Cycle pull-request default", "Switch tabs"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("settings shortcuts missing %q:\n%s", want, frame)
		}
	}
	for _, unavailable := range []string{"Run selected plan", "Search plans and notes", "Scroll diagnostics"} {
		if strings.Contains(frame, unavailable) {
			t.Fatalf("settings shortcuts contain unavailable action %q:\n%s", unavailable, frame)
		}
	}
}

type fakeSettingsService struct {
	snapshot SettingsSnapshot
	calls    int
}

func (s *fakeSettingsService) Collect(ctx context.Context) (SettingsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return SettingsSnapshot{}, err
	}
	return s.snapshot, nil
}

func (s *fakeSettingsService) SetPullRequestDefault(ctx context.Context, repositoryID string, value *bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.calls++
	for index := range s.snapshot.Repositories {
		if s.snapshot.Repositories[index].ID == repositoryID {
			s.snapshot.Repositories[index].PullRequest = value
		}
	}
	s.snapshot.CollectedAt = time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	return nil
}
