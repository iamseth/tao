package cli

import (
	"context"
	"errors"
	"flag"
	"strings"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
)

var sliceCompleteCommand = commandMetadata{
	name:                  "slice-complete",
	usageLines:            []string{"slice-complete --plan-dir DIR --slice-id ID --notes-file FILE [--verification-results-file FILE] [--commit-proposal-file FILE]"},
	completionDescription: "Verify declared gates and complete a slice",
	long:                  "Execute declared slice gates locally before commit intent, with a fixed ten-minute timeout per command within the unchanged remaining agent-session budget (even when session timeout is disabled). Commands are not sandboxed or cryptographically attested. Optional agent results are advisory claims, never completion authority. Historical intents require their original results inputs and recover without rerunning gates. Tao validates proposals, adds trusted trailers, owns Git, and removes temporary inputs only after success. Final repository verification is unchanged.",
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
	fs.String("verification-results-file", "", "optional JSON advisory claims; original results required for historical intents")
}

func (a App) sliceComplete(ctx context.Context, args []string) error {
	const usage = "usage: tao slice-complete --plan-dir DIR --slice-id ID --notes-file FILE [--verification-results-file FILE] [--commit-proposal-file FILE]"
	ctx, stop := newCommandSignalContext(ctx)
	defer stop()
	fs, positional, err := a.parseArgs("slice-complete", args, registerSliceCompleteFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, usage); err != nil {
		return err
	}
	planDir := flagStringValue(fs, "plan-dir")
	sliceID := flagStringValue(fs, "slice-id")
	files := run.SliceCompletionInputFiles{
		NotesFile:               flagStringValue(fs, "notes-file"),
		VerificationResultsFile: flagStringValue(fs, "verification-results-file"),
		CommitProposalFile:      flagStringValue(fs, "commit-proposal-file"),
	}
	if strings.TrimSpace(planDir) == "" || strings.TrimSpace(sliceID) == "" || strings.TrimSpace(files.NotesFile) == "" {
		return errors.New(usage)
	}
	record, err := plan.NewFileRepository("").ResolvePlanRecord(ctx, planDir)
	if err != nil {
		return err
	}
	loader := run.LoadVerifiedCompletionInputs
	if run.UsesHistoricalCompletionInputs(record.Detail(), sliceID) {
		if strings.TrimSpace(files.VerificationResultsFile) == "" {
			return errors.New("historical intent recovery requires the original --verification-results-file")
		}
		loader = run.LoadSliceCompletionInputs
	}
	inputs, err := loader(files)
	if err != nil {
		return err
	}
	service := run.SliceCompletionService{CommandRunner: a.CommandRunner, Output: a.Out}
	if err := service.Complete(ctx, run.SliceCompletionRequest{
		Record: record, SliceID: sliceID, Notes: inputs.Notes,
		VerificationResults: inputs.VerificationResults, VerificationClaims: inputs.VerificationClaims,
		CommitProposal: inputs.CommitProposal, Now: a.now().UTC(),
	}); err != nil {
		return err
	}
	files.Remove()
	return writef(a.Out, "Slice completed: %s\n", sliceID)
}
