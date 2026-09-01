package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"

	mergepkg "github.com/iamseth/tao/internal/merge"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
)

var abandonCommand = commandMetadata{
	name:                  "abandon",
	usageLines:            []string{"abandon --reason TEXT <plan-id-or-slug-or-path>"},
	completionDescription: "Record an intentional terminal plan outcome",
	long:                  "Mark an unfinished plan abandoned with a durable reason while preserving its slices, reviews, events, Git evidence, and workspace. Abandonment does not clean branches or worktrees and is refused while a lifecycle transaction needs recovery.",
	examples:              "  tao abandon --reason \"superseded by a different approach\" my-plan",
	registerFlags:         registerAbandonFlags,
	completion: completionContext{
		flagValues: map[string]completionFlagValue{
			"reason": {kind: completionValueText, label: "text"},
		},
		positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
	},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.abandon(c.ctx, c.repo, c.args)
	},
}

func registerAbandonFlags(fs *flag.FlagSet) {
	fs.String("reason", "", "required reason for abandoning the plan")
}

type abandonRepository interface {
	plan.PlanRecordResolver
}

type abandonBatchRegistry interface {
	RepoForRoot(string) (taodata.Repo, error)
	MergeBatchesDir(taodata.Repo) string
	ActiveMergeBatchPath(taodata.Repo) string
}

const abandonUsage = "usage: tao abandon --reason TEXT <plan-id-or-slug-or-path>"

func (a App) abandon(ctx context.Context, repo abandonRepository, args []string) error {
	fs, positional, err := a.parseArgs("abandon", args, registerAbandonFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, abandonUsage); err != nil {
		return err
	}
	reason := strings.TrimSpace(flagStringValue(fs, "reason"))
	if err := plan.ValidateAbandonmentReason(reason); err != nil {
		return err
	}

	initial, err := repo.ResolvePlanRecord(ctx, positional[0])
	if err != nil {
		return err
	}
	if initial == nil || initial.Detail() == nil {
		return errors.New("resolved plan record is nil")
	}
	resolved := initial.Detail()
	resolvedID := resolved.State.Plan.ID
	now := a.now().UTC()
	registry, ok := a.registry().(abandonBatchRegistry)
	if !ok {
		return errors.New("abandon requires repository and merge-batch registry paths")
	}
	registered, err := registry.RepoForRoot(resolved.State.Repo.Root)
	if err != nil {
		return fmt.Errorf("resolve plan repository for abandonment: %w", err)
	}
	batchesDir := registry.MergeBatchesDir(registered)
	workspaceOwner, err := mergepkg.NewBatchWorkspace(registered.Root, batchesDir, a.mergeRunner())
	if err != nil {
		return fmt.Errorf("configure merge-batch ownership for abandonment: %w", err)
	}
	ownership, err := workspaceOwner.AcquirePlanOwnership(resolvedID, resolved.Dir, now)
	if err != nil {
		return err
	}
	defer func() { _ = ownership.Release() }()

	store := mergepkg.NewBatchStore(batchesDir, registry.ActiveMergeBatchPath(registered))
	activeID, err := store.ActiveID()
	if err != nil {
		return fmt.Errorf("load active merge batch for abandonment: %w", err)
	}
	if activeID != "" {
		batch, loadErr := store.Load(activeID)
		if loadErr != nil {
			return fmt.Errorf("load active merge batch %s for abandonment: %w", activeID, loadErr)
		}
		if batch.ID == "" {
			return fmt.Errorf("active merge batch %s has no durable state; abandonment refused", activeID)
		}
		if batch.Status != mergepkg.BatchStatusCompleted && effectiveBatchCandidate(batch, resolvedID) {
			return fmt.Errorf("plan %s is an effective candidate in unsettled merge batch %s (%s) and cannot be abandoned", resolvedID, batch.ID, batch.Status)
		}
	}

	reloaded, err := repo.ResolvePlanRecord(ctx, resolvedID)
	if err != nil {
		return fmt.Errorf("reload plan while holding run lock: %w", err)
	}
	if reloaded == nil || reloaded.Detail() == nil {
		return errors.New("reload plan while holding run lock: resolved plan record is nil")
	}
	planID := reloaded.Detail().State.Plan.ID
	alreadyAbandoned := reloaded.Detail().State.Status == plan.StatusAbandoned && plan.ProjectAbandonment(reloaded.Detail().Events) != nil
	if err := reloaded.Abandon(reason, now); err != nil {
		return err
	}
	if alreadyAbandoned {
		return writef(a.Out, "Plan already abandoned: %s\n", planID)
	}
	return writef(a.Out, "Plan abandoned: %s\n", planID)
}

func effectiveBatchCandidate(batch mergepkg.BatchState, planID string) bool {
	for _, candidate := range batch.Candidates {
		if candidate.PlanID == planID {
			return batch.Ejection == nil || batch.Ejection.PlanID != planID
		}
	}
	return false
}
