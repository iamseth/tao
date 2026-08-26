package reviewcontract

import (
	"encoding/json"
	"strings"

	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
)

const (
	maxJSONBlockBytes       = 512 * 1024
	maxSummaryRunes         = 8 * 1024
	maxFindings             = 50
	maxFindingSeverityRunes = 64
	maxFindingFileRunes     = 512
	maxFindingTextRunes     = 4 * 1024
)

// CommitProposalPolicy states whether an approval is valid without a bounded,
// commit-package-validated proposal. Ordinary plan reviews use
// CommitProposalRequired; aggregate reviews use CommitProposalOptional.
type CommitProposalPolicy uint8

const (
	CommitProposalRequired CommitProposalPolicy = iota
	CommitProposalOptional
)

// Review is the safe projection of one modern structured review block.
type Review struct {
	Verdict       string
	Summary       string
	FindingsCount int
	Findings      []plan.ReviewFinding
	CommitMessage *plan.ReviewCommitMessage
}

// Parse decodes the last tao-review-json fenced block. Malformed, oversized,
// unsupported, or policy-invalid output degrades to a bounded comment whose
// summary is the original output. Findings are always non-nil in the result.
func Parse(output string, proposalPolicy CommitProposalPolicy) Review {
	fallback := fallbackReview(output)
	block, ok := lastReviewJSONBlock(output)
	if !ok {
		return fallback
	}

	var payload struct {
		Verdict       string                    `json:"verdict"`
		Summary       string                    `json:"summary"`
		Findings      json.RawMessage           `json:"findings"`
		CommitMessage *plan.ReviewCommitMessage `json:"commit_message"`
	}
	if err := json.Unmarshal([]byte(block), &payload); err != nil {
		return fallback
	}
	if !validVerdict(payload.Verdict) || !validProposalPolicy(proposalPolicy) {
		return fallback
	}

	validCommitMessage := payload.Verdict == plan.ReviewVerdictApprove && validCommitProposal(payload.CommitMessage)
	if payload.Verdict == plan.ReviewVerdictApprove && proposalPolicy == CommitProposalRequired && !validCommitMessage {
		return fallback
	}

	findings := []plan.ReviewFinding{}
	if len(payload.Findings) > 0 && string(payload.Findings) != "null" {
		if err := json.Unmarshal(payload.Findings, &findings); err != nil {
			return fallback
		}
	}
	findings = normalizeFindings(findings)

	summary := capString(strings.TrimSpace(payload.Summary), maxSummaryRunes)
	if summary == "" {
		summary = fallback.Summary
	}
	var commitMessage *plan.ReviewCommitMessage
	if validCommitMessage {
		commitMessage = payload.CommitMessage
	}
	return Review{
		Verdict:       payload.Verdict,
		Summary:       summary,
		FindingsCount: len(findings),
		Findings:      findings,
		CommitMessage: commitMessage,
	}
}

// ParseLegacyFindings returns a bounded findings-only projection from the last
// tao-review-json fenced block. It intentionally does not require a verdict,
// summary, or commit proposal so historical review artifacts remain readable.
// Missing, malformed, and oversized blocks return nil.
func ParseLegacyFindings(content string) []plan.ReviewFinding {
	block, ok := lastReviewJSONBlock(content)
	if !ok {
		return nil
	}
	var payload struct {
		Findings json.RawMessage `json:"findings"`
	}
	if err := json.Unmarshal([]byte(block), &payload); err != nil || len(payload.Findings) == 0 || string(payload.Findings) == "null" {
		return nil
	}
	var findings []plan.ReviewFinding
	if err := json.Unmarshal(payload.Findings, &findings); err != nil {
		return nil
	}
	return normalizeFindings(findings)
}

func lastReviewJSONBlock(output string) (string, bool) {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(output, "\r\n", "\n"), "\r", "\n"), "\n")
	var current []string
	last := ""
	inBlock := false
	found := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if trimmed == "```tao-review-json" || trimmed == "``` tao-review-json" {
				current = current[:0]
				inBlock = true
			}
			continue
		}
		if trimmed == "```" {
			last = strings.TrimSpace(strings.Join(current, "\n"))
			found = true
			inBlock = false
			continue
		}
		current = append(current, line)
	}
	if !found || len(last) > maxJSONBlockBytes {
		return "", false
	}
	return last, true
}

func fallbackReview(output string) Review {
	return Review{
		Verdict:  plan.ReviewVerdictComment,
		Summary:  capString(strings.TrimSpace(output), maxSummaryRunes),
		Findings: []plan.ReviewFinding{},
	}
}

func validProposalPolicy(policy CommitProposalPolicy) bool {
	return policy == CommitProposalRequired || policy == CommitProposalOptional
}

func validCommitProposal(message *plan.ReviewCommitMessage) bool {
	if message == nil || len([]rune(message.Subject)) > maxSummaryRunes || len([]rune(message.Body)) > maxSummaryRunes {
		return false
	}
	return commitcontract.ValidateProposalMessage(message.Subject, message.Body) == nil
}

func validVerdict(verdict string) bool {
	switch verdict {
	case plan.ReviewVerdictApprove, plan.ReviewVerdictChangesRequested, plan.ReviewVerdictComment:
		return true
	default:
		return false
	}
}

func normalizeFindings(findings []plan.ReviewFinding) []plan.ReviewFinding {
	if len(findings) > maxFindings {
		findings = findings[:maxFindings]
	}
	normalized := make([]plan.ReviewFinding, 0, len(findings))
	for _, finding := range findings {
		if finding.Line < 0 {
			finding.Line = 0
		}
		finding.Severity = capString(strings.TrimSpace(finding.Severity), maxFindingSeverityRunes)
		finding.File = capString(strings.TrimSpace(finding.File), maxFindingFileRunes)
		finding.Message = capString(strings.TrimSpace(finding.Message), maxFindingTextRunes)
		finding.Suggestion = capString(strings.TrimSpace(finding.Suggestion), maxFindingTextRunes)
		normalized = append(normalized, finding)
	}
	return normalized
}

func capString(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}
