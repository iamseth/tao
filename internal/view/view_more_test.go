package view

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestFormatFinalVerificationHint(t *testing.T) {
	for _, tc := range []struct {
		name, reason, want string
	}{
		{"test", "--- FAIL: TestX (0.01s)", "TestX"},
		{"package", "FAIL\tpkg", "FAIL pkg"},
		{"historical", "ok example/pkg 0.01s coverage: 90%", ""},
		{"multiline", "FAIL\tpkg\r\n--- FAIL: TestX (0.01s)", "TestX"},
		{"controls and bound", "\x1b\t--- FAIL:\x00 " + strings.Repeat("界", 120) + "\r (0.01s)", strings.Repeat("界", blockerReasonExcerptRunes-1) + "…"},
		{"package controls", "FAIL\tpkg\x00\x1b details\r\n", "FAIL pkg details"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FormatFinalVerificationHint(tc.reason); got != tc.want {
				t.Fatalf("FormatFinalVerificationHint(%q) = %q; want %q", tc.reason, got, tc.want)
			}
		})
	}
}

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
