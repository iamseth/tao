package tuipreview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
)

func TestScenarioCatalogIsStableDiscoverableAndTyped(t *testing.T) {
	catalog := Scenarios()
	var names []string
	for _, scenario := range catalog {
		names = append(names, scenario.Name)
		if scenario.Now.IsZero() || scenario.Snapshot.CollectedAt.IsZero() {
			t.Fatalf("scenario %q has a live or missing clock value", scenario.Name)
		}
	}
	if want := []string{ScenarioMixed, ScenarioEmpty, ScenarioStress}; !reflect.DeepEqual(names, want) {
		t.Fatalf("scenario names = %v, want %v", names, want)
	}
	mixed, ok := Lookup(ScenarioMixed)
	if !ok || len(mixed.Snapshot.Rows) < 10 || len(mixed.Notes.Notes) < 3 || len(mixed.Settings.Repositories) == 0 || len(mixed.Debug.RuntimeDefaults) == 0 || len(mixed.Plans) == 0 {
		t.Fatalf("mixed scenario is not comprehensive: found=%t rows=%d notes=%d repositories=%d defaults=%d plans=%d", ok, len(mixed.Snapshot.Rows), len(mixed.Notes.Notes), len(mixed.Settings.Repositories), len(mixed.Debug.RuntimeDefaults), len(mixed.Plans))
	}
	if _, ok := Lookup("missing"); ok {
		t.Fatal("Lookup found an unknown scenario")
	}

	// Every catalog call reconstructs fixtures rather than exposing shared state.
	catalog[0].Snapshot.Rows[0].PlanTitle = "mutated"
	fresh, _ := Lookup(ScenarioMixed)
	if fresh.Snapshot.Rows[0].PlanTitle == "mutated" {
		t.Fatal("scenario catalog retained caller mutation")
	}
}

func TestMixedScenarioIncludesTerminalFinalizationRecoveryGuidance(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	var recovery string
	for _, row := range scenario.Snapshot.Rows {
		if row.PlanID == "finalize" {
			recovery = row.NextAction
			if recovery == "" {
				recovery = monitor.DeriveNextAction(row)
			}
			if row.FinalizationPhase != plan.FinalizationFailurePhasePullRequest || !slices.Contains(row.AttentionReasons, monitor.AttentionFinalizationFailed) {
				t.Fatalf("finalization fixture = %+v", row)
			}
			break
		}
	}
	if recovery != "FINALIZE PR" {
		t.Fatalf("fixture recovery guidance = %q, want FINALIZE PR", recovery)
	}
}

func TestCollectorsReturnIsolatedSnapshotsAndHonorCancellation(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	plans := scenario.NewSnapshotCollector()
	notes := scenario.NewNoteSnapshotCollector()
	debug := scenario.NewDebugSnapshotCollector()
	settings := scenario.NewSettingsService()

	firstPlans, err := plans.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstNotes, err := notes.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstDebug, err := debug.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstSettings, err := settings.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	firstPlans.Rows[0].AttentionReasons[0] = "changed"
	firstNotes.Notes[0].Tags[0] = "changed"
	firstDebug.RuntimeDefaults[0].Value = "changed"
	*firstSettings.Repositories[0].PullRequest = true
	secondPlans, _ := plans.Collect(context.Background())
	secondNotes, _ := notes.Collect(context.Background())
	secondDebug, _ := debug.Collect(context.Background())
	secondSettings, _ := settings.Collect(context.Background())
	if secondPlans.Rows[0].AttentionReasons[0] == "changed" || secondNotes.Notes[0].Tags[0] == "changed" || secondDebug.RuntimeDefaults[0].Value == "changed" || *secondSettings.Repositories[0].PullRequest {
		t.Fatal("collector returned mutable shared fixture data")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := plans.Collect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("plan collector cancellation error = %v", err)
	}
	if _, err := notes.Collect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("note collector cancellation error = %v", err)
	}
	if _, err := debug.Collect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("debug collector cancellation error = %v", err)
	}
	if _, err := settings.Collect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("settings collector cancellation error = %v", err)
	}
}

func TestRenderEveryViewIsDeterministicAndBounded(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	tests := []struct {
		view      View
		selection int
		header    string
	}{
		{view: ViewPlans, header: "tao │▸plans  notes  settings  debug"},
		{view: ViewNotes, selection: 1, header: "tao │ plans ▸notes  settings  debug"},
		{view: ViewSettings, selection: 1, header: "tao │ plans  notes ▸settings  debug"},
		{view: ViewDebug, header: "tao │ plans  notes  settings ▸debug"},
		{view: ViewPlanDetail, selection: 1, header: "Tao UI | live | alpha"},
		{view: ViewNoteDetail, selection: 1, header: "Tao UI | NOTE DETAIL"},
		{view: ViewSliceDetail, selection: 1, header: "Tao UI | 002-render-boundary | in_progress"},
	}
	for _, test := range tests {
		t.Run(string(test.view), func(t *testing.T) {
			options := RenderOptions{View: test.view, Width: 72, Height: 18, Selection: test.selection, Plain: true}
			first, err := Render(scenario, options)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Render(scenario, options)
			if err != nil {
				t.Fatal(err)
			}
			if first != second {
				t.Fatalf("%s output is not deterministic", test.view)
			}
			if !strings.Contains(first, test.header) {
				t.Fatalf("%s output missing %q:\n%s", test.view, test.header, first)
			}
			assertBoundedFrame(t, first, 72, 18)
		})
	}
}

func TestSettingsPreviewStartsWithFirstSectionAfterTabRule(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	frame, err := Render(scenario, RenderOptions{View: ViewSettings, Width: 70, Height: 20, Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
	sectionIndex := -1
	for index := 2; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) != "" {
			sectionIndex = index
			break
		}
	}
	if sectionIndex < 0 || !strings.Contains(lines[sectionIndex], "▌ GLOBAL RUNTIME DEFAULTS ") {
		t.Fatalf("first Settings section does not follow the tab rule:\n%s", frame)
	}
	if strings.Contains(frame, "3 repositories") || strings.Contains(frame, "need attention") {
		t.Fatalf("Settings preview unexpectedly contains a summary:\n%s", frame)
	}
}

func TestStressViewsAreCellBoundedAcrossWidths(t *testing.T) {
	scenario, _ := Lookup(ScenarioStress)
	for _, width := range []int{199, 120, 100, 80, 70} {
		for _, view := range Views() {
			t.Run(fmt.Sprintf("%s/%d", view, width), func(t *testing.T) {
				frame, err := Render(scenario, RenderOptions{
					View: view, Width: width, Height: 30, Plain: true, Color: true,
				})
				if err != nil {
					t.Fatal(err)
				}
				assertBoundedFrame(t, frame, width, 30)
				switch view {
				case ViewPlans:
					assertUnicodeTableAlignment(t, frame, "REPO", "PLAN", "stress-")
				case ViewNotes:
					assertUnicodeTableAlignment(t, frame, "REPO", "NOTE", "stress-note-")
				}
			})
		}
	}
}

func TestPlanDetailShortcutPreviewIsContextAware(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	frame, err := Render(scenario, RenderOptions{
		View: ViewPlanDetail, Width: 72, Height: 16, Plain: true, ShowShortcuts: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Keyboard shortcuts", "Switch detail tabs", "Scroll or select", "Open slice on Slices tab", "Return to plans"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("plan detail shortcut preview missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "Search plans and notes") {
		t.Fatalf("plan detail shortcut preview contains dashboard search action:\n%s", frame)
	}
}

func TestSliceDetailShortcutPreviewIsContextAware(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	frame, err := Render(scenario, RenderOptions{
		View: ViewSliceDetail, Width: 72, Height: 12, Plain: true, ShowShortcuts: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Keyboard shortcuts", "Return to plan", "Close shortcuts"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("slice detail shortcut preview missing %q:\n%s", want, frame)
		}
	}
	if strings.Contains(frame, "Move slice selection") || strings.Contains(frame, "Search plans and notes") {
		t.Fatalf("slice detail shortcut preview contains unavailable actions:\n%s", frame)
	}
}

func TestEmptyAndStressScenarios(t *testing.T) {
	empty, _ := Lookup(ScenarioEmpty)
	plans, err := Render(empty, RenderOptions{View: ViewPlans, Width: 50, Height: 8, Plain: true})
	if err != nil || !strings.Contains(plans, "No plans.") {
		t.Fatalf("empty plans frame error=%v:\n%s", err, plans)
	}
	notes, err := Render(empty, RenderOptions{View: ViewNotes, Width: 50, Height: 8, Plain: true})
	if err != nil || !strings.Contains(notes, "No open notes.") {
		t.Fatalf("empty notes frame error=%v:\n%s", err, notes)
	}
	if _, err := Render(empty, RenderOptions{View: ViewPlanDetail, Width: 50, Height: 8}); err == nil {
		t.Fatal("empty scenario rendered unavailable plan detail")
	}
	if _, err := Render(empty, RenderOptions{View: ViewNoteDetail, Width: 50, Height: 8, Plain: true}); err == nil || !strings.Contains(err.Error(), "fixture has no notes") {
		t.Fatalf("empty scenario note detail error = %v, want actionable no-notes error", err)
	}

	stress, _ := Lookup(ScenarioStress)
	if len(stress.Snapshot.Rows) != 36 || len(stress.Notes.Notes) != 30 {
		t.Fatalf("stress fixture sizes = %d plans, %d notes", len(stress.Snapshot.Rows), len(stress.Notes.Notes))
	}
	for _, options := range []RenderOptions{
		{View: ViewPlans, Width: 44, Height: 9, Selection: 25, Plain: true},
		{View: ViewNotes, Width: 44, Height: 9, Selection: 25, Plain: true},
		{View: ViewPlanDetail, Width: 44, Height: 9, Selection: 1, Plain: true},
	} {
		frame, err := Render(stress, options)
		if err != nil {
			t.Fatalf("stress %s: %v", options.View, err)
		}
		assertBoundedFrame(t, frame, options.Width, options.Height)
	}
	if !strings.Contains(stress.Notes.Notes[0].Text, "日本語") || !strings.Contains(stress.Plans[0].Log, "🧭") {
		t.Fatal("stress fixture lost unicode values")
	}
}

func TestDetailRepositoryLookupLogsAndCancellation(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	repository := scenario.NewDetailRepository()
	planDir := scenario.Plans[0].PlanDir
	for _, row := range scenario.Snapshot.Rows {
		if row.PlanDir == "" {
			continue
		}
		if _, err := repository.ResolvePlan(context.Background(), row.PlanDir); err != nil {
			t.Fatalf("plan row %q has no detail fixture: %v", row.PlanID, err)
		}
		if _, err := repository.ReadLogTail(row.PlanDir, 1); err != nil {
			t.Fatalf("plan row %q has no log fixture: %v", row.PlanID, err)
		}
	}

	detail, err := repository.ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if detail.State.Plan.ID != "live" || detail.Dir != planDir {
		t.Fatalf("resolved detail = id %q dir %q", detail.State.Plan.ID, detail.Dir)
	}
	detail.State.Plan.Title = "mutated"
	fresh, _ := repository.ResolvePlan(context.Background(), planDir)
	if fresh.State.Plan.Title == "mutated" {
		t.Fatal("detail repository returned shared plan data")
	}

	tail, err := repository.ReadLogTail(planDir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSuffix(tail, "\n"), "\n") != 1 || !strings.Contains(tail, "go test") {
		t.Fatalf("two-line fixture tail = %q", tail)
	}
	if _, err := repository.ResolvePlan(context.Background(), "fixture://missing"); !errors.Is(err, ErrUnknownPlan) {
		t.Fatalf("unknown detail error = %v", err)
	}
	if _, err := repository.ReadLogTail("fixture://missing", 1); !errors.Is(err, ErrUnknownPlan) {
		t.Fatalf("unknown log error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	followed := make(chan error, 1)
	go func() { followed <- repository.FollowLog(ctx, planDir, io.Discard) }()
	select {
	case err := <-followed:
		t.Fatalf("FollowLog returned before cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-followed:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("FollowLog cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("FollowLog did not stop after cancellation")
	}
}

func TestPlainOutputRemovesOnlyScreenControls(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	frame, err := Render(scenario, RenderOptions{View: ViewPlans, Width: 120, Height: 30, Color: true, Plain: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(frame, "\x1b[H") || strings.Contains(frame, "\x1b[2J") {
		t.Fatalf("plain frame retained screen controls: %q", frame[:min(len(frame), 20)])
	}
	if !strings.Contains(frame, "\x1b[38;2;") || !strings.Contains(frame, "\x1b[0m") {
		t.Fatal("plain colored frame stripped intentional SGR styling")
	}

	styled := "\x1b[?1049h\x1b[?25l\x1b[H\x1b[2J\x1b[36mvisible\x1b[0m\n"
	if got, want := PlainFrame(styled), "\x1b[36mvisible\x1b[0m\n"; got != want {
		t.Fatalf("PlainFrame() = %q, want %q", got, want)
	}
}

func TestRenderRejectsInvalidOptions(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	for _, options := range []RenderOptions{
		{View: ViewPlans, Height: 10},
		{View: ViewPlans, Width: 10},
		{View: ViewPlans, Width: 10, Height: 10, Selection: -1},
		{View: View("unknown"), Width: 10, Height: 10},
		{View: ViewNotes, Width: 10, Height: 10, Selection: 99},
		{View: ViewPlanDetail, Width: 10, Height: 10, PlanDir: "fixture://missing"},
		{View: ViewSliceDetail, Width: 10, Height: 10, SliceID: "missing"},
		{View: ViewNoteDetail, Width: 10, Height: 10, ShowShortcuts: true},
		{View: ViewPlanDetail, Width: 10, Height: 10, SearchQuery: "owner"},
	} {
		if _, err := Render(scenario, options); err == nil {
			t.Fatalf("Render accepted invalid options: %+v", options)
		}
	}
}

func assertBoundedFrame(t *testing.T, frame string, width, height int) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
	if len(lines) > height {
		t.Fatalf("frame has %d lines, height is %d:\n%s", len(lines), height, frame)
	}
	for _, line := range lines {
		if got := fixtureVisibleWidth(line); got > width {
			t.Fatalf("frame line has %d visible cells, width is %d: %q", got, width, line)
		}
	}
}

func assertUnicodeTableAlignment(t *testing.T, frame, firstHeader, secondHeader, rowSecond string) {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(frame, "\n"), "\n")
	var header string
	for _, line := range lines {
		plain := stripCSI(line)
		if strings.Contains(plain, firstHeader) && strings.Contains(plain, secondHeader) {
			header = plain
			break
		}
	}
	if header == "" {
		t.Fatalf("frame has no %s/%s table header:\n%s", firstHeader, secondHeader, frame)
	}
	firstStart := fixtureCellOffset(t, header, firstHeader)
	secondStart := fixtureCellOffset(t, header, secondHeader)
	seen := make(map[string]bool)
	for _, line := range lines {
		plain := stripCSI(line)
		if !strings.Contains(plain, rowSecond) {
			continue
		}
		for _, repository := range []string{"日本語リポジトリ", "emoji-🧭-workspace", "combining-é-repo"} {
			if !strings.Contains(plain, repository) {
				continue
			}
			seen[repository] = true
			if got := fixtureCellOffset(t, plain, repository); got != firstStart {
				t.Errorf("%s row starts at cell %d, %s header starts at %d: %q", repository, got, firstHeader, firstStart, plain)
			}
			if got := fixtureCellOffset(t, plain, rowSecond); got != secondStart {
				t.Errorf("%s row %s column starts at cell %d, header starts at %d: %q", repository, secondHeader, got, secondStart, plain)
			}
		}
	}
	for _, repository := range []string{"日本語リポジトリ", "emoji-🧭-workspace", "combining-é-repo"} {
		if !seen[repository] {
			t.Errorf("frame has no visible %s table row for %q", secondHeader, repository)
		}
	}
}

func fixtureCellOffset(t *testing.T, line, marker string) int {
	t.Helper()
	index := strings.Index(line, marker)
	if index < 0 {
		t.Fatalf("line has no marker %q: %q", marker, line)
	}
	return fixtureVisibleWidth(line[:index])
}

func fixtureVisibleWidth(value string) int {
	width := 0
	for _, r := range stripCSI(value) {
		switch {
		case r == 0 || unicode.IsControl(r) || unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf):
			continue
		case r >= 0x1100 && (r <= 0x115F || r == 0x2329 || r == 0x232A ||
			(r >= 0x2E80 && r <= 0xA4CF) || (r >= 0xAC00 && r <= 0xD7A3) ||
			(r >= 0xF900 && r <= 0xFAFF) || (r >= 0xFE10 && r <= 0xFE19) ||
			(r >= 0xFE30 && r <= 0xFE6F) || (r >= 0xFF00 && r <= 0xFF60) ||
			(r >= 0xFFE0 && r <= 0xFFE6) || (r >= 0x1F000 && r <= 0x1FAFF) ||
			(r >= 0x20000 && r <= 0x3FFFD)):
			width += 2
		default:
			width++
		}
	}
	return width
}

func stripCSI(value string) string {
	var result strings.Builder
	for index := 0; index < len(value); {
		if value[index] == '\x1b' && index+1 < len(value) && value[index+1] == '[' {
			index += 2
			for index < len(value) {
				final := value[index] >= '@' && value[index] <= '~'
				index++
				if final {
					break
				}
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(value[index:])
		result.WriteRune(r)
		index += size
	}
	return result.String()
}
