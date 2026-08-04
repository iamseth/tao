package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/plan"
)

var sliceBlockedCommand = commandMetadata{
	name:                  "slice-blocked",
	usageLines:            []string{"slice-blocked --plan-dir DIR --slice-id ID --reason-file FILE [--invalid-command COMMAND --invalid-reason REASON [--corrected-command COMMAND]]"},
	completionDescription: "Block a slice after an exceptional stop",
	long:                  "Record an exceptional agent stop for a Tao plan slice. Optional verification evidence records a command that failed before tests loaded and a mechanically equivalent correction when available.",
	examples:              "  tao slice-blocked --plan-dir /path/to/plan --slice-id 001-example --reason-file /tmp/reason.txt\n  tao slice-blocked --plan-dir /path/to/plan --slice-id 001-example --reason-file /tmp/reason.txt --invalid-command 'go test ./missing' --invalid-reason 'package path does not exist' --corrected-command 'go test ./internal/cli'",
	registerFlags:         registerSliceBlockedFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"corrected-command": {kind: completionValueText, label: "command"},
		"invalid-command":   {kind: completionValueText, label: "command"},
		"invalid-reason":    {kind: completionValueText, label: "reason"},
		"plan-dir":          {kind: completionValuePath, label: "path"},
		"reason-file":       {kind: completionValuePath, label: "path"},
		"slice-id":          {kind: completionValueText, label: "slice id"},
	}},
	execute: func(c commandContext) error {
		return c.app.sliceBlocked(c.ctx, c.args)
	},
}

func registerSliceBlockedFlags(fs *flag.FlagSet) {
	fs.String("plan-dir", "", "plan directory")
	fs.String("slice-id", "", "slice id to block")
	fs.String("reason-file", "", "file containing the blocker reason")
	fs.String("invalid-command", "", "verification command that failed before tests loaded")
	fs.String("invalid-reason", "", "reason the verification command was invalid")
	fs.String("corrected-command", "", "mechanically equivalent corrected verification command")
}

type sliceBlockedEvidence struct {
	provided         bool
	invalidCommand   string
	invalidReason    string
	correctedCommand string
}

func parseSliceBlockedEvidence(fs *flag.FlagSet) (sliceBlockedEvidence, error) {
	evidence := sliceBlockedEvidence{
		provided: flagWasProvided(fs, "invalid-command") || flagWasProvided(fs, "invalid-reason") || flagWasProvided(fs, "corrected-command"),
	}
	var err error
	if evidence.invalidCommand, err = agentinput.BoundedText(flagStringValue(fs, "invalid-command"), "invalid command"); err != nil {
		return sliceBlockedEvidence{}, err
	}
	if evidence.invalidReason, err = agentinput.BoundedText(flagStringValue(fs, "invalid-reason"), "invalid reason"); err != nil {
		return sliceBlockedEvidence{}, err
	}
	if evidence.correctedCommand, err = agentinput.BoundedText(flagStringValue(fs, "corrected-command"), "corrected command"); err != nil {
		return sliceBlockedEvidence{}, err
	}
	if !evidence.provided {
		return evidence, nil
	}
	if evidence.invalidCommand == "" {
		return sliceBlockedEvidence{}, errors.New("--invalid-command is required when verification evidence flags are used")
	}
	if evidence.invalidReason == "" {
		return sliceBlockedEvidence{}, errors.New("--invalid-reason is required when verification evidence flags are used")
	}
	return evidence, nil
}

func (a App) sliceBlocked(ctx context.Context, args []string) error {
	const usage = "usage: tao slice-blocked --plan-dir DIR --slice-id ID --reason-file FILE [--invalid-command COMMAND --invalid-reason REASON [--corrected-command COMMAND]]"
	fs, positional, err := a.parseArgs("slice-blocked", args, registerSliceBlockedFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, usage); err != nil {
		return err
	}
	planDir := flagStringValue(fs, "plan-dir")
	sliceID := flagStringValue(fs, "slice-id")
	reasonFile := flagStringValue(fs, "reason-file")
	if strings.TrimSpace(planDir) == "" || strings.TrimSpace(sliceID) == "" || strings.TrimSpace(reasonFile) == "" {
		return errors.New(usage)
	}

	reasonBytes, err := agentinput.ReadBoundedFile(reasonFile, "reason file", agentinput.MaxFileBytes)
	if err != nil {
		return fmt.Errorf("read blocker reason file: %w", err)
	}
	reason, err := agentinput.BoundedText(string(reasonBytes), "blocker reason")
	if err != nil {
		return err
	}
	if reason == "" {
		return errors.New("blocker reason file is empty")
	}
	evidence, err := parseSliceBlockedEvidence(fs)
	if err != nil {
		return err
	}

	repository := a.repository(filepath.Dir(planDir))
	record, err := repository.ResolvePlanRecord(ctx, planDir)
	if err != nil {
		return err
	}
	now := a.now().UTC()
	if err := record.BlockSlice(sliceID, reason, now); err != nil {
		return err
	}
	if evidence.provided && !hasVerificationCommandInvalidEvidence(record.Detail().Events, sliceID, evidence) {
		event := plan.Event{
			Type:             plan.EventTypeVerificationCommandInvalid,
			Timestamp:        now,
			PlanID:           record.Detail().State.Plan.ID,
			SliceID:          sliceID,
			Command:          evidence.invalidCommand,
			CorrectedCommand: evidence.correctedCommand,
			Reason:           evidence.invalidReason,
			Message:          "Verification command invalid",
		}
		if err := repository.AppendEvent(record.Dir(), event); err != nil {
			return fmt.Errorf("append verification_command_invalid event: %w", err)
		}
	}
	return writef(a.Out, "Slice blocked: %s\n", sliceID)
}

func hasVerificationCommandInvalidEvidence(events []plan.Event, sliceID string, evidence sliceBlockedEvidence) bool {
	for _, event := range slices.Backward(events) {

		if event.Type == plan.EventTypeSliceBlocked && event.SliceID == sliceID {
			return false
		}
		if event.Type == plan.EventTypeVerificationCommandInvalid && event.SliceID == sliceID && event.Command == evidence.invalidCommand && event.Reason == evidence.invalidReason && event.CorrectedCommand == evidence.correctedCommand {
			return true
		}
	}
	return false
}
