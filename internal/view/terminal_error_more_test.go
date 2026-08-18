package view

import (
	"errors"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestFormatBlockerTextNormalizesBoundsAndFallsBack(t *testing.T) {
	tests := []struct {
		name        string
		reason      string
		wantDetail  string
		wantConcise string
	}{
		{name: "ordinary", reason: "waiting for access", wantDetail: "waiting for access", wantConcise: "waiting for access"},
		{name: "multiline and controls", reason: " waiting\nfor\taccess\x1b[31m ", wantDetail: "waiting for access [31m", wantConcise: "waiting for access [31m"},
		{name: "missing", reason: " \n\t ", wantDetail: blockerReasonFallback, wantConcise: blockerReasonFallback},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := FormatBlockerText(test.reason)
			if got.Detailed != test.wantDetail || got.Concise != test.wantConcise {
				t.Fatalf("FormatBlockerText(%q) = %#v, want detail %q concise %q", test.reason, got, test.wantDetail, test.wantConcise)
			}
		})
	}

	got := FormatBlockerText(strings.Repeat("界", blockerReasonDetailRunes+20))
	if len([]rune(got.Detailed)) != blockerReasonDetailRunes || len([]rune(got.Concise)) != blockerReasonExcerptRunes || !strings.HasSuffix(got.Detailed, "…") || !strings.HasSuffix(got.Concise, "…") {
		t.Fatalf("long blocker text was not rune-bounded: detailed=%d concise=%d", len([]rune(got.Detailed)), len([]rune(got.Concise)))
	}
}

type failingWriter struct{}

func (f failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestTerminalRenderersPropagateWriteErrors(t *testing.T) {
	if err := RenderVerificationFindings(failingWriter{}, []plan.VerificationFinding{{Severity: "warning", Message: "msg"}}); err == nil {
		t.Fatal("expected verification findings write error")
	}
	if err := RenderVerificationFinding(failingWriter{}, plan.VerificationFinding{Severity: "warning", SliceID: "001-a", Message: "msg", Path: "file", Suggestion: "fix"}); err == nil {
		t.Fatal("expected verification finding write error")
	}
	if err := RenderAgentBudgetWarnings(failingWriter{}, []plan.AgentBudgetWarning{{Message: "tokens", Observed: 2, Threshold: 1}}); err == nil {
		t.Fatal("expected budget warning write error")
	}
}
