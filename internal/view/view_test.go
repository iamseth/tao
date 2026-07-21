package view

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type fakeRepository struct {
	detail *plan.PlanDetail
	err    error
}

func (f fakeRepository) GetPlan(ctx context.Context, id string) (*plan.PlanDetail, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.detail, ctx.Err()
}

func TestLoadPlanDerivesState(t *testing.T) {
	now := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	detail := &plan.PlanDetail{
		State:  plan.State{Status: plan.StatusPlanned, Plan: plan.PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusPlanned}}},
	}

	loaded, err := LoadPlan(context.Background(), fakeRepository{detail: detail}, "plan", Options{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Detail != detail || loaded.Now != now {
		t.Fatalf("unexpected loaded plan: %+v", loaded)
	}
	if !loaded.Derived.Runnable || loaded.Derived.NextSliceID != "001-a" {
		t.Fatalf("unexpected derived state: %+v", loaded.Derived)
	}
}

func TestRenderVerificationFindings(t *testing.T) {
	var out bytes.Buffer
	err := RenderVerificationFindings(&out, []plan.VerificationFinding{{
		Severity:   "error",
		SliceID:    "001-a",
		Message:    "missing command",
		Path:       "slices.json",
		Suggestion: "add verification",
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := "Verification Findings:\n- error 001-a: missing command (slices.json) suggestion: add verification\n"
	if got := out.String(); got != want {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestRenderVerificationFindingsSkipsEmptyList(t *testing.T) {
	var out strings.Builder
	if err := RenderVerificationFindings(&out, nil); err != nil {
		t.Fatal(err)
	}
	if out.String() != "" {
		t.Fatalf("expected no output, got %q", out.String())
	}
}
