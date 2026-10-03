package plan

import (
	"fmt"
	"regexp"
	"strings"
)

var foreignCheckoutPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\bowning checkout\b`),
	regexp.MustCompile(`(?i)\bcontrol checkout\b`),
	regexp.MustCompile(`(?i)\bmain checkout\b`),
	regexp.MustCompile(`(?i)\banother checkout\b`),
}

// validateCheckoutConfinement is advisory and belongs only to plan-wide
// validation, never selected-slice preflight or run admission.
func validateCheckoutConfinement(slice Slice) []VerificationFinding {
	if slice.Status != StatusPending {
		return nil
	}
	var findings []VerificationFinding
	inspect := func(field, text string) {
		if !referencesForeignCheckout(text) {
			return
		}
		findings = append(findings, VerificationFinding{
			Severity: VerificationFindingWarning,
			SliceID:  slice.ID,
			Code:     "slice_foreign_checkout_reference",
			Message:  fmt.Sprintf("%s names another checkout; execution is confined to the plan workspace. Git branches and refs are shared by every worktree and are operated on from the workspace root; rephrase the text to keep work inside the plan workspace.", field),
		})
	}
	inspect("goal", slice.Goal)
	inspect("context", slice.Context)
	for i, task := range slice.Tasks {
		inspect(fmt.Sprintf("tasks[%d]", i), task)
	}
	return findings
}

func referencesForeignCheckout(text string) bool {
	text = strings.ToLower(strings.ReplaceAll(text, "’", "'"))
	for clause := range strings.FieldsFuncSeq(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == ';'
	}) {
		clause = strings.Join(strings.Fields(clause), " ")
		// Conservatively skip negated guidance even when its scope is ambiguous.
		if approvalContractNegation.MatchString(clause) {
			continue
		}
		for _, pattern := range foreignCheckoutPatterns {
			if pattern.MatchString(clause) {
				return true
			}
		}
	}
	return false
}
