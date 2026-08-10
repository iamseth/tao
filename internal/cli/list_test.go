package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
)

func TestListAppliesLimitAndShowsShortIDAndSlug(t *testing.T) {
	repo := plantest.NewRepository()
	repo.AddDetail(plantest.NewPlanDetail("20260427-1810-first-plan").
		WithTitle("First Plan").
		WithStatus(plan.StatusCompleted).
		WithCompletedSlices("001-a", "002-b").
		AddSlice(plantest.NewSlice("001-a").WithStatus(plan.StatusCompleted).Build()).
		AddSlice(plantest.NewSlice("002-b").WithStatus(plan.StatusCompleted).Build()).
		Build())
	repo.AddDetail(plantest.NewPlanDetail("20260427-1722-second-plan").
		WithTitle("Second Plan").
		WithStatus(plan.StatusInProgress).
		WithPendingSlices("002-b").
		WithCompletedSlices("001-a").
		AddSlice(plantest.NewSlice("001-a").WithStatus(plan.StatusCompleted).Build()).
		AddSlice(plantest.NewSlice("002-b").WithStatus(plan.StatusPending).Build()).
		Build())

	var out bytes.Buffer
	err := App{Out: &out, Err: &out}.list(context.Background(), repo, []string{"--limit", "1"})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "20260427-1810") || !strings.Contains(text, "first-plan") {
		t.Fatalf("expected short id and slug in output:\n%s", text)
	}
	if strings.Contains(text, "First Plan") {
		t.Fatalf("expected slug instead of title in output:\n%s", text)
	}
	if strings.Contains(text, "Second Plan") {
		t.Fatalf("expected limit to hide second plan:\n%s", text)
	}
}

func TestListRejectsNegativeLimit(t *testing.T) {
	var out bytes.Buffer
	err := App{Out: &out, Err: &out}.list(context.Background(), plantest.NewRepository(), []string{"--limit", "-1"})
	if err == nil || !strings.Contains(err.Error(), "--limit") {
		t.Fatalf("expected limit error, got %v", err)
	}
}

func TestListAllowsZeroLimitAndPropagatesRepoErrors(t *testing.T) {
	repo := plantest.NewRepository()
	repo.AddDetail(plantest.NewPlanDetail("20260427-1810-example").
		WithTitle("Example").
		WithStatus(plan.StatusPlanned).
		Build())
	var out bytes.Buffer
	if err := (App{Out: &out, Err: &out}).list(context.Background(), repo, []string{"--limit", "0"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "example") {
		t.Fatalf("expected unbounded output, got %q", out.String())
	}

	// Error propagation from repository is still tested with fakeRepository.
	err := (App{Out: &out, Err: &out}).list(context.Background(), fakeRepository{err: errors.New("boom")}, nil)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("expected repository error, got %v", err)
	}
}

func TestListFallsBackToTitleForUnparsablePlanID(t *testing.T) {
	repo := plantest.NewRepository()
	repo.AddDetail(plantest.NewPlanDetail("legacy-plan").
		WithTitle("Legacy Plan").
		WithStatus(plan.StatusPlanned).
		Build())

	var out bytes.Buffer
	if err := (App{Out: &out, Err: &out}).list(context.Background(), repo, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Legacy Plan") {
		t.Fatalf("expected title fallback for unparsable id, got %q", out.String())
	}
}

func TestRenderPlanListOmitsCurrentSliceColumn(t *testing.T) {
	var out bytes.Buffer

	err := renderPlanList(&out, []plan.PlanSummary{
		{ID: "20260525-1100-active", Status: plan.StatusInProgress, CurrentSliceID: "001-a", PendingCount: 1, TotalCount: 1},
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, notWant := range []string{"CURRENT SLICE", "001-a"} {
		if strings.Contains(text, notWant) {
			t.Fatalf("expected list output to omit %q:\n%s", notWant, text)
		}
	}
}

func TestRenderPlanListPreservesExactASCIIOutput(t *testing.T) {
	now := time.Date(2026, 8, 10, 22, 0, 0, 0, time.UTC)
	updated := now.Add(-42 * time.Minute)
	var out bytes.Buffer

	err := renderPlanList(&out, []plan.PlanSummary{{
		ID: "20260810-2142-first-plan", Status: plan.StatusPlanned,
		CompletedCount: 1, TotalCount: 2, LastActivityAt: &updated,
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := "STATUS   PLAN ID        PLAN        DONE  UPDATED\n" +
		"planned  20260810-2142  first-plan  1/2   42m    \n"
	if got := stripANSI(out.String()); got != want {
		t.Fatalf("renderPlanList() output:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderPlanListUsesRuneWidthsForUnicode(t *testing.T) {
	var out bytes.Buffer

	err := renderPlanList(&out, []plan.PlanSummary{{
		ID: "20260810-2142-café", Status: plan.StatusPlanned,
	}}, time.Date(2026, 8, 10, 22, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := "STATUS   PLAN ID        PLAN  DONE  UPDATED\n" +
		"planned  20260810-2142  café  0/0   -      \n"
	if got := stripANSI(out.String()); got != want {
		t.Fatalf("renderPlanList() Unicode output:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderPlanListUsesHumanUpdatedTime(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-42 * time.Minute)
	old := now.Add(-25 * time.Hour)
	var out bytes.Buffer

	err := renderPlanList(&out, []plan.PlanSummary{
		{ID: "20260525-1100-recent", Status: plan.StatusInProgress, PendingCount: 1, TotalCount: 1, LastActivityAt: &recent},
		{ID: "20260524-1100-old", Status: plan.StatusCompleted, CompletedCount: 1, TotalCount: 1, LastActivityAt: &old},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"UPDATED", "42m", "2026-05-24"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
	if strings.Contains(text, "11:18:00") {
		t.Fatalf("expected compact updated time, got:\n%s", text)
	}
}
