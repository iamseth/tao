package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/atomicfile"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
)

// Recovery inputs are local-only and never authority independently of frozen intent.
func recoveryDirectory(dir, sliceID, hash string) string {
	key := sha256.Sum256([]byte(sliceID + "\x00" + hash))
	return filepath.Join(dir, "completion-recovery", hex.EncodeToString(key[:]))
}

func privateRecoveryDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("recovery directory must be a private directory: %s", path)
	}
	return nil
}

func retainCompletionInputs(request SliceCompletionRequest, intent plan.SliceCommitIntent) error {
	notes := strings.TrimSpace(request.Notes)
	if int64(len(notes)) > maxCompletionNotesBytes {
		return fmt.Errorf("completion notes exceed recovery input bound")
	}
	var proposal []byte
	if request.CommitProposal != nil {
		var err error
		proposal, err = json.Marshal(request.CommitProposal)
		if err != nil {
			return err
		}
		if int64(len(proposal)) > commitcontract.MaxProposalFileBytes {
			return fmt.Errorf("completion proposal exceeds recovery input bound")
		}
		if _, err := commitcontract.DecodeProposal(proposal); err != nil {
			return err
		}
	}
	base := filepath.Join(request.Record.Dir(), "completion-recovery")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return err
	}
	if err := privateRecoveryDirectory(base); err != nil {
		return err
	}
	target := recoveryDirectory(request.Record.Dir(), request.SliceID, intent.Hash)
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("recovery inputs already exist; refusing to overwrite transaction inputs")
	} else if !os.IsNotExist(err) {
		return err
	}
	temp, err := os.MkdirTemp(base, ".pending-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	if err := atomicfile.Write(filepath.Join(temp, "notes"), []byte(notes), atomicfile.Options{Perm: 0o600}); err != nil {
		return err
	}
	if proposal != nil {
		if err := atomicfile.Write(filepath.Join(temp, "proposal.json"), proposal, atomicfile.Options{Perm: 0o600}); err != nil {
			return err
		}
	}
	if err := os.Rename(temp, target); err != nil {
		return err
	}
	if err := atomicfile.SyncDir(base); err != nil {
		return err
	}
	return atomicfile.SyncDir(request.Record.Dir())
}

func retainedCompletionFiles(detail *plan.PlanDetail, sliceID string) (SliceCompletionInputFiles, error) {
	missing := func(reason string) (SliceCompletionInputFiles, error) {
		return SliceCompletionInputFiles{}, fmt.Errorf("%s; recovery requires the original completion inputs", reason)
	}
	if detail == nil {
		return missing("missing plan")
	}
	slice := completionSlice(detail, sliceID)
	if slice == nil || slice.CommitIntent == nil {
		return missing("no durable completion intent")
	}
	intent := slice.CommitIntent
	if !strings.HasPrefix(intent.Hash, verifiedCompletionHashPrefix) || intent.Verification == nil {
		return missing("historical intent has no retained verified inputs")
	}
	if err := intent.Verification.Validate(); err != nil {
		return missing("invalid frozen verification")
	}
	if intent.StartingBranch != intent.Verification.StartingBranch || intent.StartingHead != intent.Verification.StartingHead || (intent.Policy != "slice" && intent.Policy != "none") {
		return missing("conflicting frozen intent")
	}
	dir, err := filepath.Abs(recoveryDirectory(detail.Dir, sliceID, intent.Hash))
	if err != nil {
		return missing(err.Error())
	}
	if err := privateRecoveryDirectory(filepath.Dir(dir)); err != nil {
		return missing(err.Error())
	}
	if err := privateRecoveryDirectory(dir); err != nil {
		return missing(err.Error())
	}
	files := SliceCompletionInputFiles{NotesFile: filepath.Join(dir, "notes")}
	info, err := os.Lstat(files.NotesFile)
	if err != nil || !info.Mode().IsRegular() {
		return missing("missing or unsafe retained notes")
	}
	inputs, err := LoadVerifiedCompletionInputs(files)
	if err != nil {
		return missing(err.Error())
	}
	hash, err := verifiedSliceCompletionHash(inputs.Notes, intent.Message, *intent.Verification)
	if err != nil || hash != intent.Hash {
		return missing("conflicting retained notes")
	}
	// A retained proposal is archival, not a new proposal required for replay.
	proposalPath := filepath.Join(dir, "proposal.json")
	if info, statErr := os.Lstat(proposalPath); statErr == nil {
		if !info.Mode().IsRegular() {
			return missing("unsafe retained proposal")
		}
		data, readErr := agentinput.ReadBoundedFile(proposalPath, "retained proposal", commitcontract.MaxProposalFileBytes)
		if readErr != nil {
			return missing(readErr.Error())
		}
		proposal, decodeErr := commitcontract.DecodeProposal(data)
		if decodeErr != nil {
			return missing(decodeErr.Error())
		}
		if intent.Policy == "slice" {
			message, formatErr := formatVerifiedSliceMessage(detail.State.Plan.ID, sliceID, *proposal, *intent.Verification)
			if formatErr != nil || message != intent.Message {
				return missing("conflicting retained proposal")
			}
		}
	} else if !os.IsNotExist(statErr) {
		return missing(statErr.Error())
	}
	// The frozen message is sufficient for replay, even if archival proposal is absent.
	return files, nil
}

// CompletionRecoveryCommand returns explicit replay only for inputs matching durable
// verified intent. It neither inspects Git nor reloads plan or latest-attempt evidence.
func CompletionRecoveryCommand(detail *plan.PlanDetail, sliceID string) (string, error) {
	files, err := retainedCompletionFiles(detail, sliceID)
	if err != nil {
		return "", err
	}
	dir, err := filepath.Abs(detail.Dir)
	if err != nil {
		return "", err
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	return "tao slice-complete --plan-dir " + quote(dir) + " --slice-id " + quote(sliceID) + " --notes-file " + quote(files.NotesFile), nil
}

// RemoveForPlan preserves recognized retained inputs; Remove retains its original contract.
func (f SliceCompletionInputFiles) RemoveForPlan(detail *plan.PlanDetail, sliceID string) {
	retained, err := retainedCompletionFiles(detail, sliceID)
	if err == nil {
		dir := filepath.Dir(retained.NotesFile)
		for _, path := range []*string{&f.NotesFile, &f.CommitProposalFile, &f.VerificationResultsFile} {
			absolute, absErr := filepath.Abs(*path)
			if absErr == nil && (absolute == retained.NotesFile || absolute == filepath.Join(dir, "proposal.json")) {
				*path = ""
			}
		}
	}
	f.Remove()
}
