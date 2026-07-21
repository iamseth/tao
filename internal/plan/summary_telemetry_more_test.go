package plan

import "testing"

func TestPlanSummaryRunnable(t *testing.T) {
	if !(PlanSummary{Status: StatusPlanned, PendingCount: 1}).Runnable() {
		t.Fatal("planned pending summary should be runnable")
	}
	for _, summary := range []PlanSummary{{Complete: true, PendingCount: 1}, {Status: StatusCompleted, PendingCount: 1}, {Status: StatusPlanned}} {
		if summary.Runnable() {
			t.Fatalf("summary should not be runnable: %#v", summary)
		}
	}
}

func TestAgentBudgetWarningsFromDetail(t *testing.T) {
	detail := &PlanDetail{Events: []Event{{Type: EventTypeAgentMetrics, SliceID: "001-a", Metrics: &AgentMetrics{Agent: "pi", SessionID: "s", TotalTokens: DefaultAgentTotalTokensBudget + 1, ToolCalls: DefaultAgentToolCallsBudget + 2, AssistantMessages: DefaultAgentAssistantMessagesBudget + 3, ErroredMessages: DefaultAgentErroredMessagesBudget + 4}}}}
	warnings := AgentBudgetWarnings(detail)
	if len(warnings) == 0 {
		t.Fatal("expected budget warnings")
	}
	seenPlan := false
	seenSlice := false
	for _, warning := range warnings {
		if warning.Scope == "plan" && warning.SliceID == "" {
			seenPlan = true
		}
		if warning.Scope == "slice" && warning.SliceID == "001-a" {
			seenSlice = true
		}
		if warning.Observed <= warning.Threshold || warning.Message == "" {
			t.Fatalf("unexpected warning %#v", warning)
		}
	}
	if !seenPlan || !seenSlice {
		t.Fatalf("expected plan and slice warnings, got %#v", warnings)
	}
}
