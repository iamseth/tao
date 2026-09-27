package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

const sliceBlockedUsage = "slice-blocked --plan-dir DIR --slice-id ID --reason-file FILE [--gate-command COMMAND --failing-path PATH ...] [--invalid-command COMMAND --invalid-reason REASON [--corrected-command COMMAND]]"

var sliceBlockedCommand = commandMetadata{
	name:                  "slice-blocked",
	usageLines:            []string{sliceBlockedUsage},
	completionDescription: "Block a slice after an exceptional stop",
	long:                  "Record an exceptional agent stop for a Tao plan slice. Pair --gate-command with repeatable --failing-path (up to 64 repository-relative paths) to record a failing gate; Tao verifies plan ownership from Git and fingerprints the slice's recorded execution worktree when available. Optional invalid-command evidence records a command that failed before tests loaded and a mechanically equivalent correction when available. Both evidence groups may be supplied together.",
	examples:              "  tao slice-blocked --plan-dir /path/to/plan --slice-id 001-example --reason-file /tmp/reason.txt\n  tao slice-blocked --plan-dir /path/to/plan --slice-id 001-example --reason-file /tmp/reason.txt --invalid-command 'go test ./missing' --invalid-reason 'package path does not exist' --corrected-command 'go test ./internal/cli'\n  tao slice-blocked --plan-dir /path/to/plan --slice-id 002-example --reason-file /tmp/reason.txt --gate-command 'go test ./...' --failing-path internal/cli/example.go",
	registerFlags:         registerSliceBlockedFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"corrected-command": {kind: completionValueText, label: "command"},
		"gate-command":      {kind: completionValueText, label: "command"},
		"failing-path":      {kind: completionValuePath, label: "path"},
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
	fs.String("gate-command", "", "failing verification gate command")
	fs.Var(new(stringListFlag), "failing-path", "repository-relative failing path (repeatable)")
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

func parseSliceBlockedGateEvidence(fs *flag.FlagSet) (*plan.SliceBlockedEvidence, error) {
	if !flagWasProvided(fs, "gate-command") && !flagWasProvided(fs, "failing-path") {
		return nil, nil
	}
	command, err := agentinput.BoundedText(flagStringValue(fs, "gate-command"), "gate command")
	if err != nil {
		return nil, err
	}
	paths := append([]string(nil), (*fs.Lookup("failing-path").Value.(*stringListFlag))...)
	if command == "" || len(paths) == 0 {
		return nil, errors.New("--gate-command and --failing-path are required together")
	}
	if len(paths) > 64 {
		return nil, errors.New("at most 64 failing paths may be supplied")
	}
	for i, path := range paths {
		paths[i], err = agentinput.BoundedText(path, "failing path")
		if err != nil {
			return nil, err
		}
		if paths[i] == "" {
			return nil, errors.New("failing path must not be empty")
		}
	}
	return &plan.SliceBlockedEvidence{GateCommand: command, FailingPaths: paths}, nil
}

func (a App) verifySliceBlockedOwnership(ctx context.Context, detail *plan.PlanDetail, sliceID string, evidence *plan.SliceBlockedEvidence) {
	var root string
	for _, slice := range detail.Slices.Slices {
		if slice.ID == sliceID {
			root = slice.ExecutionRoot
			break
		}
	}
	// Failure is advisory: retain the agent's bounded gate/paths without any
	// ownership or fingerprint authority if any part of Git verification fails.
	verified := false
	defer func() {
		if !verified {
			_ = writef(a.Err, "Warning: blocker ownership could not be verified; recording gate and paths only.\n")
		}
	}()
	base := plan.PlanOwnershipBase(detail)
	if root == "" || base == "" {
		return
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return
	}
	client := gitops.NewClient(root, a.CommandRunner)
	owned, err := client.ChangedFilesExact(ctx, base+"..HEAD")
	if err != nil {
		return
	}
	head, parts, err := client.WorktreeFingerprintParts(ctx)
	if err != nil {
		return
	}
	evidence.PlanOwned = plan.ClassifyBlockerPaths(evidence.FailingPaths, owned)
	evidence.HeadSHA = head
	evidence.WorktreeFingerprint = plan.WorktreeFingerprint(append([]string{head}, parts...)...)
	verified = true
}

func (a App) sliceBlocked(ctx context.Context, args []string) error {
	const usage = "usage: tao " + sliceBlockedUsage
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

	gateEvidence, err := parseSliceBlockedGateEvidence(fs)
	if err != nil {
		return err
	}

	repository := a.repository(filepath.Dir(planDir))
	record, err := repository.ResolvePlanRecord(ctx, planDir)
	if err != nil {
		return err
	}
	now := a.now().UTC()
	if gateEvidence != nil {
		a.verifySliceBlockedOwnership(ctx, record.Detail(), sliceID, gateEvidence)
		err = record.BlockSliceWithEvidence(sliceID, reason, gateEvidence, now)
	} else {
		err = record.BlockSlice(sliceID, reason, now)
	}
	if err != nil {
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
