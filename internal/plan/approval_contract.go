package plan

import (
	"fmt"
	"regexp"
	"strings"
)

// These patterns recognize explicit payload transport, not factual vocabulary
// alone. Keep them deliberately non-exhaustive: ambiguous prose is not an error.
var approvalPayloadPatterns = func() []*regexp.Regexp {
	facts := `\b(?:facts|factual observations|observations|evidence|results|check)\b`
	route := `(?:via|through) (?:the |this |this slice's |the slice's |slice )?approval\b`
	return []*regexp.Regexp{
		regexp.MustCompile(facts + ` (?:is |are |will be |must be |to be |being |be )?(?:supplied|provided) ` + route),
		regexp.MustCompile(`\b(?:supply|provide) (?:the )?` + facts + ` ` + route),
		regexp.MustCompile(`\bapproval must state:? (?:the )?(?:facts|factual observations|observations|observed results)\b`),
		regexp.MustCompile(`\bapproval must state:? (?:the )?observer[ ,/]+emulator[ ,/]+version[ ,/]+date[ ,/]+(?:and )?outcome\b`),
	}
}()

var approvalContractNegation = regexp.MustCompile(`\b(?:not|never|no|don't|doesn't|isn't|aren't|cannot|can't|avoid|prohibit|prohibited|reject(?:s|ed|ing)?)\b`)

// validateApprovalContract belongs only to plan-wide validation. Historical
// loading and selected-slice runtime preflight do not enforce prose contracts.
func validateApprovalContract(slice Slice) []VerificationFinding {
	if slice.Status != StatusPending || slice.Approval == nil || !slice.Approval.Required {
		return nil
	}
	var findings []VerificationFinding
	inspect := func(field, text string) {
		if !approvalCarriesFacts(text) {
			return
		}
		findings = append(findings, VerificationFinding{
			Severity: VerificationFindingError,
			SliceID:  slice.ID,
			Code:     "approval_factual_payload",
			Message:  fmt.Sprintf("%s explicitly treats approval as factual evidence; approval is authorization-only. Supply actual contract facts or a concrete required_inputs artifact, or defer executable work until an operator amendment supplies the facts in the contract. Correct the contradictory text; --add-task cannot remove it. Declaring an input alone does not resolve the contradiction.", field),
		})
	}
	inspect("goal", slice.Goal)
	inspect("context", slice.Context)
	for i, task := range slice.Tasks {
		inspect(fmt.Sprintf("tasks[%d]", i), task)
	}
	inspect("approval.reason", slice.Approval.Reason)
	return findings
}

func approvalCarriesFacts(text string) bool {
	text = strings.ToLower(strings.ReplaceAll(text, "’", "'"))
	// Do not join unrelated sentences or clauses into a payload assertion.
	for clause := range strings.FieldsFuncSeq(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == ';'
	}) {
		clause = strings.Join(strings.Fields(clause), " ")
		// Negated or prohibitive guidance is left alone, even when its precise
		// scope is ambiguous; this validator is not a semantic analyzer.
		if approvalContractNegation.MatchString(clause) {
			continue
		}
		for _, pattern := range approvalPayloadPatterns {
			if pattern.MatchString(clause) {
				return true
			}
		}
	}
	return false
}
