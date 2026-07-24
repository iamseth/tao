package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
)

var sliceCompleteCommand = commandMetadata{
	name:                  "slice-complete",
	usageLines:            []string{"slice-complete --plan-dir DIR --slice-id ID --notes-file FILE --verification-results-file FILE [--commit-proposal-file FILE]"},
	completionDescription: "Complete a slice from notes, verification, and a commit proposal",
	long:                  "Complete a Tao plan slice from agent-written notes, verification results, and a bounded structured commit proposal for slice-policy commits. Tao validates the proposal, adds trusted evidence trailers, owns the Git transaction, and removes temporary inputs only after successful completion.",
	examples:              "  tao slice-complete --plan-dir /path/to/plan --slice-id 001-example --notes-file /tmp/notes.txt --verification-results-file /tmp/results.json --commit-proposal-file /tmp/proposal.json",
	registerFlags:         registerSliceCompleteFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"commit-proposal-file":      {kind: completionValuePath, label: "path"},
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
	fs.String("commit-proposal-file", "", "JSON file containing a structured commit proposal (required for a new slice-policy intent)")
	fs.String("notes-file", "", "file containing completion notes")
	fs.String("verification-results-file", "", "JSON file containing verification results")
}

const (
	sliceAgentInputMaxFileBytes              int64 = 64 * 1024
	sliceAgentInputMaxTextRunes                    = 16 * 1024
	sliceCompleteMaxNotesBytes                     = sliceAgentInputMaxFileBytes
	sliceCompleteMaxCommitProposalBytes      int64 = 32 * 1024
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

func decodeSliceCommitProposal(data []byte) (*commitcontract.Proposal, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var proposal commitcontract.Proposal
	if err := decoder.Decode(&proposal); err != nil {
		return nil, fmt.Errorf("decode slice commit proposal: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("decode slice commit proposal: multiple JSON values")
		}
		return nil, fmt.Errorf("decode slice commit proposal: %w", err)
	}
	if err := commitcontract.ValidateProposal(proposal); err != nil {
		return nil, fmt.Errorf("validate slice commit proposal: %w", err)
	}
	return &proposal, nil
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
	if err := requireNoArgs(positional, "usage: tao slice-complete --plan-dir DIR --slice-id ID --notes-file FILE --verification-results-file FILE [--commit-proposal-file FILE]"); err != nil {
		return err
	}
	planDir := flagStringValue(fs, "plan-dir")
	sliceID := flagStringValue(fs, "slice-id")
	notesFile := flagStringValue(fs, "notes-file")
	verificationResultsFile := flagStringValue(fs, "verification-results-file")
	commitProposalFile := flagStringValue(fs, "commit-proposal-file")
	if strings.TrimSpace(planDir) == "" || strings.TrimSpace(sliceID) == "" || strings.TrimSpace(notesFile) == "" || strings.TrimSpace(verificationResultsFile) == "" {
		return errors.New("usage: tao slice-complete --plan-dir DIR --slice-id ID --notes-file FILE --verification-results-file FILE [--commit-proposal-file FILE]")
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
	var proposal *commitcontract.Proposal
	if strings.TrimSpace(commitProposalFile) != "" {
		proposalBytes, err := readBoundedAgentInput(commitProposalFile, "commit proposal file", sliceCompleteMaxCommitProposalBytes)
		if err != nil {
			return fmt.Errorf("read commit proposal file: %w", err)
		}
		proposal, err = decodeSliceCommitProposal(proposalBytes)
		if err != nil {
			return err
		}
	}

	record, err := plan.NewFileRepository("").ResolvePlanRecord(ctx, planDir)
	if err != nil {
		return err
	}
	service := run.SliceCompletionService{CommandRunner: a.CommandRunner, Output: a.Out}
	if err := service.Complete(ctx, run.SliceCompletionRequest{
		Record: record, SliceID: sliceID, Notes: strings.TrimSpace(string(notesBytes)),
		VerificationResults: results, CommitProposal: proposal, Now: a.now().UTC(),
	}); err != nil {
		return err
	}
	// The report and proposal files are throwaway Tao-owned inputs. Remove them
	// once consumed so they cannot be left in a
	// repository working tree and accidentally staged or committed. Best-effort:
	// a failed cleanup must not fail an otherwise successful completion.
	_ = os.Remove(notesFile)
	_ = os.Remove(verificationResultsFile)
	if commitProposalFile != "" {
		_ = os.Remove(commitProposalFile)
	}
	return writef(a.Out, "Slice completed: %s\n", sliceID)
}
