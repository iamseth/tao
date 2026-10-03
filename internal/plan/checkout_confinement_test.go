package plan

import (
	"strings"
	"testing"
)

func TestValidateCheckoutConfinementFields(t *testing.T) {
	for _, phrase := range []string{"owning checkout", "control checkout", "main checkout", "another checkout"} {
		for _, field := range []string{"goal", "context", "tasks[1]"} {
			t.Run(phrase+"/"+field, func(t *testing.T) {
				slice := Slice{ID: "001-a", Status: StatusPending}
				text := "Run git branch -d in the " + strings.ToUpper(phrase) + ". Then inspect the " + phrase + "."
				switch field {
				case "goal":
					slice.Goal = text
				case "context":
					slice.Context = text
				default:
					slice.Tasks = []string{"Stay in the workspace", text}
				}
				findings := validateCheckoutConfinement(slice)
				if len(findings) != 1 {
					t.Fatalf("expected one finding per field, got %+v", findings)
				}
				finding := findings[0]
				if finding.Code != "slice_foreign_checkout_reference" || finding.Severity != VerificationFindingWarning || finding.SliceID != slice.ID {
					t.Fatalf("unexpected finding: %+v", finding)
				}
				for _, want := range []string{field, "confined to the plan workspace", "Git branches and refs", "shared by every worktree", "workspace root", "rephrase"} {
					if !strings.Contains(finding.Message, want) {
						t.Errorf("message %q missing %q", finding.Message, want)
					}
				}
			})
		}
	}
}

func TestValidateCheckoutConfinementConservative(t *testing.T) {
	for _, text := range []string{
		"Inputs must be available in the execution worktree, not merely in the control checkout",
		"Never edit the main checkout; avoid another checkout! Do not use the owning checkout? Reject the control checkout.",
		"Don’t use the control checkout.",
		"Run git branch -d from the workspace root.",
		"Inspect the remaining checkout and main checkouts and controlling checkout.",
	} {
		t.Run(text, func(t *testing.T) {
			if got := validateCheckoutConfinement(Slice{Status: StatusPending, Goal: text}); got != nil {
				t.Fatalf("expected no findings, got %+v", got)
			}
		})
	}
	for _, separator := range []string{".", "!", "?", ";"} {
		text := "Do not leave the workspace" + separator + " Inspect the main checkout"
		if got := validateCheckoutConfinement(Slice{Status: StatusPending, Goal: text}); len(got) != 1 {
			t.Errorf("clause separator %q: %+v", separator, got)
		}
	}
	for _, status := range []string{StatusCompleted, StatusBlocked, StatusInProgress} {
		if got := validateCheckoutConfinement(Slice{Status: status, Goal: "Inspect the control checkout"}); got != nil {
			t.Errorf("status %s: %+v", status, got)
		}
	}
}

func TestValidateCheckoutConfinementReportsEachField(t *testing.T) {
	findings := validateCheckoutConfinement(Slice{
		Status: StatusPending,
		Goal:   "Inspect the main checkout", Context: "Inspect the owning checkout",
		Tasks: []string{"Inspect another checkout", "Inspect the control checkout"},
	})
	if len(findings) != 4 {
		t.Fatalf("expected four offending fields, got %+v", findings)
	}
}
