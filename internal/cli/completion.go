package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/iamseth/tao/internal/plan"
)

var completionCommand = commandMetadata{
	name:                  "completion",
	minPrefix:             "co",
	usageLines:            []string{"completion (co) zsh"},
	completionDescription: "Generate shell completion",
	long:                  "Generate a shell completion script for Tao. Zsh is currently supported and includes command aliases, plan-id completion, subcommand suggestions, and flag metadata.",
	examples: "  tao completion zsh > ~/.zfunc/_tao\n" +
		"  tao completion zsh",
	completion: completionContext{
		positional: completionPositional{index: 1, label: "shell", candidates: []string{"zsh"}},
	},
}

func (a App) completion(args []string) error {
	if len(args) != 1 || args[0] != "zsh" {
		return errors.New("usage: tao completion zsh")
	}
	_, err := fmt.Fprint(a.Out, buildZshCompletionScript())
	return err
}

func (a App) complete(ctx context.Context, repo planLister, args []string) error {
	if len(args) != 1 || (args[0] != "plan-ids" && args[0] != "run-plan-ids") {
		return errors.New("usage: tao complete plan-ids|run-plan-ids")
	}
	summaries, err := repo.ListPlans(ctx, plan.PlanFilter{})
	if err != nil {
		return err
	}
	candidates := make([]plan.PlanSummary, 0, len(summaries))
	slugCounts := make(map[string]int)
	for _, summary := range summaries {
		if args[0] == "run-plan-ids" && !summary.Runnable() {
			continue
		}
		candidates = append(candidates, summary)
		if slug, ok := plan.PlanSlug(summary.ID); ok {
			slugCounts[slug]++
		}
	}
	for _, summary := range candidates {
		candidate := summary.ID
		if slug, ok := plan.PlanSlug(summary.ID); ok && slugCounts[slug] == 1 {
			candidate = slug
		}
		if err := writeln(a.Out, candidate); err != nil {
			return err
		}
	}
	return nil
}
