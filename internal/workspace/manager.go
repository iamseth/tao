package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

// CommandRunner runs local commands for workspace operations.
type CommandRunner = commandrunner.Runner

var defaultCommandRunner CommandRunner = commandrunner.DefaultLocal

// Manager prepares and inspects git worktree-backed plan workspaces.
type Manager struct {
	repoRoot string
	config   Config
	git      gitops.Client
}

// Options configures a Manager.
type Options struct {
	RepoRoot string
	Config   Config
	Runner   CommandRunner
}

// PrepareOptions identifies the plan workspace to prepare.
type PrepareOptions struct {
	PlanID              string
	BaseBranch          string
	BaseSHA             string
	Branch              string
	RequireNewBranch    bool
	PreferDefaultBranch bool
	RebaseStale         bool
	RebaseRecorder      RebaseRecorder
	Now                 func() time.Time
}

// RebaseRecorder makes the automatic rebase transaction durable. A nil
// recorder preserves compatibility for callers that do not own plan state.
type RebaseRecorder interface {
	RecordWorkspaceRebaseIntent(plan.WorkspaceRebaseIntent) error
	SettleWorkspaceRebase(plan.WorkspaceRebaseIntent, Metadata) error
}

// IntegrationWorkspace describes a batch-owned integration worktree. Its
// namespace is deliberately disjoint from ordinary plan workspace paths.
type IntegrationWorkspace struct {
	BatchID string
	Path    string
	Branch  string
	HeadSHA string
	Dirty   bool
	Missing bool
	Reused  bool
}

const integrationBranchPrefix = "tao/integration/"

// IntegrationStatus inspects a batch integration worktree without creating it.
func (m *Manager) IntegrationStatus(ctx context.Context, batchID string) (IntegrationWorkspace, error) {
	batchID, err := requirePlanID(batchID)
	if err != nil {
		return IntegrationWorkspace{}, fmt.Errorf("integration batch id: %w", err)
	}
	identity := m.integrationIdentity(batchID)
	if !exists(identity.Path) {
		identity.Missing = true
		return identity, nil
	}
	status, err := m.git.WorktreeStatus(ctx, identity.Path)
	if err != nil {
		return IntegrationWorkspace{}, err
	}
	identity.Branch, identity.HeadSHA, identity.Dirty = status.Branch, status.HEAD, status.Dirty
	return identity, nil
}

// CreateIntegration creates or exactly reuses the batch-owned branch and
// worktree at the recorded default start SHA. It never checks out or updates
// the repository's default worktree.
func (m *Manager) CreateIntegration(ctx context.Context, batchID, startSHA string) (IntegrationWorkspace, error) {
	if strings.TrimSpace(startSHA) == "" {
		return IntegrationWorkspace{}, fmt.Errorf("integration start SHA is required")
	}
	identity, err := m.IntegrationStatus(ctx, batchID)
	if err != nil {
		return IntegrationWorkspace{}, err
	}
	if !identity.Missing {
		identity.Reused = true
		if identity.Branch != m.integrationIdentity(batchID).Branch {
			return IntegrationWorkspace{}, fmt.Errorf("integration path %s is checked out on %q, want %q", identity.Path, identity.Branch, m.integrationIdentity(batchID).Branch)
		}
		if identity.Dirty || identity.HeadSHA != startSHA {
			return IntegrationWorkspace{}, fmt.Errorf("stale integration workspace %s must be restarted before reuse: expected clean head %s, got head %s dirty=%t", identity.Path, startSHA, identity.HeadSHA, identity.Dirty)
		}
		return identity, nil
	}
	worktrees, err := m.git.Worktrees(ctx)
	if err != nil {
		return IntegrationWorkspace{}, err
	}
	for _, worktree := range worktrees {
		if worktree.Branch == identity.Branch {
			return IntegrationWorkspace{}, fmt.Errorf("integration branch %q is already checked out at %s", identity.Branch, worktree.Path)
		}
	}
	if err := os.MkdirAll(filepath.Dir(identity.Path), 0o755); err != nil { //nolint:gosec // local worktree namespace
		return IntegrationWorkspace{}, err
	}
	branchExists, err := m.git.LocalBranchExists(ctx, identity.Branch)
	if err != nil {
		return IntegrationWorkspace{}, err
	}
	if branchExists {
		if head, parseErr := m.git.RevParse(ctx, identity.Branch); parseErr != nil || head != startSHA {
			return IntegrationWorkspace{}, fmt.Errorf("stale integration branch %q must be restarted before reuse", identity.Branch)
		}
	}
	if err := m.git.AddWorktree(ctx, identity.Path, identity.Branch, startSHA, !branchExists); err != nil {
		return IntegrationWorkspace{}, err
	}
	created, err := m.IntegrationStatus(ctx, batchID)
	if err != nil {
		return IntegrationWorkspace{}, err
	}
	created.Missing = false
	created.Reused = branchExists
	return created, nil
}

// RemoveIntegration removes only the exact batch namespace. Source and default
// refs are never passed to a mutating Git operation.
func (m *Manager) RemoveIntegration(ctx context.Context, batchID string) error {
	identity, err := m.IntegrationStatus(ctx, batchID)
	if err != nil {
		return err
	}
	if identity.Branch != m.integrationIdentity(batchID).Branch {
		return fmt.Errorf("refusing to remove unexpected integration branch %q", identity.Branch)
	}
	if !identity.Missing {
		if err := m.git.RemoveWorktree(ctx, identity.Path, true); err != nil {
			return fmt.Errorf("remove integration worktree: %w", err)
		}
	}
	exists, err := m.git.LocalBranchExists(ctx, identity.Branch)
	if err != nil {
		return err
	}
	if exists {
		if err := m.git.DeleteBranch(ctx, identity.Branch, true); err != nil {
			return fmt.Errorf("remove integration branch: %w", err)
		}
	}
	return nil
}

func (m *Manager) integrationIdentity(batchID string) IntegrationWorkspace {
	return IntegrationWorkspace{BatchID: batchID, Path: filepath.Join(m.repoRoot, ".tao", "integrations", batchID), Branch: integrationBranchPrefix + batchID}
}

// NewManager returns a workspace manager using Tao's default config where omitted.
func NewManager(options Options) (*Manager, error) {
	if options.RepoRoot == "" {
		return nil, fmt.Errorf("repo root is required")
	}
	config := options.Config
	if config == (Config{}) {
		config = DefaultConfig()
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	runner := options.Runner
	if runner == nil {
		runner = defaultCommandRunner
	}
	repoRoot, err := filepath.Abs(options.RepoRoot)
	if err != nil {
		return nil, err
	}
	return &Manager{repoRoot: repoRoot, config: config, git: gitops.NewClient(repoRoot, runner)}, nil
}

// Prepare creates or reuses the selected plan worktree.
func (m *Manager) Prepare(ctx context.Context, options PrepareOptions) (Metadata, error) {
	if m.config.Strategy == StrategyCurrent {
		return m.currentWorkspace(ctx, options)
	}
	planID, err := requirePlanID(options.PlanID)
	if err != nil {
		return Metadata{}, err
	}
	branch := options.Branch
	if branch == "" {
		branch = strings.ReplaceAll(m.config.BranchNameTemplate, "{plan_id}", planID)
	}
	baseBranch, err := m.resolveBaseBranch(ctx, options)
	if err != nil {
		return Metadata{}, err
	}
	baseCurrentSHA, err := m.git.RevParse(ctx, baseBranch)
	if err != nil {
		return Metadata{}, err
	}
	baseSHA := options.BaseSHA
	if baseSHA == "" {
		baseSHA = baseCurrentSHA
	}
	path := m.workspacePath(planID)
	if exists(path) {
		metadata, err := m.Status(ctx, planID, branch)
		if err != nil {
			return Metadata{}, err
		}
		if metadata.Branch != branch {
			return Metadata{}, fmt.Errorf("workspace path %s is already checked out on branch %q, want %q", path, metadata.Branch, branch)
		}
		metadata.BaseBranch = baseBranch
		metadata.BaseSHA = baseSHA
		metadata.BaseCurrentSHA = baseCurrentSHA
		metadata.Reused = true
		if err := m.rebaseStaleWorktree(ctx, &metadata, options.RebaseStale, options.RebaseRecorder, options.Now); err != nil {
			return Metadata{}, err
		}
		return metadata, nil
	}

	branchExists, err := m.git.LocalBranchExists(ctx, branch)
	if err != nil {
		return Metadata{}, err
	}
	if options.RequireNewBranch {
		remoteTrackingBranchExists, remoteErr := m.git.RemoteTrackingBranchExists(ctx, branch)
		if remoteErr != nil {
			return Metadata{}, remoteErr
		}
		if branchExists || remoteTrackingBranchExists {
			return Metadata{}, fmt.Errorf("branch %q already exists without durable ownership for plan %s", branch, planID)
		}
		remoteBranchExists, remoteErr := m.git.RemoteBranchExists(ctx, branch)
		if remoteErr != nil {
			return Metadata{}, remoteErr
		}
		if remoteBranchExists {
			return Metadata{}, fmt.Errorf("branch %q already exists without durable ownership for plan %s", branch, planID)
		}
	}

	worktrees, err := m.git.Worktrees(ctx)
	if err != nil {
		return Metadata{}, err
	}
	for _, worktree := range worktrees {
		if worktree.Branch == branch && filepath.Clean(worktree.Path) != filepath.Clean(path) {
			return Metadata{}, fmt.Errorf("branch %q is already checked out at %s", branch, worktree.Path)
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // G301: workspace metadata dir needs standard 0755 perms
		return Metadata{}, err
	}
	if err := m.git.AddWorktree(ctx, path, branch, baseBranch, !branchExists); err != nil {
		return Metadata{}, err
	}
	status, err := m.git.WorktreeStatus(ctx, path)
	if err != nil {
		return Metadata{}, err
	}
	metadata := Metadata{PlanID: planID, Path: path, Branch: status.Branch, BaseBranch: baseBranch, BaseSHA: baseSHA, BaseCurrentSHA: baseCurrentSHA, HeadSHA: status.HEAD, Created: !branchExists, Reused: branchExists, Dirty: status.Dirty}
	if err := m.rebaseStaleWorktree(ctx, &metadata, options.RebaseStale, options.RebaseRecorder, options.Now); err != nil {
		return Metadata{}, err
	}
	return metadata, nil
}

func (m *Manager) resolveBaseBranch(ctx context.Context, options PrepareOptions) (string, error) {
	if options.PreferDefaultBranch && m.config.BaseBranchDetection != BaseBranchDetectManual {
		if branch, err := m.git.DefaultBranch(ctx); err == nil && branch != "" {
			if exists, err := m.git.LocalBranchExists(ctx, branch); err == nil && exists {
				return branch, nil
			}
		}
	}
	if options.BaseBranch != "" {
		return options.BaseBranch, nil
	}
	return m.git.CurrentBranch(ctx)
}

func (m *Manager) rebaseStaleWorktree(ctx context.Context, metadata *Metadata, enabled bool, recorder RebaseRecorder, now func() time.Time) error {
	if !enabled {
		setBaseRefreshStatus(metadata)
		return nil
	}
	containsBase, err := m.git.IsAncestor(ctx, metadata.BaseCurrentSHA, metadata.HeadSHA)
	if err != nil {
		return fmt.Errorf("check workspace rebase status for plan %s: %w", metadata.PlanID, err)
	}
	if containsBase {
		markWorkspaceBaseCurrent(metadata)
		return nil
	}
	if metadata.Dirty {
		setBaseRefreshStatus(metadata)
		return fmt.Errorf("workspace %s for plan %s is stale against local base branch %q and dirty; refusing pre-run rebase before agent execution. Commit, stash, or discard the worktree changes, then retry", metadata.Path, metadata.PlanID, metadata.BaseBranch)
	}
	rebaseTarget := metadata.BaseCurrentSHA
	proof, proofErr := m.git.CommitSeriesRebaseProof(ctx, metadata.BaseSHA, rebaseTarget, metadata.BaseSHA, metadata.HeadSHA)
	if proofErr != nil {
		return fmt.Errorf("prove workspace commit series before rebase for plan %s (%s..%s): %w", metadata.PlanID, describeSHA(metadata.BaseSHA), describeSHA(metadata.HeadSHA), proofErr)
	}
	if proofErr := m.git.ProveRebaseReplay(ctx, metadata.BaseSHA, metadata.HeadSHA, rebaseTarget, proof); proofErr != nil {
		return fmt.Errorf("prove exact workspace commit replay before rebase for plan %s (%s..%s onto %s): %w", metadata.PlanID, describeSHA(metadata.BaseSHA), describeSHA(metadata.HeadSHA), describeSHA(rebaseTarget), proofErr)
	}
	var intent plan.WorkspaceRebaseIntent
	if recorder != nil {
		createdAt := time.Now().UTC()
		if now != nil {
			createdAt = now().UTC()
		}
		intent = plan.WorkspaceRebaseIntent{Branch: metadata.Branch, BaseBranch: metadata.BaseBranch, OldHeadSHA: metadata.HeadSHA, OldBaseSHA: metadata.BaseSHA, NewBaseSHA: metadata.BaseCurrentSHA, CommitCount: proof.Count, CommitSeriesFingerprint: proof.Fingerprint, CreatedAt: createdAt}
		if err := recorder.RecordWorkspaceRebaseIntent(intent); err != nil {
			return fmt.Errorf("record workspace rebase intent for plan %s before Git mutation: %w", metadata.PlanID, err)
		}
		rebaseTarget = intent.NewBaseSHA
	}
	if err := m.git.RebaseWorktree(ctx, metadata.Path, rebaseTarget, metadata.BaseSHA); err != nil {
		abortErr := m.git.RebaseAbortWorktree(ctx, metadata.Path)
		if abortErr != nil {
			return fmt.Errorf("pre-run rebase/conflict phase failed for plan %s in %s onto recorded base %s (local base branch %q): %w; additionally failed to abort rebase: %w", metadata.PlanID, metadata.Path, describeSHA(rebaseTarget), metadata.BaseBranch, err, abortErr)
		}
		return fmt.Errorf("pre-run rebase/conflict phase failed for plan %s in %s onto recorded base %s (local base branch %q); aborted rebase before agent execution: %w", metadata.PlanID, metadata.Path, describeSHA(rebaseTarget), metadata.BaseBranch, err)
	}
	status, err := m.git.WorktreeStatus(ctx, metadata.Path)
	if err != nil {
		return fmt.Errorf("refresh workspace status after pre-run rebase for plan %s: %w", metadata.PlanID, err)
	}
	metadata.Branch = status.Branch
	metadata.HeadSHA = status.HEAD
	metadata.Dirty = status.Dirty
	metadata.Rebased = true
	markWorkspaceBaseCurrent(metadata)
	if recorder != nil {
		if metadata.Dirty {
			return fmt.Errorf("refusing to settle workspace rebase for plan %s: post-rebase worktree is dirty; rebase intent remains durable; run tao workspace status %s and recover the worktree manually", metadata.PlanID, metadata.PlanID)
		}
		active, activeErr := gitops.ActiveOperation(metadata.Path)
		if activeErr != nil {
			return fmt.Errorf("inspect post-rebase Git operation for plan %s; rebase intent remains durable: %w", metadata.PlanID, activeErr)
		}
		if active != "" {
			return fmt.Errorf("refusing to settle workspace rebase for plan %s: Git operation %q remains active; rebase intent remains durable; recover the worktree manually", metadata.PlanID, active)
		}
		proof, proofErr := m.git.CommitSeriesRebaseProof(ctx, intent.OldBaseSHA, intent.NewBaseSHA, metadata.BaseSHA, metadata.HeadSHA)
		if proofErr != nil {
			return fmt.Errorf("prove rebased workspace commit series for plan %s (%s..%s); rebase intent remains durable: %w", metadata.PlanID, describeSHA(metadata.BaseSHA), describeSHA(metadata.HeadSHA), proofErr)
		}
		if status.Branch != intent.Branch || proof.Count != intent.CommitCount || proof.Fingerprint != intent.CommitSeriesFingerprint {
			return fmt.Errorf("refusing to settle workspace rebase for plan %s: rewritten series differs from durable intent (branch %q, head %s, commits %d); rebase intent remains durable; run tao workspace status %s and recover the branch manually", metadata.PlanID, status.Branch, describeSHA(status.HEAD), proof.Count, metadata.PlanID)
		}
		if err := recorder.SettleWorkspaceRebase(intent, *metadata); err != nil {
			return fmt.Errorf("settle workspace rebase for plan %s; rebase intent remains durable: %w", metadata.PlanID, err)
		}
	}
	return nil
}

func describeSHA(sha string) string {
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	return fmt.Sprintf("%s (short %s)", sha, short)
}

func markWorkspaceBaseCurrent(metadata *Metadata) {
	metadata.BaseSHA = metadata.BaseCurrentSHA
	setBaseRefreshStatus(metadata)
}

// Status returns the current state for a plan workspace without creating it.
// When expectedBranch is supplied it is reported for a missing workspace so
// typed-plan status uses the same stable identity as preparation.
func (m *Manager) Status(ctx context.Context, planID string, expectedBranch ...string) (Metadata, error) {
	planID, err := requirePlanID(planID)
	if err != nil {
		return Metadata{}, err
	}
	branch := strings.ReplaceAll(m.config.BranchNameTemplate, "{plan_id}", planID)
	if len(expectedBranch) > 0 && strings.TrimSpace(expectedBranch[0]) != "" {
		branch = strings.TrimSpace(expectedBranch[0])
	}
	path := m.workspacePath(planID)
	if !exists(path) {
		return Metadata{PlanID: planID, Path: path, Branch: branch, Missing: true}, nil
	}
	status, err := m.git.WorktreeStatus(ctx, path)
	if err != nil {
		return Metadata{}, err
	}
	return Metadata{PlanID: planID, Path: path, Branch: status.Branch, HeadSHA: status.HEAD, Dirty: status.Dirty}, nil
}

// List returns known Tao workspaces under the configured workspace root.
func (m *Manager) List(ctx context.Context) ([]Metadata, error) {
	worktrees, err := m.git.Worktrees(ctx)
	if err != nil {
		return nil, err
	}
	root := filepath.Clean(m.workspaceRoot())
	var result []Metadata
	for _, worktree := range worktrees {
		path := filepath.Clean(worktree.Path)
		planID, ok := directChild(root, path)
		if !ok {
			continue
		}
		status, err := m.git.WorktreeStatus(ctx, path)
		if err != nil {
			return nil, err
		}
		result = append(result, Metadata{PlanID: planID, Path: path, Branch: worktree.Branch, HeadSHA: status.HEAD, Dirty: status.Dirty})
	}
	return result, nil
}

func (m *Manager) currentWorkspace(ctx context.Context, options PrepareOptions) (Metadata, error) {
	status, err := m.git.WorktreeStatus(ctx, m.repoRoot)
	if err != nil {
		return Metadata{}, err
	}
	baseBranch := options.BaseBranch
	if baseBranch == "" {
		baseBranch = status.Branch
	}
	baseCurrentSHA, _ := m.git.RevParse(ctx, baseBranch)
	baseSHA := options.BaseSHA
	if baseSHA == "" {
		baseSHA = baseCurrentSHA
	}
	metadata := Metadata{PlanID: options.PlanID, Path: m.repoRoot, Branch: status.Branch, BaseBranch: baseBranch, BaseSHA: baseSHA, BaseCurrentSHA: baseCurrentSHA, HeadSHA: status.HEAD, Dirty: status.Dirty, Reused: true}
	setBaseRefreshStatus(&metadata)
	return metadata, nil
}

func setBaseRefreshStatus(metadata *Metadata) {
	if metadata.BaseSHA == "" || metadata.BaseCurrentSHA == "" {
		metadata.BaseStatus = "unknown"
		metadata.RefreshStatus = "unknown"
		metadata.RebaseStatus = "unknown"
		return
	}
	if metadata.BaseSHA == metadata.BaseCurrentSHA {
		metadata.BaseStatus = "current"
		metadata.RefreshStatus = "not_needed"
		metadata.RebaseStatus = "not_needed"
		return
	}
	metadata.BaseStatus = "stale"
	metadata.RefreshStatus = "needed"
	metadata.RebaseStatus = "needed"
}

func directChild(root string, path string) (string, bool) {
	relative := func(base string, candidate string) (string, bool) {
		rel, err := filepath.Rel(base, candidate)
		if err != nil || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || strings.Contains(rel, string(os.PathSeparator)) {
			return "", false
		}
		return rel, true
	}
	if rel, ok := relative(root, path); ok {
		return rel, true
	}
	resolvedRoot, rootErr := filepath.EvalSymlinks(root)
	resolvedPath, pathErr := filepath.EvalSymlinks(path)
	if rootErr != nil || pathErr != nil {
		return "", false
	}
	return relative(resolvedRoot, resolvedPath)
}

func (m *Manager) workspaceRoot() string {
	if filepath.IsAbs(m.config.Root) {
		return filepath.Clean(m.config.Root)
	}
	return filepath.Clean(filepath.Join(m.repoRoot, m.config.Root))
}

func (m *Manager) workspacePath(planID string) string {
	return filepath.Join(m.workspaceRoot(), planID)
}

func requirePlanID(planID string) (string, error) {
	planID = strings.TrimSpace(planID)
	if planID == "" {
		return "", fmt.Errorf("plan id is required")
	}
	if strings.ContainsAny(planID, `/\`) {
		return "", fmt.Errorf("plan id must not contain path separators")
	}
	return planID, nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
