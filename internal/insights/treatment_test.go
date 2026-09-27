package insights

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/plan"
)

func TestNormalizePlannerLabel(t *testing.T) {
	tests := []struct {
		label, sanitized, runtime, model string
		confidence                       TreatmentConfidence
		source, key                      string
	}{
		{"pi", "pi", "pi", "", TreatmentConfidenceHigh, "plan_created", "pi"},
		{"claude", "claude", "claude", "", TreatmentConfidenceHigh, "plan_created", "claude"},
		{"claude-code", "claude-code", "claude", "", TreatmentConfidenceLow, "plan_created", "claude"},
		{"claude-code/fable-5", "claude-code/fable-5", "claude", "fable-5", TreatmentConfidenceLow, "plan_created", "claude/fable-5"},
		{"claude-fable-5", "claude-fable-5", "claude", "claude-fable-5", TreatmentConfidenceLow, "plan_created", "claude/claude-fable-5"},
		{"claude-opus-5", "claude-opus-5", "claude", "claude-opus-5", TreatmentConfidenceLow, "plan_created", "claude/claude-opus-5"},
		{"claude-opus-5[1m]", "claude-opus-5[1m]", "claude", "claude-opus-5[1m]", TreatmentConfidenceLow, "plan_created", "claude/claude-opus-5[1m]"},
		{"gpt-5.6-sol", "gpt-5.6-sol", "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"openai-codex/gpt-5.6-sol", "openai-codex/gpt-5.6-sol", "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"pi:gpt-5.6-sol", "pi:gpt-5.6-sol", "pi", "gpt-5.6-sol", TreatmentConfidenceLow, "plan_created", "pi/gpt-5.6-sol"},
		{"pi/gpt-5.6-sol", "pi/gpt-5.6-sol", "pi", "gpt-5.6-sol", TreatmentConfidenceLow, "plan_created", "pi/gpt-5.6-sol"},
		{"pi/openai-codex/gpt-5.6-sol", "pi/openai-codex/gpt-5.6-sol", "pi", "gpt-5.6-sol", TreatmentConfidenceLow, "plan_created", "pi/gpt-5.6-sol"},
		{"build", "build", "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"", "", "", "", TreatmentConfidenceAmbiguous, "none", ""},
		{"unknown", "unknown", "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"Pi:GPT-5.6-SOL", "pi:gpt-5.6-sol", "pi", "gpt-5.6-sol", TreatmentConfidenceLow, "plan_created", "pi/gpt-5.6-sol"},
		{" \tCLAUDE\n", "claude", "claude", "", TreatmentConfidenceHigh, "plan_created", "claude"},
		{" \t\n", "", "", "", TreatmentConfidenceAmbiguous, "none", ""},
		{strings.Repeat("界", 200), strings.Repeat("界", 64), "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"p\x00i\x1b[31m", "p_i_[31m", "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"claude-code/unlisted", "claude-code/unlisted", "claude", "", TreatmentConfidenceLow, "plan_created", "claude"},
		{"claude-unlisted", "claude-unlisted", "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"pi:", "pi:", "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"pi/", "pi/", "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"pi/openai-codex/", "pi/openai-codex/", "", "", TreatmentConfidenceAmbiguous, "plan_created", ""},
		{"pi/模型\x00*", "pi/模型_*", "pi", "模型_*", TreatmentConfidenceLow, "plan_created", "pi/____"},
	}
	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			got := NormalizePlannerLabel(tt.label)
			want := PlannerTreatment{Runtime: tt.runtime, Model: tt.model, Confidence: tt.confidence, Label: tt.sanitized, Source: tt.source}
			if got != want {
				t.Fatalf("NormalizePlannerLabel() = %#v, want %#v", got, want)
			}
			if key := got.CohortKey(); key != tt.key {
				t.Errorf("CohortKey() = %q, want %q", key, tt.key)
			}
			if !utf8.ValidString(got.Label) || utf8.RuneCountInString(got.Label) > maxTreatmentLabelRunes {
				t.Errorf("invalid or unbounded label: %q", got.Label)
			}
		})
	}
}

func TestResolvePlannerTreatment(t *testing.T) {
	tests := []struct {
		name, label string
		metrics     []plan.AgentMetrics
		want        PlannerTreatment
	}{
		{
			name: "no metrics", label: "pi",
			want: PlannerTreatment{Runtime: "pi", Confidence: TreatmentConfidenceHigh, Label: "pi", Source: "plan_created"},
		},
		{
			name: "override conflicting model", label: "pi:gpt-5.6-sol",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, Agent: "pi", ProviderID: " OpenAI ", ModelID: " GPT-OTHER "}},
			want:    PlannerTreatment{Runtime: "pi", Provider: "openai", Model: "gpt-other", Confidence: TreatmentConfidenceLow, Label: "pi:gpt-5.6-sol", Source: "planning_metrics"},
		},
		{
			name: "agree on runtime", label: "pi",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, Agent: "PI", ProviderID: "openai", ModelID: "gpt-5.6-sol"}},
			want:    PlannerTreatment{Runtime: "pi", Provider: "openai", Model: "gpt-5.6-sol", Confidence: TreatmentConfidenceHigh, Label: "pi", Source: "planning_metrics"},
		},
		{
			name: "agree on model", label: "pi:gpt-5.6-sol",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, Agent: "pi", ModelID: "gpt-5.6-sol"}},
			want:    PlannerTreatment{Runtime: "pi", Model: "gpt-5.6-sol", Confidence: TreatmentConfidenceHigh, Label: "pi:gpt-5.6-sol", Source: "planning_metrics"},
		},
		{
			name: "rescue ambiguous label", label: "build",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, Agent: "claude-code", ProviderID: "anthropic", ModelID: "claude-opus-5[1m]"}},
			want:    PlannerTreatment{Runtime: "claude", Provider: "anthropic", Model: "claude-opus-5[1m]", Confidence: TreatmentConfidenceLow, Label: "build", Source: "planning_metrics"},
		},
		{
			name:    "rescue absent label",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, Agent: "pi", ProviderID: "openai"}},
			want:    PlannerTreatment{Runtime: "pi", Provider: "openai", Confidence: TreatmentConfidenceLow, Source: "planning_metrics"},
		},
		{
			name: "unidentified runtime remains excluded", label: "build",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, Agent: "unknown", ModelID: "gpt-5.6-sol"}},
			want:    PlannerTreatment{Model: "gpt-5.6-sol", Confidence: TreatmentConfidenceAmbiguous, Label: "build", Source: "planning_metrics"},
		},
		{
			name: "runtime disagreement", label: "pi",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, Agent: "claude", ModelID: "claude-opus-5"}},
			want:    PlannerTreatment{Runtime: "pi", Model: "claude-opus-5", Confidence: TreatmentConfidenceLow, Label: "pi", Source: "planning_metrics"},
		},
		{
			name: "no agent agreement", label: "pi",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, ModelID: "gpt-5.6-sol"}},
			want:    PlannerTreatment{Runtime: "pi", Model: "gpt-5.6-sol", Confidence: TreatmentConfidenceLow, Label: "pi", Source: "planning_metrics"},
		},
		{
			name: "provider only preserves label model", label: "claude-opus-5",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, Agent: "claude", ProviderID: "anthropic"}},
			want:    PlannerTreatment{Runtime: "claude", Provider: "anthropic", Model: "claude-opus-5", Confidence: TreatmentConfidenceLow, Label: "claude-opus-5", Source: "planning_metrics"},
		},
		{
			name: "ignore other roles and empty identities", label: "pi",
			metrics: []plan.AgentMetrics{
				{Agent: "claude", ModelID: "legacy"},
				{Role: plan.AgentRoleExecution, Agent: "claude", ModelID: "execution"},
				{Role: plan.AgentRoleReview, Agent: "claude", ProviderID: "review"},
				{Role: plan.AgentRolePlanning, Agent: "claude", ProviderID: " \t", ModelID: "\n"},
			},
			want: PlannerTreatment{Runtime: "pi", Confidence: TreatmentConfidenceHigh, Label: "pi", Source: "plan_created"},
		},
		{
			name: "last qualifying event without mixing sessions", label: "build",
			metrics: []plan.AgentMetrics{
				{Role: plan.AgentRolePlanning, Agent: "claude", ProviderID: "anthropic", ModelID: "claude-opus-5"},
				{Role: plan.AgentRolePlanning, Agent: "pi", ModelID: "gpt-5.6-sol"},
				{Role: plan.AgentRolePlanning, Agent: "claude"},
			},
			want: PlannerTreatment{Runtime: "pi", Model: "gpt-5.6-sol", Confidence: TreatmentConfidenceLow, Label: "build", Source: "planning_metrics"},
		},
		{
			name: "bounded sanitized metrics", label: "build",
			metrics: []plan.AgentMetrics{{Role: plan.AgentRolePlanning, Agent: "pi", ProviderID: "OPEN\x00AI", ModelID: strings.Repeat("界", 200)}},
			want:    PlannerTreatment{Runtime: "pi", Provider: "open_ai", Model: strings.Repeat("界", 64), Confidence: TreatmentConfidenceLow, Label: "build", Source: "planning_metrics"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolvePlannerTreatment(tt.label, tt.metrics)
			if got != tt.want {
				t.Fatalf("ResolvePlannerTreatment() = %#v, want %#v", got, tt.want)
			}
			if got.Confidence == TreatmentConfidenceAmbiguous && got.CohortKey() != "" {
				t.Errorf("ambiguous treatment has cohort key %q", got.CohortKey())
			}
		})
	}
}

func TestPlannerTreatmentCohortKey(t *testing.T) {
	for _, treatment := range []PlannerTreatment{
		{},
		{Model: "gpt", Confidence: TreatmentConfidenceLow},
		{Runtime: "pi", Confidence: TreatmentConfidenceAmbiguous},
	} {
		if got := treatment.CohortKey(); got != "" {
			t.Errorf("%#v.CohortKey() = %q, want empty", treatment, got)
		}
	}
	treatment := PlannerTreatment{Runtime: "PI\x1b", Model: strings.Repeat("A/模型*", 200), Confidence: TreatmentConfidenceLow}
	key := treatment.CohortKey()
	if len(key) > 2*maxTreatmentLabelRunes+1 || key == "" {
		t.Fatalf("unexpected key length: %d", len(key))
	}
	for _, r := range key {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune(".-_/:[]", r) {
			continue
		}
		t.Fatalf("unsafe cohort key: %q", key)
	}
}
