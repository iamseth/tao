package run

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/agentinput"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
)

const (
	maxCompletionNotesBytes               = agentinput.MaxFileBytes
	maxCompletionVerificationResultsBytes = int64(256 * 1024)
	maxCompletionVerificationResults      = 50
	maxCompletionVerificationDetailsRunes = agentinput.MaxTextRunes
)

// SliceCompletionInputFiles names the agent-written temporary files consumed
// by one slice completion.
type SliceCompletionInputFiles struct {
	NotesFile               string
	VerificationResultsFile string
	// CommitProposalFile is optional; it is required only when a new
	// slice-policy commit intent must be recorded.
	CommitProposalFile string
}

// SliceCompletionInputs is the bounded, validated agent evidence for one
// slice completion.
type SliceCompletionInputs struct {
	Notes               string
	VerificationResults []plan.VerificationRun
	CommitProposal      *commitcontract.Proposal
}

// LoadSliceCompletionInputs reads, bounds, and validates the agent-supplied
// completion evidence. Relative verification working directories are resolved
// against the calling process's current working directory, matching where the
// implementing agent invoked completion.
func LoadSliceCompletionInputs(files SliceCompletionInputFiles) (SliceCompletionInputs, error) {
	notesBytes, err := agentinput.ReadBoundedFile(files.NotesFile, "notes file", maxCompletionNotesBytes)
	if err != nil {
		return SliceCompletionInputs{}, fmt.Errorf("read notes file: %w", err)
	}
	resultsBytes, err := agentinput.ReadBoundedFile(files.VerificationResultsFile, "verification results file", maxCompletionVerificationResultsBytes)
	if err != nil {
		return SliceCompletionInputs{}, fmt.Errorf("read verification results file: %w", err)
	}
	var results []plan.VerificationRun
	if err := json.Unmarshal(resultsBytes, &results); err != nil {
		return SliceCompletionInputs{}, fmt.Errorf("read verification results JSON: %w", err)
	}
	if err := validateCompletionVerificationRuns(results); err != nil {
		return SliceCompletionInputs{}, err
	}
	if err := normalizeVerificationRunCWDs(results); err != nil {
		return SliceCompletionInputs{}, err
	}
	inputs := SliceCompletionInputs{Notes: strings.TrimSpace(string(notesBytes)), VerificationResults: results}
	if strings.TrimSpace(files.CommitProposalFile) != "" {
		proposalBytes, err := agentinput.ReadBoundedFile(files.CommitProposalFile, "commit proposal file", commitcontract.MaxProposalFileBytes)
		if err != nil {
			return SliceCompletionInputs{}, fmt.Errorf("read commit proposal file: %w", err)
		}
		inputs.CommitProposal, err = commitcontract.DecodeProposal(proposalBytes)
		if err != nil {
			return SliceCompletionInputs{}, err
		}
	}
	return inputs, nil
}

// Remove deletes the consumed throwaway input files so they cannot be left in
// a repository working tree and accidentally staged or committed. Cleanup is
// best-effort: a failed removal must not fail an otherwise successful
// completion.
func (f SliceCompletionInputFiles) Remove() {
	_ = os.Remove(f.NotesFile)
	_ = os.Remove(f.VerificationResultsFile)
	if f.CommitProposalFile != "" {
		_ = os.Remove(f.CommitProposalFile)
	}
}

func validateCompletionVerificationRuns(results []plan.VerificationRun) error {
	if len(results) > maxCompletionVerificationResults {
		return fmt.Errorf("verification results contain %d entries; limit is %d", len(results), maxCompletionVerificationResults)
	}
	for i := range results {
		results[i].Command = strings.TrimSpace(results[i].Command)
		results[i].CWD = strings.TrimSpace(results[i].CWD)
		results[i].Result = strings.TrimSpace(results[i].Result)
		if results[i].Command == "" || results[i].CWD == "" || results[i].Result == "" {
			return fmt.Errorf("verification result %d must include command, cwd, and result", i+1)
		}
		results[i].Details = agentinput.CapRunes(results[i].Details, maxCompletionVerificationDetailsRunes)
	}
	return nil
}

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
