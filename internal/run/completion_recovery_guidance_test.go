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

func TestCompletionRecoveryGuidanceRetainedInputs(t *testing.T) {
	testenv.SkipIfShort(t)
	request := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	detail := request.Record.Detail()
	detail.Dir = filepath.Join(t.TempDir(), "plan space's")
	if err := os.Mkdir(detail.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	request.Record = record
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := (SliceCompletionService{}).CompleteVerified(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	detail = reloadVerifiedCompletion(t, request).Detail()
	action := ExecutionBoundaryAction{EffectiveDisposition: InterruptedSliceCompletionRecovery}
	action.Diagnostics.Facts = InterruptedSliceFacts{IntentPresent: true, RecordedRoot: detail.State.Workspace.Path}
	command, err := CompletionRecoveryCommand(detail, request.SliceID)
	if err != nil {
		t.Fatal(err)
	}
	message := interruptedSliceRunErrorWithDetail(detail, request.SliceID, action).Error()
	if !strings.Contains(command, `space'"'"'s`) || !strings.Contains(message, "\n"+command+"\n") || strings.Index(message, command) > strings.Index(message, "recorded_root=") {
		t.Fatalf("missing exact quoted command before facts: %s", message)
	}
	files, err := retainedCompletionFiles(detail, request.SliceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"corrupt", "deleted", "legacy"} {
		t.Run(kind, func(t *testing.T) {
			switch kind {
			case "corrupt":
				if err := os.WriteFile(files.NotesFile, []byte("stale notes"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if err := os.Remove(files.NotesFile); err != nil {
					t.Fatal(err)
				}
			case "legacy":
				detail.Slices.Slices[0].CommitIntent.Hash = "historical"
			}
			message := interruptedSliceRunErrorWithDetail(detail, request.SliceID, action).Error()
			if strings.Contains(message, "tao slice-complete --plan-dir") || !strings.Contains(message, "original notes") || !strings.Contains(message, "cannot reconstruct") {
				t.Fatalf("unsafe missing-input guidance: %s", message)
			}
		})
	}
}

func TestCompletionRecoveryGuidanceUnreadFacts(t *testing.T) {
	action := ExecutionBoundaryAction{EffectiveDisposition: InterruptedSliceCompletionRecovery}
	action.Diagnostics.Reason = strings.Repeat("long reason ", 80) + "\nexact recovery reason"
	action.Diagnostics.Facts = InterruptedSliceFacts{SliceID: "002", RecordedRoot: t.TempDir(), RecordedBranch: "fix/recovery", RecordedHead: "abc", IntentPresent: true}
	message := interruptedSliceRunError("002", action).Error()
	for _, forbidden := range []string{"live_branch=", "live_HEAD=", "baseline_branch=", "baseline_HEAD=", "changed_paths=none"} {
		if strings.Contains(message, forbidden) {
			t.Errorf("unobserved fact %s: %s", forbidden, message)
		}
	}
	if strings.Index(message, "tao slice-complete") > strings.Index(message, "recorded_root=") {
		t.Errorf("action follows verbose facts: %s", message)
	}
	if !strings.Contains(message, action.Diagnostics.Reason) {
		t.Fatal("lost full reason")
	}
}
