package view

import (
	"fmt"
	"io"

	"github.com/iamseth/tao/internal/plan"
)

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
		if _, err := fmt.Fprintf(out, "- %s: observed %d > threshold %d", warning.Message, warning.Observed, warning.Threshold); err != nil {
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
