package rework

import (
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestParseReviewFindingsUsesCanonicalLegacyProjection(t *testing.T) {
	content := "```tao-review-json\n{\"findings\":[{\"file\":\"old.go\",\"message\":\"old\"}]}\n```\n" +
		"```tao-review-json\n{\"findings\":[{\"severity\":\" major \",\"file\":\" internal/rework/findings.go \",\"line\":-3,\"message\":\" fix it \",\"suggestion\":\" use the contract \"}]}\n```"

	got := ParseReviewFindings(content)
	want := plan.ReviewFinding{Severity: "major", File: "internal/rework/findings.go", Line: 0, Message: "fix it", Suggestion: "use the contract"}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("legacy findings = %+v, want %+v", got, want)
	}
	if malformed := ParseReviewFindings("```tao-review-json\n{not-json}\n```"); malformed != nil {
		t.Fatalf("malformed legacy findings = %+v, want nil", malformed)
	}
}

func TestReworkFindingsFingerprintNormalizesEquivalentFindings(t *testing.T) {
	first := []plan.ReviewFinding{
		{Severity: " MAJOR ", File: " ./internal/rework/../rework/findings.go ", Line: 10, Message: "Fix   the\nfingerprint", Suggestion: "Include ALL fields"},
		{Severity: "minor", File: `internal\plan\review.go`, Line: 20, Message: "Fix another issue", Suggestion: "Do the other thing"},
	}
	second := []plan.ReviewFinding{
		{Severity: "MINOR", File: "internal/plan/review.go", Line: 20, Message: " fix ANOTHER issue ", Suggestion: "do the other THING"},
		{Severity: "major", File: "internal/rework/findings.go", Line: 10, Message: "fix the fingerprint", Suggestion: "include all fields"},
	}

	if ReworkFindingsFingerprint(first) != ReworkFindingsFingerprint(second) {
		t.Fatal("equivalent normalized finding sets produced different rework fingerprints")
	}
}

func TestReworkFindingsFingerprintChangesForDistinctFindingIdentity(t *testing.T) {
	base := plan.ReviewFinding{
		Severity: "major", File: "internal/rework/findings.go", Line: 10,
		Message: "fix the fingerprint", Suggestion: "include all fields",
	}
	tests := []struct {
		name    string
		finding plan.ReviewFinding
	}{
		{name: "severity", finding: plan.ReviewFinding{Severity: "minor", File: base.File, Line: base.Line, Message: base.Message, Suggestion: base.Suggestion}},
		{name: "file", finding: plan.ReviewFinding{Severity: base.Severity, File: "internal/rework/driver.go", Line: base.Line, Message: base.Message, Suggestion: base.Suggestion}},
		{name: "line", finding: plan.ReviewFinding{Severity: base.Severity, File: base.File, Line: 11, Message: base.Message, Suggestion: base.Suggestion}},
		{name: "message", finding: plan.ReviewFinding{Severity: base.Severity, File: base.File, Line: base.Line, Message: "fix a separate defect", Suggestion: base.Suggestion}},
		{name: "suggestion", finding: plan.ReviewFinding{Severity: base.Severity, File: base.File, Line: base.Line, Message: base.Message, Suggestion: "use a different repair"}},
	}

	baseFingerprint := ReworkFindingsFingerprint([]plan.ReviewFinding{base})
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if baseFingerprint == ReworkFindingsFingerprint([]plan.ReviewFinding{test.finding}) {
				t.Fatalf("finding with different %s produced the same rework fingerprint", test.name)
			}
		})
	}
}

func TestReworkFindingsFingerprintTracksFindingSetChanges(t *testing.T) {
	first := plan.ReviewFinding{Severity: "major", File: "internal/rework/findings.go", Line: 10, Message: "fix first defect", Suggestion: "first repair"}
	second := plan.ReviewFinding{Severity: "major", File: "internal/rework/findings.go", Line: 20, Message: "fix second defect", Suggestion: "second repair"}
	set := []plan.ReviewFinding{first, second}
	reorderedWithDuplicate := []plan.ReviewFinding{second, first, {
		Severity: " MAJOR ", File: "./internal/rework/findings.go", Line: 10,
		Message: " FIX  FIRST defect ", Suggestion: "first REPAIR",
	}}

	fingerprint := ReworkFindingsFingerprint(set)
	if fingerprint != ReworkFindingsFingerprint(reorderedWithDuplicate) {
		t.Fatal("order or an identical normalized duplicate changed the rework fingerprint")
	}
	if fingerprint == ReworkFindingsFingerprint([]plan.ReviewFinding{first}) {
		t.Fatal("removing one of multiple findings did not change the rework fingerprint")
	}
	if fingerprint == ReworkFindingsFingerprint([]plan.ReviewFinding{first, {
		Severity: "major", File: first.File, Line: 30, Message: "fix third defect", Suggestion: "third repair",
	}}) {
		t.Fatal("replacing a finding in the set did not change the rework fingerprint")
	}
}

func TestReworkFindingsFingerprintIsVersioned(t *testing.T) {
	findings := []plan.ReviewFinding{{Severity: "major", File: "internal/rework/findings.go", Line: 10, Message: "fix a", Suggestion: "do a"}}
	fingerprint := ReworkFindingsFingerprint(findings)

	if !strings.HasPrefix(fingerprint, reworkFingerprintV2Prefix) {
		t.Fatalf("rework fingerprint %q does not have the v2 prefix", fingerprint)
	}
	if fingerprint == BatchLocationFindingsFingerprint(findings) {
		t.Fatal("v2 rework fingerprint matches the historical fingerprint")
	}
}

func TestBatchLocationFindingsFingerprintPreservesHistoricalCompatibility(t *testing.T) {
	first := []plan.ReviewFinding{{Severity: "major", File: "internal/rework/findings.go", Line: 10, Message: "fix the fingerprint", Suggestion: "include all fields"}}
	reworded := []plan.ReviewFinding{{Severity: "major", File: "./internal/rework/findings.go", Line: 20, Message: "fix a different defect", Suggestion: "use another repair"}}

	if BatchLocationFindingsFingerprint(first) != BatchLocationFindingsFingerprint(reworded) {
		t.Fatal("historical location-oriented fingerprint behavior changed")
	}
}
