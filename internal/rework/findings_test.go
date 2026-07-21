package rework

import (
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestFindingsFingerprintIgnoresOrderWhitespacePathAndLineDrift(t *testing.T) {
	first := []plan.ReviewFinding{
		{Severity: " major ", File: " ./internal/rework/../rework/findings.go ", Line: 10, Message: "fix a", Suggestion: "do a"},
		{Severity: "minor", File: `internal\plan\review.go`, Line: 20, Message: "fix b", Suggestion: "do b"},
	}
	second := []plan.ReviewFinding{
		{Severity: "minor", File: "internal/plan/review.go", Line: 200, Message: "fix b", Suggestion: "do b"},
		{Severity: "major", File: "internal/rework/findings.go", Line: 100, Message: "fix a", Suggestion: "do a"},
	}
	if FindingsFingerprint(first) != FindingsFingerprint(second) {
		t.Fatal("equivalent finding sets produced different fingerprints")
	}
}

func TestFindingsFingerprintIgnoresRewordedFreeText(t *testing.T) {
	firstRound := []plan.ReviewFinding{{
		Severity: "major", File: "internal/rework/findings.go", Message: "fix the fingerprint", Suggestion: "include all fields",
	}}
	secondRound := []plan.ReviewFinding{{
		Severity: "major", File: "internal/rework/findings.go", Message: "stall rework sooner", Suggestion: "ignore reviewer prose",
	}}

	if FindingsFingerprint(firstRound) != FindingsFingerprint(secondRound) {
		t.Fatal("re-worded findings produced different fingerprints")
	}
}

func TestFindingsFingerprintDeduplicatesRewordedSeverityFileKeys(t *testing.T) {
	duplicates := []plan.ReviewFinding{
		{Severity: "major", File: "internal/rework/findings.go", Message: "fix the fingerprint", Suggestion: "deduplicate keys"},
		{Severity: " major ", File: "./internal/rework/findings.go", Message: "stall rework sooner", Suggestion: "ignore duplicate prose"},
	}
	consolidated := []plan.ReviewFinding{
		{Severity: "major", File: "internal/rework/findings.go", Message: "one combined finding", Suggestion: "address both comments"},
	}

	if FindingsFingerprint(duplicates) != FindingsFingerprint(consolidated) {
		t.Fatal("duplicate and consolidated re-worded findings produced different fingerprints")
	}
}

func TestFindingsFingerprintChangesForSeverityOrFile(t *testing.T) {
	base := []plan.ReviewFinding{{Severity: "major", File: "internal/rework/findings.go", Message: "fix a", Suggestion: "do a"}}
	tests := []struct {
		name    string
		finding plan.ReviewFinding
	}{
		{name: "new file", finding: plan.ReviewFinding{Severity: "major", File: "internal/rework/driver.go", Message: "fix a", Suggestion: "do a"}},
		{name: "new severity", finding: plan.ReviewFinding{Severity: "minor", File: "internal/rework/findings.go", Message: "fix a", Suggestion: "do a"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if FindingsFingerprint(base) == FindingsFingerprint([]plan.ReviewFinding{test.finding}) {
				t.Fatal("finding with different severity or file produced the same fingerprint")
			}
		})
	}
}
