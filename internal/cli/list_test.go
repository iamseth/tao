package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
	"github.com/iamseth/tao/internal/term/cells"
	planview "github.com/iamseth/tao/internal/view"
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
	err := newTestApp(App{Out: &out, Err: &out}).list(context.Background(), repo, []string{"--limit", "1"})
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
	err := newTestApp(App{Out: &out, Err: &out}).list(context.Background(), plantest.NewRepository(), []string{"--limit", "-1"})
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
	if err := (newTestApp(App{Out: &out, Err: &out})).list(context.Background(), repo, []string{"--limit", "0"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "example") {
		t.Fatalf("expected unbounded output, got %q", out.String())
	}

	// Error propagation from repository is still tested with fakeRepository.
	err := (newTestApp(App{Out: &out, Err: &out})).list(context.Background(), fakeRepository{err: errors.New("boom")}, nil)
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
	if err := (newTestApp(App{Out: &out, Err: &out})).list(context.Background(), repo, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Legacy Plan") {
		t.Fatalf("expected title fallback for unparsable id, got %q", out.String())
	}
}

func TestRenderPlanListOmitsCurrentSliceColumn(t *testing.T) {
	var out bytes.Buffer

	err := (newTestApp(App{})).renderPlanList(&out, []plan.PlanSummary{
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

	err := (newTestApp(App{})).renderPlanList(&out, []plan.PlanSummary{{
		ID: "20260810-2142-first-plan", Status: plan.StatusPlanned,
		CompletedCount: 1, TotalCount: 2, LastActivityAt: &updated,
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	want := "STATUS   PLAN ID        PLAN        DONE  UPDATED  ABANDONED  REASON\n" +
		"planned  20260810-2142  first-plan  1/2   42m      -          -     \n"
	if got := stripANSI(out.String()); got != want {
		t.Fatalf("renderPlanList() output:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderPlanListUsesCellWidthsForUnicode(t *testing.T) {
	var out bytes.Buffer

	err := (newTestApp(App{})).renderPlanList(&out, []plan.PlanSummary{{
		ID: "20260810-2142-café", Status: plan.StatusPlanned,
	}}, time.Date(2026, 8, 10, 22, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := "STATUS   PLAN ID        PLAN  DONE  UPDATED  ABANDONED  REASON\n" +
		"planned  20260810-2142  café  0/0   -        -          -     \n"
	if got := stripANSI(out.String()); got != want {
		t.Fatalf("renderPlanList() Unicode output:\n%q\nwant:\n%q", got, want)
	}
}

func TestRenderPlanListShowsSafeCompactAbandonmentEvidence(t *testing.T) {
	now := time.Date(2026, 9, 1, 18, 0, 0, 0, time.UTC)
	abandonedAt := now.Add(-30 * time.Minute)
	var out bytes.Buffer

	err := (newTestApp(App{})).renderPlanList(&out, []plan.PlanSummary{{
		ID: "20260901-1700-abandoned", Status: plan.StatusAbandoned,
		Abandonment: &plan.AbandonmentEvidence{
			AbandonedAt: abandonedAt,
			Reason:      " superseded\nby\ta safer path\x1b[31m " + strings.Repeat("界", 120),
		},
	}}, now)
	if err != nil {
		t.Fatal(err)
	}
	text := stripANSI(out.String())
	if !strings.Contains(text, "ABANDONED") || !strings.Contains(text, "30m") || !strings.Contains(text, "superseded by a safer path [31m") {
		t.Fatalf("abandonment evidence missing from list:\n%s", text)
	}
	if strings.Contains(text, "\nby") || strings.Contains(text, "\t") || strings.Contains(text, "\x1b") || strings.Contains(text, strings.Repeat("界", 100)) {
		t.Fatalf("list rendered unsafe or unbounded abandonment reason: %q", text)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 2 || cells.Width(lines[0]) != cells.Width(lines[1]) {
		t.Fatalf("list columns do not align for CJK abandonment reason: %q", text)
	}
}

func TestRenderPlanListUsesHumanUpdatedTime(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-42 * time.Minute)
	old := now.Add(-25 * time.Hour)
	var out bytes.Buffer

	err := (newTestApp(App{})).renderPlanList(&out, []plan.PlanSummary{
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

// This seam deliberately implements only ListPlans: JSON cannot load individual plans.
type jsonPlanLister struct{ rows []plan.PlanSummary }

func (r jsonPlanLister) ListPlans(context.Context, plan.PlanFilter) ([]plan.PlanSummary, error) {
	return r.rows, nil
}

func TestListJSONInvalidDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "broken-plan"), 0700); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Repository: func(dir string) Repository { return plan.NewFileRepository(dir) }})
	if err := app.Run(context.Background(), []string{"--plans-dir", dir, "list", "--json"}); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0]["id"] != "broken-plan" || rows[0]["next_action"] != nil {
		t.Fatalf("invalid rows: %s", out.String())
	}
	if warnings, ok := rows[0]["warnings"].([]any); !ok || len(warnings) == 0 {
		t.Fatalf("warnings: %s", out.String())
	}
}

func TestListJSONContract(t *testing.T) {
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	summary := plan.PlanSummary{ID: "20261111-120000-snow", Status: plan.StatusAbandoned, CompletedCount: 2, TotalCount: 3, LastActivityAt: &at, Abandonment: &plan.AbandonmentEvidence{AbandonedAt: at, Reason: "雪\n reason\x1b"}}
	summary.NextAction.Primary.Kind = "inspect"
	var out bytes.Buffer
	app := newTestApp(App{Out: &out})
	if err := app.list(context.Background(), jsonPlanLister{[]plan.PlanSummary{summary}}, []string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0]) != 8 {
		t.Fatalf("shape: %s", out.String())
	}
	var shared struct {
		Progress    planview.ShowProgress     `json:"progress"`
		Abandonment *planview.ShowAbandonment `json:"abandonment"`
		Next        plan.PlanNextAction       `json:"next_action"`
		Warnings    []string                  `json:"warnings"`
	}
	encoded, _ := json.Marshal(rows[0])
	if err := json.Unmarshal(encoded, &shared); err != nil {
		t.Fatal(err)
	}
	if shared.Progress.Completed != 2 || shared.Progress.Total != 3 || shared.Abandonment.Reason != "雪 reason" || !shared.Abandonment.AbandonedAt.Equal(at) || shared.Next.Primary.Reason != "the plan was abandoned" || shared.Next.Alternatives == nil || shared.Warnings == nil {
		t.Fatalf("shared: %+v", shared)
	}
	if string(rows[0]["id"]) != `"20261111-120000-snow"` || string(rows[0]["slug"]) != `"snow"` {
		t.Fatalf("IDs: %s", encoded)
	}
	boom := errors.New("writer failed")
	app.Out = failingWriter{err: boom}
	if err := app.list(context.Background(), jsonPlanLister{}, []string{"--json"}); !errors.Is(err, boom) {
		t.Fatalf("writer: %v", err)
	}
	if err := app.list(context.Background(), fakeRepository{err: boom}, []string{"--json"}); !errors.Is(err, boom) {
		t.Fatalf("repo: %v", err)
	}
	if err := app.list(context.Background(), jsonPlanLister{}, []string{"--json", "--limit", "-1"}); err == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestListJSONSelectionParity(t *testing.T) {
	rows := []plan.PlanSummary{{ID: "20261111-120000-first", Status: plan.StatusInProgress, IsActive: true}, {ID: "20261110-120000-second", Status: plan.StatusCompleted}}
	for _, flags := range [][]string{{}, {"--active"}, {"--limit", "1"}, {"--limit", "0"}} {
		var human, machine bytes.Buffer
		app := newTestApp(App{Out: &human, Repository: func(string) Repository { return fakeRepository{summaries: rows} }})
		args := append([]string{"--plans-dir", t.TempDir(), "list"}, flags...)
		if err := app.Run(context.Background(), args); err != nil {
			t.Fatal(err)
		}
		app.Out = &machine
		if err := app.Run(context.Background(), append(args, "--json")); err != nil {
			t.Fatal(err)
		}
		var got []map[string]any
		if err := json.Unmarshal(machine.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			selected := strings.Contains(human.String(), listPlanLabel(row))
			found := false
			for _, r := range got {
				if r["id"] == row.ID {
					found = true
				}
			}
			if selected != found {
				t.Fatalf("selection differs: %s / %s", human.String(), machine.String())
			}
		}
	}
}

func TestListJSON(t *testing.T) {
	for _, summaries := range [][]plan.PlanSummary{nil, {{ID: "legacy", Title: "雪", Warnings: []string{"invalid"}}}} {
		var out bytes.Buffer
		err := newTestApp(App{Out: &out}).list(context.Background(), fakeRepository{summaries: summaries}, []string{"--json", "--limit", "0"})
		if err != nil {
			t.Fatal(err)
		}
		var rows []map[string]any
		if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		if rows == nil || len(rows) != len(summaries) {
			t.Fatalf("rows: %s", out.String())
		}
		if len(rows) > 0 {
			r := rows[0]
			if r["id"] != "legacy" || r["slug"] != "雪" || r["updated_at"] != nil || r["next_action"] != nil {
				t.Fatalf("row: %#v", r)
			}
		}
	}
}
