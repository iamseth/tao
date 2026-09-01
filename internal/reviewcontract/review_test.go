package reviewcontract

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

const validProposalJSON = `"commit_message":{"subject":"refactor(review): centralize review parsing","body":"What:\nCentralize bounded review decoding.\n\nWhy:\nKeep every review consumer on one contract."}`

func fenced(payload string) string {
	return "Review text.\n```tao-review-json\n" + payload + "\n```"
}

func TestParseMalformedOversizedAndMultipleBlocks(t *testing.T) {
	valid := `{"verdict":"comment","summary":"structured","findings":[]}`
	tests := []struct {
		name        string
		output      string
		wantVerdict string
		wantSummary string
	}{
		{
			name:        "missing block falls back",
			output:      "  plain review  ",
			wantVerdict: plan.ReviewVerdictComment,
			wantSummary: "plain review",
		},
		{
			name:        "malformed json falls back",
			output:      fenced(`{"verdict":`),
			wantVerdict: plan.ReviewVerdictComment,
			wantSummary: fenced(`{"verdict":`),
		},
		{
			name:        "malformed findings fall back",
			output:      fenced(`{"verdict":"comment","summary":"bad","findings":"not-an-array"}`),
			wantVerdict: plan.ReviewVerdictComment,
			wantSummary: fenced(`{"verdict":"comment","summary":"bad","findings":"not-an-array"}`),
		},
		{
			name:        "oversized last block falls back",
			output:      fenced(strings.Repeat("x", maxJSONBlockBytes+1)),
			wantVerdict: plan.ReviewVerdictComment,
		},
		{
			name:        "last fenced block wins",
			output:      fenced(`{"verdict":"changes_requested","summary":"first"}`) + "\n" + fenced(valid),
			wantVerdict: plan.ReviewVerdictComment,
			wantSummary: "structured",
		},
		{
			name:        "invalid last block does not fall back to earlier block",
			output:      fenced(valid) + "\n" + fenced(`{"verdict":"unknown"}`),
			wantVerdict: plan.ReviewVerdictComment,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := Parse(test.output, CommitProposalOptional)
			if got.Verdict != test.wantVerdict {
				t.Fatalf("verdict = %q, want %q", got.Verdict, test.wantVerdict)
			}
			wantSummary := test.wantSummary
			if wantSummary == "" {
				wantSummary = capString(strings.TrimSpace(test.output), maxSummaryRunes)
			}
			if got.Summary != wantSummary {
				t.Fatalf("summary = %q, want %q", got.Summary, wantSummary)
			}
			if got.Findings == nil {
				t.Fatal("modern parse returned nil findings")
			}
		})
	}
}

func TestParseDoesNotCloseFenceOnBackticksInsideJSONString(t *testing.T) {
	marker := "```not-a-close"
	payload, err := json.Marshal(map[string]any{
		"verdict": "changes_requested",
		"summary": "fix fenced extraction",
		"findings": []map[string]any{{
			"severity": "major",
			"file":     "internal/plan/markdown.go",
			"line":     50,
			"message":  "a fence-like value " + marker + " is content",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := Parse(fenced(string(payload)), CommitProposalRequired)
	if got.Verdict != plan.ReviewVerdictChangesRequested || got.FindingsCount != 1 {
		t.Fatalf("review = %+v, want one changes-requested finding", got)
	}
	if !strings.Contains(got.Findings[0].Message, marker) {
		t.Fatalf("finding message = %q, want marker %q", got.Findings[0].Message, marker)
	}
}

func TestParseVerdictsAndProposalPolicy(t *testing.T) {
	tests := []struct {
		name         string
		payload      string
		policy       CommitProposalPolicy
		wantVerdict  string
		wantProposal bool
	}{
		{
			name:        "changes requested",
			payload:     `{"verdict":"changes_requested","summary":"fix it","findings":[]}`,
			policy:      CommitProposalRequired,
			wantVerdict: plan.ReviewVerdictChangesRequested,
		},
		{
			name:        "comment",
			payload:     `{"verdict":"comment","summary":"consider this"}`,
			policy:      CommitProposalRequired,
			wantVerdict: plan.ReviewVerdictComment,
		},
		{
			name:         "ordinary approval with valid proposal",
			payload:      `{"verdict":"approve","summary":"ready",` + validProposalJSON + `}`,
			policy:       CommitProposalRequired,
			wantVerdict:  plan.ReviewVerdictApprove,
			wantProposal: true,
		},
		{
			name:        "ordinary approval without proposal preserves substantive verdict",
			payload:     `{"verdict":"approve","summary":"ready"}`,
			policy:      CommitProposalRequired,
			wantVerdict: plan.ReviewVerdictApprove,
		},
		{
			name:        "aggregate approval without proposal",
			payload:     `{"verdict":"approve","summary":"ready"}`,
			policy:      CommitProposalOptional,
			wantVerdict: plan.ReviewVerdictApprove,
		},
		{
			name:         "aggregate approval retains valid proposal",
			payload:      `{"verdict":"approve","summary":"ready",` + validProposalJSON + `}`,
			policy:       CommitProposalOptional,
			wantVerdict:  plan.ReviewVerdictApprove,
			wantProposal: true,
		},
		{
			name:        "aggregate approval discards invalid proposal",
			payload:     `{"verdict":"approve","summary":"ready","commit_message":{"subject":"not conventional","body":"details"}}`,
			policy:      CommitProposalOptional,
			wantVerdict: plan.ReviewVerdictApprove,
		},
		{
			name:        "non approval discards valid proposal",
			payload:     `{"verdict":"changes_requested","summary":"fix",` + validProposalJSON + `}`,
			policy:      CommitProposalRequired,
			wantVerdict: plan.ReviewVerdictChangesRequested,
		},
		{
			name:        "unsupported verdict falls back",
			payload:     `{"verdict":"pass","summary":"ready"}`,
			policy:      CommitProposalOptional,
			wantVerdict: plan.ReviewVerdictComment,
		},
		{
			name:        "unknown proposal policy falls back",
			payload:     `{"verdict":"comment","summary":"note"}`,
			policy:      CommitProposalPolicy(99),
			wantVerdict: plan.ReviewVerdictComment,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := fenced(test.payload)
			got := Parse(output, test.policy)
			if got.Verdict != test.wantVerdict {
				t.Fatalf("verdict = %q, want %q; review = %+v", got.Verdict, test.wantVerdict, got)
			}
			if (got.CommitMessage != nil) != test.wantProposal {
				t.Fatalf("commit proposal = %+v, want present %t", got.CommitMessage, test.wantProposal)
			}
			if test.wantVerdict == plan.ReviewVerdictComment && strings.Contains(test.name, "falls back") && got.Summary != output {
				t.Fatalf("fallback summary = %q, want original output", got.Summary)
			}
			if got.ProposalUsable != test.wantProposal {
				t.Fatalf("proposal usability = %t, want %t", got.ProposalUsable, test.wantProposal)
			}
		})
	}
}

func TestParseTypedSeparatesSubstantiveReviewFromProposalUsability(t *testing.T) {
	tests := []struct {
		name     string
		proposal string
	}{
		{name: "missing"},
		{name: "malformed", proposal: `,"commit_message":"not-an-object"`},
		{name: "wrong type", proposal: `,"commit_message":{"subject":"feat(review): preserve typed review evidence","body":"What:\nPreserve the substantive review.\n\nWhy:\nRepair only unusable proposals."}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := fenced(`{"verdict":"approve","summary":"exact summary","findings":[]` + test.proposal + `}`)
			got := ParseTyped(output, CommitProposalRequired, plan.ChangeTypeFix)
			if got.Verdict != plan.ReviewVerdictApprove || got.Summary != "exact summary" || got.CommitMessage != nil || got.ProposalUsable {
				t.Fatalf("repairable typed approval = %+v", got)
			}
		})
	}

	valid := fenced(`{"verdict":"approve","summary":"ready","findings":[],"commit_message":{"subject":"fix(review): preserve typed review evidence","body":"What:\nPreserve the substantive review.\n\nWhy:\nRepair only unusable proposals."}}`)
	got := ParseTyped(valid, CommitProposalRequired, plan.ChangeTypeFix)
	if !got.ProposalUsable || got.CommitMessage == nil || got.CommitMessage.Subject != "fix(review): preserve typed review evidence" {
		t.Fatalf("valid typed approval = %+v", got)
	}
}

func TestParseCommitProposalRequiresBoundedCanonicalExpectedType(t *testing.T) {
	valid := "```tao-review-proposal-json\n{\"commit_message\":{\"subject\":\"fix(review): correct typed proposal\",\"body\":\"What:\\nCorrect the proposal type.\\n\\nWhy:\\nMatch the authoritative plan.\"}}\n```"
	if got := ParseCommitProposal(valid, plan.ChangeTypeFix); got == nil || got.Subject != "fix(review): correct typed proposal" {
		t.Fatalf("valid correction = %+v", got)
	}
	for name, output := range map[string]string{
		"wrong type": strings.Replace(valid, "fix(review)", "feat(review)", 1),
		"malformed":  "```tao-review-proposal-json\n{\"commit_message\":\n```",
		"oversized":  "```tao-review-proposal-json\n" + strings.Repeat("x", maxJSONBlockBytes+1) + "\n```",
	} {
		t.Run(name, func(t *testing.T) {
			if got := ParseCommitProposal(output, plan.ChangeTypeFix); got != nil {
				t.Fatalf("invalid correction = %+v", got)
			}
		})
	}
}

func TestParseBoundsAndNormalizesFindings(t *testing.T) {
	findings := make([]plan.ReviewFinding, maxFindings+1)
	for i := range findings {
		findings[i] = plan.ReviewFinding{
			Severity:   "  " + strings.Repeat("界", maxFindingSeverityRunes+1) + "  ",
			File:       "  " + strings.Repeat("f", maxFindingFileRunes+1) + "  ",
			Line:       -10,
			Message:    "  " + strings.Repeat("m", maxFindingTextRunes+1) + "  ",
			Suggestion: "  " + strings.Repeat("s", maxFindingTextRunes+1) + "  ",
		}
	}
	payload, err := json.Marshal(map[string]any{
		"verdict":  plan.ReviewVerdictChangesRequested,
		"summary":  "  " + strings.Repeat("界", maxSummaryRunes+1) + "  ",
		"findings": findings,
	})
	if err != nil {
		t.Fatal(err)
	}

	got := Parse(fenced(string(payload)), CommitProposalRequired)
	if got.Verdict != plan.ReviewVerdictChangesRequested {
		t.Fatalf("verdict = %q", got.Verdict)
	}
	if got.FindingsCount != maxFindings || len(got.Findings) != maxFindings {
		t.Fatalf("finding count = %d/%d, want %d", got.FindingsCount, len(got.Findings), maxFindings)
	}
	if len([]rune(got.Summary)) != maxSummaryRunes {
		t.Fatalf("summary runes = %d, want %d", len([]rune(got.Summary)), maxSummaryRunes)
	}
	finding := got.Findings[0]
	if finding.Line != 0 {
		t.Fatalf("negative line = %d, want 0", finding.Line)
	}
	lengths := map[string]struct {
		got  string
		want int
	}{
		"severity":   {finding.Severity, maxFindingSeverityRunes},
		"file":       {finding.File, maxFindingFileRunes},
		"message":    {finding.Message, maxFindingTextRunes},
		"suggestion": {finding.Suggestion, maxFindingTextRunes},
	}
	for name, length := range lengths {
		if gotRunes := len([]rune(length.got)); gotRunes != length.want {
			t.Errorf("%s runes = %d, want %d", name, gotRunes, length.want)
		}
		if strings.TrimSpace(length.got) != length.got {
			t.Errorf("%s was not trimmed", name)
		}
	}
}

func TestParseBoundsCommitProposalStrings(t *testing.T) {
	tests := []struct {
		name    string
		subject string
		body    string
	}{
		{
			name:    "oversized subject",
			subject: strings.Repeat("x", maxSummaryRunes+1),
			body:    "What:\nBound the subject.\n\nWhy:\nAvoid oversized evidence.",
		},
		{
			name:    "oversized body",
			subject: "refactor(review): centralize review parsing",
			body:    "What:\n" + strings.Repeat("x", maxSummaryRunes) + "\n\nWhy:\nBound it.",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{
				"verdict":        "approve",
				"summary":        "ready",
				"commit_message": map[string]string{"subject": test.subject, "body": test.body},
			})
			if err != nil {
				t.Fatal(err)
			}
			got := Parse(fenced(string(payload)), CommitProposalRequired)
			if got.Verdict != plan.ReviewVerdictApprove || got.CommitMessage != nil || got.ProposalUsable {
				t.Fatalf("oversized proposal did not preserve repairable approval: %+v", got)
			}
		})
	}
}

func TestParseLegacyFindingsProjection(t *testing.T) {
	finding := `{"severity":"  major  ","file":"  internal/run/review.go  ","line":-3,"message":"  fix it  ","suggestion":"  centralize it  "}`
	tests := []struct {
		name      string
		content   string
		wantCount int
		wantFile  string
	}{
		{name: "missing block", content: "old prose review"},
		{name: "malformed block", content: fenced(`{"findings":`)},
		{name: "malformed findings", content: fenced(`{"findings":"bad"}`)},
		{name: "oversized block", content: fenced(strings.Repeat("x", maxJSONBlockBytes+1))},
		{
			name:      "findings without modern verdict",
			content:   fenced(`{"findings":[` + finding + `]}`),
			wantCount: 1,
			wantFile:  "internal/run/review.go",
		},
		{
			name:      "last block wins",
			content:   fenced(`{"findings":[{"file":"first.go"}]}`) + "\n" + fenced(`{"findings":[`+finding+`]}`),
			wantCount: 1,
			wantFile:  "internal/run/review.go",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := ParseLegacyFindings(test.content)
			if len(got) != test.wantCount {
				t.Fatalf("findings = %+v, want count %d", got, test.wantCount)
			}
			if test.wantCount > 0 {
				if got[0].File != test.wantFile || got[0].Severity != "major" || got[0].Line != 0 || got[0].Message != "fix it" || got[0].Suggestion != "centralize it" {
					t.Fatalf("legacy finding was not canonically normalized: %+v", got[0])
				}
			}
		})
	}
}

func TestParseLegacyFindingsAppliesFindingAndStringBounds(t *testing.T) {
	findings := make([]plan.ReviewFinding, maxFindings+1)
	for i := range findings {
		findings[i] = plan.ReviewFinding{Severity: strings.Repeat("s", maxFindingSeverityRunes+1), Message: strings.Repeat("m", maxFindingTextRunes+1)}
	}
	payload, err := json.Marshal(map[string]any{"findings": findings})
	if err != nil {
		t.Fatal(err)
	}
	got := ParseLegacyFindings(fenced(string(payload)))
	if len(got) != maxFindings {
		t.Fatalf("legacy findings = %d, want %d", len(got), maxFindings)
	}
	if len([]rune(got[0].Severity)) != maxFindingSeverityRunes || len([]rune(got[0].Message)) != maxFindingTextRunes {
		t.Fatalf("legacy strings were not bounded: %+v", got[0])
	}
}
