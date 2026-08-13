package workspace

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

// PlanRecord persists plan state after workspace preparation milestones.
type PlanRecord interface {
	PersistState() error
	PersistStateChanges(*plan.ArtifactChangeSet) error
}

type rebasePlanRecord interface {
	RecordWorkspaceRebaseIntent(plan.WorkspaceRebaseIntent) error
	SettleWorkspaceRebase(plan.WorkspaceRebaseIntent, plan.WorkspaceRebaseSettlement) error
}

type PlanRecordFactory func(detail *plan.PlanDetail) (PlanRecord, error)

// ExecutionPrepareOptions contains run-time overrides for preparing an execution
// workspace. ExecutionMode is the single user-facing knob: isolated resolves to a
// dedicated worktree, current resolves to the launch checkout.
type ExecutionPrepareOptions struct {
	ExecutionMode string
}

// ExecutionPreparer prepares the workspace root used for one plan execution.
type ExecutionPreparer struct {
	Runner            CommandRunner
	PlanRecordFactory PlanRecordFactory
	Now               func() time.Time
	Config            Config
}

// Prepare resolves the plan workspace strategy, prepares the workspace, records plan metadata, and prepares dependencies.
func (p ExecutionPreparer) Prepare(ctx context.Context, detail *plan.PlanDetail, options ExecutionPrepareOptions) (string, error) {
	if detail == nil {
		return "", fmt.Errorf("plan detail is nil")
	}
	if detail.State.Repo.Root == "" {
		return "", fmt.Errorf("plan %s does not record a repo root", detail.State.Plan.ID)
	}
	strategy := plan.WorkspaceStrategyWorktree
	root := ""
	if detail.State.Workspace != nil {
		if detail.State.Workspace.Strategy != "" {
			strategy = detail.State.Workspace.Strategy
		}
		root = detail.State.Workspace.Root
	}
	if options.ExecutionMode != "" {
		modeStrategy, err := executionModeWorkspaceStrategy(options.ExecutionMode)
		if err != nil {
			return "", err
		}
		strategy = modeStrategy
	}
	if strategy != plan.WorkspaceStrategyWorktree && strategy != plan.WorkspaceStrategyCurrent {
		return "", fmt.Errorf("unsupported workspace strategy %q", strategy)
	}
	config := p.Config
	if config == (Config{}) {
		config = DefaultConfig()
	}
	if root != "" {
		config.Root = root
	}
	if err := guardExecutionContextDrift(detail, strategy, config); err != nil {
		return "", err
	}
	if strategy != plan.WorkspaceStrategyWorktree {
		return filepath.Abs(detail.State.Repo.Root)
	}
	manager, err := NewManager(Options{RepoRoot: detail.State.Repo.Root, Config: config, Runner: p.runner()})
	if err != nil {
		return "", err
	}
	branchIdentity, err := ResolvePlanBranch(detail, config)
	if err != nil {
		return "", fmt.Errorf("resolve plan workspace branch: %w", err)
	}
	recordedBaseBranch := detail.State.Repo.Branch
	recordedBaseSHA := ""
	if detail.State.Workspace != nil {
		if detail.State.Workspace.BaseBranch != "" {
			recordedBaseBranch = detail.State.Workspace.BaseBranch
		}
		recordedBaseSHA = detail.State.Workspace.BaseSHA
	}
	var rebaseRecorder RebaseRecorder
	if p.PlanRecordFactory != nil {
		record, recordErr := p.PlanRecordFactory(detail)
		if recordErr != nil {
			return "", recordErr
		}
		if rebaseRecord, ok := record.(rebasePlanRecord); ok {
			rebaseRecorder = executionRebaseRecorder{detail: detail, record: rebaseRecord}
		}
	}
	metadata, err := manager.Prepare(ctx, PrepareOptions{PlanID: detail.State.Plan.ID, BaseBranch: recordedBaseBranch, BaseSHA: recordedBaseSHA, Branch: branchIdentity.Name, RequireNewBranch: branchIdentity.RequireNew, PreferDefaultBranch: true, RebaseStale: true, RebaseRecorder: rebaseRecorder, Now: p.Now})
	if err != nil {
		return "", err
	}
	recordWorkspaceMetadata(detail, config, metadata, plan.WorkspaceStatusPreparing, p.now())
	if err := p.persistState(detail, nil); err != nil {
		return "", fmt.Errorf("record workspace metadata: %w", err)
	}
	dependencyChanges := plan.NewArtifactChangeSet(detail)
	priorFingerprint := detail.State.Workspace.DependencyFingerprint
	fingerprint := ""
	if autoDependencyInstall(config.DependencyInstallBehavior) {
		fingerprint, err = dependencyLockfileFingerprint(metadata.Path)
		if err != nil {
			return "", err
		}
	}
	if metadata.Reused && autoDependencyInstall(config.DependencyInstallBehavior) && fingerprint != "" && fingerprint == priorFingerprint {
		recordDependencyMetadata(detail, DependencyMetadata{Status: "skipped", FailureReason: "lockfile unchanged since last successful install"})
	} else {
		dependency, dependencyErr := PrepareDependencies(ctx, metadata.Path, config, p.runner(), p.now)
		recordDependencyMetadata(detail, dependency)
		if dependencyErr != nil {
			if !metadata.Reused || priorFingerprint == "" {
				detail.State.Workspace.LifecycleStatus = plan.WorkspaceStatusFailed
				if writeErr := p.persistState(detail, nil); writeErr != nil {
					return "", fmt.Errorf("record dependency failure: %w", writeErr)
				}
				return "", dependencyErr
			}
			detail.State.Workspace.DependencyFingerprint = priorFingerprint
		} else {
			if dependency.Status == plan.DependencyPreparationStatusReady {
				dependencyChanges.ClearWorkspaceDependencyFailure()
			}
			if config.DependencyInstallBehavior != DependencyInstallNever {
				fingerprint, err = dependencyLockfileFingerprint(metadata.Path)
				if err != nil {
					return "", err
				}
			}
			if fingerprint == "" {
				dependencyChanges.ClearWorkspaceDependencyFingerprint()
			} else {
				detail.State.Workspace.DependencyFingerprint = fingerprint
			}
		}
	}
	detail.State.Workspace.LifecycleStatus = plan.WorkspaceStatusReady
	preparedAt := p.now().UTC()
	detail.State.Workspace.Timing.PreparedAt = &preparedAt
	detail.State.Workspace.Timing.LastActivityAt = &preparedAt
	if err := p.persistState(detail, dependencyChanges); err != nil {
		return "", fmt.Errorf("record workspace ready metadata: %w", err)
	}
	return metadata.Path, nil
}

type executionRebaseRecorder struct {
	detail *plan.PlanDetail
	record rebasePlanRecord
}

func (r executionRebaseRecorder) recordForRebase() (rebasePlanRecord, error) {
	if r.record == nil {
		return nil, fmt.Errorf("plan record does not support workspace rebase transactions")
	}
	return r.record, nil
}

func (r executionRebaseRecorder) RecordWorkspaceRebaseIntent(intent plan.WorkspaceRebaseIntent) error {
	record, err := r.recordForRebase()
	if err != nil || record == nil {
		return err
	}
	return record.RecordWorkspaceRebaseIntent(intent)
}

func (r executionRebaseRecorder) SettleWorkspaceRebase(intent plan.WorkspaceRebaseIntent, metadata Metadata) error {
	record, err := r.recordForRebase()
	if err != nil || record == nil {
		return err
	}
	status := plan.WorkspaceStatusPreparing
	if r.detail.State.Workspace != nil && r.detail.State.Workspace.LifecycleStatus != "" {
		status = r.detail.State.Workspace.LifecycleStatus
	}
	return record.SettleWorkspaceRebase(intent, plan.WorkspaceRebaseSettlement{
		Branch: metadata.Branch, BaseSHA: metadata.BaseSHA, BaseCurrentSHA: metadata.BaseCurrentSHA,
		HeadSHA: metadata.HeadSHA, BaseStatus: metadata.BaseStatus, RefreshStatus: metadata.RefreshStatus,
		RebaseStatus: metadata.RebaseStatus, LifecycleStatus: status,
	})
}

func autoDependencyInstall(behavior string) bool {
	return behavior == "" || behavior == DependencyInstallAuto || behavior == DependencyInstallAutoIfLockfilePresent
}

func (p ExecutionPreparer) runner() CommandRunner {
	if p.Runner != nil {
		return p.Runner
	}
	return defaultCommandRunner
}

func (p ExecutionPreparer) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p ExecutionPreparer) persistState(detail *plan.PlanDetail, changes *plan.ArtifactChangeSet) error {
	if p.PlanRecordFactory == nil {
		return nil
	}
	record, err := p.PlanRecordFactory(detail)
	if err != nil {
		return err
	}
	if record == nil {
		return fmt.Errorf("plan record is nil")
	}
	if changes != nil {
		return record.PersistStateChanges(changes)
	}
	return record.PersistState()
}

// executionModeWorkspaceStrategy maps the user-facing execution mode onto the
// physical workspace strategy the manager and drift guard operate on: isolated
// (and the empty default) resolve to a dedicated worktree, current resolves to
// the launch checkout.
func executionModeWorkspaceStrategy(mode string) (string, error) {
	switch runtimeconfig.ExecutionMode(mode) {
	case "", runtimeconfig.ExecutionModeIsolated:
		return StrategyWorktree, nil
	case runtimeconfig.ExecutionModeCurrent:
		return StrategyCurrent, nil
	default:
		return "", fmt.Errorf("unsupported execution mode %q (want isolated or current)", mode)
	}
}

func guardExecutionContextDrift(detail *plan.PlanDetail, strategy string, config Config) error {
	prior := priorExecutionWorkspaceClassification(detail, config)
	if prior.Strategy == "" || prior.Strategy == strategy {
		return nil
	}
	evidence := formatVerificationCWDEvidence(detail, prior.Evidence, config)
	remediation := executionContextDriftRemediation(prior.Evidence)
	if prior.Strategy == "mixed" {
		return fmt.Errorf("plan %s has already run in multiple execution workspaces%s; refusing to continue until the plan workspace is reconciled. %s", detail.State.Plan.ID, evidence, remediation)
	}
	return fmt.Errorf("plan %s previously ran in %s%s, but this run would use %s; refusing to switch execution workspace for an in-progress plan. %s", detail.State.Plan.ID, workspaceStrategyDescription(detail, prior.Strategy, config), evidence, workspaceStrategyDescription(detail, strategy, config), remediation)
}

type priorExecutionWorkspace struct {
	Strategy string
	Evidence []executionWorkspaceEvidence
}

type executionWorkspaceEvidence struct {
	Value    string
	Source   string
	Strategy string
}

const (
	executionWorkspaceEvidenceSourceRoot = "execution root"
	executionWorkspaceEvidenceSourceCWD  = "verification cwd"
)

func priorExecutionWorkspaceClassification(detail *plan.PlanDetail, config Config) priorExecutionWorkspace {
	if !planHasExecutionHistory(detail) {
		return priorExecutionWorkspace{}
	}
	if prior, found := recordedExecutionRootWorkspaceClassification(detail, config); found {
		return prior
	}
	prior := verificationCWDWorkspaceClassification(detail, config)
	if prior.Strategy != "" {
		return prior
	}
	if detail.State.Workspace != nil && validWorkspaceStrategy(detail.State.Workspace.Strategy) && workspacePrepared(detail.State.Workspace) {
		return priorExecutionWorkspace{Strategy: detail.State.Workspace.Strategy}
	}
	return priorExecutionWorkspace{}
}

func recordedExecutionRootWorkspaceClassification(detail *plan.PlanDetail, config Config) (priorExecutionWorkspace, bool) {
	strategies := map[string]bool{}
	evidence := []executionWorkspaceEvidence{}
	found := false
	for _, slice := range detail.Slices.Slices {
		if slice.ExecutionRoot == "" {
			continue
		}
		found = true
		if strategy := executionRootWorkspaceStrategy(detail, slice.ExecutionRoot, config); strategy != "" {
			strategies[strategy] = true
			evidence = append(evidence, executionWorkspaceEvidence{Value: slice.ExecutionRoot, Source: executionWorkspaceEvidenceSourceRoot, Strategy: strategy})
		}
	}
	if !found {
		return priorExecutionWorkspace{}, false
	}
	return priorExecutionWorkspaceFromStrategies(strategies, evidence), true
}

func verificationCWDWorkspaceClassification(detail *plan.PlanDetail, config Config) priorExecutionWorkspace {
	strategies := map[string]bool{}
	evidence := []executionWorkspaceEvidence{}
	for _, slice := range detail.Slices.Slices {
		for _, result := range slice.VerificationResults {
			if strategy := verificationCWDWorkspaceStrategy(detail, result.CWD, config); strategy != "" {
				strategies[strategy] = true
				evidence = append(evidence, executionWorkspaceEvidence{Value: result.CWD, Source: executionWorkspaceEvidenceSourceCWD, Strategy: strategy})
			}
		}
	}
	return priorExecutionWorkspaceFromStrategies(strategies, evidence)
}

func priorExecutionWorkspaceFromStrategies(strategies map[string]bool, evidence []executionWorkspaceEvidence) priorExecutionWorkspace {
	if len(strategies) > 1 {
		return priorExecutionWorkspace{Strategy: "mixed", Evidence: evidence}
	}
	for strategy := range strategies {
		return priorExecutionWorkspace{Strategy: strategy, Evidence: evidence}
	}
	return priorExecutionWorkspace{}
}

func formatVerificationCWDEvidence(detail *plan.PlanDetail, evidence []executionWorkspaceEvidence, config Config) string {
	if len(evidence) == 0 {
		return ""
	}
	label := evidence[0].Source + " evidence"
	parts := make([]string, 0, len(evidence))
	for _, item := range evidence {
		parts = append(parts, fmt.Sprintf("%q classified as %s", item.Value, workspaceStrategyDescription(detail, item.Strategy, config)))
	}
	return " based on " + label + ": " + strings.Join(parts, "; ")
}

func executionContextDriftRemediation(evidence []executionWorkspaceEvidence) string {
	if len(evidence) > 0 {
		switch evidence[0].Source {
		case executionWorkspaceEvidenceSourceRoot:
			return "If the classification is wrong, correct the execution_root values in the plan's slices.json before resuming."
		case executionWorkspaceEvidenceSourceCWD:
			return "If the classification is wrong, correct the verification cwd values in the plan's slices.json before resuming."
		}
	}
	return "Re-run with the recorded workspace strategy or reconcile the plan workspace metadata before resuming."
}

func planHasExecutionHistory(detail *plan.PlanDetail) bool {
	if len(detail.State.Plan.CompletedSlices) > 0 {
		return true
	}
	for _, slice := range detail.Slices.Slices {
		if slice.ExecutionRoot != "" || slice.Status == plan.StatusCompleted || slice.Timing.CompletedAt != nil || len(slice.VerificationResults) > 0 {
			return true
		}
	}
	if detail.State.Workspace != nil {
		return workspacePrepared(detail.State.Workspace)
	}
	return false
}

func workspacePrepared(workspace *plan.Workspace) bool {
	if workspace == nil {
		return false
	}
	return workspace.Path != "" || workspace.LifecycleStatus != "" || workspace.Timing.PreparedAt != nil
}

func executionRootWorkspaceStrategy(detail *plan.PlanDetail, root string, config Config) string {
	return executionWorkspaceStrategy(detail, root, config)
}

func verificationCWDWorkspaceStrategy(detail *plan.PlanDetail, cwd string, config Config) string {
	return executionWorkspaceStrategy(detail, cwd, config)
}

func executionWorkspaceStrategy(detail *plan.PlanDetail, value string, config Config) string {
	if value == "" || detail.State.Repo.Root == "" || !filepath.IsAbs(value) {
		return ""
	}
	cleanValue := cleanPlanPath("", value)
	for _, root := range candidateWorktreeRoots(detail, config) {
		if pathWithinRoot(cleanPlanPath(detail.State.Repo.Root, root), cleanValue) {
			return plan.WorkspaceStrategyWorktree
		}
	}
	if pathWithinRoot(cleanPlanPath("", detail.State.Repo.Root), cleanValue) {
		return plan.WorkspaceStrategyCurrent
	}
	return ""
}

func candidateWorktreeRoots(detail *plan.PlanDetail, config Config) []string {
	roots := []string{}
	if detail.State.Workspace != nil {
		// Legacy drift candidates: keep the directly recorded path and the
		// workspace-root-derived path in case they differ from the current config.
		if detail.State.Workspace.Path != "" {
			roots = append(roots, detail.State.Workspace.Path)
		}
		if detail.State.Workspace.Root != "" {
			roots = append(roots, filepath.Join(detail.State.Workspace.Root, detail.State.Plan.ID))
		}
	}
	// Current-convention candidate: use the same path normalization as
	// Manager.workspacePath so classification matches what Manager would create.
	if p := resolvedWorktreePath(detail.State.Repo.Root, detail.State.Plan.ID, config); p != "" {
		roots = append(roots, p)
	}
	return roots
}

func cleanPlanPath(base string, value string) string {
	if value == "" {
		return ""
	}
	if !filepath.IsAbs(value) && base != "" {
		value = filepath.Join(base, value)
	}
	if abs, err := filepath.Abs(value); err == nil {
		value = abs
	}
	return filepath.Clean(value)
}

func pathWithinRoot(root string, value string) bool {
	if root == "" || value == "" {
		return false
	}
	root = filepath.Clean(root)
	value = filepath.Clean(value)
	if root == value {
		return true
	}
	rel, err := filepath.Rel(root, value)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return true
}

func validWorkspaceStrategy(strategy string) bool {
	return strategy == plan.WorkspaceStrategyWorktree || strategy == plan.WorkspaceStrategyCurrent
}

func workspaceStrategyDescription(detail *plan.PlanDetail, strategy string, config Config) string {
	switch strategy {
	case plan.WorkspaceStrategyCurrent:
		return fmt.Sprintf("current checkout %s", cleanPlanPath("", detail.State.Repo.Root))
	case plan.WorkspaceStrategyWorktree:
		path := ""
		if detail.State.Workspace != nil {
			path = detail.State.Workspace.Path
		}
		if path == "" && config.Root != "" {
			path = filepath.Join(config.Root, detail.State.Plan.ID)
		}
		return fmt.Sprintf("worktree %s", cleanPlanPath(detail.State.Repo.Root, path))
	default:
		return strategy
	}
}

func recordWorkspaceMetadata(detail *plan.PlanDetail, config Config, metadata Metadata, status string, currentTime time.Time) {
	if detail.State.Workspace == nil {
		detail.State.Workspace = &plan.Workspace{}
	}
	detail.State.Workspace.Strategy = config.Strategy
	detail.State.Workspace.Root = config.Root
	detail.State.Workspace.Path = metadata.Path
	detail.State.Workspace.Branch = metadata.Branch
	detail.State.Workspace.BaseBranch = metadata.BaseBranch
	detail.State.Workspace.BaseSHA = metadata.BaseSHA
	detail.State.Workspace.BaseCurrentSHA = metadata.BaseCurrentSHA
	detail.State.Workspace.BaseStatus = metadata.BaseStatus
	detail.State.Workspace.HeadSHA = metadata.HeadSHA
	detail.State.Workspace.RefreshStatus = metadata.RefreshStatus
	detail.State.Workspace.RebaseStatus = metadata.RebaseStatus
	detail.State.Workspace.LifecycleStatus = status
	now := currentTime.UTC()
	if metadata.Created && detail.State.Workspace.Timing.CreatedAt == nil {
		detail.State.Workspace.Timing.CreatedAt = &now
	}
	detail.State.Workspace.Timing.LastActivityAt = &now
}

func recordDependencyMetadata(detail *plan.PlanDetail, metadata DependencyMetadata) {
	if detail.State.Workspace == nil {
		detail.State.Workspace = &plan.Workspace{}
	}
	detail.State.Workspace.DependencyPreparation = metadata.Status
	detail.State.Workspace.DependencyCommand = metadata.Command
	detail.State.Workspace.DependencyStartedAt = metadata.StartedAt
	detail.State.Workspace.DependencyCompletedAt = metadata.CompletedAt
	if metadata.FailureReason != "" {
		detail.State.Workspace.DependencyFailure = metadata.FailureReason
	}
}
