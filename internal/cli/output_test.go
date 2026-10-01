package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/theme"
)

func TestListSelectedTheme(t *testing.T) {
	for _, mode := range []string{"injected", "env", "invalid", "frozen"} {
		t.Run(mode, func(t *testing.T) {
			clearTaoEnv(t)
			t.Setenv("TERM", "xterm-256color")
			t.Setenv("COLORTERM", "truecolor")
			t.Setenv("NO_COLOR", "")
			t.Setenv("CLICOLOR", "")
			t.Setenv("CLICOLOR_FORCE", "1")
			t.Setenv("TAO_THEME", "gruvbox")
			selected, _ := theme.Lookup("gruvbox")
			var out testTerminalBuffer
			app := App{Out: &out, Repository: func(string) Repository {
				if mode == "frozen" {
					t.Setenv("TAO_THEME", "tokyonight")
				}
				return fakeRepository{summaries: []plan.PlanSummary{{ID: "plan", Status: plan.StatusCompleted, CompletedCount: 1, TotalCount: 1}}}
			}}
			if mode == "injected" {
				app.Theme = &selected
				t.Setenv("TAO_THEME", "tokyonight")
			}
			if mode == "invalid" {
				t.Setenv("TAO_THEME", "nope")
				selected = theme.Default()
			}
			if err := app.Run(context.Background(), []string{"list"}); err != nil {
				t.Fatal(err)
			}
			want := selected.Palette(theme.ProfileTrueColor).Paint(theme.RoleSuccess, plan.StatusCompleted)
			if !strings.Contains(out.String(), want) {
				t.Fatalf("list missing selected success sequence %q: %q", want, out.String())
			}
		})
	}
}

func TestUIUsesRuntimeTheme(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	t.Setenv("TAO_THEME", "gruvbox")
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")
	var out testTerminalBuffer
	registry := taodata.NewRegistry(t.TempDir())
	app := App{
		In: strings.NewReader("q"), Out: &out, Err: &out,
		Registry: func() NoteRegistry { return registry },
		MonitorTicker: func(time.Duration) MonitorTicker {
			return &monitorTickerStub{ch: make(chan time.Time), stopped: make(chan struct{})}
		},
		UITerminal: &uiTerminalStub{size: term.Size{Width: 160, Height: 60}},
	}
	if err := app.Run(context.Background(), []string{"ui"}); err != nil {
		t.Fatal(err)
	}
	// Settings is no longer reachable by Tab, so prove theme threading on the
	// default WIP frame: the gruvbox accent paints the tab strip, the default
	// theme's accent must not appear anywhere.
	selected, _ := theme.Lookup("gruvbox")
	accent := selected.Palette(theme.ProfileTrueColor).Paint(theme.RoleAccent, "WIP")
	defaultAccent := theme.Default().Palette(theme.ProfileTrueColor).Paint(theme.RoleAccent, "WIP")
	if accent == defaultAccent {
		t.Fatal("gruvbox accent must differ from the default theme to be observable")
	}
	if !strings.Contains(out.String(), accent) || strings.Contains(out.String(), defaultAccent) {
		t.Fatalf("TUI did not render with the runtime gruvbox theme: %q", out.String())
	}
}

func TestColorStatusThemeParity(t *testing.T) {
	for _, profile := range []theme.Profile{theme.ProfileTrueColor, theme.ProfileANSI256, theme.ProfileANSI16, theme.ProfileNone} {
		t.Run(profile.String(), func(t *testing.T) {
			palette := theme.Default().Palette(profile)
			text := "completed"
			if got, want := colorStatus(palette, text, plan.StatusCompleted), palette.Paint(theme.RoleSuccess, text); got != want {
				t.Fatalf("completed status = %q, want shared theme %q", got, want)
			}
			if profile == theme.ProfileNone && colorStatus(palette, text, plan.StatusCompleted) != text {
				t.Fatal("plain palette changed text")
			}
		})
	}
}

func TestPlanListForcedPalettePreservesPlainText(t *testing.T) {
	t.Setenv("TERM", "xterm")
	t.Setenv("COLORTERM", "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	summaries := []plan.PlanSummary{{ID: "plan", Status: plan.StatusCompleted, CompletedCount: 1, TotalCount: 1}}
	var plain, colored bytes.Buffer
	if err := (App{}).renderPlanList(&plain, summaries, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain.String(), "\x1b[") {
		t.Fatalf("redirected output contains ANSI: %q", plain.String())
	}
	t.Setenv("CLICOLOR_FORCE", "1")
	if err := (App{}).renderPlanList(&colored, summaries, time.Time{}); err != nil {
		t.Fatal(err)
	}
	palette := theme.Default().Palette(theme.ProfileANSI16)
	if want := palette.Paint(theme.RoleSuccess, plan.StatusCompleted); !strings.Contains(colored.String(), want) {
		t.Fatalf("forced list missing shared theme %q: %q", want, colored.String())
	}
	if stripANSI(colored.String()) != plain.String() {
		t.Fatal("color changed list text or alignment")
	}
}

func TestDoctorStatusLabelUsesPalette(t *testing.T) {
	for _, profile := range []theme.Profile{theme.ProfileNone, theme.ProfileTrueColor} {
		palette := theme.Default().Palette(profile)
		for _, status := range []string{"current", "ok"} {
			label := "✓ " + status
			if got, want := doctorStatusLabel(palette, status, 0), palette.Paint(theme.RoleSuccess, label); got != want {
				t.Errorf("%s label = %q, want %q", profile, got, want)
			}
		}
	}
}
