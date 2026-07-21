package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
)

var sliceCompleteCommand = commandMetadata{
	name:                  "slice-complete",
	usageLines:            []string{"slice-complete --plan-dir DIR --slice-id ID --notes-file FILE --verification-results-file FILE"},
	completionDescription: "Complete a slice from notes and verification files",
	long:                  "Complete a Tao plan slice from agent-written notes and verification results. This command is agent-facing bookkeeping: it updates Tao metadata and removes the temporary input files after they are consumed.",
	examples:              "  tao slice-complete --plan-dir /path/to/plan --slice-id 001-example --notes-file /tmp/notes.txt --verification-results-file /tmp/results.json",
	registerFlags:         registerSliceCompleteFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"notes-file":                {kind: completionValuePath, label: "path"},
		"plan-dir":                  {kind: completionValuePath, label: "path"},
		"slice-id":                  {kind: completionValueText, label: "slice id"},
		"verification-results-file": {kind: completionValuePath, label: "path"},
	}},
	execute: func(c commandContext) error {
		return c.app.sliceComplete(c.ctx, c.args)
	},
}

func registerSliceCompleteFlags(fs *flag.FlagSet) {
	fs.String("plan-dir", "", "plan directory")
	fs.String("slice-id", "", "slice id to complete")
	fs.String("notes-file", "", "file containing completion notes")
	fs.String("verification-results-file", "", "JSON file containing verification results")
}

const (
	sliceAgentInputMaxFileBytes              int64 = 64 * 1024
	sliceAgentInputMaxTextRunes                    = 16 * 1024
	sliceCompleteMaxNotesBytes                     = sliceAgentInputMaxFileBytes
	sliceCompleteMaxVerificationResultsBytes int64 = 256 * 1024
	sliceCompleteMaxVerificationResults            = 50
	sliceCompleteMaxVerificationDetailsRunes       = sliceAgentInputMaxTextRunes
)

func normalizeVerificationRunCWDs(results []plan.VerificationRun) error {
	var base string
	for i := range results {
		cwd := results[i].CWD
		if cwd == "" || filepath.IsAbs(cwd) {
			continue
		}
		if base == "" {
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve verification cwd base: %w", err)
			}
			base = wd
		}
		joined := filepath.Join(base, cwd)
		canonical, err := filepath.EvalSymlinks(joined)
		if err != nil {
			results[i].CWD = joined
			continue
		}
		results[i].CWD = canonical
	}
	return nil
}

func readBoundedAgentInput(path string, label string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path) // #nosec G304 -- explicit local file input selected by the caller.
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%s exceeds %d byte limit", label, maxBytes)
	}
	return data, nil
}

func validateVerificationRuns(results []plan.VerificationRun) error {
	if len(results) > sliceCompleteMaxVerificationResults {
		return fmt.Errorf("verification results contain %d entries; limit is %d", len(results), sliceCompleteMaxVerificationResults)
	}
	for i := range results {
		results[i].Command = strings.TrimSpace(results[i].Command)
		results[i].CWD = strings.TrimSpace(results[i].CWD)
		results[i].Result = strings.TrimSpace(results[i].Result)
		if results[i].Command == "" || results[i].CWD == "" || results[i].Result == "" {
			return fmt.Errorf("verification result %d must include command, cwd, and result", i+1)
		}
		results[i].Details = capStringRunes(results[i].Details, sliceCompleteMaxVerificationDetailsRunes)
	}
	return nil
}

func capStringRunes(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	return string(runes[:maxRunes])
}

func validateBoundedAgentText(value string, label string) (string, error) {
	value = strings.TrimSpace(value)
	if len([]rune(value)) > sliceAgentInputMaxTextRunes {
		return "", fmt.Errorf("%s exceeds %d rune limit", label, sliceAgentInputMaxTextRunes)
	}
	return value, nil
}

func (a App) sliceComplete(ctx context.Context, args []string) error {
	fs, positional, err := a.parseArgs("slice-complete", args, registerSliceCompleteFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao slice-complete --plan-dir DIR --slice-id ID --notes-file FILE --verification-results-file FILE"); err != nil {
		return err
	}
	planDir := flagStringValue(fs, "plan-dir")
	sliceID := flagStringValue(fs, "slice-id")
	notesFile := flagStringValue(fs, "notes-file")
	verificationResultsFile := flagStringValue(fs, "verification-results-file")
	if strings.TrimSpace(planDir) == "" || strings.TrimSpace(sliceID) == "" || strings.TrimSpace(notesFile) == "" || strings.TrimSpace(verificationResultsFile) == "" {
		return errors.New("usage: tao slice-complete --plan-dir DIR --slice-id ID --notes-file FILE --verification-results-file FILE")
	}
	notesBytes, err := readBoundedAgentInput(notesFile, "notes file", sliceCompleteMaxNotesBytes)
	if err != nil {
		return fmt.Errorf("read notes file: %w", err)
	}
	resultsBytes, err := readBoundedAgentInput(verificationResultsFile, "verification results file", sliceCompleteMaxVerificationResultsBytes)
	if err != nil {
		return fmt.Errorf("read verification results file: %w", err)
	}
	var results []plan.VerificationRun
	if err := json.Unmarshal(resultsBytes, &results); err != nil {
		return fmt.Errorf("read verification results JSON: %w", err)
	}
	if err := validateVerificationRuns(results); err != nil {
		return err
	}
	if err := normalizeVerificationRunCWDs(results); err != nil {
		return err
	}

	record, err := plan.NewFileRepository("").ResolvePlanRecord(ctx, planDir)
	if err != nil {
		return err
	}
	service := run.SliceCompletionService{CommandRunner: a.CommandRunner, Output: a.Out}
	if err := service.Complete(ctx, run.SliceCompletionRequest{
		Record: record, SliceID: sliceID, Notes: strings.TrimSpace(string(notesBytes)),
		VerificationResults: results, Now: a.now().UTC(),
	}); err != nil {
		return err
	}
	// The notes and verification-results files are throwaway Tao-owned
	// bookkeeping inputs. Remove them once consumed so they cannot be left in a
	// repository working tree and accidentally staged or committed. Best-effort:
	// a failed cleanup must not fail an otherwise successful completion.
	_ = os.Remove(notesFile)
	_ = os.Remove(verificationResultsFile)
	return writef(a.Out, "Slice completed: %s\n", sliceID)
}
