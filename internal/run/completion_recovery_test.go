package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/testenv"
)

func TestCompletionRecoveryStorage(t *testing.T) {
	testenv.SkipIfShort(t)
	request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	// Exercise spaces and apostrophes in the plan metadata path, without changing Git roots.
	old := request.Record.Dir()
	dir := filepath.Join(filepath.Dir(old), filepath.Base(old)+" space's")
	if err := os.Rename(old, dir); err != nil {
		t.Fatal(err)
	}
	detail := request.Record.Detail()
	detail.Dir = dir
	record, err := plan.NewPlanRecord(dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	request.Record = record
	if err := (SliceCompletionService{}).CompleteVerified(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	detail = reloadVerifiedCompletion(t, request).Detail()
	command, err := CompletionRecoveryCommand(detail, request.SliceID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, `space'"'"'s`) || strings.Contains(command, "proposal-file") {
		t.Fatal(command)
	}
	files, err := retainedCompletionFiles(detail, request.SliceID)
	if err != nil {
		t.Fatal(err)
	}
	proposalPath := filepath.Join(filepath.Dir(files.NotesFile), "proposal.json")
	(SliceCompletionInputFiles{NotesFile: files.NotesFile, CommitProposalFile: proposalPath}).RemoveForPlan(detail, request.SliceID)
	for _, path := range []string{files.NotesFile, proposalPath} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("retained private input %s: %v", path, err)
		}
	}
	original, err := os.ReadFile(files.NotesFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, data string }{
		{"mismatched", "different notes"},
		{"oversized", strings.Repeat("x", int(maxCompletionNotesBytes)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(files.NotesFile, []byte(tc.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := CompletionRecoveryCommand(detail, request.SliceID); err == nil {
				t.Fatal("accepted corrupt notes")
			}
		})
	}
	if err := os.Remove(files.NotesFile); err != nil {
		t.Fatal(err)
	}
	if _, err := CompletionRecoveryCommand(detail, request.SliceID); err == nil {
		t.Fatal("accepted missing notes")
	}
	partial := filepath.Join(filepath.Dir(filepath.Dir(files.NotesFile)), ".pending-partial")
	if err := os.Mkdir(partial, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(partial, "notes"), original, 0o600); err != nil { // #nosec G703 -- path is derived from this test's private temporary plan directory.
		t.Fatal(err)
	}
	if _, err := CompletionRecoveryCommand(detail, request.SliceID); err == nil {
		t.Fatal("partial inputs authorized recovery")
	}
	if err := os.Symlink(filepath.Join(partial, "notes"), files.NotesFile); err != nil {
		t.Fatal(err)
	}
	if _, err := CompletionRecoveryCommand(detail, request.SliceID); err == nil {
		t.Fatal("symlink notes accepted")
	}
	if err := os.Remove(files.NotesFile); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.NotesFile, original, 0o600); err != nil { // #nosec G703 -- restore the test-owned retained file in its private temporary plan directory.
		t.Fatal(err)
	}
	proposal := filepath.Join(filepath.Dir(files.NotesFile), "proposal.json")
	if err := os.WriteFile(proposal, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := CompletionRecoveryCommand(detail, request.SliceID); err == nil {
		t.Fatal("accepted corrupt proposal")
	}
	if err := os.Remove(proposal); err != nil {
		t.Fatal(err)
	}
	if _, err := CompletionRecoveryCommand(detail, request.SliceID); err != nil {
		t.Fatal("frozen replay needs no proposal", err)
	}
	intent := detail.Slices.Slices[0].CommitIntent
	detail.Slices.Slices[0].CommitIntent = nil
	if _, err := CompletionRecoveryCommand(detail, request.SliceID); err == nil {
		t.Fatal("orphan inputs authorized recovery")
	}
	detail.Slices.Slices[0].CommitIntent = intent
	if err := retainCompletionInputs(request, *intent); err == nil {
		t.Fatal("overwrote immutable transaction")
	}
}

func TestCompletionRecoveryPersistenceFailsBeforeIntent(t *testing.T) {
	testenv.SkipIfShort(t)
	request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	if err := os.WriteFile(filepath.Join(request.Record.Dir(), "completion-recovery"), []byte("obstruction"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := (SliceCompletionService{}).CompleteVerified(context.Background(), request)
	if err == nil || !strings.Contains(err.Error(), "before intent") {
		t.Fatalf("got %v", err)
	}
	if reloadVerifiedCompletion(t, request).Detail().Slices.Slices[0].CommitIntent != nil {
		t.Fatal("intent recorded without retained inputs")
	}
}
