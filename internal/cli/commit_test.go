package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	taocommit "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
)

func TestCommitContextIsReadOnlyAndFinalizesStructuredProposal(t *testing.T) {
	root := newCLICommitRepo(t)
	writeCLICommitFile(t, root, "new/source.go", "package source\n")
	beforeIndex := runCLICommitGit(t, root, "diff", "--cached")

	var out bytes.Buffer
	app := App{Out: &out, Err: io.Discard}
	if err := app.Run(context.Background(), []string{"commit", "--context", "--repo-root", root}); err != nil {
		t.Fatal(err)
	}
	if afterIndex := runCLICommitGit(t, root, "diff", "--cached"); afterIndex != beforeIndex {
		t.Fatalf("context generation changed index\nbefore: %q\nafter:  %q", beforeIndex, afterIndex)
	}
	var commitContext taocommit.StandaloneContext
	if err := json.Unmarshal(out.Bytes(), &commitContext); err != nil {
		t.Fatalf("decode context: %v\n%s", err, out.String())
	}
	if strings.Join(commitContext.AllowedPaths, ",") != "new/source.go" || commitContext.Fingerprint == "" {
		t.Fatalf("context = %+v", commitContext)
	}

	proposal := taocommit.StandaloneProposal{
		ContextFingerprint: commitContext.Fingerprint,
		Proposal:           taocommit.Proposal{Type: "feat", Scope: "cli", Summary: "add standalone commit command", What: "Expose Tao's safe commit finalization through the CLI.", Why: "Let active agents propose messages while Tao owns Git mutation."},
	}
	proposalBytes, err := json.Marshal(proposal)
	if err != nil {
		t.Fatal(err)
	}
	proposalPath := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(proposalPath, proposalBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"commit", "--proposal-file", proposalPath, "--repo-root", root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Created local commit ") || !strings.Contains(out.String(), "feat(cli): add standalone commit command") {
		t.Fatalf("output = %q", out.String())
	}
	if got := strings.TrimSpace(runCLICommitGit(t, root, "show", "--name-only", "--format=", "HEAD")); got != "new/source.go" {
		t.Fatalf("committed paths = %q", got)
	}
}

func TestCommitRefusesManagedWorktreeBeforeContextOrGitMutation(t *testing.T) {
	control := newCLICommitRepo(t)
	worktree := filepath.Join(t.TempDir(), "managed")
	runCLICommitGit(t, control, "worktree", "add", "-b", "feature/managed", worktree)
	writeCLICommitFile(t, worktree, "managed.go", "package managed\n")
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	canonicalControl, err := filepath.EvalSymlinks(control)
	if err != nil {
		t.Fatal(err)
	}
	plansDir := filepath.Join(dataHome, "repos", taodata.RepoID(canonicalControl), "plans")
	planDir := writeRunPlan(t, plansDir, "plan-managed", plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
	configureManagedCommitPlan(t, planDir, control, worktree, "feature/managed", true)

	beforeHead := strings.TrimSpace(runCLICommitGit(t, worktree, "rev-parse", "HEAD"))
	beforeIndex := runCLICommitGit(t, worktree, "diff", "--cached")
	var out bytes.Buffer
	app := App{Out: &out, Err: io.Discard}
	err = app.Run(context.Background(), []string{"commit", "--context", "--repo-root", worktree})
	if err == nil || !strings.Contains(err.Error(), "active Tao-managed worktree") || !strings.Contains(err.Error(), "tao run --continue plan-managed") {
		t.Fatalf("managed context error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("managed context exposed output: %q", out.String())
	}
	if got := strings.TrimSpace(runCLICommitGit(t, worktree, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("HEAD = %q, want %q", got, beforeHead)
	}
	if got := runCLICommitGit(t, worktree, "diff", "--cached"); got != beforeIndex {
		t.Fatalf("index changed: %q", got)
	}

	message := "chore(cli): guard managed commits\n\nWhat:\nRefuse unsafe standalone commits.\n\nWhy:\nKeep workspace metadata synchronized."
	err = app.Run(context.Background(), []string{"commit", "--message", message, "--repo-root", worktree})
	if err == nil || !strings.Contains(err.Error(), "active Tao-managed worktree") {
		t.Fatalf("managed finalization error = %v", err)
	}
}

func TestCommitRefusalRendersCommandlessVerificationRecoveryInstruction(t *testing.T) {
	control := newCLICommitRepo(t)
	worktree := filepath.Join(t.TempDir(), "managed")
	runCLICommitGit(t, control, "worktree", "add", "-b", "feature/managed", worktree)
	writeCLICommitFile(t, worktree, "managed.go", "package managed\n")
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	canonicalControl, err := filepath.EvalSymlinks(control)
	if err != nil {
		t.Fatal(err)
	}
	plansDir := filepath.Join(dataHome, "repos", taodata.RepoID(canonicalControl), "plans")
	planDir := writeRunPlan(t, plansDir, "plan-managed", plan.StatusInReview, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	configureManagedCommitPlan(t, planDir, control, worktree, "feature/managed", false)

	statePath := filepath.Join(planDir, "state.json")
	content, err := os.ReadFile(statePath) //nolint:gosec // G304: test-controlled plan artifact path
	if err != nil {
		t.Fatal(err)
	}
	var state plan.State
	if err := json.Unmarshal(content, &state); err != nil {
		t.Fatal(err)
	}
	state.Plan.CurrentSlice = nil
	state.Workspace.HeadSHA = strings.TrimSpace(runCLICommitGit(t, worktree, "rev-parse", "HEAD"))
	state.Plan.FinalVerification = &plan.FinalVerification{
		Command: "make verify", HeadSHA: state.Workspace.HeadSHA, Result: "failed",
		FailureKind: plan.FinalVerificationFailureKindInvalidCommand, Fingerprint: "failure-a",
	}
	content, err = json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, append(content, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	err = (App{Out: io.Discard, Err: io.Discard}).Run(context.Background(), []string{"commit", "--context", "--repo-root", worktree})
	if err == nil || !strings.Contains(err.Error(), "active Tao-managed worktree") || !strings.Contains(err.Error(), "Correct the repository verification command") || strings.Contains(err.Error(), "--repair-verification") {
		t.Fatalf("commandless verification recovery error = %v", err)
	}
}

func TestCommitRefusesActiveManagedWorktreeAfterLiveBranchDrift(t *testing.T) {
	for _, tt := range []struct {
		name      string
		driftArgs []string
	}{
		{name: "switched branch", driftArgs: []string{"switch", "-c", "feature/switched"}},
		{name: "detached head", driftArgs: []string{"checkout", "--detach"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			control := newCLICommitRepo(t)
			worktree := filepath.Join(t.TempDir(), "managed")
			runCLICommitGit(t, control, "worktree", "add", "-b", "feature/managed", worktree)
			writeCLICommitFile(t, worktree, "managed.go", "package managed\n")

			dataHome := t.TempDir()
			t.Setenv("TAO_DATA_HOME", dataHome)
			canonicalControl, err := filepath.EvalSymlinks(control)
			if err != nil {
				t.Fatal(err)
			}
			plansDir := filepath.Join(dataHome, "repos", taodata.RepoID(canonicalControl), "plans")
			planDir := writeRunPlan(t, plansDir, "plan-managed", plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
			configureManagedCommitPlan(t, planDir, control, worktree, "feature/managed", false)
			runCLICommitGit(t, worktree, tt.driftArgs...)

			beforeHead := strings.TrimSpace(runCLICommitGit(t, worktree, "rev-parse", "HEAD"))
			beforeIndex := runCLICommitGit(t, worktree, "diff", "--cached")
			var out bytes.Buffer
			app := App{Out: &out, Err: io.Discard}
			for _, args := range [][]string{
				{"commit", "--context", "--repo-root", worktree},
				{"commit", "--message", "chore(cli): unsafe commit\n\nWhat:\nCommit drifted work.\n\nWhy:\nExercise the ownership guard.", "--repo-root", worktree},
			} {
				err := app.Run(context.Background(), args)
				if err == nil || !strings.Contains(err.Error(), "ownership cannot be safely resolved") || !strings.Contains(err.Error(), "plan-managed") {
					t.Fatalf("commit error = %v", err)
				}
			}
			if out.Len() != 0 {
				t.Fatalf("managed commit exposed output: %q", out.String())
			}
			if got := strings.TrimSpace(runCLICommitGit(t, worktree, "rev-parse", "HEAD")); got != beforeHead {
				t.Fatalf("HEAD = %q, want %q", got, beforeHead)
			}
			if got := runCLICommitGit(t, worktree, "diff", "--cached"); got != beforeIndex {
				t.Fatalf("index changed: %q", got)
			}
		})
	}
}

func TestCommitRefusesInvalidPlanAssociatedWithTargetWorktree(t *testing.T) {
	control := newCLICommitRepo(t)
	worktree := filepath.Join(t.TempDir(), "managed-invalid")
	runCLICommitGit(t, control, "worktree", "add", "-b", "feature/managed-invalid", worktree)
	writeCLICommitFile(t, worktree, "managed.go", "package managed\n")
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	canonicalControl, err := filepath.EvalSymlinks(control)
	if err != nil {
		t.Fatal(err)
	}
	plansDir := filepath.Join(dataHome, "repos", taodata.RepoID(canonicalControl), "plans")
	planDir := writeRunPlan(t, plansDir, "plan-invalid", plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
	configureManagedCommitPlan(t, planDir, control, worktree, "feature/managed-invalid", false)
	if err := os.WriteFile(filepath.Join(planDir, "slices.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	beforeHead := strings.TrimSpace(runCLICommitGit(t, worktree, "rev-parse", "HEAD"))
	beforeIndex := runCLICommitGit(t, worktree, "diff", "--cached")
	var out bytes.Buffer
	err = (App{Out: &out, Err: io.Discard}).Run(context.Background(), []string{"commit", "--context", "--repo-root", worktree})
	if err == nil || !strings.Contains(err.Error(), "ownership cannot be safely resolved") || !strings.Contains(err.Error(), "plan-invalid") {
		t.Fatalf("invalid managed plan error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("invalid managed plan exposed context: %q", out.String())
	}
	if got := strings.TrimSpace(runCLICommitGit(t, worktree, "rev-parse", "HEAD")); got != beforeHead {
		t.Fatalf("HEAD = %q, want %q", got, beforeHead)
	}
	if got := runCLICommitGit(t, worktree, "diff", "--cached"); got != beforeIndex {
		t.Fatalf("index changed: %q", got)
	}
}

func TestCommitAllowsUnrelatedWorktreeWithStaleManagedMetadata(t *testing.T) {
	control := newCLICommitRepo(t)
	unrelated := filepath.Join(t.TempDir(), "unrelated")
	runCLICommitGit(t, control, "worktree", "add", "-b", "feature/unrelated", unrelated)
	writeCLICommitFile(t, unrelated, "other.go", "package other\n")
	dataHome := t.TempDir()
	t.Setenv("TAO_DATA_HOME", dataHome)
	canonicalControl, err := filepath.EvalSymlinks(control)
	if err != nil {
		t.Fatal(err)
	}
	plansDir := filepath.Join(dataHome, "repos", taodata.RepoID(canonicalControl), "plans")
	planDir := writeRunPlan(t, plansDir, "stale-plan", plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
	configureManagedCommitPlan(t, planDir, control, filepath.Join(t.TempDir(), "removed-worktree"), "feature/stale", false)

	message := "chore(cli): keep unrelated commits\n\nWhat:\nCommit work in an unrelated worktree.\n\nWhy:\nStale plan metadata must not claim a different exact path."
	var out bytes.Buffer
	if err := (App{Out: &out, Err: io.Discard}).Run(context.Background(), []string{"commit", "--message", message, "--repo-root", unrelated}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Created local commit") {
		t.Fatalf("output = %q", out.String())
	}
}

func TestCommitMessageOverrideUsesCentralValidationAndReportsSafeNoOp(t *testing.T) {
	root := newCLICommitRepo(t)
	writeCLICommitFile(t, root, ".env.example", "TOKEN=replace-me\n")
	message := "chore(cli): check standalone commit safety\n\nWhat:\nApply live safety filtering to explicit commit messages.\n\nWhy:\nCompatibility overrides must not bypass Tao's path authority."
	var out bytes.Buffer
	app := App{Out: &out, Err: io.Discard}
	if err := app.Run(context.Background(), []string{"commit", "--message", message, "--repo-root", root}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Nothing to commit: no allowed changes.\n" {
		t.Fatalf("output = %q", got)
	}
	if got := strings.TrimSpace(runCLICommitGit(t, root, "rev-list", "--count", "HEAD")); got != "1" {
		t.Fatalf("commit count = %q, want 1", got)
	}
}

func TestCommitRejectsInvalidProposalBeforeGitCommands(t *testing.T) {
	path := filepath.Join(t.TempDir(), "proposal.json")
	if err := os.WriteFile(path, []byte(`{"context_fingerprint":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","type":"wip","scope":"cli","summary":"add invalid proposal","what":"What.","why":"Why."}`), 0o600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	app := App{
		Out: io.Discard,
		Err: io.Discard,
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			calls++
			return nil
		},
	}
	err := app.Run(context.Background(), []string{"commit", "--proposal-file", path})
	if err == nil || !strings.Contains(err.Error(), "unsupported commit type") {
		t.Fatalf("expected proposal validation error, got %v", err)
	}
	if calls != 0 {
		t.Fatalf("invalid proposal invoked %d Git commands", calls)
	}
}

func configureManagedCommitPlan(t *testing.T, planDir, repoRoot, worktree, branch string, blocked bool) {
	t.Helper()
	path := filepath.Join(planDir, "state.json")
	content, err := os.ReadFile(path) //nolint:gosec // G304: test-controlled plan artifact path
	if err != nil {
		t.Fatal(err)
	}
	var state plan.State
	if err := json.Unmarshal(content, &state); err != nil {
		t.Fatal(err)
	}
	current := "001-a"
	state.Repo.Root = repoRoot
	state.Repo.Branch = "main"
	state.Plan.CurrentSlice = &current
	state.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Path: worktree, Branch: branch}
	if blocked {
		state.Status = plan.StatusBlocked
	}
	content, err = json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(content, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if !blocked {
		return
	}
	slicesPath := filepath.Join(planDir, "slices.json")
	content, err = os.ReadFile(slicesPath) //nolint:gosec // G304: test-controlled plan artifact path
	if err != nil {
		t.Fatal(err)
	}
	var slicesFile plan.SlicesFile
	if err := json.Unmarshal(content, &slicesFile); err != nil {
		t.Fatal(err)
	}
	slicesFile.Slices[0].Status = plan.StatusBlocked
	content, err = json.MarshalIndent(slicesFile, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(slicesPath, append(content, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func newCLICommitRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runCLICommitGit(t, root, "init", "-b", "main")
	runCLICommitGit(t, root, "config", "user.name", "Tao Test")
	runCLICommitGit(t, root, "config", "user.email", "tao@example.invalid")
	writeCLICommitFile(t, root, "README.md", "initial\n")
	runCLICommitGit(t, root, "add", "README.md")
	runCLICommitGit(t, root, "commit", "-m", "chore(test): initialize repository")
	return root
}

func writeCLICommitFile(t *testing.T, root, name, contents string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runCLICommitGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...) //nolint:gosec // fixed test binary with test-controlled arguments
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
	}
	return string(output)
}
