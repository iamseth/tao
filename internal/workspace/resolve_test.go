package workspace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestResolveExecutionRoot(t *testing.T) {
	const repoRoot = "/repo/root"
	const planID = "plan-a"
	const worktreePath = "/repo/worktrees/plan-a"

	currentConfig := DefaultConfig()
	currentConfig.Strategy = StrategyCurrent
	customConfig := DefaultConfig()
	customConfig.Root = "/srv/worktrees"
	blankStrategyConfig := DefaultConfig()
	blankStrategyConfig.Strategy = ""

	tests := []struct {
		name    string
		detail  *plan.PlanDetail
		config  Config
		want    ExecutionRootIdentity
		wantErr string
	}{
		{
			name:    "nil detail errors like review and prepare",
			config:  DefaultConfig(),
			wantErr: "plan detail is nil",
		},
		{
			name:   "nil workspace uses caller-derived current strategy",
			detail: executionRootDetail(repoRoot, planID, nil),
			config: currentConfig,
			want:   ExecutionRootIdentity{Root: repoRoot, Strategy: plan.WorkspaceStrategyCurrent},
		},
		{
			name:   "nil workspace uses caller-derived worktree strategy",
			detail: executionRootDetail(repoRoot, planID, nil),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: filepath.Join(repoRoot, ".tao", "workspaces", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "current strategy returns trimmed repo root and wins over config",
			detail: executionRootDetail("  "+repoRoot+"  ", planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyCurrent, Path: worktreePath}),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: repoRoot, Strategy: plan.WorkspaceStrategyCurrent},
		},
		{
			name:   "empty persisted strategy uses caller-derived worktree default",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Path: worktreePath}),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: worktreePath, Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:    "empty persisted strategy requires caller default",
			detail:  executionRootDetail(repoRoot, planID, &plan.Workspace{Path: worktreePath}),
			config:  blankStrategyConfig,
			wantErr: "workspace strategy is required",
		},
		{
			name:   "worktree recorded path wins over root and config",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: worktreePath + "/", Root: "custom/worktrees"}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: worktreePath, Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "worktree recorded relative path resolves under repo root",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: ".tao/workspaces/" + planID}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: filepath.Join(repoRoot, ".tao", "workspaces", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "worktree recorded absolute root wins over config",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: "/opt/tao/worktrees"}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: filepath.Join("/opt/tao/worktrees", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "worktree recorded relative root resolves under repo root",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: "custom/worktrees"}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: filepath.Join(repoRoot, "custom", "worktrees", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:   "worktree without path or root falls back to default config root",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: filepath.Join(repoRoot, ".tao", "workspaces", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			// Legacy review resolution has no runtime config and would fall back to
			// the repo root for this shape. The unified resolver intentionally uses
			// the caller-supplied config root so custom-root worktree plans resolve to
			// the path Manager would create.
			name:   "worktree without path or root honors custom config root",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}),
			config: customConfig,
			want:   ExecutionRootIdentity{Root: filepath.Join("/srv/worktrees", planID), Strategy: plan.WorkspaceStrategyWorktree, Separate: true},
		},
		{
			name:    "empty plan id without recorded path or root errors",
			detail:  executionRootDetail(repoRoot, "", &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}),
			config:  DefaultConfig(),
			wantErr: "worktree root could not be resolved",
		},
		{
			name:   "repo root equals worktree path is not separate",
			detail: executionRootDetail(repoRoot, planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: repoRoot}),
			config: DefaultConfig(),
			want:   ExecutionRootIdentity{Root: repoRoot, Strategy: plan.WorkspaceStrategyWorktree, Separate: false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveExecutionRoot(tt.detail, tt.config)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ResolveExecutionRoot error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveExecutionRoot returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("ResolveExecutionRoot() = %#v, want %#v", got, tt.want)
			}
			if tt.detail != nil && tt.detail.State.Workspace != nil && tt.detail.State.Workspace.Strategy == plan.WorkspaceStrategyWorktree {
				worktree := ResolvePlanWorktree(tt.detail, tt.config)
				if got.Root != worktree.Path || got.Separate != worktree.Separate {
					t.Fatalf("ResolveExecutionRoot() = %#v, ResolvePlanWorktree() = %#v", got, worktree)
				}
			}
		})
	}
}

func TestPhysicalPathResolvesSymlinkedRoot(t *testing.T) {
	physicalRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "workspace-alias")
	if err := os.Symlink(physicalRoot, alias); err != nil {
		t.Fatal(err)
	}

	got, err := PhysicalPath(alias)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(physicalRoot) {
		t.Fatalf("PhysicalPath() = %q, want %q", got, physicalRoot)
	}
}

func TestResolveManagedWorktreeOwnershipRequiresCanonicalExactIdentity(t *testing.T) {
	repoRoot := t.TempDir()
	worktree := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(worktree, alias); err != nil {
		t.Fatal(err)
	}
	detail := executionRootDetail(repoRoot, "plan-a", &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: alias, Branch: "feature/plan-a"})
	detail.State.Status = plan.StatusInProgress

	owner, err := ResolveManagedWorktreeOwnership(repoRoot, worktree, "feature/plan-a", []*plan.PlanDetail{detail})
	if err != nil {
		t.Fatal(err)
	}
	if owner == nil || owner.PlanID != "plan-a" || owner.Command != "tao run plan-a" {
		t.Fatalf("ownership = %#v", owner)
	}
	if owner, err := ResolveManagedWorktreeOwnership(repoRoot, t.TempDir(), "feature/plan-a", []*plan.PlanDetail{detail}); err != nil || owner != nil {
		t.Fatalf("unrelated worktree ownership = %#v, %v", owner, err)
	}
	for _, branch := range []string{"other", ""} {
		owner, err := ResolveManagedWorktreeOwnership(repoRoot, worktree, branch, []*plan.PlanDetail{detail})
		if err == nil || owner != nil || !strings.Contains(err.Error(), "cannot be safely resolved") || !strings.Contains(err.Error(), "plan-a") {
			t.Fatalf("drifted branch %q ownership = %#v, %v", branch, owner, err)
		}
	}
}

func TestUnresolvedInvalidManagedWorktreeOwnersFailsClosedAndDisprovesExactMismatch(t *testing.T) {
	repoRoot := t.TempDir()
	worktree := t.TempDir()
	otherWorktree := t.TempDir()
	writeState := func(id, path, branch string) plan.PlanSummary {
		t.Helper()
		dir := filepath.Join(t.TempDir(), id)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		state := plan.State{
			Repo:      plan.Repo{Root: repoRoot},
			Plan:      plan.PlanState{ID: id},
			Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: path, Branch: branch},
		}
		content, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "state.json"), content, 0o600); err != nil {
			t.Fatal(err)
		}
		return plan.PlanSummary{ID: id, Dir: dir, Status: plan.StatusInvalid}
	}
	matching := writeState("matching", worktree, "feature/target")
	unrelated := writeState("unrelated", otherWorktree, "feature/other")
	unreadableDir := filepath.Join(t.TempDir(), "unreadable")
	if err := os.Mkdir(unreadableDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(unreadableDir, "state.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadable := plan.PlanSummary{ID: "unreadable", Dir: unreadableDir, Status: plan.StatusInvalid}

	for _, branch := range []string{"feature/target", "feature/switched", ""} {
		got, err := UnresolvedInvalidManagedWorktreeOwners(repoRoot, worktree, branch, []plan.PlanSummary{unrelated, unreadable, matching})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(got, ",") != "matching,unreadable" {
			t.Fatalf("branch %q unresolved owners = %v, want matching and unreadable", branch, got)
		}
	}
}

func TestManagedWorktreeRecoveryCommandRunsPendingVerificationRepair(t *testing.T) {
	detail := executionRootDetail("/repo", "plan-a", &plan.Workspace{HeadSHA: "head-a"})
	detail.State.Plan.FinalVerification = &plan.FinalVerification{
		Command:     "make verify",
		HeadSHA:     "head-a",
		Result:      "failed",
		Fingerprint: "failure-a",
	}
	detail.Slices.Slices = []plan.Slice{{
		ID:     "vr01-final-verification-failure-a",
		Status: plan.StatusPending,
		VerificationRepair: &plan.VerificationRepairBinding{
			Command:     "make verify",
			HeadSHA:     "head-a",
			Fingerprint: "failure-a",
		},
	}}

	if got := managedWorktreeRecoveryCommand(detail); got != "tao run plan-a" {
		t.Fatalf("managedWorktreeRecoveryCommand() = %q, want ordinary run after repair creation", got)
	}
}

func TestManagedWorktreeRecoveryCommandBoundsBlockedRecovery(t *testing.T) {
	const planID = "plan-a"
	boundary := &plan.SliceExecutionStart{
		Branch: "feature/plan-a", Head: "base-a", CommitPolicy: "slice", WorkspaceStrategy: plan.WorkspaceStrategyWorktree,
	}
	tests := []struct {
		name   string
		mutate func(*plan.PlanDetail, *plan.Slice)
		want   string
	}{
		{
			name: "isolated automatic pre-intent boundary may restart",
			mutate: func(_ *plan.PlanDetail, slice *plan.Slice) {
				slice.ExecutionRoot = "/worktrees/plan-a"
				slice.ExecutionStart = boundary
			},
			want: "tao run --restart plan-a",
		},
		{
			name: "ordinary blocked slice continues",
			want: "tao run --continue plan-a",
		},
		{
			name: "manual policy uses completion recovery",
			mutate: func(detail *plan.PlanDetail, slice *plan.Slice) {
				detail.State.Plan.LastRunCommitPolicy = "none"
				slice.ExecutionRoot = "/worktrees/plan-a"
			},
			want: "tao slice-complete",
		},
		{
			name: "current checkout uses completion recovery",
			mutate: func(detail *plan.PlanDetail, slice *plan.Slice) {
				detail.State.Workspace.Strategy = plan.WorkspaceStrategyCurrent
				slice.ExecutionRoot = "/repo"
			},
			want: "tao slice-complete",
		},
		{
			name: "post-intent automatic slice uses completion recovery",
			mutate: func(_ *plan.PlanDetail, slice *plan.Slice) {
				slice.ExecutionRoot = "/worktrees/plan-a"
				slice.ExecutionStart = boundary
				slice.CommitIntent = &plan.SliceCommitIntent{Policy: "slice"}
			},
			want: "tao slice-complete",
		},
		{
			name: "incomplete automatic metadata continues instead of suggesting restart",
			mutate: func(_ *plan.PlanDetail, slice *plan.Slice) {
				slice.ExecutionRoot = "/worktrees/plan-a"
				slice.ExecutionStart = &plan.SliceExecutionStart{CommitPolicy: "slice", WorkspaceStrategy: plan.WorkspaceStrategyWorktree}
			},
			want: "tao run --continue plan-a",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := "001-a"
			detail := executionRootDetail("/repo", planID, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree})
			detail.State.Status = plan.StatusBlocked
			detail.State.Plan.CurrentSlice = &current
			detail.Slices.Slices = []plan.Slice{{ID: current, Status: plan.StatusBlocked}}
			if tt.mutate != nil {
				tt.mutate(detail, &detail.Slices.Slices[0])
			}
			if got := managedWorktreeRecoveryCommand(detail); got != tt.want {
				t.Fatalf("managedWorktreeRecoveryCommand() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveManagedWorktreeOwnershipIgnoresCleanedAndFailsClosedForMultiplePlans(t *testing.T) {
	repoRoot := t.TempDir()
	worktree := t.TempDir()
	makeDetail := func(id string) *plan.PlanDetail {
		detail := executionRootDetail(repoRoot, id, &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: worktree, Branch: "feature/shared"})
		detail.State.Status = plan.StatusInProgress
		return detail
	}
	cleaned := makeDetail("cleaned")
	cleaned.State.Workspace.CleanupStatus = plan.WorkspaceCleanupStatusDone
	if owner, err := ResolveManagedWorktreeOwnership(repoRoot, worktree, "feature/shared", []*plan.PlanDetail{cleaned}); err != nil || owner != nil {
		t.Fatalf("cleaned ownership = %#v, %v", owner, err)
	}
	_, err := ResolveManagedWorktreeOwnership(repoRoot, worktree, "feature/shared", []*plan.PlanDetail{makeDetail("plan-a"), makeDetail("plan-b")})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") || !strings.Contains(err.Error(), "plan-a, plan-b") {
		t.Fatalf("ambiguous ownership error = %v", err)
	}
}

func TestResolveRecordedWorktreeSupportsLegacyMetadata(t *testing.T) {
	detail := executionRootDetail("/repo", "plan-a", &plan.Workspace{Root: ".tao/workspaces"})
	got := ResolveRecordedWorktree(detail)
	want := filepath.Join("/repo", ".tao/workspaces", "plan-a")
	if got.Path != want || !got.Separate {
		t.Fatalf("ResolveRecordedWorktree() = %#v, want path %q separate", got, want)
	}
}

func executionRootDetail(repoRoot string, planID string, workspace *plan.Workspace) *plan.PlanDetail {
	return &plan.PlanDetail{
		State: plan.State{
			Repo:      plan.Repo{Root: repoRoot},
			Plan:      plan.PlanState{ID: planID},
			Workspace: workspace,
		},
	}
}
