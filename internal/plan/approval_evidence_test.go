package plan

import (
	"strings"
	"testing"
	"unicode"
)

func TestCheckSliceApprovalEvidence(t *testing.T) {
	tests := []struct {
		name, text string
		want       bool
	}{
		{"state observations", "Approval must state factual observations from the deployment.", true},
		{"include observations", "Approval must include observations from production.", true},
		{"facts route", "Facts supplied through approval determine the implementation.", true},
		{"required route", "Factual observations must be supplied via approval.", true},
		{"case whitespace", "APPROVAL\t MUST\n INCLUDE factual\tobservations.", true},
		{"authorization", "Approval authorizes overwriting the selected files.", false},
		{"confirmation", "Approval must confirm the chosen approach.", false},
		{"isolated include", "Approval is required. The report must include factual observations.", false},
		{"non factual include", "Approval must include a reviewer name.", false},
		{"authorization references facts", "Approval must include confirmation that observations were reviewed.", false},
		{"observations elsewhere", "Approval must state that facts are recorded in the report.", false},
		{"no facts", "Approval carries no facts; put observations in tasks.", false},
		{"negated", "Approval must not include factual observations.", false},
		{"negated instruction", "Do not require that approval must include factual observations.", false},
		{"quoted negative", "Never say approval must state factual observations.", false},
		{"optional", "Approval may include observations.", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, field := range []string{"reason", "goal", "context", "tasks"} {
				s := &Slice{ID: "001-a", Approval: &Approval{Required: true, Approved: true}}
				switch field {
				case "reason":
					s.Approval.Reason = tt.text
				case "goal":
					s.Goal = tt.text
				case "context":
					s.Context = tt.text
				case "tasks":
					s.Tasks = []string{tt.text}
				}
				got := CheckSliceApprovalEvidence(s)
				if (got != nil) != tt.want {
					t.Fatalf("%s: got %v, want refusal=%t", field, got, tt.want)
				}
				if got != nil && (got.SliceID != s.ID || got.Requirement == "" || !strings.Contains(got.Error(), s.ID)) {
					t.Fatalf("bad error: %#v", got)
				}
			}
		})
	}
}

func TestCheckSliceApprovalEvidenceRoutes(t *testing.T) {
	contract := "Approval must include factual observations."
	for _, tt := range []struct {
		name  string
		slice *Slice
		want  bool
	}{
		{name: "nil"},
		{name: "legacy", slice: &Slice{Goal: contract}},
		{name: "ungated", slice: &Slice{Goal: contract, Approval: &Approval{}}},
		{name: "optional approved", slice: &Slice{Goal: contract, Approval: &Approval{Approved: true}}},
		{name: "blocker and logs", slice: &Slice{BlockerNote: contract, Notes: contract, Approval: &Approval{Required: true}}},
		{name: "required input", slice: &Slice{Goal: contract, Approval: &Approval{Required: true}, RequiredInputs: []RequiredInput{{Path: "facts.md", Kind: RequiredInputFile}}}},
		// The classifier does not validate declarations: ordinary preflight still must.
		{name: "invalid input declaration", slice: &Slice{Goal: contract, Approval: &Approval{Required: true}, RequiredInputs: []RequiredInput{{Kind: "invalid"}}}},
		{name: "reapproved", slice: &Slice{Goal: contract, Approval: &Approval{Required: true, Approved: true}}, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := CheckSliceApprovalEvidence(tt.slice); (got != nil) != tt.want {
				t.Fatalf("got %v, want refusal=%t", got, tt.want)
			}
		})
	}
	for _, field := range []string{"goal", "tasks", "reason", "expected_files", "manual_checks", "verification.manual_checks", ""} {
		t.Run("amend "+field, func(t *testing.T) {
			s := &Slice{Goal: contract, Approval: &Approval{Required: true}, Amendments: []SliceAmendment{{Fields: []string{field}, Reason: "goal and tasks now supply all facts"}}}
			want := field != "goal" && field != "tasks"
			if got := CheckSliceApprovalEvidence(s); (got != nil) != want {
				t.Fatalf("got %v, want refusal=%t", got, want)
			}
		})
	}
}

func TestApprovalEvidenceRequirementSafeAndBounded(t *testing.T) {
	s := &Slice{ID: "001-a", Approval: &Approval{Required: true}, Goal: "Approval must include factual observations \x1b[31m\x00\u202e" + strings.Repeat("界", 1000)}
	got := CheckSliceApprovalEvidence(s)
	if got == nil {
		t.Fatal("missing refusal")
	}
	if len(got.Requirement) > 256 {
		t.Fatalf("requirement length = %d", len(got.Requirement))
	}
	for _, r := range got.Requirement {
		if !unicode.IsGraphic(r) {
			t.Fatalf("unsafe rune %U", r)
		}
	}
}
