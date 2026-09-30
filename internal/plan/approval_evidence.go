package plan

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// ApprovalEvidenceError identifies an unresolved approval-as-data contract.
// Approval authorizes work; it does not supply factual observations.
type ApprovalEvidenceError struct {
	SliceID     string
	Requirement string
}

func (e *ApprovalEvidenceError) Error() string {
	return fmt.Sprintf("slice %s requires factual evidence through approval, but approval is authorization-only: %s", e.SliceID, e.Requirement)
}

var approvalEvidencePatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bapproval must (explicitly )?(state|include|provide|supply|record) (the |these |concrete |actual |production |user |runtime |factual )*(facts|observations|observed results)\b`),
	regexp.MustCompile(`\b(facts|factual observations|observations) (must be |are |will be )?(supplied|provided|recorded) (through|via|in) (the )?approval\b`),
}
var approvalEvidenceNegation = regexp.MustCompile(`\b(no|not|never|without|don't|doesn't|isn't|cannot)\b`)

// CheckSliceApprovalEvidence is a conservative admission heuristic, not a
// factual-completeness check. A recorded goal/tasks amendment is operator
// remediation, not attestation. Required inputs take the ordinary declaration
// and execution-root preflight route; their contents are never inspected here.
func CheckSliceApprovalEvidence(slice *Slice) *ApprovalEvidenceError {
	if slice == nil || slice.Approval == nil || !slice.Approval.Required || len(slice.RequiredInputs) > 0 {
		return nil
	}
	for _, amendment := range slice.Amendments {
		for _, field := range amendment.Fields {
			if field == "goal" || field == "tasks" {
				return nil
			}
		}
	}
	texts := append([]string{slice.Approval.Reason, slice.Goal, slice.Context}, slice.Tasks...)
	for _, text := range texts {
		normalized := strings.ToLower(strings.Join(strings.Fields(text), " "))
		for _, clause := range strings.FieldsFunc(normalized, func(r rune) bool { return strings.ContainsRune(".!?;:", r) }) {
			if approvalEvidenceNegation.MatchString(clause) {
				continue
			}
			for _, pattern := range approvalEvidencePatterns {
				if pattern.MatchString(clause) {
					return &ApprovalEvidenceError{SliceID: slice.ID, Requirement: safeApprovalRequirement(clause)}
				}
			}
		}
	}
	return nil
}

func safeApprovalRequirement(text string) string {
	var result strings.Builder
	for _, r := range strings.TrimSpace(text) {
		if !unicode.IsGraphic(r) {
			continue
		}
		if result.Len()+len(string(r)) > 240 {
			result.WriteString("...")
			break
		}
		result.WriteRune(r)
	}
	return result.String()
}
