package plan

import (
	"fmt"
	"strings"
	"testing"
)

func TestValidateApprovalContractWording(t *testing.T) {
	for _, tt := range []struct {
		name string
		text string
		want bool
	}{
		{"reported check", "The user's mGBA check is supplied through this slice's approval.", true},
		{"being supplied", "The user's mGBA check being supplied through this slice's approval is required.", true},
		{"facts via", "Facts supplied via approval are the implementation input.", true},
		{"observations through", "Observations will be provided through approval.", true},
		{"results via", "Test results must be supplied via the approval.", true},
		{"active supply", "Supply factual observations through this slice's approval.", true},
		{"must state observations", "Approval must state the factual observations.", true},
		{"must state facts", "The approval must state the facts.", true},
		{"template", "Approval must state observer/emulator/version/date/outcome.", true},
		{"template colon", "Approval must state: observer, emulator/version, date, and outcome.", true},
		{"template prose", "Approval must state the observer, emulator, version, date, and outcome.", true},
		{"case whitespace", "FACTS\twill BE\n SUPPLIED  THROUGH\t THIS SLICE’S APPROVAL.", true},
		{"negated supplied", "Facts must not be supplied through approval.", false},
		{"never supplied", "Observations are never supplied via approval.", false},
		{"negated guidance", "Do not supply factual observations through approval.", false},
		{"negated state", "Approval must not state factual observations.", false},
		{"quoted prohibition", "Never require that approval must state observer/emulator/version/date/outcome.", false},
		{"avoid guidance", "Avoid contracts where facts are supplied through approval.", false},
		{"reject guidance", "Reject contracts where facts are supplied through approval.", false},
		{"reject template guidance", "Reject contracts requiring that approval must state observer/emulator/version/date/outcome.", false},
		{"rejected guidance", "Contracts where facts are supplied through approval must be rejected.", false},
		{"reject guidance then assertion", "Reject contracts where facts are supplied through approval. Observations will be provided through approval.", true},
		{"reject guidance then clause assertion", "Reject contracts where facts are supplied through approval; supply factual observations through approval.", true},
		{"affirmative facts", "Facts are supplied through approval.", true},
		{"authorization", "Approval authorizes overwriting the generated file.", false},
		{"release date", "Approval must state the release date chosen by the operator.", false},
		{"overwrite", "Ask for approval before overwriting the observations file.", false},
		{"declared evidence", "Read observer, emulator, version, date, and outcome from evidence.md in required_inputs; approval authorizes publication.", false},
		{"facts in contract", "Observer: Alice. Emulator: mGBA. Version: 0.10. Date: 2026-09-30. Outcome: passed. Approval authorizes release.", false},
		{"ambiguous", "Approval follows the user's mGBA check.", false},
		{"factual words", "Discuss facts, observations, evidence, emulator versions, and approval.", false},
		{"incomplete template", "Approval must state the emulator and date.", false},
		{"separate clauses", "Facts are supplied in evidence.md; proceed through approval.", false},
		{"separate sentences", "Record facts. The release date is supplied through approval.", false},
		{"negative then contradiction", "Approval is not evidence. Facts are supplied via approval.", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			slice := approvalContractSlice()
			slice.Goal = tt.text
			findings := validateApprovalContract(slice)
			if got := len(findings) != 0; got != tt.want {
				t.Fatalf("findings = %+v, want rejection %v for %q", findings, tt.want, tt.text)
			}
		})
	}
}

func TestValidateApprovalContractFields(t *testing.T) {
	text := "Facts are supplied through approval."
	for _, field := range []string{"goal", "context", "tasks[0]", "tasks[1]", "approval.reason"} {
		t.Run(field, func(t *testing.T) {
			slice := approvalContractSlice()
			switch field {
			case "goal":
				slice.Goal = text
			case "context":
				slice.Context = text
			case "tasks[0]":
				slice.Tasks = []string{text, "Implement the change."}
			case "tasks[1]":
				slice.Tasks = []string{"Implement the change.", text}
			case "approval.reason":
				slice.Approval.Reason = text
			}
			findings := validateApprovalContract(slice)
			if len(findings) != 1 {
				t.Fatalf("expected one finding, got %+v", findings)
			}
			finding := findings[0]
			if finding.Code != "approval_factual_payload" || finding.Severity != VerificationFindingError || finding.SliceID != slice.ID || finding.Path != "" || finding.Command != "" {
				t.Fatalf("unexpected owned finding fields: %+v", finding)
			}
			for _, want := range []string{field, "authorization-only", "actual contract facts", "required_inputs", "operator amendment", "contradictory text", "--add-task cannot remove it"} {
				if !strings.Contains(finding.Message, want) {
					t.Errorf("message %q missing %q", finding.Message, want)
				}
			}
		})
	}
}

func TestValidateApprovalContractEligibility(t *testing.T) {
	for _, status := range []string{StatusPending, StatusPlanned, StatusInProgress, StatusInReview, StatusReviewed, StatusChangesRequested, StatusVerificationFailed, StatusCompleted, StatusAbandoned, StatusSkipped, StatusBlocked, StatusInvalid, "", "unknown"} {
		for _, required := range []bool{false, true} {
			for _, approved := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/required=%v/approved=%v", status, required, approved), func(t *testing.T) {
					slice := approvalContractSlice()
					slice.Status = status
					slice.Goal = "Facts are supplied via approval."
					slice.Approval.Required = required
					slice.Approval.Approved = approved
					want := status == StatusPending && required
					if findings := validateApprovalContract(slice); (len(findings) != 0) != want {
						t.Fatalf("findings = %+v, want rejection %v", findings, want)
					}
				})
			}
		}
	}
	slice := approvalContractSlice()
	slice.Goal = "Facts are supplied via approval."
	slice.Approval = nil
	if findings := validateApprovalContract(slice); len(findings) != 0 {
		t.Fatalf("nil approval rejected: %+v", findings)
	}
}

func TestValidateApprovalContractDeclaredInputDoesNotExcuseContradiction(t *testing.T) {
	slice := approvalContractSlice()
	slice.RequiredInputs = []RequiredInput{{Path: "evidence.md", Kind: RequiredInputFile, Reason: "Actual observation record"}}
	slice.Goal = "Facts are supplied through approval."
	slice.Context = "Read evidence.md."
	slice.Tasks = []string{"Approval must state observer/emulator/version/date/outcome.", "Use evidence.md instead of approval for facts."}
	findings := validateApprovalContract(slice)
	if len(findings) != 2 || !strings.Contains(findings[0].Message, "goal") || !strings.Contains(findings[1].Message, "tasks[0]") {
		t.Fatalf("expected both contradictions in field order despite declared input and appended guidance: %+v", findings)
	}
}

func approvalContractSlice() Slice {
	return Slice{ID: "001-a", Status: StatusPending, Approval: &Approval{Required: true}}
}
