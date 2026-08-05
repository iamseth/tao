package workspace

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestExecutionPreparerDefaultsToWorktree(t *testing.T) {
	repoRoot := t.TempDir()
	detail := executionPreparerPlanDetail(repoRoot)
	detail.State.Workspace = nil
	var calls []string

	root, err := (ExecutionPreparer{Runner: executionPreparerGitFake(&calls)}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("prepare execution workspace: %v", err)
	}

	want := filepath.Join(repoRoot, ".tao", "workspaces", "plan-a")
	if root != want {
		t.Fatalf("expected workspace root %q, got %q", want, root)
	}
	if detail.State.Workspace == nil || detail.State.Workspace.Strategy != plan.WorkspaceStrategyWorktree || detail.State.Workspace.Path != want {
		t.Fatalf("expected worktree metadata, got %#v", detail.State.Workspace)
	}
	if !executionPreparerHasGitCall(calls, "worktree add -b tao/plan-a "+want+" feature") {
		t.Fatalf("expected worktree add call, got %v", calls)
	}
}

func TestExecutionPreparerWorktreeRunOptionOverridesPlanCurrent(t *testing.T) {
	repoRoot := t.TempDir()
	detail := executionPreparerPlanDetail(repoRoot)
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent}
	var calls []string

	root, err := (ExecutionPreparer{Runner: executionPreparerGitFake(&calls)}).Prepare(context.Background(), detail, ExecutionPrepareOptions{ExecutionMode: "isolated"})
	if err != nil {
		t.Fatalf("prepare execution workspace: %v", err)
	}

	want := filepath.Join(repoRoot, ".tao", "workspaces", "plan-a")
	if root != want {
		t.Fatalf("expected workspace root %q, got %q", want, root)
	}
	if detail.State.Workspace == nil || detail.State.Workspace.Strategy != plan.WorkspaceStrategyWorktree {
		t.Fatalf("expected worktree override metadata, got %#v", detail.State.Workspace)
	}
}

func TestExecutionPreparerBlocksWorktreeAfterCurrentCheckoutVerification(t *testing.T) {
	repoRoot := t.TempDir()
	detail := executionPreparerPlanDetail(repoRoot)
	detail.State.Workspace = nil
	detail.State.Plan.CompletedSlices = []string{"001-a"}
	detail.Slices.Slices = []plan.Slice{{
		ID:     "001-a",
		Status: plan.StatusCompleted,
		VerificationResults: []plan.VerificationRun{{
			Command: "make test",
			CWD:     repoRoot,
			Result:  "pass",
		}},
	}}
	var calls []string

	_, err := (ExecutionPreparer{Runner: executionPreparerGitFake(&calls)}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err == nil {
		t.Fatal("expected execution context drift error")
	}
	message := err.Error()
	for _, want := range []string{"previously ran in current checkout", "would use worktree", "refusing to switch execution workspace", "verification cwd evidence", repoRoot, "classified as current checkout", "slices.json"} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected %q in error, got %q", want, message)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("expected guardrail before git calls, got %v", calls)
	}
}

func TestExecutionPreparerBlocksCurrentAfterWorktreeVerification(t *testing.T) {
	repoRoot := t.TempDir()
	workspacePath := filepath.Join(repoRoot, ".tao", "workspaces", "plan-a")
	detail := executionPreparerPlanDetail(repoRoot)
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: workspacePath, LifecycleStatus: plan.WorkspaceStatusReady}
	detail.State.Plan.CompletedSlices = []string{"001-a"}
	detail.Slices.Slices = []plan.Slice{{
		ID:     "001-a",
		Status: plan.StatusCompleted,
		VerificationResults: []plan.VerificationRun{{
			Command: "make test",
			CWD:     workspacePath,
			Result:  "pass",
		}},
	}}
	var calls []string

	_, err := (ExecutionPreparer{Runner: executionPreparerGitFake(&calls)}).Prepare(context.Background(), detail, ExecutionPrepareOptions{ExecutionMode: "current"})
	if err == nil {
		t.Fatal("expected execution context drift error")
	}
	message := err.Error()
	for _, want := range []string{"previously ran in worktree", "would use current checkout", "refusing to switch execution workspace", "verification cwd evidence", workspacePath, "classified as worktree", "slices.json"} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected %q in error, got %q", want, message)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("expected guardrail before git calls, got %v", calls)
	}
}

func TestExecutionPreparerAllowsWorktreeResumeWithRelativeVerificationCWD(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := filepath.Join(repoRoot, ".tao", "workspaces")
	workspacePath := filepath.Join(workspaceRoot, "plan-a")
	detail := executionPreparerPlanDetail(repoRoot)
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: workspaceRoot, Path: workspacePath, LifecycleStatus: plan.WorkspaceStatusReady}
	detail.State.Plan.CompletedSlices = []string{"001-a"}
	detail.Slices.Slices = []plan.Slice{{
		ID:     "001-a",
		Status: plan.StatusCompleted,
		VerificationResults: []plan.VerificationRun{{
			Command: "go test ./internal/workspace",
			CWD:     "internal/workspace",
			Result:  "pass",
		}},
	}}
	var calls []string

	root, err := (ExecutionPreparer{Runner: executionPreparerGitFake(&calls)}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("prepare execution workspace: %v", err)
	}
	if root != workspacePath {
		t.Fatalf("expected workspace root %q, got %q", workspacePath, root)
	}
}

func TestPriorExecutionWorkspaceClassificationPrefersRecordedExecutionRoots(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := filepath.Join(repoRoot, ".tao", "workspaces")
	workspacePath := filepath.Join(workspaceRoot, "plan-a")
	currentCWD := filepath.Join(repoRoot, "internal", "workspace")
	worktreeCWD := filepath.Join(workspacePath, "internal", "workspace")
	config := DefaultConfig()
	config.Root = workspaceRoot

	tests := []struct {
		name          string
		slices        []plan.Slice
		runStrategy   string
		wantStrategy  string
		wantSource    string
		wantErrorText []string
	}{
		{
			name:         "recorded worktree root overrides contradictory verification cwd",
			runStrategy:  plan.WorkspaceStrategyWorktree,
			wantStrategy: plan.WorkspaceStrategyWorktree,
			wantSource:   executionWorkspaceEvidenceSourceRoot,
			slices: []plan.Slice{{
				ID:            "001-a",
				Status:        plan.StatusInProgress,
				ExecutionRoot: workspacePath,
				VerificationResults: []plan.VerificationRun{{
					Command: "go test ./internal/workspace",
					CWD:     currentCWD,
					Result:  "pass",
				}},
			}},
		},
		{
			name:         "legacy plan without recorded root falls back to verification cwd",
			runStrategy:  plan.WorkspaceStrategyWorktree,
			wantStrategy: plan.WorkspaceStrategyCurrent,
			wantSource:   executionWorkspaceEvidenceSourceCWD,
			wantErrorText: []string{
				"previously ran in current checkout",
				"verification cwd evidence",
				currentCWD,
			},
			slices: []plan.Slice{{
				ID:     "001-a",
				Status: plan.StatusCompleted,
				VerificationResults: []plan.VerificationRun{{
					Command: "go test ./internal/workspace",
					CWD:     currentCWD,
					Result:  "pass",
				}},
			}},
		},
		{
			name:         "mixed recorded roots refuse",
			runStrategy:  plan.WorkspaceStrategyWorktree,
			wantStrategy: "mixed",
			wantSource:   executionWorkspaceEvidenceSourceRoot,
			wantErrorText: []string{
				"multiple execution workspaces",
				"execution root evidence",
				workspacePath,
				repoRoot,
			},
			slices: []plan.Slice{
				{ID: "001-a", Status: plan.StatusCompleted, ExecutionRoot: workspacePath, VerificationResults: []plan.VerificationRun{{Command: "make test", CWD: worktreeCWD, Result: "pass"}}},
				{ID: "002-b", Status: plan.StatusInProgress, ExecutionRoot: repoRoot, VerificationResults: []plan.VerificationRun{{Command: "make test", CWD: worktreeCWD, Result: "pass"}}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := executionPreparerPlanDetail(repoRoot)
			detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: workspaceRoot, Path: workspacePath, LifecycleStatus: plan.WorkspaceStatusReady}
			detail.Slices.Slices = tt.slices
			prior := priorExecutionWorkspaceClassification(detail, config)
			if prior.Strategy != tt.wantStrategy {
				t.Fatalf("prior strategy = %q, want %q", prior.Strategy, tt.wantStrategy)
			}
			if len(prior.Evidence) == 0 || prior.Evidence[0].Source != tt.wantSource {
				t.Fatalf("prior evidence = %#v, want source %q", prior.Evidence, tt.wantSource)
			}

			err := guardExecutionContextDrift(detail, tt.runStrategy, config)
			if len(tt.wantErrorText) == 0 {
				if err != nil {
					t.Fatalf("expected no drift error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected drift error")
			}
			for _, want := range tt.wantErrorText {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("expected %q in error, got %q", want, err.Error())
				}
			}
		})
	}
}

func TestVerificationCWDWorkspaceStrategy(t *testing.T) {
	repoRoot := t.TempDir()
	workspaceRoot := filepath.Join(repoRoot, ".tao", "workspaces")
	workspacePath := filepath.Join(workspaceRoot, "plan-a")
	detail := executionPreparerPlanDetail(repoRoot)
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: workspaceRoot, Path: workspacePath}
	config := DefaultConfig()
	config.Root = workspaceRoot

	tests := []struct {
		name string
		cwd  string
		want string
	}{
		{
			name: "relative cwd is inconclusive",
			cwd:  "internal/workspace",
		},
		{
			name: "absolute cwd inside worktree path is worktree",
			cwd:  filepath.Join(workspacePath, "internal", "workspace"),
			want: plan.WorkspaceStrategyWorktree,
		},
		{
			name: "absolute cwd inside repo root outside worktree roots is current",
			cwd:  filepath.Join(repoRoot, "internal", "workspace"),
			want: plan.WorkspaceStrategyCurrent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verificationCWDWorkspaceStrategy(detail, tt.cwd, config); got != tt.want {
				t.Fatalf("verification cwd strategy = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExecutionPreparerBlocksMixedVerificationWorkspaces(t *testing.T) {
	repoRoot := t.TempDir()
	workspacePath := filepath.Join(repoRoot, ".tao", "workspaces", "plan-a")
	currentCWD := filepath.Join(repoRoot, "internal", "workspace")
	detail := executionPreparerPlanDetail(repoRoot)
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: workspacePath, LifecycleStatus: plan.WorkspaceStatusReady}
	detail.State.Plan.CompletedSlices = []string{"001-a"}
	detail.Slices.Slices = []plan.Slice{{
		ID:     "001-a",
		Status: plan.StatusCompleted,
		VerificationResults: []plan.VerificationRun{
			{Command: "go test ./internal/workspace", CWD: workspacePath, Result: "pass"},
			{Command: "go test ./internal/cli", CWD: currentCWD, Result: "pass"},
		},
	}}
	var calls []string

	_, err := (ExecutionPreparer{Runner: executionPreparerGitFake(&calls)}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err == nil {
		t.Fatal("expected mixed execution context drift error")
	}
	message := err.Error()
	for _, want := range []string{"multiple execution workspaces", workspacePath, "classified as worktree", currentCWD, "classified as current checkout", "slices.json"} {
		if !strings.Contains(message, want) {
			t.Fatalf("expected %q in error, got %q", want, message)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("expected guardrail before git calls, got %v", calls)
	}
}

func TestExecutionPreparerWritesPreparingThenReadyMetadata(t *testing.T) {
	createdAt := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	readyAt := createdAt.Add(time.Minute)
	repoRoot := t.TempDir()
	workspaceRoot := filepath.Join(t.TempDir(), "workspaces")
	workspacePath := filepath.Join(workspaceRoot, "plan-a")
	detail := executionPreparerPlanDetail(repoRoot)
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: workspaceRoot}
	var calls []string
	var statuses []string
	recordFactory := func(detail *plan.PlanDetail) (PlanRecord, error) {
		return persistStateFunc(func() error {
			statuses = append(statuses, detail.State.Workspace.LifecycleStatus)
			return nil
		}), nil
	}

	root, err := (ExecutionPreparer{Runner: executionPreparerGitFake(&calls), PlanRecordFactory: recordFactory, Now: executionPreparerClock(createdAt, readyAt)}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("prepare execution workspace: %v", err)
	}

	if root != workspacePath {
		t.Fatalf("expected worktree path %q, got %q", workspacePath, root)
	}
	if strings.Join(statuses, ",") != strings.Join([]string{plan.WorkspaceStatusPreparing, plan.WorkspaceStatusReady}, ",") {
		t.Fatalf("expected preparing then ready writes, got %v", statuses)
	}
	if detail.State.Workspace.Path != workspacePath || detail.State.Workspace.Branch != "tao/plan-a" || detail.State.Workspace.BaseSHA != "base123" || detail.State.Workspace.BaseCurrentSHA != "base123" || detail.State.Workspace.BaseStatus != "current" || detail.State.Workspace.HeadSHA != "head123" || detail.State.Workspace.RebaseStatus != "not_needed" || detail.State.Workspace.DependencyPreparation != "skipped" {
		t.Fatalf("unexpected workspace metadata: %#v", detail.State.Workspace)
	}
	if detail.State.Workspace.Timing.CreatedAt == nil || !detail.State.Workspace.Timing.CreatedAt.Equal(createdAt) {
		t.Fatalf("expected created timestamp %s, got %#v", createdAt, detail.State.Workspace.Timing.CreatedAt)
	}
	if detail.State.Workspace.Timing.PreparedAt == nil || !detail.State.Workspace.Timing.PreparedAt.Equal(readyAt) {
		t.Fatalf("expected ready timestamp %s, got %#v", readyAt, detail.State.Workspace.Timing.PreparedAt)
	}
	if !executionPreparerCallBefore(calls, "worktree add -b tao/plan-a "+workspacePath+" feature", "branch --show-current") {
		t.Fatalf("expected status after worktree add, got calls %v", calls)
	}
}

func TestPrepareRebasesStaleWorktreeBeforeRun(t *testing.T) {
	repo := newTestRepo(t)
	initialBase := gitHead(t, repo.path)
	detail := executionPreparerPlanDetail(repo.path)
	detail.State.Repo.Branch = "master"
	detail.State.Repo.BaseCommit = initialBase

	root, err := (ExecutionPreparer{}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("initial prepare: %v", err)
	}
	commitTestFile(t, root, "plan.txt", "plan work\n", "plan work")
	oldPlanHead := gitHead(t, root)
	commitTestFile(t, repo.path, "default.txt", "default work\n", "advance default")
	defaultHead := gitHead(t, repo.path)

	root, err = (ExecutionPreparer{}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("prepare stale worktree: %v", err)
	}

	if !gitIsAncestor(t, root, defaultHead, "HEAD") {
		t.Fatalf("expected plan branch to contain default branch head %s", defaultHead)
	}
	if head := gitHead(t, root); head == oldPlanHead || detail.State.Workspace.HeadSHA != head {
		t.Fatalf("expected refreshed rebased head, old=%s current=%s metadata=%s", oldPlanHead, head, detail.State.Workspace.HeadSHA)
	}
	if detail.State.Workspace.BaseBranch != "master" || detail.State.Workspace.BaseSHA != defaultHead || detail.State.Workspace.BaseCurrentSHA != defaultHead || detail.State.Workspace.BaseStatus != "current" || detail.State.Workspace.RebaseStatus != "not_needed" {
		t.Fatalf("expected current base metadata after rebase, got %#v", detail.State.Workspace)
	}
	if detail.State.Workspace.Timing.PreparedAt == nil || detail.State.Workspace.Timing.LastActivityAt == nil {
		t.Fatalf("expected workspace timing refresh, got %#v", detail.State.Workspace.Timing)
	}
	if detail.State.Repo.BaseCommit != initialBase {
		t.Fatalf("state.repo.base_commit changed: want %s got %s", initialBase, detail.State.Repo.BaseCommit)
	}
}

func TestPrepareSkipsRebaseWhenBranchContainsDefault(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	metadata, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	commitTestFile(t, metadata.Path, "plan.txt", "plan work\n", "plan work")
	commitTestFile(t, repo.path, "default.txt", "default work\n", "advance default")
	runGit(t, metadata.Path, "rebase", "master")
	currentHead := gitHead(t, metadata.Path)
	defaultHead := gitHead(t, repo.path)

	refreshed, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master", BaseSHA: metadata.BaseSHA, RebaseStale: true})
	if err != nil {
		t.Fatalf("prepare current worktree: %v", err)
	}

	if refreshed.Rebased {
		t.Fatalf("expected rebase skip when branch already contains default, got %#v", refreshed)
	}
	if refreshed.HeadSHA != currentHead || gitHead(t, metadata.Path) != currentHead {
		t.Fatalf("expected head to remain %s, got metadata=%s worktree=%s", currentHead, refreshed.HeadSHA, gitHead(t, metadata.Path))
	}
	if refreshed.BaseSHA != defaultHead || refreshed.BaseCurrentSHA != defaultHead || refreshed.RebaseStatus != "not_needed" {
		t.Fatalf("expected refreshed current base metadata, got %#v", refreshed)
	}
}

func TestPrepareRefusesDirtyStaleWorktree(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	metadata, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	commitTestFile(t, metadata.Path, "plan.txt", "plan work\n", "plan work")
	commitTestFile(t, repo.path, "default.txt", "default work\n", "advance default")
	if err := os.WriteFile(filepath.Join(metadata.Path, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatalf("write dirty file: %v", err)
	}
	beforeHead := gitHead(t, metadata.Path)
	defaultHead := gitHead(t, repo.path)

	_, err = manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master", BaseSHA: metadata.BaseSHA, RebaseStale: true})
	if err == nil {
		t.Fatal("expected dirty stale worktree refusal")
	}
	for _, want := range []string{"dirty", "pre-run rebase", "before agent execution"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error %q", want, err.Error())
		}
	}
	if head := gitHead(t, metadata.Path); head != beforeHead {
		t.Fatalf("dirty refusal changed head: want %s got %s", beforeHead, head)
	}
	if gitIsAncestor(t, metadata.Path, defaultHead, "HEAD") {
		t.Fatalf("dirty stale worktree should not have been rebased onto %s", defaultHead)
	}
}

func TestPrepareAbortsRebaseConflict(t *testing.T) {
	repo := newTestRepo(t)
	manager := newTestManager(t, repo.path)
	metadata, err := manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master"})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	commitTestFile(t, metadata.Path, "README.md", "# plan\n", "plan readme")
	beforeHead := gitHead(t, metadata.Path)
	commitTestFile(t, repo.path, "README.md", "# default\n", "default readme")

	_, err = manager.Prepare(context.Background(), PrepareOptions{PlanID: "plan-a", BaseBranch: "master", BaseSHA: metadata.BaseSHA, RebaseStale: true})
	if err == nil {
		t.Fatal("expected rebase conflict")
	}
	for _, want := range []string{"rebase/conflict phase", "aborted rebase", "before agent execution"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error %q", want, err.Error())
		}
	}
	if status := strings.TrimSpace(runGit(t, metadata.Path, "status", "--porcelain")); status != "" {
		t.Fatalf("expected abort to leave clean worktree, status=%q", status)
	}
	if head := gitHead(t, metadata.Path); head != beforeHead {
		t.Fatalf("expected abort to restore head %s, got %s", beforeHead, head)
	}
	content, err := os.ReadFile(filepath.Join(metadata.Path, "README.md"))
	if err != nil {
		t.Fatalf("read README: %v", err)
	}
	if string(content) != "# plan\n" {
		t.Fatalf("expected branch version after abort, got %q", string(content))
	}
}

func TestPrepareUsesDefaultBranchForWorkspaceBase(t *testing.T) {
	repo := newTestRepo(t)
	runGit(t, repo.path, "checkout", "-b", "feature")
	commitTestFile(t, repo.path, "feature.txt", "feature only\n", "feature work")
	runGit(t, repo.path, "checkout", "master")
	detail := executionPreparerPlanDetail(repo.path)
	detail.State.Repo.Branch = "feature"

	root, err := (ExecutionPreparer{}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if detail.State.Workspace.BaseBranch != "master" {
		t.Fatalf("expected local default base branch master, got %#v", detail.State.Workspace)
	}
	if _, err := os.Stat(filepath.Join(root, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected worktree from default branch without feature-only file, stat err=%v", err)
	}
}

func TestPrepareUsesDefaultBranchForWorkspaceBaseFallbackWhenRemoteDefaultMissing(t *testing.T) {
	repo := newTestRepo(t)
	runGit(t, repo.path, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, repo.path, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	detail := executionPreparerPlanDetail(repo.path)
	detail.State.Repo.Branch = "master"

	_, err := (ExecutionPreparer{}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if detail.State.Workspace.BaseBranch != "master" {
		t.Fatalf("expected fallback to recorded base branch master, got %#v", detail.State.Workspace)
	}
}

func TestPrepareUsesDefaultBranchForWorkspaceBaseFallbackWhenRemoteDefaultIsOnlyTag(t *testing.T) {
	repo := newTestRepo(t)
	runGit(t, repo.path, "tag", "--no-sign", "main")
	runGit(t, repo.path, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, repo.path, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	commitTestFile(t, repo.path, "master-only.txt", "default branch content\n", "advance master")
	detail := executionPreparerPlanDetail(repo.path)
	detail.State.Repo.Branch = "master"

	root, err := (ExecutionPreparer{}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	if detail.State.Workspace.BaseBranch != "master" {
		t.Fatalf("expected fallback to recorded base branch master, got %#v", detail.State.Workspace)
	}
	if _, err := os.Stat(filepath.Join(root, "master-only.txt")); err != nil {
		t.Fatalf("expected worktree from local master rather than tag named main: %v", err)
	}
}

func TestExecutionPreparerSkipsUnchangedDependenciesOnReuse(t *testing.T) {
	preparer, detail, workspacePath, installCalls := dependencyPreparerFixture(t, DefaultConfig(), nil)
	oldFingerprint := detail.State.Workspace.DependencyFingerprint

	root, err := preparer.Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("prepare reused workspace: %v", err)
	}
	if root != workspacePath || *installCalls != 1 {
		t.Fatalf("expected reused workspace without another install, root=%q installs=%d", root, *installCalls)
	}
	if detail.State.Workspace.LifecycleStatus != plan.WorkspaceStatusReady || detail.State.Workspace.DependencyPreparation != "skipped" {
		t.Fatalf("expected ready workspace with skipped dependencies, got %#v", detail.State.Workspace)
	}
	if detail.State.Workspace.DependencyFailure != "lockfile unchanged since last successful install" || detail.State.Workspace.DependencyFingerprint != oldFingerprint {
		t.Fatalf("unexpected unchanged-lockfile metadata: %#v", detail.State.Workspace)
	}
}

func TestExecutionPreparerSoftFailsChangedDependenciesOnReuse(t *testing.T) {
	installErr := false
	preparer, detail, workspacePath, installCalls := dependencyPreparerFixture(t, DefaultConfig(), func(stderr io.Writer) error {
		if !installErr {
			return nil
		}
		_, _ = io.WriteString(stderr, "registry unavailable")
		return errors.New("exit status 1")
	})
	oldFingerprint := detail.State.Workspace.DependencyFingerprint
	if err := os.WriteFile(filepath.Join(workspacePath, "package-lock.json"), []byte(`{"changed":true}`), 0o644); err != nil { //nolint:gosec // G306: test fixture file
		t.Fatal(err)
	}
	installErr = true

	root, err := preparer.Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("expected reused dependency failure to be downgraded: %v", err)
	}
	if root != workspacePath || *installCalls != 2 {
		t.Fatalf("expected changed lockfile install attempt, root=%q installs=%d", root, *installCalls)
	}
	if detail.State.Workspace.LifecycleStatus != plan.WorkspaceStatusReady || detail.State.Workspace.DependencyFailure != "registry unavailable" {
		t.Fatalf("expected ready workspace with dependency warning, got %#v", detail.State.Workspace)
	}
	if detail.State.Workspace.DependencyFingerprint != oldFingerprint {
		t.Fatalf("expected prior fingerprint %q to be retained, got %q", oldFingerprint, detail.State.Workspace.DependencyFingerprint)
	}
}

func TestExecutionPreparerFreshDependencyFailureIsHard(t *testing.T) {
	repo := newTestRepo(t)
	commitTestFile(t, repo.path, "package-lock.json", "{}\n", "add lockfile")
	detail := executionPreparerPlanDetail(repo.path)
	config := DefaultConfig()
	runner := dependencyPreparerRunner(func(stderr io.Writer) error {
		_, _ = io.WriteString(stderr, "install failed")
		return errors.New("exit status 1")
	}, new(int))

	_, err := (ExecutionPreparer{Runner: runner, Config: config}).Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err == nil {
		t.Fatal("expected fresh workspace dependency failure")
	}
	if detail.State.Workspace.LifecycleStatus != plan.WorkspaceStatusFailed || detail.State.Workspace.DependencyFailure != "install failed" {
		t.Fatalf("expected failed workspace metadata, got %#v", detail.State.Workspace)
	}
}

func TestExecutionPreparerRecordsSuccessfulDependencyFingerprint(t *testing.T) {
	_, detail, workspacePath, _ := dependencyPreparerFixture(t, DefaultConfig(), nil)
	want, err := dependencyLockfileFingerprint(workspacePath)
	if err != nil {
		t.Fatal(err)
	}
	if want == "" || detail.State.Workspace.DependencyFingerprint != want {
		t.Fatalf("dependency fingerprint = %q, want %q", detail.State.Workspace.DependencyFingerprint, want)
	}
}

func TestExecutionPreparerAlwaysInstallsWithMatchingFingerprint(t *testing.T) {
	config := DefaultConfig()
	config.DependencyInstallBehavior = DependencyInstallAlways
	preparer, detail, _, installCalls := dependencyPreparerFixture(t, config, nil)
	fingerprint := detail.State.Workspace.DependencyFingerprint

	if _, err := preparer.Prepare(context.Background(), detail, ExecutionPrepareOptions{}); err != nil {
		t.Fatalf("prepare reused workspace: %v", err)
	}
	if *installCalls != 2 {
		t.Fatalf("expected always behavior to install twice, got %d calls", *installCalls)
	}
	if detail.State.Workspace.DependencyFingerprint != fingerprint {
		t.Fatalf("fingerprint changed for unchanged lockfile: got %q want %q", detail.State.Workspace.DependencyFingerprint, fingerprint)
	}
}

func dependencyPreparerFixture(t *testing.T, config Config, install func(io.Writer) error) (ExecutionPreparer, *plan.PlanDetail, string, *int) {
	t.Helper()
	repo := newTestRepo(t)
	commitTestFile(t, repo.path, "package-lock.json", "{}\n", "add lockfile")
	detail := executionPreparerPlanDetail(repo.path)
	installCalls := new(int)
	preparer := ExecutionPreparer{Runner: dependencyPreparerRunner(install, installCalls), Config: config}
	workspacePath, err := preparer.Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("prepare initial workspace: %v", err)
	}
	return preparer, detail, workspacePath, installCalls
}

func dependencyPreparerRunner(install func(io.Writer) error, installCalls *int) CommandRunner {
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" {
			return defaultCommandRunner(ctx, cwd, name, args, stdout, stderr)
		}
		*installCalls++
		if install != nil {
			return install(stderr)
		}
		return nil
	}
}

// TestExecutionPreparerClearsDependencyFailureOnRetrySuccess verifies that a
// successful retry declares both writer-owned dependency clears while the
// merge-preserving state write retains unknown workspace metadata.
func TestExecutionPreparerClearsDependencyFailureOnRetrySuccess(t *testing.T) {
	planDir := t.TempDir()
	repoRoot := t.TempDir()
	workspaceRoot := t.TempDir()
	workspacePath := filepath.Join(workspaceRoot, "plan-a")

	// A custom command runs without a supported lockfile, making successful
	// install fingerprint evidence explicitly unknown.
	if err := os.MkdirAll(workspacePath, 0o755); err != nil { //nolint:gosec // G301: test workspace dir
		t.Fatal(err)
	}

	// Write an initial state.json so the real PlanRecord mutator can deep-merge into it.
	detail := executionPreparerPlanDetail(repoRoot)
	detail.Dir = planDir
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: workspaceRoot}
	record, err := plan.NewPlanRecord(planDir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistState(); err != nil {
		t.Fatal(err)
	}

	// Build a runner that fails npm ci the first call and succeeds the second.
	npmCallCount := 0
	var gitCalls []string
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" {
			return executionPreparerGitFake(&gitCalls)(ctx, cwd, name, args, stdout, stderr)
		}
		npmCallCount++
		if npmCallCount == 1 {
			_, _ = io.WriteString(stderr, "npm install: network error")
			return errors.New("exit status 1")
		}
		return nil
	}

	// Use the real PlanRecord mutator so persistence is exercised.
	realRecordFactory := func(detail *plan.PlanDetail) (PlanRecord, error) {
		return plan.NewPlanRecord(planDir, detail)
	}

	config := DefaultConfig()
	config.DependencyInstallBehavior = DependencyInstallCommand
	config.DependencyInstallCommand = "custom install"
	preparer := ExecutionPreparer{Runner: runner, PlanRecordFactory: realRecordFactory, Config: config}

	// First prepare: fails due to dependency error.
	_, err = preparer.Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err == nil {
		t.Fatal("expected first prepare to fail due to dependency error")
	}

	// Confirm the failure is persisted in state.json.
	state1, err := plan.ReadState(planDir)
	if err != nil {
		t.Fatalf("ReadState after failed prepare: %v", err)
	}
	if state1.Workspace == nil || state1.Workspace.DependencyFailure == "" {
		t.Fatalf("expected DependencyFailure set in state.json after failed prepare, got workspace=%#v", state1.Workspace)
	}

	// Seed prior fingerprint evidence and an unknown sibling in the persisted
	// workspace object before retrying.
	statePath := filepath.Join(planDir, "state.json")
	payload, err := os.ReadFile(statePath) //nolint:gosec // Test path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatal(err)
	}
	workspaceObject := raw["workspace"].(map[string]any)
	workspaceObject["dependency_fingerprint"] = "stale-fingerprint"
	workspaceObject["unknown_workspace"] = "keep"
	payload, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	detail.State.Workspace.DependencyFingerprint = "stale-fingerprint"

	// Second prepare: retry succeeds (custom command call count == 2 now).
	_, err = preparer.Prepare(context.Background(), detail, ExecutionPrepareOptions{})
	if err != nil {
		t.Fatalf("expected second prepare to succeed: %v", err)
	}

	// Confirm the failure is cleared in state.json.
	state2, err := plan.ReadState(planDir)
	if err != nil {
		t.Fatalf("ReadState after successful prepare: %v", err)
	}
	if state2.Workspace == nil {
		t.Fatal("expected State.Workspace non-nil after successful prepare")
	}
	if state2.Workspace.DependencyFailure != "" {
		t.Errorf("expected DependencyFailure cleared in state.json after retry success, got %q", state2.Workspace.DependencyFailure)
	}
	if state2.Workspace.DependencyFingerprint != "" {
		t.Errorf("expected DependencyFingerprint cleared when evidence is unknown, got %q", state2.Workspace.DependencyFingerprint)
	}
	payload, err = os.ReadFile(statePath) //nolint:gosec // Test path is rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatal(err)
	}
	workspaceObject = raw["workspace"].(map[string]any)
	if workspaceObject["dependency_preparation_failure"] != "" || workspaceObject["dependency_fingerprint"] != "" {
		t.Fatalf("expected explicit dependency clears in state.json, got %#v", workspaceObject)
	}
	if workspaceObject["unknown_workspace"] != "keep" {
		t.Fatalf("unknown workspace sibling was not preserved: %#v", workspaceObject)
	}
}

func executionPreparerPlanDetail(repoRoot string) *plan.PlanDetail {
	return &plan.PlanDetail{
		Dir: "/plans/plan-a",
		State: plan.State{
			Repo: plan.Repo{Root: repoRoot, Branch: "feature"},
			Plan: plan.PlanState{ID: "plan-a"},
		},
	}
}

type persistStateFunc func() error

func (f persistStateFunc) PersistState() error {
	return f()
}

func (f persistStateFunc) PersistStateChanges(_ *plan.ArtifactChangeSet) error {
	return f()
}

func executionPreparerGitFake(calls *[]string) CommandRunner {
	return func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name != "git" {
			return nil
		}
		key := executionPreparerGitKey(args)
		*calls = append(*calls, key)
		switch {
		case key == "rev-parse feature":
			_, _ = io.WriteString(stdout, "base123\n")
		case key == "worktree list --porcelain":
		case key == "rev-parse --verify tao/plan-a":
			return errors.New("not found")
		case strings.HasPrefix(key, "worktree add "):
		case key == "branch --show-current":
			_, _ = io.WriteString(stdout, "tao/plan-a\n")
		case key == "rev-parse HEAD":
			_, _ = io.WriteString(stdout, "head123\n")
		case key == "status --porcelain":
		}
		return nil
	}
}

func executionPreparerClock(times ...time.Time) func() time.Time {
	index := 0
	return func() time.Time {
		if index >= len(times) {
			return times[len(times)-1]
		}
		now := times[index]
		index++
		return now
	}
}

func executionPreparerGitKey(args []string) string {
	if len(args) >= 2 && args[0] == "-C" {
		args = args[2:]
	}
	return strings.Join(args, " ")
}

func executionPreparerHasGitCall(calls []string, want string) bool {
	return slices.Contains(calls, want)
}

func executionPreparerCallBefore(calls []string, before string, after string) bool {
	beforeIndex := -1
	afterIndex := -1
	for i, call := range calls {
		if call == before {
			beforeIndex = i
		}
		if call == after {
			afterIndex = i
		}
	}
	return beforeIndex >= 0 && afterIndex >= 0 && beforeIndex < afterIndex
}
