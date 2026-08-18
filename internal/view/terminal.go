package view

import (
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/iamseth/tao/internal/plan"
)

const (
	blockerReasonDetailRunes  = 320
	blockerReasonExcerptRunes = 96
	blockerReasonFallback     = "No blocker reason was recorded."
)

// BlockerText returns display-only detailed and concise forms of an untrusted
// blocker reason. Both forms are single-line, bounded, and safe from terminal
// control-character injection.
type BlockerText struct {
	Detailed string
	Concise  string
}

func FormatBlockerText(reason string) BlockerText {
	normalized := strings.Join(strings.FieldsFunc(reason, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if normalized == "" {
		normalized = blockerReasonFallback
	}
	return BlockerText{
		Detailed: boundBlockerText(normalized, blockerReasonDetailRunes),
		Concise:  boundBlockerText(normalized, blockerReasonExcerptRunes),
	}
}

// FormatBlockedRunGuidance presents a persisted blocker as display-only context
// followed by the explicit command to use after resolving it.
func FormatBlockedRunGuidance(sliceID, reason, continueCommand string) string {
	return fmt.Sprintf("Blocked slice %s: %s\nResolve this blocker before continuing, then run:\n  %s", sliceID, FormatBlockerText(reason).Detailed, continueCommand)
}

func boundBlockerText(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func RenderVerificationFindings(out io.Writer, findings []plan.VerificationFinding) error {
	if len(findings) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(out, "Verification Findings:"); err != nil {
		return err
	}
	for _, finding := range findings {
		if err := RenderVerificationFinding(out, finding); err != nil {
			return err
		}
	}
	return nil
}

func RenderVerificationFinding(out io.Writer, finding plan.VerificationFinding) error {
	if _, err := fmt.Fprintf(out, "- %s", finding.Severity); err != nil {
		return err
	}
	if finding.SliceID != "" {
		if _, err := fmt.Fprintf(out, " %s", finding.SliceID); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(out, ": %s", finding.Message); err != nil {
		return err
	}
	if finding.Path != "" {
		if _, err := fmt.Fprintf(out, " (%s)", finding.Path); err != nil {
			return err
		}
	}
	if finding.Suggestion != "" {
		if _, err := fmt.Fprintf(out, " suggestion: %s", finding.Suggestion); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out)
	return err
}

func RenderAgentBudgetWarnings(out io.Writer, warnings []plan.AgentBudgetWarning) error {
	if len(warnings) == 0 {
		return nil
	}
	if _, err := fmt.Fprintln(out, "Agent Metrics Budget Warnings:"); err != nil {
		return err
	}
	for _, warning := range warnings {
		if _, err := fmt.Fprintf(out, "- %s: observed %g > threshold %g", warning.Message, warning.Observed, warning.Threshold); err != nil {
			return err
		}
		if warning.SliceID != "" {
			if _, err := fmt.Fprintf(out, " (%s)", warning.SliceID); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(out); err != nil {
			return err
		}
	}
	return nil
}
