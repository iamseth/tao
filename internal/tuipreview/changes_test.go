package tuipreview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/tui"
)

func TestChangesLoaderIsolationAndCancellation(t *testing.T) {
	s := mixedScenario()
	p := &s.Plans[0].Detail
	loader := s.NewChangesLoader()
	s.Plans[0].Changes.Files[0].Display = "mutated source"
	snapshot, err := loader.Snapshot(context.Background(), p, "")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Files[0].Display == "mutated source" {
		t.Fatal("source alias")
	}
	path := snapshot.Files[0].Path
	snapshot.Files[0].Display = "mutated output"
	next, err := loader.Snapshot(context.Background(), p, tui.DetailChangesScopeBranch)
	if err != nil || next.Scope != tui.DetailChangesScopeBranch || next.Files[0].Display == "mutated output" {
		t.Fatalf("snapshot isolation: %+v %v", next, err)
	}
	diff, err := loader.FileDiff(context.Background(), snapshot, path)
	if err != nil {
		t.Fatal(err)
	}
	diff.Lines[0].Text = "mutated diff"
	diff.Hunks[0] = 999
	nextDiff, err := loader.FileDiff(context.Background(), snapshot, path)
	if err != nil || nextDiff.Lines[0].Text == "mutated diff" || nextDiff.Hunks[0] != 0 {
		t.Fatalf("diff alias: %+v %v", nextDiff, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := loader.Snapshot(ctx, p, ""); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := loader.FileDiff(ctx, snapshot, path); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	unavailable, err := loader.Snapshot(context.Background(), nil, "")
	if err != nil || unavailable.Availability != "unavailable" {
		t.Fatalf("unavailable: %+v %v", unavailable, err)
	}
}

func TestPlanChangesPreview(t *testing.T) {
	for _, name := range []string{ScenarioMixed, ScenarioStress} {
		s, ok := Lookup(name)
		if !ok {
			t.Fatal(name)
		}
		for _, width := range []int{40, 160} {
			frame, err := Render(s, RenderOptions{View: ViewPlanChanges, Width: width, Height: 24, Plain: true})
			if err != nil {
				t.Fatal(err)
			}
			wants := []string{"CHANGES", "FILES", "*"}
			if width == 160 {
				wants = append(wants, "DIFF", "+ content", "@@")
			}
			for _, want := range wants {
				if !strings.Contains(frame, want) {
					t.Fatalf("%s width %d missing %q:\n%s", name, width, want, frame)
				}
			}
		}
	}
	s := mixedScenario()
	s.Plans[0].Changes = nil
	frame, err := Render(s, RenderOptions{View: ViewPlanChanges, Width: 100, Height: 24, Plain: true})
	if err != nil || !strings.Contains(frame, "Changes fixture unavailable") {
		t.Fatalf("absent: %v\n%s", err, frame)
	}
}
