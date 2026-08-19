package cli

import (
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestApprovalTargetSlice(t *testing.T) {
	if _, err := approvalTargetSlice(nil, ""); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("expected nil detail error, got %v", err)
	}
	detail := &plan.PlanDetail{State: plan.State{Status: plan.StatusPlanned, Plan: plan.PlanState{ID: "plan-a", PendingSlices: []string{"001-a"}}}, Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusPending}}}}
	if got, err := approvalTargetSlice(detail, " explicit "); err != nil || got != "explicit" {
		t.Fatalf("explicit target = %q, %v", got, err)
	}
	current := " current "
	detail.State.Plan.CurrentSlice = &current
	if got, err := approvalTargetSlice(detail, ""); err != nil || got != "current" {
		t.Fatalf("current target = %q, %v", got, err)
	}
	detail.State.Plan.CurrentSlice = nil
	if got, err := approvalTargetSlice(detail, ""); err != nil || got != "001-a" {
		t.Fatalf("pending target = %q, %v", got, err)
	}
	detail.State.Plan.PendingSlices = nil
	if _, err := approvalTargetSlice(detail, ""); err == nil || !strings.Contains(err.Error(), "no pending") {
		t.Fatalf("expected no pending error, got %v", err)
	}
}
