package tuipreview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/tui"
)

func TestTrendsRegistryAndIsolation(t *testing.T) {
	want := []View{ViewPlans, ViewHistory, ViewTrends, ViewNotes, ViewSettings, ViewDebug, ViewPlanDetail, ViewPlanChanges, ViewNoteDetail, ViewSliceDetail}
	if !reflect.DeepEqual(Views(), want) {
		t.Fatalf("views = %v", Views())
	}
	s, _ := Lookup(ScenarioMixed)
	original := cloneTrends(s.Trends)
	c := NewTrendsCollector(s.Trends)
	s.Trends.Buckets[0].Metrics[0].Rate.Rate = 99
	first, _ := c.Collect(context.Background())
	first.Buckets[0].Metrics[0].Rate.Rate = 98
	first.Buckets[0].Metrics[3].Median.Median = 97
	first.Buckets[0].Month = "changed"
	second, _ := c.Collect(context.Background())
	fresh, _ := Lookup(ScenarioMixed)
	if !reflect.DeepEqual(original, second) || !reflect.DeepEqual(original, fresh.Trends) {
		t.Fatal("fixture aliases mutable input/output")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Collect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}

func TestTrendsPreviewsEvidenceAndBounds(t *testing.T) {
	for _, name := range []string{ScenarioMixed, ScenarioEmpty, ScenarioStress} {
		s, _ := Lookup(name)
		for cohort := range 3 {
			for _, width := range []int{20, 40, 60, 120, 240} {
				for _, help := range []bool{false, true} {
					opts := RenderOptions{View: ViewTrends, Selection: cohort, Width: width, Height: 45, Plain: true, ShowShortcuts: help}
					plain, err := Render(s, opts)
					if err != nil {
						t.Fatal(err)
					}
					opts.Color = true
					color, err := Render(s, opts)
					if err != nil {
						t.Fatal(err)
					}
					assertBoundedFrame(t, plain, width, 45)
					assertBoundedFrame(t, color, width, 45)
					if name == ScenarioMixed && cohort == 1 && width == 60 && !help {
						t.Logf("narrow provisional plain:\n%s\ncolored:\n%s", plain, color)
					}
					if stripCSI(color) != plain {
						t.Fatal("color changes trend content")
					}
				}
			}
		}
	}
	s, _ := Lookup(ScenarioMixed)
	for cohort, wants := range [][]string{
		{"↑ better", "↑ worse", "incomplete", "partial=2", "sparse", "missing duration evidence", "calendar gap"},
		{"comparison withheld", "provisional", "age-immature"},
		{"no monthly evidence for this cohort"},
	} {
		frame, err := Render(s, RenderOptions{View: ViewTrends, Selection: cohort, Width: 240, Height: 50, Plain: true})
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(frame, want) {
				t.Errorf("cohort %d missing %q:\n%s", cohort, want, frame)
			}
		}
	}
}

func TestTrendsStalePresentation(t *testing.T) {
	s, _ := Lookup(ScenarioMixed)
	for _, snapshot := range []tui.TrendsSnapshot{{}, s.Trends} {
		frame := tui.Render(tui.Model{Page: tui.PageTrends, Width: 80, Height: 30, Trends: &tui.TrendsModel{Snapshot: snapshot, Stale: true, Failure: "synthetic collector failure"}})
		if !strings.Contains(frame, "synthetic collector failure") {
			t.Fatal(frame)
		}
		if len(snapshot.Buckets) > 0 && !strings.Contains(frame, "n=20") {
			t.Fatal("lost last-good evidence")
		}
	}
}
