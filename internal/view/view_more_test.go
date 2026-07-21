package view

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestBasicLabelsAndIDs(t *testing.T) {
	if Empty("") != "-" || Empty("x") != "x" {
		t.Fatal("unexpected Empty result")
	}
	for _, tc := range []struct {
		id   string
		want string
	}{
		{id: "20260531-1200-example", want: "20260531-1200"},
		{id: "20260531-120045-example", want: "20260531-120045"},
		{id: "short", want: "short"},
	} {
		if got := ShortPlanID(tc.id); got != tc.want {
			t.Fatalf("ShortPlanID(%q) = %q; want %q", tc.id, got, tc.want)
		}
	}
	summary := plan.PlanSummary{CompletedCount: 2, TotalCount: 5}
	if DoneLabel(summary) != "2/5" {
		t.Fatal("unexpected summary label")
	}
}

func TestRenderAgentBudgetWarnings(t *testing.T) {
	var out bytes.Buffer
	err := RenderAgentBudgetWarnings(&out, []plan.AgentBudgetWarning{{Message: "cost high", Observed: 12, Threshold: 10, SliceID: "001-a"}})
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"Agent Metrics Budget Warnings:", "cost high", "observed 12 > threshold 10", "001-a"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q from %q", want, got)
		}
	}
}
