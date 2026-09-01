package merge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/workspace"
)

// BatchDrift is one independently actionable reason an existing transaction
// cannot safely resume.
type BatchDrift struct {
	Scope    string `json:"scope"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
	Reason   string `json:"reason"`
}

// BatchResumeError reports every observed mismatch rather than stopping at the
// first drift, allowing the operator to make one restart decision.
type BatchResumeError struct {
	Drifts []BatchDrift
}

func (e *BatchResumeError) Error() string {
	parts := make([]string, 0, len(e.Drifts))
	for _, drift := range e.Drifts {
		parts = append(parts, fmt.Sprintf("%s: %s (expected %q, actual %q)", drift.Scope, drift.Reason, drift.Expected, drift.Actual))
	}
	return "merge batch cannot resume: " + strings.Join(parts, "; ")
}

// BatchWorkspace owns the isolated Git namespace and cross-process ownership
// used by one repository-wide merge transaction.
type BatchWorkspace struct {
	repoRoot   string
	batchesDir string
	git        gitops.Client
	workspaces *workspace.Manager
	store      *BatchStore
}

// NewBatchWorkspace constructs the integration-workspace boundary. batchesDir
// must be the repository-scoped merge-batches data directory.
func NewBatchWorkspace(repoRoot, batchesDir string, runner commandrunner.Runner) (*BatchWorkspace, error) {
	if strings.TrimSpace(repoRoot) == "" {
		return nil, fmt.Errorf("repo root is required")
	}
	if strings.TrimSpace(batchesDir) == "" {
		return nil, fmt.Errorf("merge batches directory is required")
	}
	repoRootAbs, err := filepath.Abs(repoRoot)
	if err != nil {
		return nil, err
	}
	manager, err := workspace.NewManager(workspace.Options{RepoRoot: repoRootAbs, Runner: runner})
	if err != nil {
		return nil, err
	}
	return &BatchWorkspace{
		repoRoot:   filepath.Clean(repoRootAbs),
		batchesDir: batchesDir,
		git:        gitops.NewClient(repoRoot, runner),
		workspaces: manager,
		store:      NewBatchStore(batchesDir, filepath.Join(batchesDir, "active.json")),
	}, nil
}

// BatchOwnership holds the repository batch lock followed by all candidate plan
// locks. Release is safe to call more than once.
type BatchOwnership struct {
	file      *os.File
	planLocks *run.PlanLocks
}

// AcquireOwnership excludes another batch process and ordinary runners for all
// candidates. Plan locks are acquired in stable plan-ID order by run.
func (b *BatchWorkspace) AcquireOwnership(state BatchState, timestamp time.Time) (*BatchOwnership, error) {
	requests := make([]run.PlanLockRequest, 0, len(state.Candidates))
	for _, candidate := range state.Candidates {
		requests = append(requests, run.PlanLockRequest{PlanID: candidate.PlanID, PlanDir: candidate.PlanDir})
	}
	return b.acquireOwnership(requests, timestamp)
}

// AcquirePlanOwnership takes repository ownership before one plan lock. It is
// used by non-batch lifecycle mutations that must inspect active batch state
// without reversing the batch-to-plan lock order.
func (b *BatchWorkspace) AcquirePlanOwnership(planID, planDir string, timestamp time.Time) (*BatchOwnership, error) {
	return b.acquireOwnership([]run.PlanLockRequest{{PlanID: planID, PlanDir: planDir}}, timestamp)
}

func (b *BatchWorkspace) acquireOwnership(requests []run.PlanLockRequest, timestamp time.Time) (*BatchOwnership, error) {
	if err := os.MkdirAll(b.batchesDir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(b.batchesDir, "batch.lock"), os.O_RDWR|os.O_CREATE, 0o600) // #nosec G304 -- repository data path supplied by registry
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("merge batch ownership is held by another process: %w", err)
	}
	locks, err := run.AcquirePlanLocks(requests, timestamp)
	if err != nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
		return nil, fmt.Errorf("acquire merge batch plan ownership: %w", err)
	}
	return &BatchOwnership{file: file, planLocks: locks}, nil
}

// Release relinquishes plan ownership before repository ownership.
func (o *BatchOwnership) Release() error {
	if o == nil {
		return nil
	}
	planErr := o.planLocks.Release()
	o.planLocks = nil
	if o.file == nil {
		return planErr
	}
	file := o.file
	o.file = nil
	return errors.Join(planErr, syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
}

// Start validates immutable inputs before creating the isolated integration
// branch and worktree at the exact default start commit.
func (b *BatchWorkspace) Start(ctx context.Context, state BatchState) (workspace.IntegrationWorkspace, error) {
	if err := state.validate(); err != nil {
		return workspace.IntegrationWorkspace{}, err
	}
	drifts := b.validateInputs(ctx, state, "")
	if len(drifts) != 0 {
		return workspace.IntegrationWorkspace{}, &BatchResumeError{Drifts: drifts}
	}
	return b.workspaces.CreateIntegration(ctx, state.ID, state.DefaultStartSHA)
}

// Status returns the typed integration workspace state.
func (b *BatchWorkspace) Status(ctx context.Context, batchID string) (workspace.IntegrationWorkspace, error) {
	return b.workspaces.IntegrationStatus(ctx, batchID)
}

// ValidateResume verifies immutable inputs, live review evidence, the isolated
// worktree, branch head, and persisted progress without mutating any ref.
func (b *BatchWorkspace) ValidateResume(ctx context.Context, state BatchState) error {
	return b.validateResume(ctx, state, "", false)
}

// ValidateEjectionResume verifies a resumable ejection while excluding the
// candidate that will be discarded. Generic dirt is allowed only when the
// resumed ejection will restore the integration worktree before using it.
func (b *BatchWorkspace) ValidateEjectionResume(ctx context.Context, state BatchState, planID string) error {
	planID = strings.TrimSpace(planID)
	if candidateByID(state.Candidates, planID) == nil {
		return fmt.Errorf("batch eject names unknown plan %s", planID)
	}
	if state.Ejection != nil && (!slices.Contains([]string{batchEjectionPending, batchEjectionReintegrating}, state.Ejection.Status) || state.Ejection.PlanID != planID) {
		return fmt.Errorf("batch eject resume does not target in-progress plan %s", planID)
	}
	return b.validateResume(ctx, state, planID, true)
}

func (b *BatchWorkspace) validateResume(ctx context.Context, state BatchState, excludedPlanID string, allowResettableDirt bool) error {
	if err := state.validate(); err != nil {
		return err
	}
	drifts := b.validateInputs(ctx, state, excludedPlanID)
	status, err := b.workspaces.IntegrationStatus(ctx, state.ID)
	if err != nil {
		drifts = append(drifts, BatchDrift{Scope: "integration worktree", Reason: err.Error()})
	} else {
		expectedBranch := "tao/integration/" + state.ID
		expectedHead := state.IntegrationHead
		if expectedHead == "" {
			expectedHead = state.DefaultStartSHA
		}
		switch {
		case status.Missing:
			drifts = append(drifts, BatchDrift{Scope: "integration worktree", Expected: status.Path, Actual: "missing", Reason: "recorded workspace is absent"})
		case status.Branch != expectedBranch:
			drifts = append(drifts, BatchDrift{Scope: "integration branch", Expected: expectedBranch, Actual: status.Branch, Reason: "workspace branch changed"})
		}
		ejectReset := state.Ejection != nil && state.Ejection.Status == batchEjectionPending && status.HeadSHA == state.DefaultStartSHA
		resettableDirt := allowResettableDirt && resettableBatchEjectionDirt(state, status.HeadSHA, expectedHead)
		if !status.Missing && status.Dirty && !recoverableBatchResolutionWork(state, status.HeadSHA) && !resettableDirt {
			drifts = append(drifts, BatchDrift{Scope: "integration worktree", Expected: "clean", Actual: "dirty", Reason: "uncommitted or conflicted changes remain"})
		}
		if !status.Missing && status.HeadSHA != expectedHead && !ejectReset && !b.matchesApplyingCommitIntent(ctx, state, status.HeadSHA, expectedBranch) {
			drifts = append(drifts, BatchDrift{Scope: "integration head", Expected: expectedHead, Actual: status.HeadSHA, Reason: "batch branch does not match persisted progress"})
		}
	}
	drifts = append(drifts, validatePersistedProgress(state)...)
	if len(drifts) != 0 {
		return &BatchResumeError{Drifts: drifts}
	}
	return nil
}

func resettableBatchEjectionDirt(state BatchState, head, expectedHead string) bool {
	if state.Ejection == nil {
		return false
	}
	if state.Ejection.Status == batchEjectionPending {
		return head == expectedHead || head == state.DefaultStartSHA
	}
	if state.Ejection.Status != batchEjectionReintegrating || state.Status != BatchStatusIntegrating || head != expectedHead {
		return false
	}
	for _, integration := range state.Integrations {
		if integration.Status == batchIntegrationApplying && integration.IntegrationBaseSHA == head {
			return true
		}
	}
	return false
}

func recoverableBatchResolutionWork(state BatchState, head string) bool {
	if state.Review != nil && (state.Review.Status == "reworking" || state.Review.Status == "applying") && strings.TrimSpace(head) == strings.TrimSpace(state.IntegrationHead) {
		return true
	}
	for _, integration := range state.Integrations {
		if strings.TrimSpace(integration.IntegrationBaseSHA) != strings.TrimSpace(head) {
			continue
		}
		if integration.Status == batchIntegrationDeferred && activeBatchResolution(&integration) != nil {
			return true
		}
		if integration.Status == batchIntegrationApplying && len(integration.Resolutions) > 0 {
			resolution := integration.Resolutions[len(integration.Resolutions)-1]
			if resolution.CompletedAt != "" && strings.TrimSpace(resolution.BaseSHA) == strings.TrimSpace(integration.IntegrationBaseSHA) {
				return true
			}
		}
	}
	return false
}

func (b *BatchWorkspace) matchesApplyingCommitIntent(ctx context.Context, state BatchState, head, branch string) bool {
	for _, integration := range state.Integrations {
		if integration.Status != batchIntegrationApplying || integration.IntegrationBaseSHA != state.IntegrationHead {
			continue
		}
		candidateIndex := batchCandidateIndex(state, integration.PlanID)
		if candidateIndex < 0 {
			return false
		}
		_, committed, err := recoverApplyingBatchIntegrationAtRevision(ctx, b.git, state.Candidates[candidateIndex], integration, head, branch)
		return err == nil && committed
	}
	matched, err := aggregateReworkCommitMatches(ctx, b.git, state, branch)
	return err == nil && matched
}

func (b *BatchWorkspace) validateInputs(ctx context.Context, state BatchState, excludedPlanID string) []BatchDrift {
	var drifts []BatchDrift
	if filepath.Clean(state.RepoRoot) != b.repoRoot {
		drifts = append(drifts, BatchDrift{Scope: "repository root", Expected: state.RepoRoot, Actual: b.repoRoot, Reason: "batch is bound to another repository"})
	}
	defaultSHA, err := b.git.RevParse(ctx, state.DefaultBranch)
	if err != nil {
		drifts = append(drifts, BatchDrift{Scope: "default branch", Expected: state.DefaultStartSHA, Reason: err.Error()})
	} else if defaultSHA != state.DefaultStartSHA {
		drifts = append(drifts, BatchDrift{Scope: "default branch", Expected: state.DefaultStartSHA, Actual: defaultSHA, Reason: "default tip drifted"})
	}
	candidates := effectiveBatchCandidates(state)
	slices.SortFunc(candidates, func(a, c BatchCandidate) int { return strings.Compare(a.PlanID, c.PlanID) })
	for _, candidate := range candidates {
		if candidate.PlanID == excludedPlanID {
			continue
		}
		if filepath.Clean(candidate.RepoRoot) != filepath.Clean(state.RepoRoot) {
			drifts = append(drifts, BatchDrift{Scope: "plan " + candidate.PlanID + " repository", Expected: state.RepoRoot, Actual: candidate.RepoRoot, Reason: "candidate repository snapshot differs"})
		}
		if candidate.DefaultBranch != state.DefaultBranch || candidate.DefaultStartSHA != state.DefaultStartSHA {
			drifts = append(drifts, BatchDrift{Scope: "plan " + candidate.PlanID + " default snapshot", Expected: state.DefaultBranch + "@" + state.DefaultStartSHA, Actual: candidate.DefaultBranch + "@" + candidate.DefaultStartSHA, Reason: "candidate default snapshot differs"})
		}
		tip, tipErr := b.git.RevParse(ctx, candidate.Branch)
		if tipErr != nil {
			drifts = append(drifts, BatchDrift{Scope: "plan " + candidate.PlanID + " source", Expected: candidate.SourceTip, Reason: tipErr.Error()})
		} else if tip != candidate.SourceTip {
			drifts = append(drifts, BatchDrift{Scope: "plan " + candidate.PlanID + " source", Expected: candidate.SourceTip, Actual: tip, Reason: "source tip drifted"})
		}
		if candidate.ReviewHead != candidate.SourceTip {
			drifts = append(drifts, BatchDrift{Scope: "plan " + candidate.PlanID + " review head", Expected: candidate.SourceTip, Actual: candidate.ReviewHead, Reason: "persisted review no longer covers source"})
		}
		base, baseErr := b.git.MergeBase(ctx, state.DefaultBranch, candidate.Branch)
		if baseErr != nil {
			drifts = append(drifts, BatchDrift{Scope: "plan " + candidate.PlanID + " review base", Expected: candidate.ReviewBase, Reason: baseErr.Error()})
		} else if base != candidate.ReviewBase {
			drifts = append(drifts, BatchDrift{Scope: "plan " + candidate.PlanID + " review base", Expected: candidate.ReviewBase, Actual: base, Reason: "live merge base drifted"})
		}
	}
	return drifts
}

func validatePersistedProgress(state BatchState) []BatchDrift {
	byPlan := make(map[string]BatchCandidate, len(state.Candidates))
	for _, candidate := range state.Candidates {
		byPlan[candidate.PlanID] = candidate
	}
	var drifts []BatchDrift
	previousHead := state.DefaultStartSHA
	seen := make(map[string]bool, len(state.Integrations))
	for _, integration := range state.Integrations {
		candidate, ok := byPlan[integration.PlanID]
		if !ok {
			drifts = append(drifts, BatchDrift{Scope: "integration progress", Actual: integration.PlanID, Reason: "unknown plan recorded"})
			continue
		}
		if seen[integration.PlanID] {
			drifts = append(drifts, BatchDrift{Scope: "integration progress", Actual: integration.PlanID, Reason: "plan is recorded more than once"})
		}
		seen[integration.PlanID] = true
		if integration.SourceHead != candidate.SourceTip {
			drifts = append(drifts, BatchDrift{Scope: "plan " + integration.PlanID + " progress source", Expected: candidate.SourceTip, Actual: integration.SourceHead, Reason: "integration source differs from immutable input"})
		}
		if integration.CommitMessage != "" {
			expectedMessage := candidate.CommitMessage
			if !candidate.CommitMessageResolved && candidate.ReviewCommitMessage != nil {
				reviewMessage, err := singleMergeCommitMessage(*candidate.ReviewCommitMessage, candidate.PlanID, candidate.SourceTip)
				if err != nil {
					drifts = append(drifts, BatchDrift{Scope: "plan " + integration.PlanID + " commit message", Reason: "approved review proposal is invalid: " + err.Error()})
				} else {
					expectedMessage = reviewMessage
				}
			}
			if err := validateBatchCommitMessage(integration.CommitMessage, candidate); err != nil {
				drifts = append(drifts, BatchDrift{Scope: "plan " + integration.PlanID + " commit message", Expected: "valid exact intent", Actual: "invalid", Reason: err.Error()})
			} else if expectedMessage == "" || integration.CommitMessage != expectedMessage {
				drifts = append(drifts, BatchDrift{Scope: "plan " + integration.PlanID + " commit message", Expected: expectedMessage, Actual: integration.CommitMessage, Reason: "integration message differs from immutable candidate intent"})
			}
		}
		if integration.IntegrationBaseSHA != previousHead {
			drifts = append(drifts, BatchDrift{Scope: "plan " + integration.PlanID + " integration base", Expected: previousHead, Actual: integration.IntegrationBaseSHA, Reason: "integration chain is not contiguous"})
		}
		if integration.IntegrationSHA != "" {
			previousHead = integration.IntegrationSHA
		}
	}
	expectedAggregateHead := previousHead
	if state.Review != nil && len(state.Review.ResolutionSHAs) > 0 {
		expectedAggregateHead = state.Review.ResolutionSHAs[len(state.Review.ResolutionSHAs)-1]
	}
	if len(state.Integrations) > 0 && state.IntegrationHead == "" {
		drifts = append(drifts, BatchDrift{Scope: "integration progress", Expected: "recorded head", Actual: "empty", Reason: "integrations exist without an aggregate head"})
	} else if len(state.Integrations) > 0 && state.IntegrationHead != expectedAggregateHead {
		drifts = append(drifts, BatchDrift{Scope: "integration progress", Expected: expectedAggregateHead, Actual: state.IntegrationHead, Reason: "aggregate head differs from final integration or review resolution"})
	}
	if state.Verification != nil && state.Verification.HeadSHA != "" && state.Verification.HeadSHA != state.IntegrationHead {
		drifts = append(drifts, BatchDrift{Scope: "verification progress", Expected: state.IntegrationHead, Actual: state.Verification.HeadSHA, Reason: "verification belongs to another head"})
	}
	if state.Review != nil && state.Review.HeadSHA != "" && state.Review.HeadSHA != state.IntegrationHead {
		drifts = append(drifts, BatchDrift{Scope: "review progress", Expected: state.IntegrationHead, Actual: state.Review.HeadSHA, Reason: "aggregate review belongs to another head"})
	}
	return drifts
}

// DefaultReachedLandingIntent reports whether the checked-out default branch
// has reached the integration head named by durable landing intent. This is
// the crash-recovery proof used before ordinary resume validation or restart.
func (b *BatchWorkspace) DefaultReachedLandingIntent(ctx context.Context, state BatchState) (bool, error) {
	if state.Landing == nil {
		return false, nil
	}
	defaultSHA, err := b.git.RevParse(ctx, state.DefaultBranch)
	if err != nil {
		return false, fmt.Errorf("inspect default branch for landing recovery: %w", err)
	}
	return defaultSHA == state.Landing.IntegrationHead, nil
}

// BatchRestartPlan is an explicit preview of the only resources restart may
// remove.
type BatchRestartPlan struct {
	BatchID        string
	Branch         string
	WorktreePath   string
	RemoveBranch   bool
	RemoveWorktree bool
	RemoveRecovery bool
}

// PlanRestart refuses all post-landing phases and reports exact batch-owned
// resources. It does not inspect or propose source/default refs.
func (b *BatchWorkspace) PlanRestart(ctx context.Context, state BatchState) (BatchRestartPlan, error) {
	if state.LandedSHA != "" || state.Status == BatchStatusLanded || state.Status == BatchStatusSettling || state.Status == BatchStatusCompleted {
		return BatchRestartPlan{}, fmt.Errorf("merge batch %s has landed; restart is forbidden", state.ID)
	}
	landed, err := b.DefaultReachedLandingIntent(ctx, state)
	if err != nil {
		return BatchRestartPlan{}, err
	}
	if landed {
		return BatchRestartPlan{}, fmt.Errorf("merge batch %s default has reached durable landing intent; restart is forbidden", state.ID)
	}
	status, err := b.workspaces.IntegrationStatus(ctx, state.ID)
	if err != nil {
		return BatchRestartPlan{}, err
	}
	branch := "tao/integration/" + state.ID
	branchExists, err := b.git.LocalBranchExists(ctx, branch)
	if err != nil {
		return BatchRestartPlan{}, err
	}
	return BatchRestartPlan{BatchID: state.ID, Branch: branch, WorktreePath: status.Path, RemoveBranch: branchExists, RemoveWorktree: !status.Missing, RemoveRecovery: true}, nil
}

// Restart executes a previously previewable safe restart. Cleanup failure stops
// before recovery metadata is removed, preserving an actionable retry point.
func (b *BatchWorkspace) Restart(ctx context.Context, state BatchState) (BatchRestartPlan, error) {
	plan, err := b.PlanRestart(ctx, state)
	if err != nil {
		return BatchRestartPlan{}, err
	}
	if err := b.workspaces.RemoveIntegration(ctx, state.ID); err != nil {
		return plan, err
	}
	if err := b.removeRecovery(state.ID); err != nil {
		return plan, err
	}
	return plan, nil
}

// RemoveIntegration removes only the batch-owned integration worktree and
// branch. Settlement calls it after every source plan is durably recorded.
func (b *BatchWorkspace) RemoveIntegration(ctx context.Context, batchID string) error {
	return b.workspaces.RemoveIntegration(ctx, batchID)
}

// ClearActive completes a landed transaction without deleting its retained
// audit directory.
func (b *BatchWorkspace) ClearActive(batchID string) error {
	return b.store.ClearActive(batchID)
}

func (b *BatchWorkspace) removeRecovery(batchID string) error {
	if strings.TrimSpace(batchID) == "" || strings.ContainsAny(batchID, `/\\`) {
		return fmt.Errorf("invalid merge batch id %q", batchID)
	}
	active, err := b.store.ActiveID()
	if err != nil {
		return err
	}
	if active != "" && active != batchID {
		return fmt.Errorf("merge batch %s is active, not %s", active, batchID)
	}
	if active == batchID {
		if err := b.store.ClearActive(batchID); err != nil {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(b.batchesDir, batchID))
}
