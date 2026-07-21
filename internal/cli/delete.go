package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

var deleteCommand = commandMetadata{
	name:                  "delete",
	minPrefix:             "de",
	usageLines:            []string{"delete (de) <plan-id-or-slug-or-path> --force"},
	completionDescription: "Delete local plan artifacts",
	long:                  "Delete a local Tao plan directory and its metadata after explicit confirmation. This removes local plan artifacts only and does not delete repository history.",
	examples: "  tao delete my-plan --force\n" +
		"  tao delete 20260628-1618-kubectl-style-help --force",
	registerFlags: registerDeleteFlags,
	repository:    repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.delete(c.ctx, c.repo, c.args)
	},
}

func registerDeleteFlags(fs *flag.FlagSet) {
	fs.Bool("force", false, "confirm deletion of local plan artifacts")
}

const deleteUsage = "usage: tao delete <plan-id-or-slug-or-path> --force"

func (a App) delete(ctx context.Context, repo plan.PlanDeleter, args []string) error {
	input, force, err := a.parseDeleteArgs(args)
	if err != nil {
		return err
	}
	if !force {
		return errors.New("--force is required to delete a plan")
	}

	result, err := repo.DeletePlan(ctx, input, plan.DeletePlanOptions{ConfirmInvalid: true, AllowActive: true})
	if err != nil {
		return err
	}
	label := result.ID
	if label == "" {
		label = result.Dir
	}
	return writeln(a.Out, fmt.Sprintf("deleted plan %s (%s)", label, result.Dir))
}

func (a App) parseDeleteArgs(args []string) (string, bool, error) {
	var input string
	for _, arg := range args {
		if arg == "--force" {
			continue
		}
		if strings.HasPrefix(arg, "-") {
			return "", false, fmt.Errorf("unknown flag %q", arg)
		}
		if input != "" {
			return "", false, errors.New(deleteUsage)
		}
		input = arg
	}
	if input == "" {
		return "", false, errors.New(deleteUsage)
	}
	fs, _, err := a.parseArgs("delete", args, registerDeleteFlags)
	if err != nil {
		return "", false, err
	}
	return input, flagBoolValue(fs, "force"), nil
}
