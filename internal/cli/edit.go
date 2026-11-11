package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/plan"
	verificationimpl "github.com/iamseth/tao/internal/plan/verification"
)

var editCommand = commandMetadata{
	name:                  "edit",
	minPrefix:             "e",
	usageLines:            []string{"edit (e) remove <plan-id-or-slug-or-path> <slice-id>", "edit (e) skip <plan-id-or-slug-or-path> <slice-id>", "edit (e) move <plan-id-or-slug-or-path> <slice-id> (--before ID | --after ID)", "edit (e) amend <plan-id-or-slug-or-path> <slice-id> --allow-file PATH... --mechanical REASON", "edit (e) amend <plan-id-or-slug-or-path> <slice-id> --reason-file FILE [--goal-file FILE] [--add-task TEXT]... [--allow-file PATH]... [--add-manual-check TEXT]... [--replace-verification-command OLD --with NEW]..."},
	completionDescription: "Edit pending slices in a plan",
	long:                  "Edit pending slices in a Tao plan without changing application files. Use it to remove, skip, or reorder work before running the remaining queue, or to amend a pending or blocked slice's contract with a recorded reason. Amend appends tasks, expected files, and manual checks or replaces the goal. Use --mechanical REASON with --allow-file PATH for file-only mechanical extensions, mutually exclusive with --reason-file and all other contract edits. Classification does not bypass ownership, review, or merge checks. Repeatable --replace-verification-command OLD and --with NEW flags are paired by index for one-for-one replacement, never deleting a command. Generated repair slices accept appended files, tasks, and manual checks but never a replacement goal or verification command. Amend never changes slice status, blocker notes, or approval, refuses while a run holds the plan lock, and validates the amended plan before persisting.",
	examples: "  tao edit remove my-plan 003-old-slice\n" +
		"  tao edit skip my-plan 004-optional\n" +
		"  tao edit move my-plan 005-tests --before 004-docs\n" +
		"  tao edit amend my-plan 002-blocked --reason-file /tmp/reason.txt --allow-file internal/cli/helper.go --add-task 'Add the helper' --add-manual-check 'Run the helper once'\n" +
		"  tao edit amend my-plan 002-blocked --allow-file internal/cli/testdata/zsh_completion.golden --mechanical 'Regenerate completion for the new flag'",
	subcommands: []commandSubcommand{
		{name: "remove", description: "Remove a pending slice from the plan", completion: completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}}},
		{name: "skip", description: "Mark a pending slice skipped", completion: completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}}},
		{name: "move", description: "Move a pending slice before or after another pending slice", registerFlags: registerEditMoveFlags, completion: completionContext{positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs}}},
		{name: "amend", description: "Amend a pending or blocked slice's contract with a recorded reason; verification replacements are one-for-one, never delete commands, and are refused for generated repair slices, as are goal replacements", registerFlags: registerEditAmendFlags, completion: completionContext{
			positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
			flagValues: map[string]completionFlagValue{
				"mechanical":                   {kind: completionValueText, label: "reason"},
				"reason-file":                  {kind: completionValuePath, label: "path"},
				"goal-file":                    {kind: completionValuePath, label: "path"},
				"allow-file":                   {kind: completionValuePath, label: "path"},
				"add-task":                     {kind: completionValueText, label: "task"},
				"add-manual-check":             {kind: completionValueText, label: "check"},
				"replace-verification-command": {kind: completionValueText, label: "command"},
				"with":                         {kind: completionValueText, label: "command"},
			},
		}},
	},
	registerFlags: registerEditFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.edit(c.ctx, c.repo, c.args)
	},
}

const (
	editAmendUsage = "tao edit amend <plan-id-or-slug-or-path> <slice-id> --allow-file PATH... --mechanical REASON | tao edit amend <plan-id-or-slug-or-path> <slice-id> --reason-file FILE [--goal-file FILE] [--add-task TEXT]... [--allow-file PATH]... [--add-manual-check TEXT]... [--replace-verification-command OLD --with NEW]... (one-for-one; never deletes commands; refused for generated repair slices)"
	editUsage      = "usage: tao edit remove <plan-id-or-slug-or-path> <slice-id> | tao edit skip <plan-id-or-slug-or-path> <slice-id> | " + editAmendUsage + " | tao edit move <plan-id-or-slug-or-path> <slice-id> (--before ID | --after ID)"
)

type editRepository interface {
	plan.PlanRecordResolver
}

func (a App) edit(ctx context.Context, repo editRepository, args []string) error {
	if len(args) == 0 {
		return errors.New(editUsage)
	}

	switch args[0] {
	case "remove":
		return a.editRemove(ctx, repo, args[1:])
	case "skip":
		return a.editSkip(ctx, repo, args[1:])
	case "move":
		return a.editMove(ctx, repo, args[1:])
	case "amend":
		return a.editAmend(ctx, repo, args[1:])
	default:
		return fmt.Errorf("unknown edit subcommand %q", args[0])
	}
}

func (a App) editRemove(ctx context.Context, repo editRepository, args []string) error {
	input, sliceID, err := parseEditSliceArgs(args)
	if err != nil {
		return err
	}
	record, err := repo.ResolvePlanRecord(ctx, input)
	if err != nil {
		return err
	}
	if err := record.RemoveSlice(sliceID, a.now().UTC()); err != nil {
		return err
	}
	return a.writeEditResult(record.Detail(), fmt.Sprintf("Removed pending slice: %s", sliceID))
}

func (a App) editSkip(ctx context.Context, repo editRepository, args []string) error {
	input, sliceID, err := parseEditSliceArgs(args)
	if err != nil {
		return err
	}
	record, err := repo.ResolvePlanRecord(ctx, input)
	if err != nil {
		return err
	}
	if err := record.SkipSlice(sliceID, a.now().UTC()); err != nil {
		return err
	}
	return a.writeEditResult(record.Detail(), fmt.Sprintf("Skipped pending slice: %s", sliceID))
}

// registerEditFlags registers every subcommand flag for help and top-level
// completion; each subcommand parses only its own flags.
func registerEditFlags(fs *flag.FlagSet) {
	registerEditMoveFlags(fs)
	registerEditAmendFlags(fs)
}

func registerEditMoveFlags(fs *flag.FlagSet) {
	fs.String("before", "", "move before slice id")
	fs.String("after", "", "move after slice id")
}

func registerEditAmendFlags(fs *flag.FlagSet) {
	fs.String("reason-file", "", "file containing the amendment reason (required unless --mechanical)")
	fs.String("mechanical", "", "reason for a file-only mechanical scope extension (requires --allow-file; excludes --reason-file and other contract edits)")
	fs.String("goal-file", "", "file containing the replacement slice goal")
	fs.Var(new(stringListFlag), "add-task", "task to append to the slice (repeatable)")
	fs.Var(new(stringListFlag), "allow-file", "repository-relative expected file to append (repeatable)")
	fs.Var(new(stringListFlag), "add-manual-check", "manual check to append to the slice (repeatable)")
	fs.Var(new(stringListFlag), "replace-verification-command", "exact verification command to replace (repeatable, paired by index with --with)")
	fs.Var(new(stringListFlag), "with", "replacement verification command (repeatable, paired by index with --replace-verification-command)")
}

func (a App) editMove(ctx context.Context, repo editRepository, args []string) error {
	fs, positional, err := a.parseArgs("edit move", args, registerEditMoveFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 2, editUsage); err != nil {
		return err
	}
	before := strings.TrimSpace(flagStringValue(fs, "before"))
	after := strings.TrimSpace(flagStringValue(fs, "after"))
	if before == "" && after == "" {
		return errors.New("edit move requires --before or --after")
	}
	if before != "" && after != "" {
		return errors.New("edit move accepts only one of --before or --after")
	}

	record, err := repo.ResolvePlanRecord(ctx, positional[0])
	if err != nil {
		return err
	}
	detail := record.Detail()
	order, err := movedPendingOrder(detail.State.Plan.PendingSlices, positional[1], before, after)
	if err != nil {
		return err
	}
	if err := record.ReorderPendingSlices(order, a.now().UTC()); err != nil {
		return err
	}
	return a.writeEditResult(detail, fmt.Sprintf("Moved pending slice: %s", positional[1]))
}

// editAmend records one operator amendment of a pending or blocked slice's
// contract. It bounds every operator input, refuses while the plan run lock is
// held, and validates the amended plan in memory before the journaled record
// mutation persists anything.
func (a App) editAmend(ctx context.Context, repo editRepository, args []string) error {
	fs, positional, err := a.parseArgs("edit amend", args, registerEditAmendFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 2, "usage: "+editAmendUsage); err != nil {
		return err
	}
	request, err := parseEditAmendRequest(fs)
	if err != nil {
		return err
	}
	record, err := repo.ResolvePlanRecord(ctx, positional[0])
	if err != nil {
		return err
	}
	sliceID := positional[1]
	now := a.now().UTC()
	var amendedFields []string
	err = plan.WithRunLock(ctx, record.Detail(), now, func(context.Context) error {
		preview, err := previewAmendedPlan(record.Detail(), sliceID, request)
		if err != nil {
			return err
		}
		result := plan.ValidatePlanVerification(preview)
		if result.HasErrors() {
			return fmt.Errorf("amended plan verification is invalid: %s", strings.Join(verificationFindingMessages(result, plan.VerificationFindingError), "; "))
		}
		for _, warning := range verificationFindingMessages(result, plan.VerificationFindingWarning) {
			if err := writef(a.Err, "Warning: %s\n", warning); err != nil {
				return err
			}
		}
		if err := record.AmendSlice(sliceID, request, now); err != nil {
			return err
		}
		amendedFields = latestAmendmentFields(record.Detail(), sliceID)
		return nil
	})
	if err != nil {
		return err
	}
	detail := record.Detail()
	if err := writef(a.Out, "Amended slice %s: %s\n", sliceID, strings.Join(amendedFields, ", ")); err != nil {
		return err
	}
	return a.writeEditResult(detail, fmt.Sprintf("Operator Amendments: %d recorded for slice %s", len(findAmendedSlice(detail, sliceID).Amendments), sliceID))
}

// parseEditAmendRequest bounds the operator's reason, goal, and repeatable
// contract additions and refuses requests that change nothing.
func parseEditAmendRequest(fs *flag.FlagSet) (plan.SliceAmendmentRequest, error) {
	provided := make(map[string]bool)
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	if provided["mechanical"] {
		for _, name := range []string{"reason-file", "goal-file", "add-task", "add-manual-check", "replace-verification-command", "with"} {
			if provided[name] {
				return plan.SliceAmendmentRequest{}, fmt.Errorf("--mechanical cannot be combined with --%s", name)
			}
		}
		reason, err := agentinput.BoundedText(flagStringValue(fs, "mechanical"), "mechanical reason")
		if err != nil {
			return plan.SliceAmendmentRequest{}, err
		}
		if reason == "" {
			return plan.SliceAmendmentRequest{}, errors.New("--mechanical requires a nonblank reason")
		}
		paths, err := boundedEditList(fs, "allow-file", "allowed file")
		if err != nil {
			return plan.SliceAmendmentRequest{}, err
		}
		if len(paths) == 0 {
			return plan.SliceAmendmentRequest{}, errors.New("--mechanical requires at least one nonblank --allow-file")
		}
		return plan.SliceAmendmentRequest{Reason: reason, MechanicalReason: reason, AllowFiles: paths}, nil
	}
	reasonFile := strings.TrimSpace(flagStringValue(fs, "reason-file"))
	if reasonFile == "" {
		return plan.SliceAmendmentRequest{}, errors.New("edit amend requires --reason-file FILE containing the amendment reason")
	}
	reason, err := readBoundedEditText(reasonFile, "amendment reason")
	if err != nil {
		return plan.SliceAmendmentRequest{}, err
	}
	if reason == "" {
		return plan.SliceAmendmentRequest{}, errors.New("amendment reason file is empty")
	}
	request := plan.SliceAmendmentRequest{Reason: reason}
	if goalFile := strings.TrimSpace(flagStringValue(fs, "goal-file")); goalFile != "" {
		if request.Goal, err = readBoundedEditText(goalFile, "amendment goal"); err != nil {
			return plan.SliceAmendmentRequest{}, err
		}
		if request.Goal == "" {
			return plan.SliceAmendmentRequest{}, errors.New("amendment goal file is empty")
		}
	}
	if request.AddTasks, err = boundedEditList(fs, "add-task", "added task"); err != nil {
		return plan.SliceAmendmentRequest{}, err
	}
	if request.AllowFiles, err = boundedEditList(fs, "allow-file", "allowed file"); err != nil {
		return plan.SliceAmendmentRequest{}, err
	}
	if request.AddManualChecks, err = boundedEditList(fs, "add-manual-check", "added manual check"); err != nil {
		return plan.SliceAmendmentRequest{}, err
	}
	olds := *fs.Lookup("replace-verification-command").Value.(*stringListFlag)
	news := *fs.Lookup("with").Value.(*stringListFlag)
	if len(olds) != len(news) {
		return plan.SliceAmendmentRequest{}, errors.New("--replace-verification-command and --with require equal counts")
	}
	for i, raw := range olds {
		old, err := agentinput.BoundedText(raw, "verification command to replace")
		if err != nil {
			return plan.SliceAmendmentRequest{}, err
		}
		replacement, err := agentinput.BoundedText(news[i], "replacement verification command")
		if err != nil {
			return plan.SliceAmendmentRequest{}, err
		}
		request.ReplaceVerificationCommands = append(request.ReplaceVerificationCommands, plan.VerificationCommandReplacement{Old: old, New: replacement})
	}
	if request.Goal == "" && len(request.AddTasks) == 0 && len(request.AllowFiles) == 0 && len(request.AddManualChecks) == 0 && len(request.ReplaceVerificationCommands) == 0 {
		return plan.SliceAmendmentRequest{}, errors.New("edit amend requires at least one contract change: --goal-file, --add-task, --allow-file, --add-manual-check, or --replace-verification-command")
	}
	return request, nil
}

func readBoundedEditText(path string, label string) (string, error) {
	data, err := agentinput.ReadBoundedFile(path, label+" file", agentinput.MaxFileBytes)
	if err != nil {
		return "", fmt.Errorf("read %s file: %w", label, err)
	}
	return agentinput.BoundedText(string(data), label)
}

// boundedEditList bounds each repeatable flag value and drops blank entries.
func boundedEditList(fs *flag.FlagSet, name string, label string) ([]string, error) {
	var values []string
	for _, raw := range *fs.Lookup(name).Value.(*stringListFlag) {
		value, err := agentinput.BoundedText(raw, label)
		if err != nil {
			return nil, err
		}
		if value != "" {
			values = append(values, value)
		}
	}
	return values, nil
}

// previewAmendedPlan returns an in-memory copy of detail with the amendment
// applied to the named slice so the plan can be validated before anything is
// persisted. The copy shares no slice storage with detail.
func previewAmendedPlan(detail *plan.PlanDetail, sliceID string, request plan.SliceAmendmentRequest) (*plan.PlanDetail, error) {
	preview := *detail
	preview.Slices.Slices = slices.Clone(detail.Slices.Slices)
	slice := findAmendedSlice(&preview, sliceID)
	if slice == nil {
		return &preview, nil
	}
	verification, err := plan.ApplyVerificationCommandReplacements(slice.Verification, request.ReplaceVerificationCommands)
	if err != nil {
		return nil, err
	}
	slice.Verification = verification
	if goal := strings.TrimSpace(request.Goal); goal != "" {
		slice.Goal = goal
	}
	slice.Tasks = appendMissingEditValues(slice.Tasks, request.AddTasks)
	slice.ExpectedFiles = appendMissingEditValues(slice.ExpectedFiles, request.AllowFiles)
	slice.Verification.ManualChecks = appendMissingEditValues(slice.Verification.ManualChecks, request.AddManualChecks)
	return &preview, nil
}

func appendMissingEditValues(existing []string, additions []string) []string {
	result := slices.Clone(existing)
	for _, value := range additions {
		if !slices.Contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func findAmendedSlice(detail *plan.PlanDetail, sliceID string) *plan.Slice {
	for i := range detail.Slices.Slices {
		if detail.Slices.Slices[i].ID == sliceID {
			return &detail.Slices.Slices[i]
		}
	}
	return nil
}

func latestAmendmentFields(detail *plan.PlanDetail, sliceID string) []string {
	slice := findAmendedSlice(detail, sliceID)
	if slice == nil || len(slice.Amendments) == 0 {
		return nil
	}
	return slices.Clone(slice.Amendments[len(slice.Amendments)-1].Fields)
}

func verificationFindingMessages(result plan.VerificationValidationResult, severity verificationimpl.FindingSeverity) []string {
	var messages []string
	for _, finding := range result.Findings {
		if finding.Severity == severity {
			messages = append(messages, finding.Message)
		}
	}
	return messages
}

func parseEditSliceArgs(args []string) (string, string, error) {
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return "", "", fmt.Errorf("unknown flag %q", arg)
		}
	}
	if len(args) != 2 {
		return "", "", errors.New(editUsage)
	}
	return args[0], args[1], nil
}

func movedPendingOrder(pending []string, moveID string, beforeID string, afterID string) ([]string, error) {
	targetID := beforeID
	if targetID == "" {
		targetID = afterID
	}
	if moveID == targetID {
		return nil, errors.New("slice id and move target must differ")
	}

	order := make([]string, 0, len(pending))
	foundMove := false
	foundTarget := false
	for _, id := range pending {
		if id == moveID {
			foundMove = true
			continue
		}
		if id == targetID {
			foundTarget = true
		}
		order = append(order, id)
	}
	if !foundMove {
		return nil, fmt.Errorf("slice %s is not in pending_slices", moveID)
	}
	if !foundTarget {
		return nil, fmt.Errorf("move target %s is not in pending_slices", targetID)
	}

	insertAt := -1
	for i, id := range order {
		if id == targetID {
			insertAt = i
			if afterID != "" {
				insertAt++
			}
			break
		}
	}
	order = append(order, "")
	copy(order[insertAt+1:], order[insertAt:])
	order[insertAt] = moveID
	return order, nil
}

func (a App) writeEditResult(detail *plan.PlanDetail, summary string) error {
	if err := writeln(a.Out, summary); err != nil {
		return err
	}
	if len(detail.State.Plan.PendingSlices) == 0 {
		return writef(a.Out, "Next: tao validate %s\n", detail.State.Plan.ID)
	}
	return renderPrimaryNextAction(a.Out, plan.DeriveNextAction(detail))
}
