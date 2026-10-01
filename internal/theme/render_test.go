package theme_test

import (
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/theme"
	"github.com/iamseth/tao/internal/tui"
	"github.com/iamseth/tao/internal/tuipreview"
)

// The custom-theme case lives here because export_test.go helpers are only
// available to the theme package's own test binary, not imported TUI tests.
func TestRenderGoldenColorModesDistinctTheme(t *testing.T) {
	snapshot := monitor.Snapshot{Rows: []monitor.Row{{RepositoryName: "repo", PlanID: "plan", Status: plan.StatusPlanned}}}
	model := tui.Model{Snapshot: snapshot, Profile: theme.ProfileTrueColor}
	original := tui.Render(model)
	custom := theme.DistinctTestTheme()
	model.Theme = custom
	colored := tui.Render(model)
	accent := custom.Palette(model.Profile).Paint(theme.RoleAccent, "WIP")
	defaultAccent := theme.Default().Palette(model.Profile).Paint(theme.RoleAccent, "WIP")
	if accent == defaultAccent || !strings.Contains(colored, accent) || strings.Contains(colored, defaultAccent) {
		t.Fatalf("custom frame does not use its distinct accent: %q", colored)
	}
	model.Theme = theme.Theme{}
	if got := tui.Render(model); got != original {
		t.Fatal("rendering a custom theme changed the default frame")
	}
	model.Profile = theme.ProfileNone
	plain := tui.Render(model)
	model.Theme = custom
	if got := tui.Render(model); got != plain {
		t.Fatal("theme changed plain output")
	}
}

func TestPreviewThreadsThemeThroughEveryView(t *testing.T) {
	scenario, ok := tuipreview.Lookup(tuipreview.ScenarioMixed)
	if !ok {
		t.Fatal("missing mixed scenario")
	}
	for _, view := range tuipreview.Views() {
		t.Run(string(view), func(t *testing.T) {
			for _, shortcuts := range []bool{false, true} {
				if view == tuipreview.ViewNoteDetail && shortcuts {
					continue
				}
				options := tuipreview.RenderOptions{View: view, Width: 100, Height: 40, Color: true, Plain: true, ShowShortcuts: shortcuts}
				render := func() string {
					t.Helper()
					frame, err := tuipreview.Render(scenario, options)
					if err != nil {
						t.Fatal(err)
					}
					return frame
				}
				original := render()
				options.Theme = theme.DistinctTestTheme()
				custom := render()
				if view != tuipreview.ViewNoteDetail && custom == original {
					t.Fatalf("preview ignored theme (shortcuts=%t)", shortcuts)
				}
				options.Theme = theme.Theme{}
				if render() != original {
					t.Fatal("custom render mutated default preview")
				}
				options.Color = false
				plain := render()
				options.Theme = theme.DistinctTestTheme()
				if render() != plain {
					t.Fatal("theme changed plain preview")
				}
			}
		})
	}
}
