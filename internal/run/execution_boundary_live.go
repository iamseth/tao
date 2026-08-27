package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

func selectedRunSlice(detail *plan.PlanDetail) *plan.Slice {
	if detail == nil {
		return nil
	}
	if detail.State.Plan.CurrentSlice != nil {
		if slice := interruptedSlice(detail, *detail.State.Plan.CurrentSlice); slice != nil {
			return slice
		}
	}
	return plan.Derive(detail, time.Time{}).NextSlice
}

// InspectSelected collects the live facts needed to classify the selected
// slice. It does not prepare or mutate a workspace. Durable completion intent
// is classified before Git inspection because that transaction must be settled
// without consulting or handing off to the implementation workspace.
func (controller ExecutionBoundaryController) InspectSelected(ctx context.Context, durable ExecutionBoundaryDurableFacts, execution runExecution) (*ExecutionBoundaryAction, error) {
	detail := durable.Detail
	slice := selectedRunSlice(detail)
	if slice == nil {
		return nil, nil
	}
	durable.SliceID = slice.ID

	if slice.Status == plan.StatusPending && detail.State.Plan.CurrentSlice == nil && slice.ExecutionStart == nil && slice.CommitIntent == nil && slice.Completion == nil {
		action := controller.Classify(durable, ExecutionBoundaryLiveFacts{})
		if err := inspectRecordedWorkspaceBeforeAutomaticStart(ctx, detail, execution); err != nil {
			return nil, err
		}
		return &action, nil
	}

	root := strings.TrimSpace(slice.ExecutionRoot)
	if root == "" && stateAdvancedSliceStart(detail, slice) {
		preparedRoot, err := preparedInterruptedExecutionRoot(detail, execution.Config)
		if err != nil {
			return nil, fmt.Errorf("inspect interrupted slice prepared root: %w", err)
		}
		root = preparedRoot
	}
	live := ExecutionBoundaryLiveFacts{ExecutionRoot: root}
	if root == "" {
		action := controller.Classify(durable, live)
		if action.Disposition == InterruptedSliceBlockedContinue && action.EffectiveDisposition == InterruptedSliceNewStart {
			if err := inspectRecordedWorkspaceBeforeAutomaticStart(ctx, detail, execution); err != nil {
				return nil, err
			}
			return &action, nil
		}
		return &action, nil
	}

	// Completion ownership is entirely durable. Do not touch Git or any
	// workspace collaborator once intent or completion metadata exists.
	if slice.CommitIntent != nil || slice.Completion != nil {
		action := controller.Classify(durable, live)
		return &action, nil
	}

	_, recordedStrategy, _ := effectiveInterruptedBoundary(detail, slice)
	if recordedStrategy == plan.WorkspaceStrategyWorktree {
		if reason := interruptedWorktreeIdentityError(detail, root); reason != "" {
			action := refuseExecutionBoundary(controller, durable, live, reason)
			return &action, nil
		}
		if err := inspectLinkedWorktreeIdentity(ctx, detail, root, execution.Dependencies.CommandRunner); err != nil {
			action := refuseExecutionBoundary(controller, durable, live, err.Error())
			return &action, nil
		}
	}

	git := gitClient(execution, root)
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect interrupted slice branch: %w", err)
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("inspect interrupted slice HEAD: %w", err)
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect interrupted slice worktree: %w", err)
	}
	active, err := gitops.ActiveOperation(root)
	if err != nil {
		return nil, fmt.Errorf("inspect interrupted slice Git state: %w", err)
	}
	strategy := plan.WorkspaceStrategyWorktree
	if execution.Config.ExecutionMode == ExecutionModeCurrent {
		strategy = plan.WorkspaceStrategyCurrent
	}
	live.WorkspaceStrategy = strategy
	live.CommitPolicy = execution.Config.CommitPolicy.String()
	live.Branch = branch
	live.Head = head
	live.PorcelainStatus = status
	live.ActiveGitOperation = active
	if durable.RestartBlocked && slice.ExecutionStart != nil {
		baselineBranch, baselineErr := resolvePreparationBaseBranch(ctx, detail, execution.Config, execution.Dependencies.CommandRunner)
		if baselineErr != nil {
			return nil, fmt.Errorf("inspect blocked restart baseline: %w", baselineErr)
		}
		baselineHead, baselineErr := gitClient(execution, detail.State.Repo.Root).RevParse(ctx, baselineBranch)
		if baselineErr != nil {
			return nil, fmt.Errorf("inspect blocked restart baseline %q: %w", baselineBranch, baselineErr)
		}
		ancestor, ancestorErr := git.IsAncestor(ctx, slice.ExecutionStart.Head, baselineHead)
		if ancestorErr != nil {
			return nil, fmt.Errorf("prove blocked restart baseline ancestry: %w", ancestorErr)
		}
		live.BaselineBranch = baselineBranch
		live.BaselineHead = baselineHead
		live.BoundaryAncestor = ancestor
		live.AncestryKnown = true
	}
	action := controller.Classify(durable, live)
	if (action.EffectiveDisposition == InterruptedSliceResume || action.EffectiveDisposition == InterruptedSliceCleanStartRepair) && len(action.StartingDirtyPaths) > 0 {
		action = refuseExecutionBoundary(controller, durable, live, "recorded automatic clean-start metadata contains dirty paths")
	}
	return &action, nil
}

func refuseExecutionBoundary(controller ExecutionBoundaryController, durable ExecutionBoundaryDurableFacts, live ExecutionBoundaryLiveFacts, reason string) ExecutionBoundaryAction {
	action := controller.Classify(durable, live)
	action.Disposition = InterruptedSliceRefuse
	action.EffectiveDisposition = InterruptedSliceRefuse
	action.FixedRoot = ""
	action.Diagnostics.Reason = reason
	action.RepairRequirement = ExecutionBoundaryRepairNone
	action.AllowWorkspacePreparation = false
	action.AllowAgentHandoff = false
	return action
}

func inspectLinkedWorktreeIdentity(ctx context.Context, detail *plan.PlanDetail, sliceRoot string, runner CommandRunner) error {
	physicalRoot, err := workspace.PhysicalPath(sliceRoot)
	if err != nil {
		return fmt.Errorf("resolve interrupted worktree path: %w", err)
	}
	physicalRepo, err := workspace.PhysicalPath(detail.State.Repo.Root)
	if err != nil {
		return fmt.Errorf("resolve recorded repository path: %w", err)
	}
	if physicalRoot == physicalRepo {
		return fmt.Errorf("interrupted automatic slice worktree physically resolves to the repository checkout")
	}

	worktreeGit := gitops.NewClient(sliceRoot, runner)
	worktreeTop, err := worktreeGit.TopLevel(ctx)
	if err != nil {
		return fmt.Errorf("resolve interrupted worktree Git top-level: %w", err)
	}
	physicalTop, err := workspace.PhysicalPath(worktreeTop)
	if err != nil {
		return fmt.Errorf("resolve interrupted worktree Git top-level path: %w", err)
	}
	if physicalTop != physicalRoot {
		return fmt.Errorf("interrupted automatic slice Git top-level does not match the recorded execution root")
	}

	repoGit := gitops.NewClient(detail.State.Repo.Root, runner)
	repoTop, err := repoGit.TopLevel(ctx)
	if err != nil {
		return fmt.Errorf("resolve recorded repository Git top-level: %w", err)
	}
	physicalRepoTop, err := workspace.PhysicalPath(repoTop)
	if err != nil {
		return fmt.Errorf("resolve recorded repository Git top-level path: %w", err)
	}
	if physicalRepoTop != physicalRepo {
		return fmt.Errorf("recorded repository root is not its Git top-level")
	}

	worktreeCommon, err := physicalGitMetadataPath(ctx, worktreeGit, sliceRoot, "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve interrupted worktree common Git directory: %w", err)
	}
	repoCommon, err := physicalGitMetadataPath(ctx, repoGit, detail.State.Repo.Root, "--git-common-dir")
	if err != nil {
		return fmt.Errorf("resolve recorded repository common Git directory: %w", err)
	}
	if worktreeCommon != repoCommon {
		return fmt.Errorf("interrupted automatic slice is not a worktree of the recorded repository")
	}
	worktreeGitDir, err := physicalGitMetadataPath(ctx, worktreeGit, sliceRoot, "--git-dir")
	if err != nil {
		return fmt.Errorf("resolve interrupted worktree Git directory: %w", err)
	}
	linked, err := gitops.IsLinkedWorktreeDirectory(worktreeCommon, worktreeGitDir)
	if err != nil {
		return fmt.Errorf("inspect interrupted linked worktree: %w", err)
	}
	if !linked {
		return fmt.Errorf("interrupted automatic slice is not a linked worktree")
	}
	return nil
}

func physicalGitMetadataPath(ctx context.Context, git gitops.Client, root string, revParseArg string) (string, error) {
	path, err := git.RevParse(ctx, revParseArg)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	return workspace.PhysicalPath(path)
}

func inspectRecordedWorkspaceBeforeAutomaticStart(ctx context.Context, detail *plan.PlanDetail, execution runExecution) error {
	if execution.Config.ExecutionMode != ExecutionModeIsolated || detail == nil {
		return nil
	}
	if detail.State.Workspace == nil {
		if execution.Config.CommitPolicy != CommitPolicySlice {
			return nil
		}
		return inspectUnrecordedDefaultWorkspaceBeforeAutomaticStart(ctx, detail, execution)
	}
	workspaceState := detail.State.Workspace
	if !recordedAutomaticWorktree(workspaceState) {
		return nil
	}
	hasRebaseIntent := workspaceState.RebaseIntent != nil
	if !hasRebaseIntent && execution.Config.CommitPolicy != CommitPolicySlice {
		return nil
	}
	root := workspace.ResolveRecordedWorktree(detail).Path
	if workspaceState.LifecycleStatus == plan.WorkspaceStatusCleaned && !hasRebaseIntent {
		return nil
	}
	if workspaceState.LifecycleStatus != plan.WorkspaceStatusReady {
		if _, err := os.Stat(root); err != nil {
			if os.IsNotExist(err) && !hasRebaseIntent {
				return nil
			}
			return fmt.Errorf("inspect recorded workspace before automatic slice: %w", err)
		}
	}
	label := workspaceState.LifecycleStatus + " workspace"
	if workspaceState.LifecycleStatus == "" || strings.TrimSpace(workspaceState.Strategy) == "" {
		label = "legacy workspace"
	}
	if reason := recordedWorktreeIdentityError(detail, root); reason != "" {
		return fmt.Errorf("inspect %s before automatic slice: %s", label, reason)
	}
	if workspaceState.Branch == "" || workspaceState.HeadSHA == "" {
		return fmt.Errorf("inspect %s before automatic slice: durable workspace metadata is missing branch or HEAD", label)
	}
	if err := inspectLinkedWorktreeIdentity(ctx, detail, root, execution.Dependencies.CommandRunner); err != nil {
		return fmt.Errorf("inspect %s before automatic slice identity: %w", label, err)
	}

	git := gitClient(execution, root)
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("inspect %s branch before automatic slice: %w", label, err)
	}
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return fmt.Errorf("inspect %s HEAD before automatic slice: %w", label, err)
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect %s status before automatic slice: %w", label, err)
	}
	active, err := gitops.ActiveOperation(root)
	if err != nil {
		return fmt.Errorf("inspect %s Git state before automatic slice: %w", label, err)
	}
	facts := interruptedSliceFacts(InterruptedSliceInput{PorcelainStatus: status, ActiveGitOperation: active})
	if hasRebaseIntent {
		return recoverWorkspaceRebaseIntent(ctx, detail, execution, git, label, branch, head, facts)
	}
	switch {
	case facts.ActiveGitOperation != "":
		return fmt.Errorf("automatic slice refused before workspace preparation: Git operation %q is active in the %s", facts.ActiveGitOperation, label)
	case facts.AmbiguousStatus:
		return fmt.Errorf("automatic slice refused before workspace preparation: %s git status contains an ambiguous entry", label)
	case facts.Conflicted:
		return fmt.Errorf("automatic slice refused before workspace preparation: %s contains conflicted entries", label)
	case facts.Dirty:
		return fmt.Errorf("automatic slice refused before workspace preparation: %s contains unattributed changes (%s)", label, strings.Join(facts.ChangedPaths, ", "))
	case branch == "" || gitops.ProtectedBranch(branch):
		return fmt.Errorf("automatic slice refused before workspace preparation: unsafe %s branch %q", label, branch)
	case matchesWorkspaceBoundary(workspaceState, branch, head):
		return nil
	case completedAutomaticSliceProvesBoundary(detail, branch, head):
		record, err := planMutationRecord(execution, detail)
		if err != nil {
			return fmt.Errorf("migrate stale %s boundary: %w", label, err)
		}
		if err := record.AdvanceWorkspaceHead(branch, workspaceState.HeadSHA, head); err != nil {
			return fmt.Errorf("migrate stale %s boundary: %w", label, err)
		}
		return nil
	default:
		return fmt.Errorf("automatic slice refused before workspace preparation: %s differs from durable workspace metadata: durable branch %q HEAD %s; live branch %q HEAD %s. Run tao workspace status %s, reconcile the worktree manually, then retry", label, workspaceState.Branch, diagnosticSHA(workspaceState.HeadSHA), branch, diagnosticSHA(head), detail.State.Plan.ID)
	}
}

func recoverWorkspaceRebaseIntent(ctx context.Context, detail *plan.PlanDetail, execution runExecution, git gitops.Client, label, branch, head string, facts InterruptedSliceFacts) error {
	intent := *detail.State.Workspace.RebaseIntent
	refuse := func(reason string) error {
		return fmt.Errorf("automatic rebase recovery refused for %s: %s; durable branch %q old HEAD %s new base %s; live branch %q HEAD %s. Run tao workspace status %s, recover the worktree manually, then retry", label, reason, intent.Branch, diagnosticSHA(intent.OldHeadSHA), diagnosticSHA(intent.NewBaseSHA), branch, diagnosticSHA(head), detail.State.Plan.ID)
	}
	switch {
	case facts.ActiveGitOperation != "":
		return refuse(fmt.Sprintf("Git operation %q is active", facts.ActiveGitOperation))
	case facts.AmbiguousStatus:
		return refuse("git status contains an ambiguous entry")
	case facts.Conflicted:
		return refuse("worktree contains conflicted entries")
	case facts.Dirty:
		return refuse("worktree contains unattributed changes (" + strings.Join(facts.ChangedPaths, ", ") + ")")
	case branch != intent.Branch:
		return refuse(fmt.Sprintf("live branch %q does not match the recorded rebase branch %q", branch, intent.Branch))
	}
	if head == intent.OldHeadSHA {
		proof, err := git.CommitSeriesRebaseProof(ctx, intent.OldBaseSHA, intent.NewBaseSHA, intent.OldBaseSHA, head)
		if err != nil {
			return refuse(fmt.Sprintf("the untouched pre-rebase commit series is inaccessible or unsupported: %v", err))
		}
		if proof.Count != intent.CommitCount || proof.Fingerprint != intent.CommitSeriesFingerprint {
			return refuse(fmt.Sprintf("untouched pre-rebase commit-series proof differs from intent (got count %d fingerprint %s, want count %d fingerprint %s)", proof.Count, proof.Fingerprint, intent.CommitCount, intent.CommitSeriesFingerprint))
		}
		rebaseRecord, err := workspaceRebaseMutationRecord(execution, detail)
		if err != nil {
			return fmt.Errorf("clear untouched workspace rebase intent: %w", err)
		}
		if err := rebaseRecord.ClearWorkspaceRebaseIntent(intent); err != nil {
			return fmt.Errorf("clear untouched workspace rebase intent; intent remains durable: %w", err)
		}
		return nil
	}
	proof, err := git.CommitSeriesRebaseProof(ctx, intent.OldBaseSHA, intent.NewBaseSHA, intent.NewBaseSHA, head)
	if err != nil {
		return refuse(fmt.Sprintf("the live commit series from the recorded new base is inaccessible or unsupported: %v", err))
	}
	if proof.Count != intent.CommitCount || proof.Fingerprint != intent.CommitSeriesFingerprint {
		return refuse(fmt.Sprintf("live commit-series proof differs from intent (got count %d fingerprint %s, want count %d fingerprint %s)", proof.Count, proof.Fingerprint, intent.CommitCount, intent.CommitSeriesFingerprint))
	}
	rebaseRecord, err := workspaceRebaseMutationRecord(execution, detail)
	if err != nil {
		return fmt.Errorf("settle recovered workspace rebase: %w", err)
	}
	status := detail.State.Workspace.LifecycleStatus
	if status == "" {
		status = plan.WorkspaceStatusPreparing
	}
	settlement := plan.WorkspaceRebaseSettlement{
		Branch: branch, BaseSHA: intent.NewBaseSHA, BaseCurrentSHA: intent.NewBaseSHA, HeadSHA: head,
		BaseStatus: "current", RefreshStatus: "not_needed", RebaseStatus: "not_needed", LifecycleStatus: status,
	}
	if err := rebaseRecord.SettleWorkspaceRebase(intent, settlement); err != nil {
		return fmt.Errorf("settle recovered workspace rebase; intent remains durable: %w", err)
	}
	return nil
}

type workspaceRebaseRecord interface {
	ClearWorkspaceRebaseIntent(plan.WorkspaceRebaseIntent) error
	SettleWorkspaceRebase(plan.WorkspaceRebaseIntent, plan.WorkspaceRebaseSettlement) error
}

func workspaceRebaseMutationRecord(execution runExecution, detail *plan.PlanDetail) (workspaceRebaseRecord, error) {
	record, err := planMutationRecord(execution, detail)
	if err != nil {
		return nil, err
	}
	rebaseRecord, ok := record.(workspaceRebaseRecord)
	if !ok {
		return nil, fmt.Errorf("plan record does not support workspace rebase transactions")
	}
	return rebaseRecord, nil
}

func diagnosticSHA(sha string) string {
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	return fmt.Sprintf("%s (short %s)", sha, short)
}

func inspectUnrecordedDefaultWorkspaceBeforeAutomaticStart(ctx context.Context, detail *plan.PlanDetail, execution runExecution) error {
	config := workspace.DefaultConfig()
	root := config.Root
	if !filepath.IsAbs(root) {
		root = filepath.Join(detail.State.Repo.Root, root)
	}
	root = filepath.Clean(filepath.Join(root, detail.State.Plan.ID))
	if _, err := os.Stat(root); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect unrecorded managed workspace before automatic slice: %w", err)
	}
	if err := inspectLinkedWorktreeIdentity(ctx, detail, root, execution.Dependencies.CommandRunner); err != nil {
		return fmt.Errorf("inspect unrecorded managed workspace before automatic slice identity: %w", err)
	}

	git := gitClient(execution, root)
	branch, err := git.CurrentBranch(ctx)
	if err != nil {
		return fmt.Errorf("inspect unrecorded managed workspace branch before automatic slice: %w", err)
	}
	status, err := git.StatusPorcelain(ctx)
	if err != nil {
		return fmt.Errorf("inspect unrecorded managed workspace status before automatic slice: %w", err)
	}
	active, err := gitops.ActiveOperation(root)
	if err != nil {
		return fmt.Errorf("inspect unrecorded managed workspace Git state before automatic slice: %w", err)
	}
	facts := interruptedSliceFacts(InterruptedSliceInput{PorcelainStatus: status, ActiveGitOperation: active})
	switch {
	case facts.ActiveGitOperation != "":
		return fmt.Errorf("automatic slice refused before workspace preparation: Git operation %q is active in the unrecorded managed workspace", facts.ActiveGitOperation)
	case facts.AmbiguousStatus:
		return fmt.Errorf("automatic slice refused before workspace preparation: unrecorded managed workspace git status contains an ambiguous entry")
	case facts.Conflicted:
		return fmt.Errorf("automatic slice refused before workspace preparation: unrecorded managed workspace contains conflicted entries")
	case facts.Dirty:
		return fmt.Errorf("automatic slice refused before workspace preparation: unrecorded managed workspace contains unattributed changes (%s)", strings.Join(facts.ChangedPaths, ", "))
	}
	identity, err := workspace.ResolvePlanBranch(detail, config)
	if err != nil {
		return fmt.Errorf("resolve unrecorded managed workspace branch before automatic slice: %w", err)
	}
	if branch != identity.Name {
		return fmt.Errorf("automatic slice refused before workspace preparation: unrecorded managed workspace branch %q differs from expected branch %q", branch, identity.Name)
	}
	return nil
}

func recordedAutomaticWorktree(workspace *plan.Workspace) bool {
	if workspace == nil {
		return false
	}
	switch strings.TrimSpace(workspace.Strategy) {
	case plan.WorkspaceStrategyWorktree:
		return true
	case "":
		return strings.TrimSpace(workspace.Path) != "" || strings.TrimSpace(workspace.Root) != ""
	default:
		return false
	}
}

// completedAutomaticSliceProvesBoundary recognizes the state shape written by
// Tao versions that completed an automatic slice without refreshing the
// workspace HEAD mirror. Only the latest state-recorded completion can advance
// that mirror, and all available legacy execution-boundary fields must agree.
func completedAutomaticSliceProvesBoundary(detail *plan.PlanDetail, branch string, head string) bool {
	if detail == nil || detail.State.Workspace == nil || detail.State.Workspace.LifecycleStatus != plan.WorkspaceStatusReady ||
		detail.State.Workspace.Branch != branch || detail.State.Workspace.HeadSHA == head || len(detail.State.Plan.CompletedSlices) == 0 {
		return false
	}
	completedID := detail.State.Plan.CompletedSlices[len(detail.State.Plan.CompletedSlices)-1]
	for i := range detail.Slices.Slices {
		slice := &detail.Slices.Slices[i]
		if slice.ID != completedID {
			continue
		}
		if slice.Status != plan.StatusCompleted || slice.ExecutionStart == nil || slice.ExecutionStart.Branch != branch ||
			detail.State.Workspace.HeadSHA != slice.ExecutionStart.Head || slice.CommitIntent == nil ||
			slice.CommitIntent.Policy != CommitPolicySlice.String() || slice.Completion == nil ||
			(slice.Completion.Outcome != plan.SliceCompletionCommitted && slice.Completion.Outcome != plan.SliceCompletionNoChanges) ||
			slice.Completion.CommitSHA != head {
			return false
		}
		start := slice.ExecutionStart
		intent := slice.CommitIntent
		return (start.CommitPolicy == "" || start.CommitPolicy == CommitPolicySlice.String()) &&
			(start.WorkspaceStrategy == "" || start.WorkspaceStrategy == plan.WorkspaceStrategyWorktree) &&
			(intent.StartingBranch == "" || intent.StartingBranch == start.Branch) &&
			(intent.StartingHead == "" || intent.StartingHead == start.Head)
	}
	return false
}
