package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	mergepkg "github.com/iamseth/tao/internal/merge"
	"github.com/iamseth/tao/internal/plan"
	runpkg "github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/workspace"
)

var mergeCommand = commandMetadata{
	name:      "merge",
	minPrefix: "m",
	usageLines: []string{
		"merge (m) [--force] [--record-only] [--no-squash] [--no-verify] [--verify-command CMD] <plan-id-or-slug-or-path>",
		"merge (m) --all [--dry-run] [--restart] [--auto-eject] [--verify-command CMD]",
	},
	completionDescription: "Merge approved plans into the default branch",
	long:                  "Merge one reviewed, approved Tao plan, or atomically stage every reviewed and approved plan with --all. An ordinary single-plan squash conflict gets one automatic resolver attempt, exact structural validation, the configured verification gate, and an independent fresh-session review before completion; --force cannot bypass these safety and review gates, while --no-verify skips only command verification. --no-squash rebase conflicts remain manual. Batch mode keeps default unchanged while it orders and stages one squash per source, uses bounded agent resolution, then requires full verification and aggregate approval before one fast-forward. Eligible attributed aggregate-review non-convergence stops and offers to eject that plan on the next rerun; --auto-eject performs the eject-and-reland in the same run. Ejection is offered only when it leaves a non-empty batch and no plan was already ejected. Reruns resume durable progress; --dry-run previews and --restart discards only pre-landing batch recovery. Batch mode rejects --force, --record-only, --no-squash, and --no-verify.",
	examples: "  tao merge my-plan\n" +
		"  tao merge --all\n" +
		"  tao merge --all --auto-eject\n" +
		"  tao merge --all --dry-run\n" +
		"  tao merge --verify-command \"go test ./...\" my-plan\n" +
		"  tao merge --no-verify 20260628-1618-kubectl-style-help\n" +
		"  tao merge --force my-plan",
	registerFlags: registerMergeFlags,
	completion: completionContext{
		flagValues: map[string]completionFlagValue{
			"verify-command": {kind: completionValueText, label: "command"},
		},
		positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs, disallowAfterFlags: []string{"--all"}},
	},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.merge(c.ctx, c.repo, c.args)
	},
}

func registerMergeFlags(fs *flag.FlagSet) {
	fs.Bool("all", false, "merge every reviewed and approved plan in one atomic batch")
	fs.Bool("dry-run", false, "preview batch candidates and order without durable changes")
	fs.Bool("restart", false, "discard safe pre-landing batch recovery state and start again")
	fs.Bool("auto-eject", false, "automatically eject an attributed non-converging plan and reland the rest")
	fs.Bool("force", false, "bypass pre-merge approval, review-base, and dirty-worktree gates, not conflict-resolution safety or independent review (single-plan only)")
	fs.Bool("record-only", false, "record an external merge and run cleanup (single-plan only)")
	fs.Bool("no-squash", false, "preserve plan commits with rebase-plus-fast-forward; conflicts remain manual (single-plan only)")
	fs.Bool("no-verify", false, "skip post-merge command verification, not structural validation or independent review (single-plan only)")
	fs.String("verify-command", "", "override the post-merge build/test verification command")
}

type mergeServiceRunner interface {
	Merge(ctx context.Context, detail *plan.PlanDetail, options mergepkg.Options) error
}

type mergeBatchOptions = mergepkg.BatchCoordinatorOptions

type mergeBatchResult = mergepkg.BatchCoordinatorResult

type mergeBatchRunner interface {
	Run(context.Context, mergeBatchOptions) (mergeBatchResult, error)
}

var newMergeBatchRunner = func(ctx context.Context, a App, repo mergepkg.BatchPlanRepository) (mergeBatchRunner, error) {
	return a.newMergeBatchRunner(ctx, repo)
}

var newMergeServiceRunner = func(a App, detail *plan.PlanDetail) (mergeServiceRunner, error) {
	return a.newMergeServiceRunner(detail)
}

func (a App) merge(ctx context.Context, repo plan.Resolver, args []string) error {
	if repo == nil {
		return errors.New("merge requires a plan repository")
	}
	fs, positional, err := a.parseArgs("merge", args, registerMergeFlags)
	if err != nil {
		return err
	}
	all := flagBoolValue(fs, "all")
	if all {
		if len(positional) != 0 {
			return errors.New("usage: tao merge --all [--dry-run] [--restart] [--auto-eject] [--verify-command CMD]")
		}
		for _, incompatible := range []string{"force", "record-only", "no-squash", "no-verify"} {
			if flagBoolValue(fs, incompatible) {
				return fmt.Errorf("--all cannot be combined with --%s", incompatible)
			}
		}
		batchRepo, ok := repo.(mergepkg.BatchPlanRepository)
		if !ok {
			return errors.New("merge --all requires a repository that can list and resolve plans")
		}
		runner, err := newMergeBatchRunner(ctx, a, batchRepo)
		if err != nil {
			return err
		}
		result, runErr := runner.Run(ctx, mergeBatchOptions{DryRun: flagBoolValue(fs, "dry-run"), Restart: flagBoolValue(fs, "restart"), AutoEject: flagBoolValue(fs, "auto-eject"), VerifyCommand: flagStringValue(fs, "verify-command")})
		if err := renderMergeBatchResult(a.Out, result); err != nil {
			return err
		}
		return runErr
	}
	if flagBoolValue(fs, "dry-run") || flagBoolValue(fs, "restart") || flagBoolValue(fs, "auto-eject") {
		return errors.New("--dry-run, --restart, and --auto-eject require --all")
	}
	if err := requirePositionals(positional, 1, "usage: tao merge [--force] [--record-only] [--no-squash] [--no-verify] [--verify-command CMD] <plan-id-or-slug-or-path>"); err != nil {
		return err
	}

	detail, err := repo.ResolvePlan(ctx, positional[0])
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("plan %q not found", positional[0])
	}
	options := mergepkg.Options{
		Force:         flagBoolValue(fs, "force"),
		NoVerify:      flagBoolValue(fs, "no-verify"),
		VerifyCommand: flagStringValue(fs, "verify-command"),
		RecordOnly:    flagBoolValue(fs, "record-only"),
		NoSquash:      flagBoolValue(fs, "no-squash"),
	}
	return runpkg.WithPlanRunLock(ctx, detail, a.now().UTC(), func(ownedCtx context.Context) error {
		// Reload by exact directory after acquisition so every merge gate and
		// mutation uses state protected from concurrent lifecycle drivers.
		refreshed, err := repo.ResolvePlan(ownedCtx, detail.Dir)
		if err != nil {
			return err
		}
		if refreshed == nil {
			return fmt.Errorf("plan %q not found", detail.Dir)
		}
		if err := plan.RequireNotAbandoned(refreshed); err != nil {
			return err
		}
		service, err := newMergeServiceRunner(a, refreshed)
		if err != nil {
			return err
		}
		if err := service.Merge(ownedCtx, refreshed, options); err != nil {
			if renderErr := renderMergeFailure(a.Out, refreshed, err); renderErr != nil {
				return renderErr
			}
			return err
		}
		return renderMergeSuccess(a.Out, refreshed)
	})
}

type mergeBatchRegistry interface {
	Current(context.Context) (taodata.Repo, error)
	MergeBatchesDir(taodata.Repo) string
	ActiveMergeBatchPath(taodata.Repo) string
}

func (a App) newMergeBatchRunner(ctx context.Context, repository mergepkg.BatchPlanRepository) (mergeBatchRunner, error) {
	runner := a.mergeRunner()
	var registry mergeBatchRegistry
	if a.Registry != nil {
		var ok bool
		registry, ok = a.Registry().(mergeBatchRegistry)
		if !ok {
			return nil, errors.New("merge --all requires merge-batch registry paths")
		}
	} else {
		defaultRegistry := taodata.NewRegistry("")
		defaultRegistry.Runner = runner
		registry = defaultRegistry
	}
	current, err := registry.Current(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve current repository for merge batch: %w", err)
	}
	batchesDir := registry.MergeBatchesDir(current)
	workspaceOwner, err := mergepkg.NewBatchWorkspace(current.Root, batchesDir, runner)
	if err != nil {
		return nil, err
	}
	service := mergepkg.NewService(current.Root, runner)
	service.Runner = runner
	store := mergepkg.NewBatchStore(batchesDir, registry.ActiveMergeBatchPath(current))
	agentConfig := newMergeBatchAgentConfig(a, current.Root, runner, store)
	session, err := mergepkg.NewBatchAgentSession(agentConfig)
	if err != nil {
		return nil, fmt.Errorf("configure merge-batch agent: %w", err)
	}
	generator, err := mergepkg.NewMergeProposalGenerator(agentConfig)
	if err != nil {
		return nil, fmt.Errorf("configure exceptional merge-batch proposal generator: %w", err)
	}
	service.ProposalGenerator = generator
	return mergepkg.NewBatchCoordinator(mergepkg.BatchCoordinatorSeams{
		Store:      store,
		Workspace:  workspaceOwner,
		Discovery:  mergepkg.BatchCandidateDiscovery{Repository: repository, Merge: service},
		Planner:    service,
		Integrator: mergepkg.BatchIntegrator{Store: store, Service: service, Now: a.Now},
		Resolver:   mergepkg.BatchAgentResolver{Store: store, Service: service, Agent: session, Now: a.Now},
		Reviewer:   mergepkg.BatchAggregateReviewer{Store: store, Service: service, Agent: session, Now: a.Now},
		Lander:     mergepkg.BatchLander{Store: store, Service: service, Repository: repository, Now: a.Now},
		Settler:    mergepkg.BatchSettler{Store: store, Service: service, Repository: repository, Workspace: workspaceOwner, Now: a.Now},
		Now:        a.Now,
	}), nil
}

func newMergeBatchAgentConfig(a App, controlRoot string, runner commandrunner.Runner, store *mergepkg.BatchStore) mergepkg.BatchAgentSessionConfig {
	return mergepkg.BatchAgentSessionConfig{
		ProcessStarter: a.ProcessStarter, Log: a.Out, ControlRoot: controlRoot, CommandRunner: runner,
		EventAppender: store, Now: a.Now,
	}
}

func renderMergeBatchResult(out io.Writer, result mergeBatchResult) error {
	if result.Restarted != nil {
		if err := writef(out, "Restarted merge batch %s: worktree=%t branch=%t recovery=%t\n", result.Restarted.BatchID, result.Restarted.RemoveWorktree, result.Restarted.RemoveBranch, result.Restarted.RemoveRecovery); err != nil {
			return err
		}
	}
	if result.Resumed {
		if err := writef(out, "Resuming merge batch %s from %s\n", result.State.ID, result.State.Status); err != nil {
			return err
		}
	}
	if len(result.Candidates) == 0 {
		if err := writeln(out, "Merge batch: no reviewed and approved plans; nothing to do."); err != nil {
			return err
		}
	} else {
		if err := writeln(out, "Candidate snapshot:"); err != nil {
			return err
		}
		for _, candidate := range result.Candidates {
			if err := writef(out, "- %s: %s@%s (review %s..%s)\n", candidate.PlanID, candidate.Branch, candidate.SourceTip, candidate.ReviewBase, candidate.ReviewHead); err != nil {
				return err
			}
		}
	}
	if len(result.State.ChosenOrder) != 0 {
		if err := writef(out, "Order: %s\n", strings.Join(result.State.ChosenOrder, " -> ")); err != nil {
			return err
		}
	}
	for _, blocker := range result.Blockers {
		if err := writef(out, "Blocked [%s] %s: %s\n", blocker.Stage, blocker.PlanID, blocker.Reason); err != nil {
			return err
		}
	}
	for _, deferred := range result.Deferred {
		if err := writef(out, "Deferred %s: %s\n", deferred.PlanID, deferred.Reason); err != nil {
			return err
		}
	}
	if ejection := result.State.Ejection; ejection != nil && ejection.Status == "completed" {
		if err := writef(out, "Ejected %s: %s\n", ejection.PlanID, ejection.Reason); err != nil {
			return err
		}
	}
	for _, integration := range result.State.Integrations {
		if len(integration.ConflictFiles) != 0 {
			if err := writef(out, "Conflict files for %s: %s\n", integration.PlanID, strings.Join(integration.ConflictFiles, ", ")); err != nil {
				return err
			}
		}
		if integration.VerificationOutput != "" {
			if err := writef(out, "Verification output for %s:\n%s\n", integration.PlanID, integration.VerificationOutput); err != nil {
				return err
			}
		}
	}
	if result.State.Verification != nil {
		if err := writef(out, "Verification: command=%s passed=%t\n", result.State.Verification.Command, result.State.Verification.Passed); err != nil {
			return err
		}
	}
	if result.State.Status == mergepkg.BatchStatusBlocked && result.State.NonConvergence != nil {
		nonConvergence := result.State.NonConvergence
		if err := writeln(out, "Aggregate review is not converging:"); err != nil {
			return err
		}
		if nonConvergence.PlanID != "" {
			if err := writef(out, "- Plan: %s\n", nonConvergence.PlanID); err != nil {
				return err
			}
		}
		for _, file := range nonConvergence.Files {
			if err := writef(out, "- File: %s\n", file); err != nil {
				return err
			}
		}
		if nonConvergence.Reason != "" {
			if err := writef(out, "- Reason: %s\n", nonConvergence.Reason); err != nil {
				return err
			}
		}
		switch {
		case mergepkg.BatchOperatorEjectAvailable(result.State):
			if err := writef(out, "Batch remains blocked. Rerun `tao merge --all` to eject %s, rebuild and verify the reduced batch, and reland the remaining plans. Use `--auto-eject` on the initial run to do this automatically.\n", nonConvergence.PlanID); err != nil {
				return err
			}
		case nonConvergence.PlanID != "":
			if err := writeln(out, "Batch remains blocked because this attributed plan is not eligible for operator ejection from the current batch; review the findings manually."); err != nil {
				return err
			}
		default:
			if err := writeln(out, "Batch remains blocked because the recurring files could not be attributed to one plan; review the findings manually."); err != nil {
				return err
			}
		}
	}
	for _, settlement := range result.State.Settlement {
		status := "pending"
		switch {
		case settlement.RequiresAttention:
			status = "requires attention"
		case settlement.Completed:
			status = "cleaned"
		case settlement.MergeEvidenceRecorded:
			status = "recorded; cleanup pending"
		}
		if settlement.Error != "" {
			status += ": " + settlement.Error
		}
		if err := writef(out, "Settlement %s: %s\n", settlement.PlanID, status); err != nil {
			return err
		}
	}
	if result.State.Status == mergepkg.BatchStatusCompleted {
		if err := writeln(out, "Merge batch settlement completed."); err != nil {
			return err
		}
	}
	if result.DryRun {
		if err := writeln(out, "Dry run: no batch state or integration changes were retained."); err != nil {
			return err
		}
	}
	if result.DefaultMoved {
		return writeln(out, "Default branch moved.")
	}
	return writeln(out, "Default branch has not moved.")
}

func (a App) newMergeServiceRunner(detail *plan.PlanDetail) (mergeServiceRunner, error) {
	if detail == nil {
		return nil, fmt.Errorf("plan detail is nil")
	}
	repoRoot := strings.TrimSpace(detail.State.Repo.Root)
	if repoRoot == "" {
		return nil, fmt.Errorf("plan %s does not record a repo root", mergePlanID(detail))
	}
	runner := a.mergeRunner()
	manager, err := a.mergeWorkspaceManager(repoRoot, runner)
	if err != nil {
		return nil, err
	}
	var logf func(format string, args ...any)
	if a.Out != nil {
		logf = func(format string, args ...any) {
			_ = writef(a.Out, format+"\n", args...)
		}
	}
	generator, err := mergepkg.NewMergeProposalGenerator(mergepkg.MergeProposalGeneratorConfig{
		ProcessStarter: a.ProcessStarter, Log: a.Out, ControlRoot: repoRoot, CommandRunner: runner,
	})
	if err != nil {
		return nil, fmt.Errorf("configure exceptional merge proposal generator: %w", err)
	}
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		return nil, fmt.Errorf("configure single-plan merge record: %w", err)
	}
	eventAppender := plan.NewFileRepository("")
	agentSession := mergepkg.NewFreshSingleMergeAgentSession(newSingleMergeAgentConfig(a, detail, repoRoot, runner, eventAppender))
	svc := mergepkg.NewService(repoRoot, runner)
	git := svc.Git
	svc.Runner = runner
	svc.Cleaner = manager
	svc.Logf = logf
	svc.Now = a.Now
	svc.ProposalGenerator = generator
	svc.SingleResolver = mergepkg.GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: agentSession, Now: a.Now}
	svc.SingleReviewer = mergepkg.GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: agentSession, Now: a.Now}
	return svc, nil
}

func newSingleMergeAgentConfig(a App, detail *plan.PlanDetail, controlRoot string, runner commandrunner.Runner, appender plan.EventAppender) mergepkg.SingleMergeAgentSessionConfig {
	return mergepkg.SingleMergeAgentSessionConfig{
		ProcessStarter: a.ProcessStarter, Log: a.Out, ControlRoot: controlRoot, CommandRunner: runner, Now: a.Now,
		Observe: func(request mergepkg.BatchAgentSessionRequest, result mergepkg.BatchAgentSessionResult, sessionErr error) {
			event := mergepkg.SingleMergeAgentMetricsEvent(request, result, sessionErr, a.now())
			if event == nil || appender == nil {
				return
			}
			if err := appender.AppendEvent(detail.Dir, *event); err != nil && a.Out != nil {
				_ = writef(a.Out, "tao telemetry warning: append %s event: %v\n", event.Type, err)
			}
		},
	}
}

func (a App) mergeRunner() commandrunner.Runner {
	if a.CommandRunner != nil {
		return a.CommandRunner
	}
	return commandrunner.DefaultLocal
}

func (a App) mergeWorkspaceManager(repoRoot string, runner commandrunner.Runner) (WorkspaceManager, error) {
	if a.WorkspaceManager != nil {
		return a.WorkspaceManager(repoRoot)
	}
	return workspace.NewManager(workspace.Options{RepoRoot: repoRoot, Runner: runner})
}

func renderMergeSuccess(out io.Writer, detail *plan.PlanDetail) error {
	planID := mergePlanID(detail)
	defaultBranch := mergeDefaultBranch(detail)
	planBranch := mergePlanBranch(detail)
	if review := singleMergeResolutionReview(detail); review != nil && review.IsApproved() {
		if err := writeln(out, "Independent review approved the exact conflict-resolved integration."); err != nil {
			return err
		}
	}
	if err := writef(out, "Merge completed: %s merged into %s\n", planID, defaultBranch); err != nil {
		return err
	}
	if planBranch != "" {
		return writef(out, "Cleanup completed: worktree/branch %s removed\n", planBranch)
	}
	return writeln(out, "Cleanup completed: plan worktree/branch removed")
}

func renderMergeFailure(out io.Writer, detail *plan.PlanDetail, err error) error {
	planID := mergePlanID(detail)
	switch {
	case errors.Is(err, mergepkg.ErrNotApproved):
		return renderMergeNotApproved(out, planID, err)
	case errors.Is(err, mergepkg.ErrReviewBaseMismatch):
		return renderMergeReviewBaseMismatch(out, planID, err)
	case errors.Is(err, mergepkg.ErrReviewHeadMismatch):
		return renderMergeReviewHeadMismatch(out, planID, err)
	case errors.Is(err, mergepkg.ErrDirtyWorktree):
		return renderMergeDirtyWorktree(out, detail, planID, err)
	case errors.Is(err, mergepkg.ErrSingleResolutionDrift):
		return renderMergeResolutionRecoveryRefusal(out, detail, planID, err)
	case errors.Is(err, mergepkg.ErrSingleResolutionRejected):
		return renderMergeResolutionRejected(out, detail, planID, err)
	case errors.Is(err, mergepkg.ErrSingleReviewNotApproved), errors.Is(err, mergepkg.ErrSingleReviewRejected):
		return renderMergeIndependentReviewFailure(out, detail, err)
	case errors.Is(err, mergepkg.ErrMergeConflict):
		return renderMergeConflict(out, detail, planID, err)
	case errors.Is(err, mergepkg.ErrVerifyFailed):
		return renderMergeVerifyFailed(out, detail, planID, err)
	case errors.Is(err, mergepkg.ErrCleanupDeclined):
		return renderMergeCleanupDeclined(out, planID, err)
	default:
		if err := writef(out, "Merge failed: %v\n", err); err != nil {
			return err
		}
		return writeln(out, "Next: inspect the repository state, fix the reported problem, then rerun `tao merge`.")
	}
}

func renderMergeNotApproved(out io.Writer, planID string, err error) error {
	if err := writef(out, "Merge refused: %v\n", err); err != nil {
		return err
	}
	return writef(out, "Next: review the plan with `tao review %s` or `tao review --run %s`; merge only after an approved review, or pass --force to bypass the gate.\n", planID, planID)
}

func renderMergeReviewBaseMismatch(out io.Writer, planID string, err error) error {
	var mismatch *mergepkg.ReviewBaseMismatchError
	if !errors.As(err, &mismatch) {
		return renderMergeGenericRefusal(out, planID, err, "refresh the review, or pass --force if you accept the drift")
	}
	if err := writeln(out, "Merge refused: review base mismatch"); err != nil {
		return err
	}
	lines := []string{
		"Review Base: " + emptyMergeField(mismatch.ReviewBase),
		"Merge Base: " + emptyMergeField(mismatch.MergeBase),
		"Default Branch: " + emptyMergeField(mismatch.DefaultBranch),
		"Plan Branch: " + emptyMergeField(mismatch.PlanBranch),
	}
	if err := writeLines(out, lines...); err != nil {
		return err
	}
	return writef(out, "Next: bring the plan branch up to date and rerun `tao review --run %s`, or pass --force if you intentionally accept this base drift.\n", planID)
}

func renderMergeReviewHeadMismatch(out io.Writer, planID string, err error) error {
	mismatch, ok := errors.AsType[*mergepkg.ReviewHeadMismatchError](err)
	if !ok {
		return renderMergeGenericRefusal(out, planID, err, "refresh the review so it covers the branch tip, or pass --force if you accept merging unreviewed commits")
	}
	if err := writeln(out, "Merge refused: plan branch has commits the review did not cover"); err != nil {
		return err
	}
	lines := []string{
		"Review Head: " + emptyMergeField(mismatch.ReviewHead),
		"Branch Tip: " + emptyMergeField(mismatch.BranchTip),
		"Plan Branch: " + emptyMergeField(mismatch.PlanBranch),
	}
	if err := writeLines(out, lines...); err != nil {
		return err
	}
	return writef(out, "Next: rerun `tao review --run %s` so the review covers the branch tip, or pass --force if you intentionally merge the unreviewed commits.\n", planID)
}

func renderMergeDirtyWorktree(out io.Writer, detail *plan.PlanDetail, planID string, err error) error {
	if err := writeln(out, "Merge refused: worktree is dirty"); err != nil {
		return err
	}
	if dirty, ok := errors.AsType[*mergepkg.DirtyWorktreeError](err); ok {
		if err := renderIndentedBlock(out, "Status:", dirty.Status); err != nil {
			return err
		}
	}
	repoRoot := strings.TrimSpace(detail.State.Repo.Root)
	if repoRoot == "" {
		repoRoot = "the repository"
	}
	return writef(out, "Next: commit, stash, or discard changes in %s, then rerun `tao merge %s`; pass --force only if you intentionally bypass the dirty-worktree gate.\n", repoRoot, planID)
}

func renderMergeResolutionRejected(out io.Writer, detail *plan.PlanDetail, planID string, err error) error {
	if startup, ok := errors.AsType[*mergepkg.SingleResolutionStartupError](err); ok {
		heading := "Automatic squash conflict resolution failed during startup"
		if errors.Is(err, mergepkg.ErrSingleResolutionPreflight) {
			heading = "Automatic squash conflict resolution could not start safely"
		}
		if err := writeln(out, heading); err != nil {
			return err
		}
		if err := writef(out, "Failed capability: %s\n", strings.ReplaceAll(string(startup.Capability), "_", " ")); err != nil {
			return err
		}
		if err := writef(out, "Reason: %v\n", startup.Cause); err != nil {
			return err
		}
		switch startup.Authority {
		case mergepkg.SingleResolutionAuthorityPreserved:
			if err := writef(out, "One-shot authority preserved: Tao sent no attributed prompt, recorded no one-shot resolution request, and restored %s to its pre-merge boundary when possible.\n", mergeDefaultBranch(detail)); err != nil {
				return err
			}
			return writef(out, "Next: run `tao doctor`, fix the reported %s capability, then explicitly rerun `tao merge %s`; Tao will not retry automatically.\n", strings.ReplaceAll(string(startup.Capability), "_", " "), planID)
		case mergepkg.SingleResolutionAuthorityRearmed:
			if err := writef(out, "One-shot authority rearmed: structured %s proof showed no prompt was accepted, and Tao restored the exact default/source refs, HEAD, branch, and clean worktree boundary.\n", startup.PromptAcceptance); err != nil {
				return err
			}
			return writef(out, "Next: fix the reported %s capability, then explicitly rerun `tao merge %s`; Tao will not retry automatically.\n", strings.ReplaceAll(string(startup.Capability), "_", " "), planID)
		default:
			if err := writeln(out, "One-shot authority consumed: prompt acceptance was accepted or ambiguous, or exact rollback/rearm settlement could not be proven."); err != nil {
				return err
			}
			return writef(out, "Next: inspect %s and %s, then reconcile the recorded transaction manually; do not rerun the resolver and do not use --force as retry authority.\n", mergeDefaultBranch(detail), emptyMergeField(mergePlanBranch(detail)))
		}
	}
	if errors.Is(err, mergepkg.ErrSingleResolutionPreflight) {
		if err := writeln(out, "Automatic squash conflict resolution could not start safely"); err != nil {
			return err
		}
		if err := writef(out, "Reason: %v\n", err); err != nil {
			return err
		}
		if err := writef(out, "Tao sent no attributed prompt, recorded no one-shot resolution request, and restored %s to its pre-merge boundary when possible.\n", mergeDefaultBranch(detail)); err != nil {
			return err
		}
		return writef(out, "Next: run `tao doctor`, fix the reported capability, then explicitly rerun `tao merge %s`; Tao will not retry automatically.\n", planID)
	}
	heading := "Automatic squash conflict resolution failed"
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unsafe") || strings.Contains(message, "unresolved") || strings.Contains(message, "conflict marker") || strings.Contains(message, "no content or path changes") {
		heading = "Automatic squash conflict resolution rejected unsafe or unresolved edits"
	}
	if err := writeln(out, heading); err != nil {
		return err
	}
	if err := writef(out, "Reason: %v\n", err); err != nil {
		return err
	}
	if err := writef(out, "Tao discarded resolver edits and restored %s to its recorded pre-merge boundary when possible.\n", mergeDefaultBranch(detail)); err != nil {
		return err
	}
	if strings.Contains(message, "restore durable parent") || strings.Contains(message, "rollback") {
		if err := writeln(out, "Rollback warning: automatic restoration reported a problem; inspect the default worktree and protected refs before any retry."); err != nil {
			return err
		}
	}
	return writef(out, "Next: resolve the source interaction manually on %s, refresh its source review, then start a new `tao merge %s`; automatic resolution is limited to one attempt and --force cannot bypass its safety checks.\n", emptyMergeField(mergePlanBranch(detail)), planID)
}

func renderMergeResolutionRecoveryRefusal(out io.Writer, detail *plan.PlanDetail, planID string, err error) error {
	if err := writeln(out, "Automatic conflict-resolution recovery refused"); err != nil {
		return err
	}
	if err := writef(out, "Reason: %v\n", err); err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(err.Error()), "rollback refused") || strings.Contains(strings.ToLower(err.Error()), "no longer matches") {
		if err := writeln(out, "Rollback warning: Tao left the current Git state untouched because it no longer matched the exact recorded transaction."); err != nil {
			return err
		}
	}
	return writef(out, "Next: inspect %s, %s, and the recorded default/source SHAs; restore the exact recorded boundary or reconcile the transaction manually before rerunning `tao merge %s`.\n", mergeDefaultBranch(detail), emptyMergeField(mergePlanBranch(detail)), planID)
}

func renderMergeIndependentReviewFailure(out io.Writer, detail *plan.PlanDetail, err error) error {
	review := singleMergeResolutionReview(detail)
	heading := "Independent integration review failed"
	if review != nil {
		switch {
		case review.Verdict == plan.ReviewVerdictChangesRequested:
			heading = "Independent integration review requested changes"
		case strings.HasPrefix(review.Summary, "Independent integration review provider failed"):
			heading = "Independent integration review provider failed"
		case strings.HasPrefix(review.Summary, "Independent integration review timed out"):
			heading = "Independent integration review timed out"
		case strings.HasPrefix(review.Summary, "Independent integration reviewer returned malformed"):
			heading = "Independent integration review returned malformed output"
		case review.Verdict == plan.ReviewVerdictComment:
			heading = "Independent integration review returned comment"
		}
	}
	if err := writeln(out, heading); err != nil {
		return err
	}
	if review != nil {
		if summary := strings.TrimSpace(review.Summary); summary != "" {
			if err := writef(out, "Summary: %s\n", summary); err != nil {
				return err
			}
		}
		if len(review.Findings) > 0 {
			if err := writeln(out, "Findings:"); err != nil {
				return err
			}
			for _, finding := range review.Findings {
				if err := writef(out, "- %s: %s\n", emptyMergeField(finding.File), strings.TrimSpace(finding.Message)); err != nil {
					return err
				}
			}
		}
	} else if err := writef(out, "Reason: %v\n", err); err != nil {
		return err
	}
	if err := writef(out, "Tao did not accept the integration and restored %s to its recorded pre-merge SHA when the exact rollback boundary still matched.\n", mergeDefaultBranch(detail)); err != nil {
		return err
	}
	if strings.Contains(strings.ToLower(err.Error()), "automatic rollback refused") {
		if err := writeln(out, "Rollback warning: Git no longer matched the exact resolved transaction, so Tao refused to overwrite it."); err != nil {
			return err
		}
	}
	return writef(out, "Next: inspect the recorded one-shot review and reconcile its findings or provider failure on %s; automatic review is not retried, and neither --force nor --no-verify can authorize a non-approval.\n", emptyMergeField(mergePlanBranch(detail)))
}

func singleMergeResolutionReview(detail *plan.PlanDetail) *plan.SingleMergeResolutionReview {
	resolution := singleMergeResolution(detail)
	if resolution == nil {
		return nil
	}
	return resolution.Review
}

func singleMergeResolution(detail *plan.PlanDetail) *plan.SingleMergeResolution {
	return plan.CurrentSingleMergeResolution(detail)
}

func renderMergeConflict(out io.Writer, detail *plan.PlanDetail, planID string, err error) error {
	var conflict *mergepkg.MergeConflictError
	phase := "integration"
	if errors.As(err, &conflict) && strings.TrimSpace(conflict.Phase) != "" {
		phase = strings.TrimSpace(conflict.Phase)
	}
	if err := writef(out, "Merge conflict during %s\n", phase); err != nil {
		return err
	}
	if conflict != nil && len(conflict.Files) > 0 {
		if err := writeln(out, "Files:"); err != nil {
			return err
		}
		for _, file := range conflict.Files {
			if err := writef(out, "- %s\n", file); err != nil {
				return err
			}
		}
	} else if err := writeln(out, "Files: not reported by git"); err != nil {
		return err
	}
	if conflict != nil && len(conflict.CleanupErrors) > 0 {
		if err := writeln(out, "Rollback warnings:"); err != nil {
			return err
		}
		for _, cleanupErr := range conflict.CleanupErrors {
			if cleanupErr == nil {
				continue
			}
			if err := writef(out, "- %v\n", cleanupErr); err != nil {
				return err
			}
		}
	}
	if err := writeln(out, "Tao aborted the merge attempt and restored the default branch tip when possible."); err != nil {
		return err
	}
	return writef(out, "Next: resolve conflicts on %s manually, refresh review if content changes, then rerun `tao merge %s`.\n", emptyMergeField(mergePlanBranch(detail)), planID)
}

func renderMergeVerifyFailed(out io.Writer, detail *plan.PlanDetail, planID string, err error) error {
	if err := writeln(out, "Verification failed after merge"); err != nil {
		return err
	}
	if verify, ok := errors.AsType[*mergepkg.VerifyFailedError](err); ok {
		if command := strings.TrimSpace(verify.Command); command != "" {
			if err := writef(out, "Command: %s\n", command); err != nil {
				return err
			}
		}
		if repoRoot := strings.TrimSpace(verify.RepoRoot); repoRoot != "" {
			if err := writef(out, "Repo: %s\n", repoRoot); err != nil {
				return err
			}
		}
		if err := renderIndentedBlock(out, "Output:", verify.Output); err != nil {
			return err
		}
	}
	if err := writef(out, "Tao reset %s to its pre-merge SHA when possible.\n", mergeDefaultBranch(detail)); err != nil {
		return err
	}
	if singleMergeResolution(detail) != nil {
		if err := writeln(out, "The exact conflict-resolved integration was not independently approved, recorded, or cleaned up."); err != nil {
			return err
		}
		return writef(out, "Next: reproduce and fix verification on %s, then reconcile the recorded one-shot transaction before rerunning `tao merge %s`; --no-verify skips only the command and cannot bypass structural validation or independent review.\n", emptyMergeField(mergePlanBranch(detail)), planID)
	}
	return writef(out, "Next: fix verification failures on %s and rerun `tao merge %s`, or pass --no-verify only if you intentionally skip this check.\n", emptyMergeField(mergePlanBranch(detail)), planID)
}

func renderMergeCleanupDeclined(out io.Writer, planID string, err error) error {
	if err := writef(out, "Merge requires cleanup attention: %v\n", err); err != nil {
		return err
	}
	return writef(out, "Next: inspect cleanup with `tao workspace clean %s` or `tao cleanup --dry-run`; rerun with --force only after reviewing cleanup safety.\n", planID)
}

func renderMergeGenericRefusal(out io.Writer, planID string, err error, guidance string) error {
	if err := writef(out, "Merge refused: %v\n", err); err != nil {
		return err
	}
	return writef(out, "Next: %s for %s.\n", guidance, planID)
}

func renderIndentedBlock(out io.Writer, heading string, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if err := writeln(out, heading); err != nil {
		return err
	}
	for line := range strings.SplitSeq(value, "\n") {
		if err := writef(out, "  %s\n", line); err != nil {
			return err
		}
	}
	return nil
}

func mergePlanID(detail *plan.PlanDetail) string {
	if detail == nil {
		return "plan"
	}
	if id := strings.TrimSpace(detail.State.Plan.ID); id != "" {
		return id
	}
	return "plan"
}

func mergeDefaultBranch(detail *plan.PlanDetail) string {
	if detail != nil && detail.State.Workspace != nil {
		if branch := strings.TrimSpace(detail.State.Workspace.BaseBranch); branch != "" {
			return branch
		}
	}
	if detail != nil {
		if branch := strings.TrimSpace(detail.State.Repo.Branch); branch != "" {
			return branch
		}
	}
	return "default branch"
}

func mergePlanBranch(detail *plan.PlanDetail) string {
	if detail == nil || detail.State.Workspace == nil {
		return ""
	}
	return strings.TrimSpace(detail.State.Workspace.Branch)
}

func emptyMergeField(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(unknown)"
	}
	return value
}
