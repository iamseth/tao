package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/term"
)

func TestRenderSettingsShowsGlobalAndRepositoryDefaults(t *testing.T) {
	explicit := true
	frame := Render(Model{
		Page: PageSettings, Selected: 1, Width: 120, Height: 30,
		SettingsSnapshot: SettingsSnapshot{
			InheritedPullRequest: false,
			RuntimeDefaults: []SettingsRuntimeDefault{
				{Name: "TAO_AGENT", Value: "pi", Source: "default"},
				{Name: "TAO_PULL_REQUEST", Value: "false", Source: "environment", Warning: "example warning"},
			},
			Repositories: []RepositorySetting{
				{ID: "alpha-123", Name: "alpha", Root: "/repos/alpha", Health: "ok", Finding: "ok", PullRequest: &explicit},
				{ID: "beta-456", Name: "beta", Root: "/repos/beta", Health: "missing_root", Finding: "repo root does not exist"},
			},
		},
	})
	for _, want := range []string{
		"Tao UI | Settings | 2 repositories", "GLOBAL RUNTIME DEFAULTS", "TAO_AGENT", "TAO_PULL_REQUEST", "environment", "warning: example warning",
		"REPOSITORY DEFAULTS", "alpha (alpha-123)", "explicit true", "> beta (beta-456)", "inherit (false)", "finding: repo root does not exist",
	} {
		if !strings.Contains(frame, want) {
			t.Fatalf("settings frame missing %q:\n%s", want, frame)
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
	if !strings.Contains(state.confirm.message, "inherit (false) to explicit true") {
		t.Fatalf("settings confirmation = %q", state.confirm.message)
	}
	if quit := app.handleKey(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'y'}); quit || state.confirm != nil || service.calls != 1 {
		t.Fatalf("settings confirmation did not update: quit=%t confirm=%#v calls=%d", quit, state.confirm, service.calls)
	}
	value := state.settingsSnapshot.Repositories[0].PullRequest
	if value == nil || !*value || !strings.Contains(state.settingsMessage, "explicit true") {
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
