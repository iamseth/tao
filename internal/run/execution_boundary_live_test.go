package run

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

type rebaseRecoveryRecord struct {
	PlanMutationRecord
	detail  *plan.PlanDetail
	cleared bool
}

func (r *rebaseRecoveryRecord) ClearWorkspaceRebaseIntent(expected plan.WorkspaceRebaseIntent) error {
	if r.detail.State.Workspace.RebaseIntent == nil || *r.detail.State.Workspace.RebaseIntent != expected {
		return os.ErrInvalid
	}
	r.detail.State.Workspace.RebaseIntent = nil
	r.cleared = true
	return nil
}

func (r *rebaseRecoveryRecord) SettleWorkspaceRebase(expected plan.WorkspaceRebaseIntent, settlement plan.WorkspaceRebaseSettlement) error {
	if r.detail.State.Workspace.RebaseIntent == nil || *r.detail.State.Workspace.RebaseIntent != expected {
		return os.ErrInvalid
	}
	workspace := r.detail.State.Workspace
	workspace.Branch = settlement.Branch
	workspace.BaseSHA = settlement.BaseSHA
	workspace.BaseCurrentSHA = settlement.BaseCurrentSHA
	workspace.HeadSHA = settlement.HeadSHA
	workspace.BaseStatus = settlement.BaseStatus
	workspace.RefreshStatus = settlement.RefreshStatus
	workspace.RebaseStatus = settlement.RebaseStatus
	workspace.LifecycleStatus = settlement.LifecycleStatus
	workspace.RebaseIntent = nil
	r.cleared = true
	return nil
}

func TestInspectRecordedWorkspaceRecoversIntentPersistedBeforeGitMutation(t *testing.T) {
	repo, oldBase, oldHead, newBase := newRebaseRecoveryRepo(t, false)
	intent := rebaseRecoveryIntent(t, repo, oldBase, oldHead, newBase)
	detail, record := rebaseRecoveryDetail(intent)

	err := recoverWorkspaceRebaseIntent(context.Background(), detail, runExecution{Dependencies: RunDependencies{
		PlanRecordFactory: func(*plan.PlanDetail) (PlanMutationRecord, error) { return record, nil },
	}}, gitops.NewClient(repo, nil), "ready workspace", intent.Branch, oldHead, InterruptedSliceFacts{})
	if err != nil {
		t.Fatal(err)
	}
	if !record.cleared || detail.State.Workspace.RebaseIntent != nil {
		t.Fatal("expected exact untouched rebase intent to be cleared")
	}
	if detail.State.Workspace.HeadSHA != oldHead || detail.State.Workspace.BaseSHA != oldBase {
		t.Fatalf("untouched recovery changed workspace boundary: %#v", detail.State.Workspace)
	}
}

func TestInspectRecordedWorkspaceRecoversIntentAfterConflictAbort(t *testing.T) {
	repo, oldBase, oldHead, newBase := newRebaseRecoveryRepo(t, true)
	intent := rebaseRecoveryIntent(t, repo, oldBase, oldHead, newBase)
	if err := runRebaseRecoveryGitError(repo, "rebase", "master"); err == nil {
		t.Fatal("expected conflicting rebase to fail")
	}
	runRebaseRecoveryGit(t, repo, "rebase", "--abort")
	if got := rebaseRecoveryGitOutput(t, repo, "rev-parse", "HEAD"); got != oldHead {
		t.Fatalf("rebase abort restored HEAD %s, want %s", got, oldHead)
	}
	detail, record := rebaseRecoveryDetail(intent)

	err := recoverWorkspaceRebaseIntent(context.Background(), detail, runExecution{Dependencies: RunDependencies{
		PlanRecordFactory: func(*plan.PlanDetail) (PlanMutationRecord, error) { return record, nil },
	}}, gitops.NewClient(repo, nil), "ready workspace", intent.Branch, oldHead, InterruptedSliceFacts{})
	if err != nil {
		t.Fatal(err)
	}
	if !record.cleared || detail.State.Workspace.RebaseIntent != nil {
		t.Fatal("expected successfully aborted rebase intent to be cleared")
	}
}

func TestInspectRecordedWorkspaceRecoversRebaseIntentForNonePolicy(t *testing.T) {
	tests := []struct {
		name          string
		rebaseBefore  bool
		wantBaseAtEnd bool
	}{
		{name: "before git mutation"},
		{name: "after git mutation before settlement", rebaseBefore: true, wantBaseAtEnd: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repoRoot, workspaceRoot, oldBase, oldHead, newBase := newLinkedRebaseRecoveryRepo(t)
			intent := rebaseRecoveryIntent(t, workspaceRoot, oldBase, oldHead, newBase)
			detail, record := rebaseRecoveryDetail(intent)
			detail.State.Repo.Root = repoRoot
			detail.State.Workspace.Root = filepath.Dir(workspaceRoot)
			detail.State.Workspace.Path = workspaceRoot
			if tt.rebaseBefore {
				runRebaseRecoveryGit(t, workspaceRoot, "-c", "commit.gpgSign=false", "rebase", "--onto", newBase, oldBase)
			}
			execution := runExecution{
				Config:       ExecutionConfig{ResolvedRunOptions: ResolvedRunOptions{CommitPolicy: CommitPolicyNone, ExecutionMode: ExecutionModeIsolated}},
				Dependencies: RunDependencies{PlanRecordFactory: func(*plan.PlanDetail) (PlanMutationRecord, error) { return record, nil }},
			}

			if err := inspectRecordedWorkspaceBeforeAutomaticStart(context.Background(), detail, execution); err != nil {
				t.Fatal(err)
			}
			if !record.cleared || detail.State.Workspace.RebaseIntent != nil {
				t.Fatal("expected none-policy run to settle the durable rebase intent")
			}
			wantHead, wantBase := oldHead, oldBase
			if tt.wantBaseAtEnd {
				wantHead = rebaseRecoveryGitOutput(t, workspaceRoot, "rev-parse", "HEAD")
				wantBase = newBase
			}
			if detail.State.Workspace.HeadSHA != wantHead || detail.State.Workspace.BaseSHA != wantBase {
				t.Fatalf("recovered boundary = head %s base %s, want head %s base %s", detail.State.Workspace.HeadSHA, detail.State.Workspace.BaseSHA, wantHead, wantBase)
			}
		})
	}
}

func newRebaseRecoveryRepo(t *testing.T, conflict bool) (repo, oldBase, oldHead, newBase string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	repo = t.TempDir()
	runRebaseRecoveryGit(t, repo, "init", "-b", "master")
	runRebaseRecoveryGit(t, repo, "config", "user.name", "Tao Test")
	runRebaseRecoveryGit(t, repo, "config", "user.email", "tao@example.com")
	writeRebaseRecoveryFile(t, repo, "shared.txt", "base\n")
	runRebaseRecoveryGit(t, repo, "add", "shared.txt")
	runRebaseRecoveryGit(t, repo, "commit", "-m", "base")
	oldBase = rebaseRecoveryGitOutput(t, repo, "rev-parse", "HEAD")
	runRebaseRecoveryGit(t, repo, "switch", "-c", "tao/plan-a")
	featurePath := "feature.txt"
	if conflict {
		featurePath = "shared.txt"
	}
	writeRebaseRecoveryFile(t, repo, featurePath, "feature\n")
	runRebaseRecoveryGit(t, repo, "add", featurePath)
	runRebaseRecoveryGit(t, repo, "commit", "-m", "feature work")
	oldHead = rebaseRecoveryGitOutput(t, repo, "rev-parse", "HEAD")
	runRebaseRecoveryGit(t, repo, "switch", "master")
	basePath := "base.txt"
	if conflict {
		basePath = "shared.txt"
	}
	writeRebaseRecoveryFile(t, repo, basePath, "default\n")
	runRebaseRecoveryGit(t, repo, "add", basePath)
	runRebaseRecoveryGit(t, repo, "commit", "-m", "advance default")
	newBase = rebaseRecoveryGitOutput(t, repo, "rev-parse", "HEAD")
	runRebaseRecoveryGit(t, repo, "switch", "tao/plan-a")
	return repo, oldBase, oldHead, newBase
}

func newLinkedRebaseRecoveryRepo(t *testing.T) (repoRoot, workspaceRoot, oldBase, oldHead, newBase string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_TERMINAL_PROMPT", "0")
	repoRoot = t.TempDir()
	workspaceRoot = filepath.Join(t.TempDir(), "plan-a")
	runRebaseRecoveryGit(t, repoRoot, "init", "-b", "master")
	runRebaseRecoveryGit(t, repoRoot, "config", "user.name", "Tao Test")
	runRebaseRecoveryGit(t, repoRoot, "config", "user.email", "tao@example.com")
	writeRebaseRecoveryFile(t, repoRoot, "shared.txt", "base\n")
	runRebaseRecoveryGit(t, repoRoot, "add", "shared.txt")
	runRebaseRecoveryGit(t, repoRoot, "commit", "-m", "base")
	oldBase = rebaseRecoveryGitOutput(t, repoRoot, "rev-parse", "HEAD")
	runRebaseRecoveryGit(t, repoRoot, "worktree", "add", "-b", "tao/plan-a", workspaceRoot, oldBase)
	writeRebaseRecoveryFile(t, workspaceRoot, "feature.txt", "feature\n")
	runRebaseRecoveryGit(t, workspaceRoot, "add", "feature.txt")
	runRebaseRecoveryGit(t, workspaceRoot, "commit", "-m", "feature work")
	oldHead = rebaseRecoveryGitOutput(t, workspaceRoot, "rev-parse", "HEAD")
	writeRebaseRecoveryFile(t, repoRoot, "base.txt", "default\n")
	runRebaseRecoveryGit(t, repoRoot, "add", "base.txt")
	runRebaseRecoveryGit(t, repoRoot, "commit", "-m", "advance default")
	newBase = rebaseRecoveryGitOutput(t, repoRoot, "rev-parse", "HEAD")
	return repoRoot, workspaceRoot, oldBase, oldHead, newBase
}

func rebaseRecoveryIntent(t *testing.T, repo, oldBase, oldHead, newBase string) plan.WorkspaceRebaseIntent {
	t.Helper()
	proof, err := gitops.NewClient(repo, nil).CommitSeriesRebaseProof(context.Background(), oldBase, newBase, oldBase, oldHead)
	if err != nil {
		t.Fatal(err)
	}
	return plan.WorkspaceRebaseIntent{
		Branch: "tao/plan-a", BaseBranch: "master", OldHeadSHA: oldHead, OldBaseSHA: oldBase,
		NewBaseSHA: newBase, CommitCount: proof.Count, CommitSeriesFingerprint: proof.Fingerprint,
		CreatedAt: time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC),
	}
}

func rebaseRecoveryDetail(intent plan.WorkspaceRebaseIntent) (*plan.PlanDetail, *rebaseRecoveryRecord) {
	detail := &plan.PlanDetail{State: plan.State{Plan: plan.PlanState{ID: "plan-a"}, Workspace: &plan.Workspace{
		Strategy: plan.WorkspaceStrategyWorktree, LifecycleStatus: plan.WorkspaceStatusReady,
		Branch: intent.Branch, HeadSHA: intent.OldHeadSHA, BaseSHA: intent.OldBaseSHA, RebaseIntent: &intent,
	}}}
	record := &rebaseRecoveryRecord{PlanMutationRecord: memoryPlanMutationRecord{detail: detail}, detail: detail}
	return detail, record
}

func writeRebaseRecoveryFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runRebaseRecoveryGit(t *testing.T, cwd string, args ...string) {
	t.Helper()
	if err := runRebaseRecoveryGitError(cwd, args...); err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
}

func runRebaseRecoveryGitError(cwd string, args ...string) error {
	cmd := exec.Command("git", args...) // #nosec G204 -- fixed test Git command with test-controlled arguments.
	cmd.Dir = cwd
	return cmd.Run()
}

func rebaseRecoveryGitOutput(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- fixed test Git command with test-controlled arguments.
	cmd.Dir = cwd
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, stderr.String())
	}
	return string(bytes.TrimSpace(stdout.Bytes()))
}
