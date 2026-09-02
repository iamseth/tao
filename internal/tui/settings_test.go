package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/term/cells"
)

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

func TestRenderSettingsDefaultsUsesResponsivePairGrid(t *testing.T) {
	rows := []SettingsRuntimeDefault{
		{Name: "TAO_COMMIT_POLICY", Value: "slice", Source: "default"},
		{Name: "TAO_EXECUTION_MODE", Value: "isolated", Source: "default"},
		{Name: "TAO_AGENT", Value: "pi", Source: "default"},
		{Name: "TAO_SESSION_TIMEOUT", Value: "20m", Source: "default"},
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
	statuses, err := runtimeconfig.RuntimeEnvStatus()
	if err != nil {
		t.Fatal(err)
	}
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
		if strings.HasPrefix(status.Name, "TAO_BUDGET_") {
			if counts[status.Name] != 0 {
				t.Errorf("advisory budget %s rendered in defaults", status.Name)
			}
			continue
		}
		if _, known := settingsDefaultGroupForName(status.Name); !known {
			t.Errorf("runtime setting %s is not explicitly classified", status.Name)
		}
		if counts[status.Name] != 1 {
			t.Errorf("runtime setting %s grouped %d times, want once", status.Name, counts[status.Name])
		}
		if got := strings.Count(rendered, humanizeSettingsName(status.Name)); got != 1 {
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

func TestRenderSettingsBudgetsPairsScopesAndKeepsZeroTruthful(t *testing.T) {
	rows := []SettingsRuntimeDefault{
		{Name: "TAO_BUDGET_PLAN_TOOL_CALLS", Value: "400"},
		{Name: "TAO_BUDGET_SLICE_ERRORED_MESSAGES", Value: "0"},
		{Name: "TAO_BUDGET_PLAN_OUTPUT_TOKENS", Value: "150000"},
		{Name: "TAO_BUDGET_SLICE_COST", Value: "5.00"},
		{Name: "TAO_BUDGET_PLAN_ASSISTANT_MESSAGES", Value: "300"},
		{Name: "TAO_BUDGET_SLICE_OUTPUT_TOKENS", Value: "40000"},
		{Name: "TAO_BUDGET_PLAN_ERRORED_MESSAGES", Value: "2"},
		{Name: "TAO_BUDGET_SLICE_TOOL_CALLS", Value: "120"},
		{Name: "TAO_BUDGET_PLAN_COST", Value: "20.000"},
		{Name: "TAO_BUDGET_SLICE_ASSISTANT_MESSAGES", Value: "80"},
	}
	lines, _ := renderSettingsBudgets(Model{Page: PageSettings, Width: 120, SettingsSnapshot: SettingsSnapshot{RuntimeDefaults: rows}})
	for _, want := range [][]string{
		{"Output tokens", "40 000", "150 000"},
		{"Cost", "5.00", "20.000"},
		{"Tool calls", "120", "400"},
		{"Assistant messages", "80", "300"},
		{"Errored messages", "0", "2"},
	} {
		if !lineContainsAll(lines, want...) {
			t.Errorf("budget row missing exact slice/plan pair %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	joined := strings.ToLower(strings.Join(lines, "\n"))
	if strings.Contains(joined, "unlimited") || strings.Contains(joined, "none") {
		t.Fatalf("zero budget rendered as a sentinel:\n%s", joined)
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
	if got := settingsStyledRepositoryName(ProfileANSI16, repository); got != Paint(ProfileANSI16, RepoColor("beta"), "βeta") {
		t.Fatalf("styled repository name = %q", got)
	}
	if got := settingsRepositoryHealth(ProfileANSI16, "missing_root"); !strings.Contains(got, Paint(ProfileANSI16, RoleWarn, "●")) || !strings.Contains(got, "missing root") {
		t.Fatalf("styled unhealthy status = %q", got)
	}
}

func TestSettingsDefaultPairsUseSemanticStyles(t *testing.T) {
	labelWidth := cells.Width("Pull request")
	for _, test := range []struct {
		row  SettingsRuntimeDefault
		role Role
	}{
		{row: SettingsRuntimeDefault{Name: "TAO_PULL_REQUEST", Value: "true"}, role: RoleSuccess},
		{row: SettingsRuntimeDefault{Name: "TAO_PULL_REQUEST", Value: "false"}, role: RoleNeutral2},
		{row: SettingsRuntimeDefault{Name: "TAO_PULL_REQUEST", Value: "none"}, role: RoleNeutral2},
	} {
		got := settingsDefaultPair(ProfileANSI16, test.row, labelWidth, false)
		if !strings.Contains(got, Paint(ProfileANSI16, RoleNeutral2, "Pull request")) || !strings.Contains(got, Paint(ProfileANSI16, test.role, test.row.Value)) {
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
		Page: PageSettings, Width: 120, Profile: ProfileANSI16,
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
		Paint(ProfileANSI16, RoleNeutral5, "claude"),
		Paint(ProfileANSI16, RoleWarn, "warning: fallback warning"),
		Paint(ProfileANSI16, RepoColor("beta"), "← βeta"),
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
