package view

import (
	"errors"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

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
