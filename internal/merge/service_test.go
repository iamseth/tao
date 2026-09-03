package merge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

type fakeGitClient struct {
	root                string
	defaultBranch       string
	defaultErr          error
	currentBranch       string
	currentBranchErr    error
	revParse            map[string]string
	revParseSequence    map[string][]string
	revParseErrors      map[string][]error
	revParseErr         error
	commitMessages      map[string]string
	commitMessageErr    error
	commitPathStates    []gitops.CommitPathState
	commitPathStateErr  error
	mergeBase           string
	mergeErr            error
	ancestors           map[string]bool
	ancestorErr         error
	status              string
	statusErr           error
	ignoredStatus       string
	ignoredStatusErr    error
	changedFiles        []string
	changedErr          error
	diff                string
	diffErr             error
	diffStat            string
	diffStatErr         error
	dirtyFingerprints   []gitops.DirtyFingerprint
	dirtyFingerprintErr error
	checkoutErr         error
	mergeFFErr          error
	mergeSquashErr      error
	stagedChanges       bool
	stagedChangesErr    error
	cleanErr            error
	addErr              error
	commitErr           error
	updateRefCASErr     error
	rebaseErr           error
	rebaseAbortErr      error
	resetHardErr        error
	calls               []string
}

func (f *fakeGitClient) Root() string {
	return f.root
}

func (f *fakeGitClient) DefaultBranch(ctx context.Context) (string, error) {
	_ = ctx
	f.calls = append(f.calls, "default-branch")
	return f.defaultBranch, f.defaultErr
}

func (f *fakeGitClient) CurrentBranch(ctx context.Context) (string, error) {
	_ = ctx
	f.calls = append(f.calls, "current-branch")
	return f.currentBranch, f.currentBranchErr
}

func (f *fakeGitClient) RevParse(ctx context.Context, rev string) (string, error) {
	_ = ctx
	f.calls = append(f.calls, "rev-parse "+rev)
	if f.revParseErr != nil {
		return "", f.revParseErr
	}
	if len(f.revParseErrors[rev]) > 0 {
		err := f.revParseErrors[rev][0]
		f.revParseErrors[rev] = f.revParseErrors[rev][1:]
		if err != nil {
			return "", err
		}
	}
	if len(f.revParseSequence[rev]) > 0 {
		value := f.revParseSequence[rev][0]
		f.revParseSequence[rev] = f.revParseSequence[rev][1:]
		return value, nil
	}
	return f.revParse[rev], nil
}

func (f *fakeGitClient) CommitMessage(ctx context.Context, rev string) (string, error) {
	_ = ctx
	f.calls = append(f.calls, "commit-message "+rev)
	if f.commitMessageErr != nil {
		return "", f.commitMessageErr
	}
	return f.commitMessages[rev], nil
}

func (f *fakeGitClient) CommitPathStates(ctx context.Context, parent, commit string) ([]gitops.CommitPathState, error) {
	_ = ctx
	f.calls = append(f.calls, "commit-path-states "+parent+" "+commit)
	return append([]gitops.CommitPathState(nil), f.commitPathStates...), f.commitPathStateErr
}

func (f *fakeGitClient) MergeBase(ctx context.Context, a string, b string) (string, error) {
	_ = ctx
	f.calls = append(f.calls, "merge-base "+a+" "+b)
	return f.mergeBase, f.mergeErr
}

func (f *fakeGitClient) IsAncestor(ctx context.Context, ancestor string, descendant string) (bool, error) {
	_ = ctx
	f.calls = append(f.calls, "is-ancestor "+ancestor+" "+descendant)
	if f.ancestorErr != nil {
		return false, f.ancestorErr
	}
	return f.ancestors[ancestor+".."+descendant], nil
}

func (f *fakeGitClient) StatusPorcelain(ctx context.Context) (string, error) {
	_ = ctx
	f.calls = append(f.calls, "status")
	return f.status, f.statusErr
}

func (f *fakeGitClient) StatusPorcelainIgnoredV1Z(ctx context.Context) (string, error) {
	_ = ctx
	f.calls = append(f.calls, "status-ignored")
	return f.ignoredStatus, f.ignoredStatusErr
}

func (f *fakeGitClient) ChangedFiles(ctx context.Context, revspec string) ([]string, error) {
	_ = ctx
	f.calls = append(f.calls, "changed-files "+revspec)
	return append([]string(nil), f.changedFiles...), f.changedErr
}

func (f *fakeGitClient) ChangedFilesExact(ctx context.Context, revspec string) ([]string, error) {
	_ = ctx
	f.calls = append(f.calls, "changed-files-exact "+revspec)
	return append([]string(nil), f.changedFiles...), f.changedErr
}

func (f *fakeGitClient) Diff(ctx context.Context, revspec string) (string, error) {
	_ = ctx
	f.calls = append(f.calls, "diff "+revspec)
	return f.diff, f.diffErr
}

func (f *fakeGitClient) DiffBounded(ctx context.Context, revspec string, maxBytes int) (string, bool, error) {
	_ = ctx
	f.calls = append(f.calls, fmt.Sprintf("diff-bounded %s %d", revspec, maxBytes))
	if f.diffErr != nil {
		return "", false, f.diffErr
	}
	if len(f.diff) > maxBytes {
		return f.diff[:maxBytes], true, nil
	}
	return f.diff, false, nil
}

func (f *fakeGitClient) DiffStat(ctx context.Context, revspec string) (string, error) {
	_ = ctx
	f.calls = append(f.calls, "diff-stat "+revspec)
	return f.diffStat, f.diffStatErr
}

func (f *fakeGitClient) DirtyFingerprint(ctx context.Context) (gitops.DirtyFingerprint, error) {
	_ = ctx
	f.calls = append(f.calls, "dirty-fingerprint")
	if f.dirtyFingerprintErr != nil {
		return gitops.DirtyFingerprint{}, f.dirtyFingerprintErr
	}
	if len(f.dirtyFingerprints) == 0 {
		return gitops.DirtyFingerprint{}, nil
	}
	fingerprint := f.dirtyFingerprints[0]
	f.dirtyFingerprints = f.dirtyFingerprints[1:]
	return fingerprint, nil
}

func (f *fakeGitClient) Checkout(ctx context.Context, branch string) error {
	_ = ctx
	f.calls = append(f.calls, "checkout "+branch)
	return f.checkoutErr
}

func (f *fakeGitClient) MergeFFOnly(ctx context.Context, ref string) error {
	_ = ctx
	f.calls = append(f.calls, "merge-ff-only "+ref)
	return f.mergeFFErr
}

func (f *fakeGitClient) MergeSquash(ctx context.Context, ref string) error {
	_ = ctx
	f.calls = append(f.calls, "merge-squash "+ref)
	return f.mergeSquashErr
}

func (f *fakeGitClient) HasStagedChanges(ctx context.Context) (bool, error) {
	_ = ctx
	f.calls = append(f.calls, "has-staged-changes")
	return f.stagedChanges, f.stagedChangesErr
}

func (f *fakeGitClient) CleanUntracked(ctx context.Context) error {
	_ = ctx
	f.calls = append(f.calls, "clean-untracked")
	return f.cleanErr
}

func (f *fakeGitClient) Add(ctx context.Context, paths ...string) error {
	_ = ctx
	f.calls = append(f.calls, "add "+strings.Join(paths, " "))
	return f.addErr
}

func (f *fakeGitClient) Commit(ctx context.Context, message string) error {
	_ = ctx
	f.calls = append(f.calls, "commit "+message)
	return f.commitErr
}

func (f *fakeGitClient) CommitWithoutHooks(ctx context.Context, message string) error {
	_ = ctx
	f.calls = append(f.calls, "commit-without-hooks "+message)
	return f.commitErr
}

func (f *fakeGitClient) UpdateRefCAS(ctx context.Context, ref, newSHA, oldSHA string) error {
	_ = ctx
	f.calls = append(f.calls, "update-ref-cas "+ref+" "+newSHA+" "+oldSHA)
	return f.updateRefCASErr
}

func (f *fakeGitClient) Rebase(ctx context.Context, onto string) error {
	_ = ctx
	f.calls = append(f.calls, "rebase "+onto)
	return f.rebaseErr
}

func (f *fakeGitClient) RebaseAbort(ctx context.Context) error {
	_ = ctx
	f.calls = append(f.calls, "rebase-abort")
	return f.rebaseAbortErr
}

func (f *fakeGitClient) ResetHard(ctx context.Context, ref string) error {
	_ = ctx
	f.calls = append(f.calls, "reset-hard "+ref)
	return f.resetHardErr
}

// fakeGitRegistry manages per-root fake git clients for tests that exercise
// multi-working-copy scenarios. Constructing a client via newGit binds it to
// the root's state bucket, making per-root call logs independently observable.
type fakeGitRegistry struct {
	copies map[string]*fakeGitClient
}

func newFakeGitRegistry() *fakeGitRegistry {
	return &fakeGitRegistry{copies: make(map[string]*fakeGitClient)}
}

// seed registers a pre-configured fake client for root and returns the registry
// for chaining.
func (r *fakeGitRegistry) seed(root string, c *fakeGitClient) *fakeGitRegistry {
	r.copies[root] = c
	return r
}

// client returns the registered client for root, creating an empty zero-state
// one if absent.
func (r *fakeGitRegistry) client(root string) *fakeGitClient {
	if c, ok := r.copies[root]; ok {
		return c
	}
	c := &fakeGitClient{}
	r.copies[root] = c
	return c
}

// newGit is the GitClient factory for Service.NewGit: it binds the returned
// client to dir's state bucket so callers can inspect per-root call logs.
// It also sets the client's root field so worktreeGit's Root() assertion passes.
func (r *fakeGitRegistry) newGit(dir string) GitClient {
	c := r.client(dir)
	c.root = dir
	return c
}

func TestCheckPreMergeGateSkipsWorktreeCheckWhenWorktreePathEqualsRepoRoot(t *testing.T) {
	// When the workspace path is the same filesystem path as the repo root the
	// plan is not running in a separate worktree. CheckPreMergeGate must not
	// call NewGit for the plan worktree in that case.
	const root = "/repo/root"
	git := &fakeGitClient{defaultBranch: "main", mergeBase: "base123"}
	calledNewGit := false
	detail := mergeReadyDetail("base123")
	detail.State.Repo.Root = root
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	detail.State.Workspace.Path = root // same as repo root: not a separate worktree

	service := Service{
		Git: git,
		NewGit: func(dir string) GitClient {
			calledNewGit = true
			return &fakeGitClient{}
		},
	}
	if err := service.CheckPreMergeGate(context.Background(), detail, Options{}); err != nil {
		t.Fatal(err)
	}
	if calledNewGit {
		t.Fatal("NewGit must not be called when worktree path equals repo root")
	}
}

func TestCheckPreMergeGateAllowsApprovedMatchingReviewBase(t *testing.T) {
	git := &fakeGitClient{defaultBranch: "main", mergeBase: "base123"}
	detail := mergeReadyDetail("base123")

	if err := (Service{Git: git}).CheckPreMergeGate(context.Background(), detail, Options{}); err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{"status", "default-branch", "merge-base main tao/plan-a"}
	if !reflect.DeepEqual(git.calls, wantCalls) {
		t.Fatalf("calls mismatch\nwant: %#v\n got: %#v", wantCalls, git.calls)
	}
}

func TestMergeRefusesAbandonedPlanBeforeEverySideEffect(t *testing.T) {
	for _, options := range []Options{
		{},
		{Force: true},
		{RecordOnly: true},
		{RecordOnly: true, Force: true},
		{NoSquash: true, NoVerify: true},
	} {
		t.Run(strings.ReplaceAll(strings.TrimSpace(strings.Join([]string{
			fmt.Sprintf("force=%t", options.Force),
			fmt.Sprintf("record=%t", options.RecordOnly),
			fmt.Sprintf("no-squash=%t", options.NoSquash),
		}, "_")), "=", "-"), func(t *testing.T) {
			detail := mergeReadyDetail("base123")
			detail.State.Status = plan.StatusAbandoned
			detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypePlanAbandoned, Reason: "superseded"})
			git := &fakeGitClient{}
			cleaner := successfulCleanup()
			events := &fakeEventAppender{}
			generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}

			err := (Service{Git: git, Cleaner: cleaner, Events: events, ProposalGenerator: generator}).Merge(context.Background(), detail, options)
			if err == nil || !strings.Contains(err.Error(), "plan plan-a is abandoned: superseded") {
				t.Fatalf("Merge() error = %v, want abandonment refusal", err)
			}
			if len(git.calls) != 0 || len(cleaner.calls) != 0 || len(events.events) != 0 || len(events.stateWrites) != 0 || generator.calls != 0 {
				t.Fatalf("abandoned merge had side effects: git=%v cleaner=%v events=%v states=%v proposals=%d", git.calls, cleaner.calls, events.events, events.stateWrites, generator.calls)
			}
		})
	}
}

func TestCheckPreMergeGateRefusesAbandonedPlanEvenWithForce(t *testing.T) {
	detail := mergeReadyDetail("base123")
	detail.State.Status = plan.StatusAbandoned
	git := &fakeGitClient{}
	if err := (Service{Git: git}).CheckPreMergeGate(context.Background(), detail, Options{Force: true}); err == nil || !strings.Contains(err.Error(), "is abandoned") {
		t.Fatalf("CheckPreMergeGate() error = %v, want abandonment refusal", err)
	}
	if len(git.calls) != 0 {
		t.Fatalf("abandoned preflight called Git: %v", git.calls)
	}
}

func TestCheckPreMergeGateRefusesNotApproved(t *testing.T) {
	git := &fakeGitClient{defaultBranch: "main", mergeBase: "base123"}
	detail := mergeReadyDetail("base123")
	detail.State.Plan.Review.Verdict = plan.ReviewVerdictChangesRequested

	err := (Service{Git: git}).CheckPreMergeGate(context.Background(), detail, Options{})
	if err == nil {
		t.Fatal("expected not-approved error")
	}
	if !errors.Is(err, ErrNotApproved) {
		t.Fatalf("expected ErrNotApproved, got %v", err)
	}
	var notApproved *NotApprovedError
	if !errors.As(err, &notApproved) || notApproved.PlanID != "plan-a" {
		t.Fatalf("expected NotApprovedError with plan id, got %#v", err)
	}
	if len(git.calls) != 0 {
		t.Fatalf("not-approved gate should not call git, got %#v", git.calls)
	}
}

func TestCheckPreMergeGateRefusesReviewBaseMismatch(t *testing.T) {
	git := &fakeGitClient{defaultBranch: "main", mergeBase: "merge-base-sha"}
	detail := mergeReadyDetail("review-base-sha")

	err := (Service{Git: git}).CheckPreMergeGate(context.Background(), detail, Options{})
	if err == nil {
		t.Fatal("expected review-base mismatch")
	}
	if !errors.Is(err, ErrReviewBaseMismatch) {
		t.Fatalf("expected ErrReviewBaseMismatch, got %v", err)
	}
	var mismatch *ReviewBaseMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected ReviewBaseMismatchError, got %#v", err)
	}
	if mismatch.ReviewBase != "review-base-sha" || mismatch.MergeBase != "merge-base-sha" || mismatch.DefaultBranch != "main" || mismatch.PlanBranch != "tao/plan-a" {
		t.Fatalf("unexpected mismatch details: %#v", mismatch)
	}
}

func TestCheckPreMergeGateRefusesDirtyWorktree(t *testing.T) {
	git := &fakeGitClient{defaultBranch: "main", mergeBase: "base123", status: " M internal/merge/service.go\n"}
	detail := mergeReadyDetail("base123")

	err := (Service{Git: git}).CheckPreMergeGate(context.Background(), detail, Options{})
	if err == nil {
		t.Fatal("expected dirty-worktree error")
	}
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected ErrDirtyWorktree, got %v", err)
	}
	var dirty *DirtyWorktreeError
	if !errors.As(err, &dirty) || dirty.Status != " M internal/merge/service.go\n" {
		t.Fatalf("expected DirtyWorktreeError with status, got %#v", err)
	}
	wantCalls := []string{"status"}
	if !reflect.DeepEqual(git.calls, wantCalls) {
		t.Fatalf("calls mismatch\nwant: %#v\n got: %#v", wantCalls, git.calls)
	}
}

func TestCheckPreMergeGateRefusesDirtyPlanWorktree(t *testing.T) {
	// Real directories: hasSeparatePlanWorktree only trusts a worktree that
	// exists on disk.
	repoRoot := t.TempDir()
	worktreePath := t.TempDir()
	reg := newFakeGitRegistry()
	reg.seed(repoRoot, &fakeGitClient{defaultBranch: "main", mergeBase: "base123"})
	reg.seed(worktreePath, &fakeGitClient{status: " M feature.go\n"})
	detail := mergeReadyDetail("base123")
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	detail.State.Workspace.Path = worktreePath

	service := Service{
		Git:    reg.client(repoRoot),
		NewGit: reg.newGit,
	}
	err := service.CheckPreMergeGate(context.Background(), detail, Options{})
	if err == nil {
		t.Fatal("expected dirty-worktree error")
	}
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected ErrDirtyWorktree, got %v", err)
	}
	var dirty *DirtyWorktreeError
	if !errors.As(err, &dirty) || dirty.Status != " M feature.go\n" {
		t.Fatalf("expected plan worktree DirtyWorktreeError with status, got %#v", err)
	}
	if wantCalls := []string{"status"}; !reflect.DeepEqual(reg.client(repoRoot).calls, wantCalls) {
		t.Fatalf("repo-root calls mismatch\nwant: %#v\n got: %#v", wantCalls, reg.client(repoRoot).calls)
	}
	if wantCalls := []string{"status"}; !reflect.DeepEqual(reg.client(worktreePath).calls, wantCalls) {
		t.Fatalf("plan worktree calls mismatch\nwant: %#v\n got: %#v", wantCalls, reg.client(worktreePath).calls)
	}
}

func TestWorktreeGitRejectsRootMismatch(t *testing.T) {
	worktreePath := t.TempDir()
	// NewGit returns a client whose Root() does not match the requested dir.
	badClient := &fakeGitClient{root: "/some/other/path"}
	service := Service{
		Git: &fakeGitClient{},
		NewGit: func(dir string) GitClient {
			return badClient
		},
	}
	detail := mergeReadyDetail("base123")
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	detail.State.Workspace.Path = worktreePath

	_, err := service.worktreeGit(detail)
	if err == nil {
		t.Fatal("expected root mismatch error")
	}
	if !strings.Contains(err.Error(), worktreePath) {
		t.Fatalf("error should mention the requested path %q, got %q", worktreePath, err.Error())
	}
	if !strings.Contains(err.Error(), "/some/other/path") {
		t.Fatalf("error should mention the actual bound root, got %q", err.Error())
	}
}

func TestWorktreeGitUsesConfigDerivedPathWhenWorkspacePathIsEmpty(t *testing.T) {
	// When Workspace.Strategy=worktree but Workspace.Path is empty, worktreeGit
	// must derive the path from workspace.ResolvePlanWorktree (which falls back to
	// the config-derived path) rather than passing "" to NewGit.
	//
	// Default config root is ".tao/workspaces" (relative); with planID=plan-a
	// the resolved path is <repoRoot>/.tao/workspaces/plan-a. The directory is
	// created because hasSeparatePlanWorktree only trusts a worktree that
	// exists on disk.
	repoRoot := t.TempDir()
	const planID = "plan-a"
	wantPath := filepath.Join(repoRoot, ".tao", "workspaces", planID)
	if err := os.MkdirAll(wantPath, 0o750); err != nil {
		t.Fatal(err)
	}

	var gotPath string
	reg := newFakeGitRegistry()
	reg.seed(wantPath, &fakeGitClient{})
	service := Service{
		Git: &fakeGitClient{},
		NewGit: func(dir string) GitClient {
			gotPath = dir
			return reg.newGit(dir)
		},
	}
	detail := mergeReadyDetail("base123")
	detail.State.Repo.Root = repoRoot
	detail.State.Plan.ID = planID
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	detail.State.Workspace.Path = "" // empty: must be resolved from config

	_, err := service.worktreeGit(detail)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath == "" {
		t.Fatal("NewGit was not called")
	}
	if gotPath != wantPath {
		t.Fatalf("NewGit called with wrong path: got %q, want %q", gotPath, wantPath)
	}
}

func TestMergeRefusesDirtyPlanWorktreeBeforeDefaultMutation(t *testing.T) {
	fixture := newRealGitWorktree(t)
	ctx := context.Background()
	repoGit := gitops.NewClient(fixture.repoRoot, nil)
	baseSHA, err := repoGit.RevParse(ctx, fixture.defaultBranch)
	if err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "feature.txt"), []byte("feature\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "feature")
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	detail := mergeReadyDetail(baseSHA)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = fixture.repoRoot
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	detail.State.Workspace.Path = fixture.worktreePath
	detail.State.Workspace.Branch = fixture.planBranch
	detail.State.Workspace.BaseBranch = fixture.defaultBranch
	service := Service{
		Git: repoGit,
		NewGit: func(dir string) GitClient {
			return gitops.NewClient(dir, nil)
		},
		Cleaner: successfulCleanup(),
		Events:  &fakeEventAppender{},
	}

	err = service.Merge(ctx, detail, Options{NoVerify: true})
	if err == nil {
		t.Fatal("expected dirty plan worktree to refuse merge")
	}
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected ErrDirtyWorktree, got %v", err)
	}
	afterSHA, err := repoGit.RevParse(ctx, fixture.defaultBranch)
	if err != nil {
		t.Fatal(err)
	}
	if afterSHA != baseSHA {
		t.Fatalf("default branch mutated despite dirty plan worktree: before %s after %s", baseSHA, afterSHA)
	}
	if err := service.CheckPreMergeGate(ctx, detail, Options{Force: true}); err != nil {
		t.Fatalf("force should bypass the dirty-worktree gate: %v", err)
	}
}

func TestCheckPreMergeGateForceBypassesReviewAndScopeGate(t *testing.T) {
	tests := []struct {
		name   string
		detail *plan.PlanDetail
	}{
		{
			name:   "not approved",
			detail: notApprovedDetail(),
		},
		{
			name:   "review base mismatch",
			detail: mergeReadyDetail("review-base-sha"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			git := &fakeGitClient{defaultErr: errors.New("default unavailable"), mergeBase: "merge-base-sha", status: " M dirty.go\n"}
			if err := (Service{Git: git}).CheckPreMergeGate(context.Background(), tt.detail, Options{Force: true}); err != nil {
				t.Fatal(err)
			}
			if len(git.calls) != 0 {
				t.Fatalf("force should bypass gate without git calls, got %#v", git.calls)
			}
		})
	}
}

func TestCheckPreMergeGateFallsBackToWorkspaceBaseBranch(t *testing.T) {
	git := &fakeGitClient{defaultErr: errors.New("origin HEAD missing"), mergeBase: "base123"}
	detail := mergeReadyDetail("base123")
	detail.State.Workspace.BaseBranch = "master"

	if err := (Service{Git: git}).CheckPreMergeGate(context.Background(), detail, Options{}); err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{"status", "default-branch", "merge-base master tao/plan-a"}
	if !reflect.DeepEqual(git.calls, wantCalls) {
		t.Fatalf("calls mismatch\nwant: %#v\n got: %#v", wantCalls, git.calls)
	}
}

func TestIntegrateFastForwardsDescendantPlanBranch(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "pre123"},
		ancestors:     map[string]bool{"main..tao/plan-a": true},
	}

	if err := (Service{Git: git}).Integrate(context.Background(), mergeReadyDetail("base123")); err != nil {
		t.Fatal(err)
	}

	wantCalls := []string{"default-branch", "rev-parse main", "is-ancestor main tao/plan-a", "checkout main", "merge-ff-only tao/plan-a"}
	if !reflect.DeepEqual(git.calls, wantCalls) {
		t.Fatalf("calls mismatch\nwant: %#v\n got: %#v", wantCalls, git.calls)
	}
}

func TestIntegrateRebasesThenFastForwardsWhenDefaultAdvanced(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "pre123"},
	}

	if err := (Service{Git: git}).Integrate(context.Background(), mergeReadyDetail("base123")); err != nil {
		t.Fatal(err)
	}

	// No NewGit on the service, so worktreeGit falls back to the repo-root client
	// and the plan branch must be checked out before rebasing.
	wantCalls := []string{"default-branch", "rev-parse main", "is-ancestor main tao/plan-a", "checkout tao/plan-a", "rebase main", "checkout main", "merge-ff-only tao/plan-a"}
	if !reflect.DeepEqual(git.calls, wantCalls) {
		t.Fatalf("calls mismatch\nwant: %#v\n got: %#v", wantCalls, git.calls)
	}
}

func TestIntegrateRebaseConflictAbortsAndRestoresDefault(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "pre123"},
		status:        "UU internal/merge/integrate.go\n",
		changedFiles:  []string{"internal/merge/service.go", "internal/merge/integrate.go"},
		rebaseErr:     errors.New("conflict"),
	}

	err := (Service{Git: git}).Integrate(context.Background(), mergeReadyDetail("base123"))
	if err == nil {
		t.Fatal("expected merge conflict")
	}
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("expected ErrMergeConflict, got %v", err)
	}
	var conflict *MergeConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("expected MergeConflictError, got %#v", err)
	}
	wantFiles := []string{"internal/merge/integrate.go"}
	if !reflect.DeepEqual(conflict.Files, wantFiles) {
		t.Fatalf("conflict files mismatch\nwant: only the unmerged path %#v\n got: %#v", wantFiles, conflict.Files)
	}
	wantCalls := []string{
		"default-branch",
		"rev-parse main",
		"is-ancestor main tao/plan-a",
		"checkout tao/plan-a",
		"rebase main",
		"status",
		"rebase-abort",
		"checkout main",
		"reset-hard pre123",
		"checkout main",
	}
	if !reflect.DeepEqual(git.calls, wantCalls) {
		t.Fatalf("calls mismatch\nwant: %#v\n got: %#v", wantCalls, git.calls)
	}
}

// TestExternalMergeRefsDropsStaleSnapshotsAfterReopen guards the data-loss bug
// where a plan reopened for rework still exposed its previously-merged head
// snapshots (review/PR/workspace) as external-merge candidates. Detecting the
// stale merge would delete the branch holding the unmerged rework commits.
func TestExternalMergeRefsDropsStaleSnapshotsAfterReopen(t *testing.T) {
	newDetail := func() *plan.PlanDetail {
		detail := mergeReadyDetail("base123")
		detail.State.Plan.Review.Head = "stale-review-head"
		detail.State.Plan.PullRequest = &plan.PullRequest{HeadSHA: "stale-pr-head"}
		detail.State.Workspace.HeadSHA = "stale-workspace-head"
		return detail
	}

	// Before any reopen the snapshots are current and must be included.
	merged := newDetail()
	merged.Events = []plan.Event{{Type: plan.EventTypePlanReviewed}}
	got := externalMergeRefs(merged)
	want := []string{"tao/plan-a", "stale-review-head", "stale-pr-head", "stale-workspace-head"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pre-reopen refs mismatch\nwant: %#v\n got: %#v", want, got)
	}

	// After a reopen supersedes the review, only the live plan branch survives.
	reopened := newDetail()
	reopened.Events = []plan.Event{
		{Type: plan.EventTypePlanReviewed},
		{Type: plan.EventTypePlanMerged},
		{Type: plan.EventTypePlanReopened},
	}
	got = externalMergeRefs(reopened)
	if want := []string{"tao/plan-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("post-reopen refs mismatch\nwant: %#v\n got: %#v", want, got)
	}

	// A fresh review after the reopen restores trust in the snapshots.
	reReviewed := newDetail()
	reReviewed.Events = []plan.Event{
		{Type: plan.EventTypePlanMerged},
		{Type: plan.EventTypePlanReopened},
		{Type: plan.EventTypePlanReviewed},
	}
	got = externalMergeRefs(reReviewed)
	want = []string{"tao/plan-a", "stale-review-head", "stale-pr-head", "stale-workspace-head"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("re-reviewed refs mismatch\nwant: %#v\n got: %#v", want, got)
	}
}

// TestMergeRefusesReopenedPlanWithoutReworkCommits guards the data-loss bug
// where `tao merge --force` on a merged-then-reopened plan re-recorded the
// superseded merge: with no rework commits yet, the live branch tip is still
// the previously merged commit, so it satisfies both ancestry against default
// and refCarriesPlanWork — external-merge detection (and, under --force, the
// gate-free full merge) would mark the reopened plan completed and cleanup
// would delete the worktree and branch carrying the pending rework slices.
func TestMergeRefusesReopenedPlanWithoutReworkCommits(t *testing.T) {
	newGit := func() *fakeGitClient {
		return &fakeGitClient{
			defaultBranch: "main",
			ancestors: map[string]bool{
				"tao/plan-a..main":    true, // old tip is merged: exactly the superseded merge
				"base123..tao/plan-a": true, // and ahead of the plan base, so it "carries work"
				"old-tip..merged456":  true, // ...but nothing beyond the recorded merge
			},
			revParse: map[string]string{"main": "merged999", "tao/plan-a": "old-tip"},
		}
	}
	newDetail := func() *plan.PlanDetail {
		detail := mergeReadyDetail("base123")
		detail.Dir = t.TempDir()
		detail.State.Status = plan.StatusInProgress
		detail.State.Plan.PendingSlices = []string{"002-rework"}
		detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{ID: "002-rework", Status: plan.StatusPending})
		detail.Events = []plan.Event{
			{Type: plan.EventTypePlanReviewed},
			{Type: plan.EventTypePlanMerged, MergedDefaultSHA: "merged456"},
			{Type: plan.EventTypePlanReopened},
		}
		return detail
	}

	t.Run("force refuses", func(t *testing.T) {
		cleaner := successfulCleanup()
		events := &fakeEventAppender{}
		err := (Service{Git: newGit(), Cleaner: cleaner, Events: events}).Merge(context.Background(), newDetail(), Options{Force: true})
		if err == nil || !strings.Contains(err.Error(), "no commits beyond the previously recorded merge") {
			t.Fatalf("expected rework refusal, got %v", err)
		}
		if events.count(plan.EventTypePlanMerged) != 0 {
			t.Fatalf("no merge may be recorded, got %#v", events.events)
		}
		if len(cleaner.cleaned) != 0 {
			t.Fatalf("cleanup must not run, got %#v", cleaner.cleaned)
		}
	})

	t.Run("plain merge refuses with the rework reason", func(t *testing.T) {
		err := (Service{Git: newGit()}).Merge(context.Background(), newDetail(), Options{})
		if err == nil || !strings.Contains(err.Error(), "no commits beyond the previously recorded merge") {
			t.Fatalf("expected rework refusal, got %v", err)
		}
	})

	t.Run("rework commits pass the guard", func(t *testing.T) {
		git := newGit()
		git.revParse["tao/plan-a"] = "rework-tip"
		git.ancestors = map[string]bool{"rework-tip..merged456": false, "tao/plan-a..main": false}
		err := (Service{Git: git}).Merge(context.Background(), newDetail(), Options{})
		if !errors.Is(err, ErrNotApproved) {
			t.Fatalf("expected the guard to pass through to the approval gate, got %v", err)
		}
	})

	t.Run("record-only force stays the escape hatch", func(t *testing.T) {
		cleaner := successfulCleanup()
		events := &fakeEventAppender{}
		err := (Service{Git: newGit(), Cleaner: cleaner, Events: events}).Merge(context.Background(), newDetail(), Options{RecordOnly: true, Force: true})
		if err != nil {
			t.Fatal(err)
		}
		events.first(t, plan.EventTypePlanMerged)
	})
}

// TestCheckPreMergeGateRefusesReviewHeadMismatch guards the unreviewed-commits
// hole: an approved review covers base..head, so commits added to the plan
// branch after the review (e.g. leftover worktree changes committed later, or
// fresh work) must invalidate the approval rather than merging unreviewed on
// the strength of the old verdict.
func TestCheckPreMergeGateRefusesReviewHeadMismatch(t *testing.T) {
	newGit := func(tip string) *fakeGitClient {
		return &fakeGitClient{
			defaultBranch: "main",
			mergeBase:     "base123",
			revParse:      map[string]string{"tao/plan-a": tip},
		}
	}
	newDetail := func() *plan.PlanDetail {
		detail := mergeReadyDetail("base123")
		detail.State.Plan.Review.Head = "reviewed-head"
		return detail
	}

	err := (Service{Git: newGit("follow-up-sha")}).CheckPreMergeGate(context.Background(), newDetail(), Options{})
	if err == nil {
		t.Fatal("expected review-head mismatch refusal")
	}
	if !errors.Is(err, ErrReviewHeadMismatch) {
		t.Fatalf("expected ErrReviewHeadMismatch, got %v", err)
	}
	mismatch, ok := errors.AsType[*ReviewHeadMismatchError](err)
	if !ok || mismatch.ReviewHead != "reviewed-head" || mismatch.BranchTip != "follow-up-sha" || mismatch.PlanBranch != "tao/plan-a" {
		t.Fatalf("unexpected mismatch payload %#v", mismatch)
	}

	if err := (Service{Git: newGit("reviewed-head")}).CheckPreMergeGate(context.Background(), newDetail(), Options{}); err != nil {
		t.Fatalf("matching head should pass the gate: %v", err)
	}
}

// TestMergeRetriesCleanupWhenMergeAlreadyRecorded guards the leak where a
// recorded merge whose cleanup failed (e.g. tao merge run from inside the plan
// worktree) could never be cleaned via tao merge again: the already-recorded
// short-circuit reported success without retrying cleanup.
func TestMergeRetriesCleanupWhenMergeAlreadyRecorded(t *testing.T) {
	newDetail := func() *plan.PlanDetail {
		detail := mergeVerifyDetail()
		detail.Events = []plan.Event{{Type: plan.EventTypePlanMerged}}
		return detail
	}

	t.Run("leftover worktree cleaned", func(t *testing.T) {
		cleaner := successfulCleanup()
		if err := (Service{Git: &fakeGitClient{}, Cleaner: cleaner}).Merge(context.Background(), newDetail(), Options{}); err != nil {
			t.Fatal(err)
		}
		if len(cleaner.cleaned) != 1 || cleaner.cleaned[0].Branch != "tao/plan-a" {
			t.Fatalf("expected retried cleanup to remove the leftover branch, got %#v", cleaner.cleaned)
		}
	})

	t.Run("already cleaned is success", func(t *testing.T) {
		cleaner := successfulCleanup()
		cleaner.managed = nil // branch no longer managed: nothing left to clean
		if err := (Service{Git: &fakeGitClient{}, Cleaner: cleaner}).Merge(context.Background(), newDetail(), Options{}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cleanup failure surfaces", func(t *testing.T) {
		cleaner := successfulCleanup()
		cleaner.cleanErr = errors.New("worktree busy")
		err := (Service{Git: &fakeGitClient{}, Cleaner: cleaner}).Merge(context.Background(), newDetail(), Options{})
		if err == nil || !strings.Contains(err.Error(), "already recorded, but cleanup failed") {
			t.Fatalf("expected surfaced cleanup failure, got %v", err)
		}
	})
}

// TestMergeSurfacesCleanupFailureAfterRecordingExternalMerge guards the silent
// leak on the external-record path: a cleanup failure after recording was only
// logged, so the command reported success while the branch and worktree
// remained. The failure must surface (the recorded merge stays recorded).
func TestMergeSurfacesCleanupFailureAfterRecordingExternalMerge(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "merged789"},
		ancestors:     map[string]bool{"tao/plan-a..main": true, "base123..tao/plan-a": true},
	}
	cleaner := successfulCleanup()
	cleaner.cleanErr = errors.New("worktree busy")
	events := &fakeEventAppender{}

	err := (Service{Git: git, Cleaner: cleaner, Events: events}).Merge(context.Background(), mergeVerifyDetail(), Options{})
	if err == nil || !strings.Contains(err.Error(), "external merge recorded, but cleanup failed") {
		t.Fatalf("expected surfaced cleanup failure, got %v", err)
	}
	events.first(t, plan.EventTypePlanMerged)
}

// TestDetectExternalMergeSkipsSnapshotBehindAdvancedBranchTip guards the
// data-loss bug where a recorded head snapshot (review/PR/workspace) merged
// externally was still trusted after follow-up commits advanced the plan branch
// past it: detection would mark the plan completed while the follow-up commits
// never reached the default branch, and cleanup could delete the branch
// carrying them.
func TestDetectExternalMergeSkipsSnapshotBehindAdvancedBranchTip(t *testing.T) {
	newGit := func() *fakeGitClient {
		return &fakeGitClient{
			defaultBranch: "main",
			ancestors: map[string]bool{
				"tao/plan-a..main":      false, // live branch tip is not merged
				"merged-head..main":     true,  // the old reviewed head was merged externally
				"base-sha..merged-head": true,  // and it carries plan work
			},
			revParse: map[string]string{
				"main":        "merged-default-sha",
				"merged-head": "merged-head",
			},
		}
	}
	newDetail := func() *plan.PlanDetail {
		detail := mergeReadyDetail("base123")
		detail.State.Plan.Review.Head = "merged-head"
		detail.State.Workspace.BaseSHA = "base-sha"
		return detail
	}

	t.Run("branch tip advanced past snapshot", func(t *testing.T) {
		git := newGit()
		git.revParse["tao/plan-a"] = "follow-up-sha"
		_, ok, err := (Service{Git: git}).detectExternalMerge(context.Background(), git, newDetail(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("snapshot behind the advanced branch tip must not be detected as an external merge")
		}
	})

	t.Run("snapshot equals branch tip", func(t *testing.T) {
		git := newGit()
		git.revParse["tao/plan-a"] = "merged-head"
		merged, ok, err := (Service{Git: git}).detectExternalMerge(context.Background(), git, newDetail(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !ok || merged.Ref != "merged-head" {
			t.Fatalf("snapshot matching the live tip should be detected, got ok=%v merged=%#v", ok, merged)
		}
	})

	t.Run("branch deleted keeps snapshots trusted", func(t *testing.T) {
		git := newGit()
		merged, ok, err := (Service{Git: git}).detectExternalMerge(context.Background(), git, newDetail(), Options{})
		if err != nil {
			t.Fatal(err)
		}
		if !ok || merged.Ref != "merged-head" {
			t.Fatalf("snapshots are the only evidence once the branch is gone, got ok=%v merged=%#v", ok, merged)
		}
	})
}

func TestPrepareSingleMergeIntentSupersedesUnmutatedPriorSource(t *testing.T) {
	detail := mergeReadyDetail("base123")
	detail.Dir = t.TempDir()
	detail.State.Plan.Review.Head = "source-new"
	old := plan.SingleMergeCommitIntent{
		Message: "feat(merge): use old proposal\n\nWhat:\nUse old work.\n\nWhy:\nKeep old recovery exact.\n\nTao-Plan: plan-a\nTao-Source-Head: source-old",
		PlanID:  "plan-a", SourceHead: "source-old", DefaultBranch: "main", DefaultParent: "base123",
		CreatedAt: time.Date(2026, 7, 23, 19, 0, 0, 0, time.UTC),
	}
	detail.State.Plan.MergeCommitIntent = &old
	git := &fakeGitClient{defaultBranch: "main", mergeBase: "base123", revParse: map[string]string{"tao/plan-a": "source-new", "main": "base123"}}
	events := &fakeEventAppender{}
	service := Service{Git: git, Events: events, Now: func() time.Time { return time.Date(2026, 7, 23, 20, 0, 0, 0, time.UTC) }}

	intent, err := service.prepareSingleMergeIntent(context.Background(), git, detail)
	if err != nil {
		t.Fatal(err)
	}
	if intent.SourceHead != "source-new" || intent.DefaultParent != "base123" || intent.Message == old.Message {
		t.Fatalf("prior source intent was not safely superseded: %#v", intent)
	}
	if len(events.stateWrites) != 2 || events.stateWrites[0].Plan.MergeCommitIntent != nil || events.stateWrites[1].Plan.MergeCommitIntent == nil {
		t.Fatalf("expected clear then new intent persistence, got %#v", events.stateWrites)
	}
}

func TestMergeRejectsInvalidReviewProposalBeforeIntentOrGitMutation(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main", mergeBase: "base123",
		ancestors: map[string]bool{"main..tao/plan-a": true},
		revParse:  map[string]string{"main": "pre123", "tao/plan-a": "tip-sha"},
	}
	detail := mergeReadyDetail("base123")
	detail.Dir = t.TempDir()
	detail.State.Plan.Review.Head = "tip-sha"
	detail.State.Plan.Review.CommitMessage.Body += "\n\nTao-Plan: forged"
	events := &fakeEventAppender{}

	err := (Service{Git: git, Cleaner: successfulCleanup(), Events: events}).Merge(context.Background(), detail, Options{NoVerify: true})
	if err == nil || !strings.Contains(err.Error(), "review commit proposal is invalid") {
		t.Fatalf("expected invalid proposal refusal, got %v", err)
	}
	if detail.State.Plan.MergeCommitIntent != nil || len(events.stateWrites) != 0 {
		t.Fatalf("invalid proposal persisted intent: detail=%#v writes=%#v", detail.State.Plan.MergeCommitIntent, events.stateWrites)
	}
	for _, call := range git.calls {
		if strings.HasPrefix(call, "checkout ") || strings.HasPrefix(call, "merge-squash ") || strings.HasPrefix(call, "commit ") {
			t.Fatalf("invalid proposal mutated Git: %#v", git.calls)
		}
	}
}

type fakeMergeProposalGenerator struct {
	proposal commitcontract.Proposal
	err      error
	mutate   func() error
	calls    int
	exact    commitcontract.MergeProposalContext
	identity batchProposalSessionIdentity
}

func (f *fakeMergeProposalGenerator) GenerateMergeProposal(ctx context.Context, exact commitcontract.MergeProposalContext) (commitcontract.Proposal, error) {
	f.calls++
	f.exact = exact
	f.identity, _ = ctx.Value(batchProposalSessionIdentityKey{}).(batchProposalSessionIdentity)
	if f.mutate != nil {
		if err := f.mutate(); err != nil {
			return commitcontract.Proposal{}, err
		}
	}
	return f.proposal, f.err
}

func generatedMergeProposal() commitcontract.Proposal {
	return commitcontract.Proposal{
		Type: "feat", Scope: "merge", Summary: "generate legacy merge messages",
		What: "Generate a proposal from the exact source diff.", Why: "Keep exceptional squash merges recoverable without a fallback.",
	}
}

func TestMergeResolvesSquashConflictVerifiesAndRequiresIndependentApproval(t *testing.T) {
	tests := []struct {
		name        string
		verdict     string
		force       bool
		noVerify    bool
		verifyCalls int
		wantMerged  bool
	}{
		{name: "approved", verdict: "approve", verifyCalls: 1, wantMerged: true},
		{name: "explicit verification skip still reviews", verdict: "approve", noVerify: true, wantMerged: true},
		{name: "force cannot bypass changes requested", verdict: "changes_requested", force: true, verifyCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, sourceHead, defaultHead, _ := batchAgentConflictFixture(t)
			base := realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, sourceHead)
			detail := mergeReadyDetail(base)
			detail.Dir = t.TempDir()
			detail.State.Repo.Root = fixture.repoRoot
			detail.State.Plan.Title = "Resolve an ordinary squash conflict"
			detail.State.Plan.Review.Head = sourceHead
			detail.State.Workspace.Branch = fixture.planBranch
			detail.State.Workspace.BaseBranch = fixture.defaultBranch
			detail.Review.Content = "approved source review"
			events := &fakeEventAppender{}
			record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			git := gitops.NewClient(fixture.repoRoot, nil)
			resolverCalls, reviewerCalls, verifyCalls := 0, 0, 0
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				resolverCalls++
				if head := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); head != defaultHead {
					t.Fatalf("default moved before resolver edit: got %s want %s", head, defaultHead)
				}
				return BatchAgentSessionResult{Output: batchResolutionJSON("combined both sides")}, os.WriteFile(filepath.Join(request.IntegrationRoot, "README.md"), []byte("combined\n"), 0o600)
			})}
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				reviewerCalls++
				if verifyCalls != tt.verifyCalls {
					t.Fatalf("review ran at wrong verification boundary: calls=%d want=%d", verifyCalls, tt.verifyCalls)
				}
				finding := ""
				if tt.verdict != "approve" {
					finding = "resolve interaction"
				}
				return BatchAgentSessionResult{Output: reviewJSON(tt.verdict, "independent result", finding)}, nil
			})}
			runner := func(_ context.Context, cwd, name string, args []string, stdout, _ io.Writer) error {
				verifyCalls++
				if cwd != fixture.repoRoot || name != "sh" || !reflect.DeepEqual(args, []string{"-c", "test -f README.md"}) {
					t.Fatalf("unexpected verification invocation: %s %s %#v", cwd, name, args)
				}
				_, _ = io.WriteString(stdout, "verified resolved head\n")
				return nil
			}
			service := Service{Git: git, Runner: runner, Cleaner: successfulCleanup(), Events: events, SingleResolver: resolver, SingleReviewer: reviewer}
			err = service.Merge(context.Background(), detail, Options{Force: tt.force, NoVerify: tt.noVerify, VerifyCommand: "test -f README.md"})
			if tt.wantMerged {
				if err != nil {
					t.Fatal(err)
				}
				if events.count(plan.EventTypePlanMerged) != 1 {
					t.Fatalf("approved resolution did not record merge: %#v", events.events)
				}
				if got := realGitOutput(t, fixture.repoRoot, "show", "HEAD:README.md"); got != "combined" {
					t.Fatalf("resolved content = %q", got)
				}
			} else {
				if !errors.Is(err, ErrSingleReviewNotApproved) {
					t.Fatalf("non-approval error = %v", err)
				}
				if events.count(plan.EventTypePlanMerged) != 0 {
					t.Fatalf("non-approval recorded merge: %#v", events.events)
				}
				if head := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); head != defaultHead {
					t.Fatalf("non-approval did not roll back default: got %s want %s", head, defaultHead)
				}
			}
			if resolverCalls != 1 || reviewerCalls != 1 || verifyCalls != tt.verifyCalls {
				t.Fatalf("transaction calls resolver=%d verify=%d reviewer=%d", resolverCalls, verifyCalls, reviewerCalls)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceHead {
				t.Fatalf("source moved: got %s want %s", got, sourceHead)
			}
		})
	}
}

func TestMergeResolutionFailureRollsBackAfterCallerCancellation(t *testing.T) {
	tests := []struct {
		name       string
		cancelAt   string
		wantReason plan.SingleMergeResolutionRollbackReason
	}{
		{name: "verification", cancelAt: "verification", wantReason: plan.SingleMergeResolutionRollbackVerificationFailed},
		{name: "independent review", cancelAt: "review", wantReason: plan.SingleMergeResolutionRollbackReviewNotApproved},
		{name: "approved review before recording", cancelAt: "approved-review", wantReason: plan.SingleMergeResolutionRollbackMergeRecordingFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, sourceHead, defaultHead, _ := batchAgentConflictFixture(t)
			base := realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, sourceHead)
			detail := mergeReadyDetail(base)
			detail.Dir = t.TempDir()
			detail.State.Repo.Root = fixture.repoRoot
			detail.State.Plan.Title = "Roll back a canceled single-plan resolution"
			detail.State.Plan.Review.Head = sourceHead
			detail.State.Workspace.Branch = fixture.planBranch
			detail.State.Workspace.BaseBranch = fixture.defaultBranch
			detail.Review.Content = "approved source review"
			events := &fakeEventAppender{}
			record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			git := gitops.NewClient(fixture.repoRoot, nil)
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				return BatchAgentSessionResult{Output: batchResolutionJSON("combined both sides")}, os.WriteFile(filepath.Join(request.IntegrationRoot, "README.md"), []byte("combined\n"), 0o600)
			})}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reviewerCalls := 0
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(agentCtx context.Context, _ BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				reviewerCalls++
				if tt.cancelAt == "review" {
					cancel()
					return BatchAgentSessionResult{}, agentCtx.Err()
				}
				if tt.cancelAt == "approved-review" {
					cancel()
				}
				return BatchAgentSessionResult{Output: reviewJSON("approve", "independent result", "")}, nil
			})}
			runner := func(verifyCtx context.Context, _, _ string, _ []string, stdout, _ io.Writer) error {
				if tt.cancelAt == "verification" {
					cancel()
					return verifyCtx.Err()
				}
				_, _ = io.WriteString(stdout, "verified resolved head\n")
				return nil
			}
			service := Service{Git: git, Runner: runner, Cleaner: successfulCleanup(), Events: events, SingleResolver: resolver, SingleReviewer: reviewer}

			err = service.Merge(ctx, detail, Options{VerifyCommand: "test -f README.md"})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("merge error = %v, want caller cancellation", err)
			}
			if tt.cancelAt == "verification" {
				var verifyErr *VerifyFailedError
				if !errors.As(err, &verifyErr) || len(verifyErr.CleanupErrors) != 0 {
					t.Fatalf("verification rollback error = %#v, want successful detached cleanup", verifyErr)
				}
				if reviewerCalls != 0 {
					t.Fatalf("reviewer calls after failed verification = %d, want 0", reviewerCalls)
				}
			} else {
				if reviewerCalls != 1 {
					t.Fatalf("reviewer calls = %d, want 1", reviewerCalls)
				}
				if tt.cancelAt == "approved-review" {
					approved := detail.State.Plan.MergeCommitIntent
					if approved == nil || approved.Resolution == nil || approved.Resolution.Review == nil || !approved.Resolution.Review.IsApproved() {
						t.Fatalf("reviewer did not return durable approval before canceled recording: %#v", approved)
					}
				}
			}
			if events.count(plan.EventTypePlanMerged) != 0 {
				t.Fatalf("canceled transaction recorded merge evidence: %#v", events.events)
			}
			rolledBack := detail.State.Plan.MergeCommitIntent
			if rolledBack == nil || rolledBack.Resolution == nil || rolledBack.Resolution.Phase != plan.SingleMergeResolutionPhaseRolledBack || rolledBack.Resolution.RollbackReason != tt.wantReason {
				t.Fatalf("canceled rollback settlement = %#v, want reason %s", rolledBack, tt.wantReason)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != defaultHead {
				t.Fatalf("canceled transaction left default at %s, want %s", got, defaultHead)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceHead {
				t.Fatalf("canceled transaction moved source to %s, want %s", got, sourceHead)
			}
			if status := realGitOutput(t, fixture.repoRoot, "status", "--porcelain"); status != "" {
				t.Fatalf("canceled transaction left dirty worktree: %q", status)
			}
		})
	}
}

func TestMergeRecoversExactResolutionCommitBeforeSettlementPersistence(t *testing.T) {
	fixture, request, git := preparedSingleResolutionFixture(t)
	detail := mergeReadyDetail(request.Intent.DefaultParent)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = fixture.repoRoot
	detail.State.Plan.Title = request.PlanTitle
	detail.State.Plan.Review.Head = request.Intent.SourceHead
	detail.State.Plan.MergeCommitIntent = &request.Intent
	detail.State.Workspace.Branch = fixture.planBranch
	detail.State.Workspace.BaseBranch = fixture.defaultBranch
	detail.Review.Content = request.SourceReview
	events := &fakeEventAppender{}
	record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}

	resolverCalls := 0
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, session BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		resolverCalls++
		return BatchAgentSessionResult{Output: batchResolutionJSON("combined both sides before interruption")}, os.WriteFile(filepath.Join(session.IntegrationRoot, "README.md"), []byte("combined\n"), 0o600)
	})}
	resolved, err := resolver.ResolveConflict(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Intent.Resolution == nil || resolved.Intent.Resolution.Phase != plan.SingleMergeResolutionPhaseResolved {
		t.Fatalf("pre-crash resolution evidence = %#v", resolved.Intent.Resolution)
	}
	runRealGit(t, fixture.repoRoot, "add", "--", "README.md")
	runRealGit(t, fixture.repoRoot, "commit", "-m", resolved.Intent.Resolution.CommitMessage)
	exactHead := realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")
	if detail.State.Plan.MergeCommitIntent == nil || detail.State.Plan.MergeCommitIntent.Resolution == nil || detail.State.Plan.MergeCommitIntent.Resolution.Phase != plan.SingleMergeResolutionPhaseResolved {
		t.Fatalf("simulated crash did not leave resolved evidence: %#v", detail.State.Plan.MergeCommitIntent)
	}

	reviewerCalls := 0
	reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, _ BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		reviewerCalls++
		return BatchAgentSessionResult{Output: reviewJSON("approve", "recovered exact resolution approved", "")}, nil
	})}
	service := Service{Git: git, Cleaner: successfulCleanup(), Events: events, SingleResolver: resolver, SingleReviewer: reviewer}
	if err := service.Merge(context.Background(), detail, Options{NoVerify: true}); err != nil {
		t.Fatal(err)
	}
	if resolverCalls != 1 {
		t.Fatalf("settlement recovery reran resolver: calls=%d", resolverCalls)
	}
	if reviewerCalls != 1 {
		t.Fatalf("settlement recovery reviewer calls=%d, want 1", reviewerCalls)
	}
	if events.count(plan.EventTypePlanMerged) != 1 {
		t.Fatalf("settlement recovery merge evidence = %#v", events.events)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != exactHead {
		t.Fatalf("recovered default head = %s, want %s", got, exactHead)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != request.Intent.SourceHead {
		t.Fatalf("settlement recovery moved source: got %s want %s", got, request.Intent.SourceHead)
	}
}

func TestMergeAutomaticallyResolvesConflictsWithExactFilenames(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "non-ASCII", path: "café.txt"},
		{name: "control character", path: "control\tname.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRealGitWorktree(t)
			conflictPath := filepath.Join(fixture.repoRoot, tt.path)
			if err := os.WriteFile(filepath.Join(fixture.worktreePath, tt.path), []byte("source\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, fixture.worktreePath, "add", "--", tt.path)
			runRealGit(t, fixture.worktreePath, "commit", "-m", "add source path")
			sourceHead := realGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD")
			if err := os.WriteFile(conflictPath, []byte("default\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, fixture.repoRoot, "add", "--", tt.path)
			runRealGit(t, fixture.repoRoot, "commit", "-m", "add default path")
			defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")
			base := realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, sourceHead)

			detail := mergeReadyDetail(base)
			detail.Dir = t.TempDir()
			detail.State.Repo.Root = fixture.repoRoot
			detail.State.Plan.Title = "Resolve an exact-path squash conflict"
			detail.State.Plan.Review.Head = sourceHead
			detail.State.Workspace.Branch = fixture.planBranch
			detail.State.Workspace.BaseBranch = fixture.defaultBranch
			detail.Review.Content = "approved source review"
			events := &fakeEventAppender{}
			record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			git := gitops.NewClient(fixture.repoRoot, nil)
			resolverCalls := 0
			var conflictFiles []string
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				resolverCalls++
				conflictFiles = append([]string(nil), detail.State.Plan.MergeCommitIntent.Resolution.ConflictFiles...)
				return BatchAgentSessionResult{Output: batchResolutionJSON("resolved exact filename")}, os.WriteFile(filepath.Join(request.IntegrationRoot, tt.path), []byte("combined\n"), 0o600)
			})}
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, _ BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				return BatchAgentSessionResult{Output: reviewJSON("approve", "exact path approved", "")}, nil
			})}
			service := Service{Git: git, Cleaner: successfulCleanup(), Events: events, SingleResolver: resolver, SingleReviewer: reviewer}

			if err := service.Merge(context.Background(), detail, Options{NoVerify: true}); err != nil {
				t.Fatal(err)
			}
			if resolverCalls != 1 {
				t.Fatalf("resolver calls = %d, want 1", resolverCalls)
			}
			if !slices.Equal(conflictFiles, []string{tt.path}) {
				t.Fatalf("conflict files = %q, want exact path %q", conflictFiles, tt.path)
			}
			if got := realGitOutput(t, fixture.repoRoot, "show", "HEAD:"+tt.path); got != "combined" {
				t.Fatalf("resolved content = %q, want combined", got)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceHead {
				t.Fatalf("source moved: got %s want %s", got, sourceHead)
			}
		})
	}
}

func TestMergeAutomaticallyResolvesConflictWithExactSourcePathScope(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*testing.T, realGitWorktree) string
	}{
		{
			name: "rename includes both endpoints",
			configure: func(t *testing.T, fixture realGitWorktree) string {
				t.Helper()
				const oldPath = "before.txt"
				if err := os.WriteFile(filepath.Join(fixture.repoRoot, oldPath), []byte("renamed content\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runRealGit(t, fixture.repoRoot, "add", "--", oldPath)
				runRealGit(t, fixture.repoRoot, "commit", "-m", "add file before branches diverge")
				runRealGit(t, fixture.worktreePath, "merge", "--ff-only", fixture.defaultBranch)
				const newPath = "after.txt"
				runRealGit(t, fixture.worktreePath, "mv", "--", oldPath, newPath)
				return newPath
			},
		},
		{
			name: "non-conflicting control-character filename remains exact",
			configure: func(t *testing.T, fixture realGitWorktree) string {
				t.Helper()
				const path = "control\tname.txt"
				if err := os.WriteFile(filepath.Join(fixture.worktreePath, path), []byte("unusual path\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newRealGitWorktree(t)
			extraPath := tt.configure(t, fixture)
			if err := os.WriteFile(filepath.Join(fixture.worktreePath, "README.md"), []byte("source\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, fixture.worktreePath, "add", "--all")
			runRealGit(t, fixture.worktreePath, "commit", "-m", "source conflict and exact path change")
			sourceHead := realGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD")
			if err := os.WriteFile(filepath.Join(fixture.repoRoot, "README.md"), []byte("default\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, fixture.repoRoot, "add", "--", "README.md")
			runRealGit(t, fixture.repoRoot, "commit", "-m", "default conflict")
			defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")
			base := realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, sourceHead)

			detail := mergeReadyDetail(base)
			detail.Dir = t.TempDir()
			detail.State.Repo.Root = fixture.repoRoot
			detail.State.Plan.Title = "Resolve a conflict without losing exact source scope"
			detail.State.Plan.Review.Head = sourceHead
			detail.State.Workspace.Branch = fixture.planBranch
			detail.State.Workspace.BaseBranch = fixture.defaultBranch
			detail.Review.Content = "approved source review"
			events := &fakeEventAppender{}
			record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			git := gitops.NewClient(fixture.repoRoot, nil)
			resolverCalls := 0
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				resolverCalls++
				return BatchAgentSessionResult{Output: batchResolutionJSON("resolved conflict with exact source scope")}, os.WriteFile(filepath.Join(request.IntegrationRoot, "README.md"), []byte("combined\n"), 0o600)
			})}
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, _ BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				return BatchAgentSessionResult{Output: reviewJSON("approve", "exact source scope approved", "")}, nil
			})}
			service := Service{Git: git, Cleaner: successfulCleanup(), Events: events, SingleResolver: resolver, SingleReviewer: reviewer}

			if err := service.Merge(context.Background(), detail, Options{NoVerify: true}); err != nil {
				t.Fatal(err)
			}
			if resolverCalls != 1 {
				t.Fatalf("resolver calls = %d, want 1", resolverCalls)
			}
			if got := realGitOutput(t, fixture.repoRoot, "show", "HEAD:README.md"); got != "combined" {
				t.Fatalf("resolved content = %q, want combined", got)
			}
			if got := realGitOutput(t, fixture.repoRoot, "show", "HEAD:"+extraPath); got == "" {
				t.Fatalf("exact source path %q was not integrated", extraPath)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceHead {
				t.Fatalf("source moved: got %s want %s", got, sourceHead)
			}
		})
	}
}

func TestMergeConflictResolutionRejectsDefaultOnlyPathEdits(t *testing.T) {
	fixture, sourceHead, _, _ := batchAgentConflictFixture(t)
	const defaultOnlyPath = "default-only.txt"
	if err := os.WriteFile(filepath.Join(fixture.repoRoot, defaultOnlyPath), []byte("default-owned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.repoRoot, "add", "--", defaultOnlyPath)
	runRealGit(t, fixture.repoRoot, "commit", "-m", "add default-only path")
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", "HEAD")
	base := realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, sourceHead)

	detail := mergeReadyDetail(base)
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = fixture.repoRoot
	detail.State.Plan.Title = "Reject edits outside the source-owned scope"
	detail.State.Plan.Review.Head = sourceHead
	detail.State.Workspace.Branch = fixture.planBranch
	detail.State.Workspace.BaseBranch = fixture.defaultBranch
	detail.Review.Content = "approved source review"
	events := &fakeEventAppender{}
	record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	git := gitops.NewClient(fixture.repoRoot, nil)
	resolverCalls, reviewerCalls := 0, 0
	resolver := GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		resolverCalls++
		if strings.Contains(request.Prompt, defaultOnlyPath) {
			t.Fatalf("default-only path leaked into source-owned resolution scope: %q", request.Prompt)
		}
		if err := os.WriteFile(filepath.Join(request.IntegrationRoot, "README.md"), []byte("combined\n"), 0o600); err != nil {
			return BatchAgentSessionResult{}, err
		}
		return BatchAgentSessionResult{Output: batchResolutionJSON("attempted out-of-scope edit")}, os.WriteFile(filepath.Join(request.IntegrationRoot, defaultOnlyPath), []byte("resolver overwrite\n"), 0o600)
	})}
	reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, _ BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
		reviewerCalls++
		return BatchAgentSessionResult{Output: reviewJSON("approve", "should not review unsafe scope", "")}, nil
	})}
	service := Service{Git: git, Cleaner: successfulCleanup(), Events: events, SingleResolver: resolver, SingleReviewer: reviewer}

	err = service.Merge(context.Background(), detail, Options{NoVerify: true})
	if !errors.Is(err, ErrSingleResolutionRejected) {
		t.Fatalf("merge error = %v, want source-scope rejection", err)
	}
	if resolverCalls != 1 || reviewerCalls != 0 {
		t.Fatalf("transaction calls resolver=%d reviewer=%d, want 1 and 0", resolverCalls, reviewerCalls)
	}
	if events.count(plan.EventTypePlanMerged) != 0 {
		t.Fatalf("unsafe resolution recorded merge: %#v", events.events)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != defaultHead {
		t.Fatalf("default moved after rejection: got %s want %s", got, defaultHead)
	}
	if got := realGitOutput(t, fixture.repoRoot, "show", "HEAD:"+defaultOnlyPath); got != "default-owned" {
		t.Fatalf("default-only path = %q after rejection, want preserved content", got)
	}
	if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != sourceHead {
		t.Fatalf("source moved after rejection: got %s want %s", got, sourceHead)
	}
	if status := realGitOutput(t, fixture.repoRoot, "status", "--porcelain"); status != "" {
		t.Fatalf("rejected resolution left dirty worktree: %q", status)
	}
}

func TestMergeResolutionFailureSettlesRollbackAndAllowsReworkedRerun(t *testing.T) {
	tests := []struct {
		name       string
		failVerify bool
		wantReason plan.SingleMergeResolutionRollbackReason
	}{
		{name: "verification failure", failVerify: true, wantReason: plan.SingleMergeResolutionRollbackVerificationFailed},
		{name: "review non-approval", wantReason: plan.SingleMergeResolutionRollbackReviewNotApproved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture, sourceHead, defaultHead, _ := batchAgentConflictFixture(t)
			base := realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, sourceHead)
			detail := mergeReadyDetail(base)
			detail.Dir = t.TempDir()
			detail.State.Status = plan.StatusReviewed
			detail.State.Repo.Root = fixture.repoRoot
			detail.State.Plan.Title = "Resolve and rework an ordinary squash conflict"
			detail.State.Plan.Review.Head = sourceHead
			detail.State.Workspace.Branch = fixture.planBranch
			detail.State.Workspace.BaseBranch = fixture.defaultBranch
			detail.Review.Content = "approved source review"
			events := &fakeEventAppender{}
			record, err := plan.NewPlanRecordWithStore(events, detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			git := gitops.NewClient(fixture.repoRoot, nil)
			resolverCalls, reviewerCalls, verifyCalls := 0, 0, 0
			resolver := GuardedSingleConflictResolver{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, request BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				resolverCalls++
				content := fmt.Sprintf("combined round %d\n", resolverCalls)
				return BatchAgentSessionResult{Output: batchResolutionJSON("combined both sides")}, os.WriteFile(filepath.Join(request.IntegrationRoot, "README.md"), []byte(content), 0o600)
			})}
			reviewer := GuardedSingleIntegrationReviewer{Git: git, Recorder: record, Agent: batchSessionAgentFunc(func(_ context.Context, _ BatchAgentSessionRequest) (BatchAgentSessionResult, error) {
				reviewerCalls++
				if !tt.failVerify && reviewerCalls == 1 {
					return BatchAgentSessionResult{Output: reviewJSON("changes_requested", "rework required", "resolve interaction")}, nil
				}
				return BatchAgentSessionResult{Output: reviewJSON("approve", "independent result", "")}, nil
			})}
			runner := func(_ context.Context, _, _ string, _ []string, stdout, _ io.Writer) error {
				verifyCalls++
				if tt.failVerify && verifyCalls == 1 {
					return errors.New("verification failed")
				}
				_, _ = io.WriteString(stdout, "verified resolved head\n")
				return nil
			}
			service := Service{Git: git, Runner: runner, Cleaner: successfulCleanup(), Events: events, SingleResolver: resolver, SingleReviewer: reviewer}

			err = service.Merge(context.Background(), detail, Options{VerifyCommand: "test -f README.md"})
			if tt.failVerify {
				if !errors.Is(err, ErrVerifyFailed) {
					t.Fatalf("first merge error = %v, want verification failure", err)
				}
			} else if !errors.Is(err, ErrSingleReviewNotApproved) {
				t.Fatalf("first merge error = %v, want review non-approval", err)
			}
			rolledBack := detail.State.Plan.MergeCommitIntent
			if rolledBack == nil || rolledBack.Resolution == nil || rolledBack.Resolution.Phase != plan.SingleMergeResolutionPhaseRolledBack || rolledBack.Resolution.RollbackReason != tt.wantReason || rolledBack.Resolution.RolledBackAt.IsZero() {
				t.Fatalf("rollback settlement = %#v, want durable %s settlement", rolledBack, tt.wantReason)
			}
			if !tt.failVerify && (rolledBack.Resolution.Review == nil || rolledBack.Resolution.Review.Verdict != plan.ReviewVerdictChangesRequested) {
				t.Fatalf("rollback settlement lost reviewer diagnostics: %#v", rolledBack.Resolution.Review)
			}
			if got := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != defaultHead {
				t.Fatalf("first merge left default at %s, want restored %s", got, defaultHead)
			}
			rollbackEvent := events.requireSingle(t, plan.EventTypeSingleMergeRolledBack)
			if rollbackEvent.Reason != string(tt.wantReason) || rollbackEvent.SingleMergeResolution == nil || rollbackEvent.SingleMergeResolution.Phase != plan.SingleMergeResolutionPhaseRolledBack {
				t.Fatalf("rollback event lost durable diagnostics: %#v", rollbackEvent)
			}
			if tt.failVerify {
				if err := record.ClearSingleMergeCommitIntent(*rolledBack); err != nil {
					t.Fatalf("clear settled rollback: %v", err)
				}
			}
			if events.count(plan.EventTypeSingleMergeRolledBack) != 1 {
				t.Fatalf("superseding inactive intent changed rollback history: %#v", events.events)
			}

			if err := os.WriteFile(filepath.Join(fixture.worktreePath, "README.md"), []byte("reworked source\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			runRealGit(t, fixture.worktreePath, "commit", "-am", "rework conflict")
			newSourceHead := realGitOutput(t, fixture.worktreePath, "rev-parse", "HEAD")
			newBase := realGitOutput(t, fixture.repoRoot, "merge-base", defaultHead, newSourceHead)
			freshReview := plan.PlanReview{
				Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove,
				CommitMessage: &plan.ReviewCommitMessage{
					Subject: "fix(merge): integrate reworked conflict",
					Body:    "What:\nIntegrate the refreshed source resolution.\n\nWhy:\nReplace the rolled-back integration safely.",
				},
				Base: newBase, Head: newSourceHead, ReviewedAt: time.Now().UTC(),
			}
			if err := record.RecordReviewCompleted(freshReview, "pi"); err != nil {
				t.Fatalf("record refreshed source review: %v", err)
			}
			if detail.State.Plan.MergeCommitIntent != nil {
				t.Fatalf("refreshed changed-source review retained inactive rollback: %#v", detail.State.Plan.MergeCommitIntent)
			}

			if err := service.Merge(context.Background(), detail, Options{VerifyCommand: "test -f README.md"}); err != nil {
				t.Fatalf("merge reworked and refreshed source: %v", err)
			}
			wantReviewerCalls := 2
			if tt.failVerify {
				wantReviewerCalls = 1
			}
			if resolverCalls != 2 || reviewerCalls != wantReviewerCalls || verifyCalls != 2 {
				t.Fatalf("rerun calls resolver=%d reviewer=%d verify=%d", resolverCalls, reviewerCalls, verifyCalls)
			}
			if events.count(plan.EventTypePlanMerged) != 1 {
				t.Fatalf("rerun merge evidence = %#v", events.events)
			}
			if got := realGitOutput(t, fixture.repoRoot, "show", "HEAD:README.md"); got != "combined round 2" {
				t.Fatalf("rerun resolved content = %q", got)
			}
		})
	}
}

func TestPrepareSingleMergeIntentGeneratesLegacyProposalOnceBeforeMutation(t *testing.T) {
	detail := mergeReadyDetail("base123")
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = "/repo"
	detail.State.Plan.Review.Head = ""
	detail.State.Plan.Review.CommitMessage = nil
	git := &fakeGitClient{
		root: "/repo", defaultBranch: "main", mergeBase: "base123", diff: "diff --git a/a.go b/a.go\n+change\n",
		revParse:          map[string]string{"main": "pre123", "tao/plan-a": "tip-sha"},
		dirtyFingerprints: []gitops.DirtyFingerprint{{Hash: "clean"}, {Hash: "clean"}},
	}
	generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
	events := &fakeEventAppender{}
	service := Service{Git: git, Events: events, ProposalGenerator: generator, Now: func() time.Time { return time.Date(2026, 7, 23, 21, 0, 0, 0, time.UTC) }}

	intent, err := service.prepareSingleMergeIntent(context.Background(), git, detail)
	if err != nil {
		t.Fatal(err)
	}
	if generator.calls != 1 || generator.exact.MergeBase != "base123" || generator.exact.SourceHead != "tip-sha" || generator.exact.Diff != git.diff {
		t.Fatalf("generator call/context = %d, %#v", generator.calls, generator.exact)
	}
	if !slices.Contains(git.calls, "merge-base pre123 tip-sha") {
		t.Fatalf("merge base was not bound to exact revisions: %#v", git.calls)
	}
	if !strings.Contains(intent.Message, "feat(merge): generate legacy merge messages") || !strings.Contains(intent.Message, "Tao-Source-Head: tip-sha") {
		t.Fatalf("unexpected generated intent message: %q", intent.Message)
	}
	if len(events.stateWrites) != 1 || events.stateWrites[0].Plan.MergeCommitIntent == nil {
		t.Fatalf("intent was not persisted exactly once: %#v", events.stateWrites)
	}
	for _, call := range git.calls {
		if strings.HasPrefix(call, "checkout ") || strings.HasPrefix(call, "merge-squash ") || strings.HasPrefix(call, "commit ") {
			t.Fatalf("proposal preparation mutated merge state: %#v", git.calls)
		}
	}
}

func TestMergeForceGeneratesExceptionalIntentBeforeSquash(t *testing.T) {
	detail := mergeReadyDetail("base123")
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = "/repo"
	detail.State.Plan.Review = nil
	git := &fakeGitClient{
		root: "/repo", defaultBranch: "main", mergeBase: "base123", diff: "diff --git a/a.go b/a.go\n+change\n",
		revParse:  map[string]string{"main": "pre123", "tao/plan-a": "tip-sha"},
		ancestors: map[string]bool{"tao/plan-a..main": false}, dirtyFingerprints: []gitops.DirtyFingerprint{{Hash: "clean"}, {Hash: "clean"}},
	}
	generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
	events := &fakeEventAppender{}
	service := Service{Git: git, Cleaner: successfulCleanup(), Events: events, ProposalGenerator: generator}

	if err := service.Merge(context.Background(), detail, Options{Force: true, NoVerify: true}); err != nil {
		t.Fatal(err)
	}
	if generator.calls != 1 {
		t.Fatalf("force generation calls = %d, want 1", generator.calls)
	}
	intentWrite := -1
	checkoutCall := -1
	for i, state := range events.stateWrites {
		if state.Plan.MergeCommitIntent != nil && intentWrite < 0 {
			intentWrite = i
		}
	}
	for i, call := range git.calls {
		if strings.HasPrefix(call, "checkout ") {
			checkoutCall = i
			break
		}
	}
	if intentWrite < 0 || checkoutCall < 0 {
		t.Fatalf("missing durable intent or squash mutation: writes=%#v calls=%#v", events.stateWrites, git.calls)
	}
}

func TestMergeForceGeneratesFromLiveDiffWhenApprovedReviewBaseIsStale(t *testing.T) {
	detail := mergeReadyDetail("reviewed-base")
	detail.Dir = t.TempDir()
	detail.State.Repo.Root = "/repo"
	detail.State.Plan.Review.Head = "tip-sha"
	git := &fakeGitClient{
		root: "/repo", defaultBranch: "main", mergeBase: "live-base", diff: "diff --git a/a.go b/a.go\n+live change\n",
		revParse:  map[string]string{"main": "pre123", "tao/plan-a": "tip-sha"},
		ancestors: map[string]bool{"tao/plan-a..main": false}, dirtyFingerprints: []gitops.DirtyFingerprint{{Hash: "clean"}, {Hash: "clean"}},
	}
	generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
	service := Service{Git: git, Cleaner: successfulCleanup(), Events: &fakeEventAppender{}, ProposalGenerator: generator}

	if err := service.Merge(context.Background(), detail, Options{Force: true, NoVerify: true}); err != nil {
		t.Fatal(err)
	}
	if generator.calls != 1 {
		t.Fatalf("force generation calls = %d, want 1", generator.calls)
	}
	if generator.exact.MergeBase != "live-base" || generator.exact.SourceHead != "tip-sha" || generator.exact.Diff != git.diff {
		t.Fatalf("generator did not receive the exact live diff: %#v", generator.exact)
	}
	if slices.Contains(git.calls, "diff reviewed-base..tip-sha") || !slices.Contains(git.calls, "diff live-base..tip-sha") {
		t.Fatalf("stale review base influenced generated diff: %#v", git.calls)
	}
	for _, call := range git.calls {
		if strings.HasPrefix(call, "commit ") && strings.Contains(call, "use approved review message") {
			t.Fatalf("forced merge reused stale review proposal: %q", call)
		}
	}
}

func TestMergeNonSquashAndPreGateFailuresNeverGenerateProposal(t *testing.T) {
	t.Run("no squash", func(t *testing.T) {
		detail := mergeVerifyDetail()
		git := mergeVerifyGit()
		generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
		err := (Service{Git: git, Cleaner: successfulCleanup(), Events: &fakeEventAppender{}, ProposalGenerator: generator}).Merge(context.Background(), detail, Options{NoSquash: true, NoVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		if generator.calls != 0 {
			t.Fatalf("no-squash merge generated %d proposals", generator.calls)
		}
	})

	t.Run("pre gate", func(t *testing.T) {
		detail := notApprovedDetail()
		generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
		err := (Service{Git: &fakeGitClient{}, ProposalGenerator: generator}).Merge(context.Background(), detail, Options{})
		if !errors.Is(err, ErrNotApproved) {
			t.Fatalf("error = %v, want approval gate", err)
		}
		if generator.calls != 0 {
			t.Fatalf("pre-gate failure generated %d proposals", generator.calls)
		}
	})

	t.Run("record only", func(t *testing.T) {
		detail := mergeReadyDetail("base123")
		git := &fakeGitClient{defaultBranch: "main", revParse: map[string]string{"main": "pre123", "tao/plan-a": "tip-sha"}, ancestors: map[string]bool{"tao/plan-a..main": false}}
		generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
		err := (Service{Git: git, ProposalGenerator: generator}).Merge(context.Background(), detail, Options{RecordOnly: true})
		if err == nil || !strings.Contains(err.Error(), "plan is not already merged") {
			t.Fatalf("error = %v, want record-only refusal", err)
		}
		if generator.calls != 0 {
			t.Fatalf("record-only path generated %d proposals", generator.calls)
		}
	})

	t.Run("external detection", func(t *testing.T) {
		detail := mergeReadyDetail("base123")
		detail.Dir = t.TempDir()
		git := &fakeGitClient{defaultBranch: "main", revParse: map[string]string{"main": "merged123", "tao/plan-a": "tip-sha"}, ancestors: map[string]bool{"tao/plan-a..main": true, "base123..tao/plan-a": true}}
		generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
		err := (Service{Git: git, Cleaner: successfulCleanup(), Events: &fakeEventAppender{}, ProposalGenerator: generator}).Merge(context.Background(), detail, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if generator.calls != 0 {
			t.Fatalf("external detection generated %d proposals", generator.calls)
		}
	})

	t.Run("already recorded cleanup", func(t *testing.T) {
		detail := mergeReadyDetail("base123")
		detail.Events = []plan.Event{{Type: plan.EventTypePlanMerged}}
		generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
		err := (Service{Git: &fakeGitClient{}, Cleaner: successfulCleanup(), ProposalGenerator: generator}).Merge(context.Background(), detail, Options{NoSquash: true})
		if err != nil {
			t.Fatal(err)
		}
		if generator.calls != 0 {
			t.Fatalf("already-recorded cleanup generated %d proposals", generator.calls)
		}
	})
}

func TestPrepareSingleMergeIntentReusesExceptionalIntentWithoutGeneratorAndRefusesDrift(t *testing.T) {
	newDetail := func() *plan.PlanDetail {
		detail := mergeReadyDetail("base123")
		detail.State.Plan.Review = nil
		detail.State.Plan.MergeCommitIntent = &plan.SingleMergeCommitIntent{
			Message: "feat(merge): generate legacy merge messages\n\nWhat:\nGenerate the message.\n\nWhy:\nKeep recovery exact.\n\nTao-Plan: plan-a\nTao-Source-Head: tip-sha",
			PlanID:  "plan-a", SourceHead: "tip-sha", DefaultBranch: "main", DefaultParent: "pre123", CreatedAt: time.Now(),
		}
		return detail
	}
	generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
	git := &fakeGitClient{defaultBranch: "main", revParse: map[string]string{"main": "pre123", "tao/plan-a": "tip-sha"}}
	intent, err := (Service{Git: git, ProposalGenerator: generator}).prepareSingleMergeIntent(context.Background(), git, newDetail())
	if err != nil || intent.SourceHead != "tip-sha" {
		t.Fatalf("reuse = %#v, %v", intent, err)
	}
	if generator.calls != 0 {
		t.Fatalf("persisted exceptional intent started %d sessions", generator.calls)
	}

	drifted := &fakeGitClient{defaultBranch: "main", revParse: map[string]string{"main": "drifted", "tao/plan-a": "tip-sha"}}
	_, err = (Service{Git: drifted, ProposalGenerator: generator}).prepareSingleMergeIntent(context.Background(), drifted, newDetail())
	if err == nil || !strings.Contains(err.Error(), "drifted from single-merge intent") {
		t.Fatalf("expected exact ref-drift refusal, got %v", err)
	}
	if generator.calls != 0 {
		t.Fatalf("drifted persisted intent started %d sessions", generator.calls)
	}
}

func TestPrepareSingleMergeIntentRejectsProviderAndGitMutationBeforeIntent(t *testing.T) {
	tests := []struct {
		name         string
		generatorErr error
		fingerprints []gitops.DirtyFingerprint
		want         string
	}{
		{name: "provider", generatorErr: errors.New("provider unavailable"), fingerprints: []gitops.DirtyFingerprint{{Hash: "same"}, {Hash: "same"}}, want: "provider unavailable"},
		{name: "git mutation", fingerprints: []gitops.DirtyFingerprint{{Hash: "before"}, {Hash: "after"}}, want: "mutated Git state"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := mergeReadyDetail("base123")
			detail.Dir = t.TempDir()
			detail.State.Plan.Review.CommitMessage = nil
			git := &fakeGitClient{root: "/repo", defaultBranch: "main", mergeBase: "base123", diff: "diff", revParse: map[string]string{"main": "pre123", "tao/plan-a": "tip-sha"}, dirtyFingerprints: tt.fingerprints}
			generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal(), err: tt.generatorErr}
			events := &fakeEventAppender{}
			_, err := (Service{Git: git, Events: events, ProposalGenerator: generator}).prepareSingleMergeIntent(context.Background(), git, detail)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if detail.State.Plan.MergeCommitIntent != nil || len(events.stateWrites) != 0 {
				t.Fatalf("failed generation persisted intent: %#v %#v", detail.State.Plan.MergeCommitIntent, events.stateWrites)
			}
			if generator.calls != 1 {
				t.Fatalf("generator calls = %d, want 1", generator.calls)
			}
		})
	}
}

func TestGenerateSingleMergeMessageDetectsInPlaceUntrackedMutation(t *testing.T) {
	fixture := newRealGitWorktree(t)
	ctx := context.Background()
	untrackedPath := filepath.Join(fixture.repoRoot, "scratch.txt")
	if err := os.WriteFile(untrackedPath, []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	defaultHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch)
	sourceHead := realGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch)
	generator := &fakeMergeProposalGenerator{
		proposal: generatedMergeProposal(),
		mutate: func() error {
			return os.WriteFile(untrackedPath, []byte("omega\n"), 0o600)
		},
	}
	service := Service{Git: gitops.NewClient(fixture.repoRoot, nil), ProposalGenerator: generator}

	_, err := service.generateSingleMergeMessage(ctx, service.Git, commitcontract.MergeProposalContext{
		RepoRoot: fixture.repoRoot, PlanID: "plan-a", DefaultBranch: fixture.defaultBranch,
		DefaultParent: defaultHead, MergeBase: defaultHead, SourceBranch: fixture.planBranch, SourceHead: sourceHead,
	})
	if err == nil || !strings.Contains(err.Error(), "mutated Git state") {
		t.Fatalf("expected in-place untracked mutation refusal, got %v", err)
	}
	if generator.calls != 1 {
		t.Fatalf("generator calls = %d, want 1", generator.calls)
	}
}

func TestPrepareSingleMergeIntentCurrentReviewIsZeroCall(t *testing.T) {
	detail := mergeReadyDetail("base123")
	detail.Dir = t.TempDir()
	detail.State.Plan.Review.Head = "tip-sha"
	git := &fakeGitClient{defaultBranch: "main", mergeBase: "base123", revParse: map[string]string{"main": "pre123", "tao/plan-a": "tip-sha"}}
	generator := &fakeMergeProposalGenerator{proposal: generatedMergeProposal()}
	if _, err := (Service{Git: git, Events: &fakeEventAppender{}, ProposalGenerator: generator}).prepareSingleMergeIntent(context.Background(), git, detail); err != nil {
		t.Fatal(err)
	}
	if generator.calls != 0 {
		t.Fatalf("current approved review started %d proposal sessions", generator.calls)
	}
	for _, call := range git.calls {
		if strings.HasPrefix(call, "diff ") || call == "dirty-fingerprint" {
			t.Fatalf("current approved review entered exceptional context path: %#v", git.calls)
		}
	}
}

func TestMergeRollsBackWhenMergedSHACaptureFails(t *testing.T) {
	captureErr := errors.New("rev-parse unavailable")
	git := &fakeGitClient{
		defaultBranch:    "main",
		mergeBase:        "base123",
		ancestors:        map[string]bool{"main..tao/plan-a": true},
		revParse:         map[string]string{"tao/plan-a": "tip-sha"},
		revParseSequence: map[string][]string{"main": {"pre123", "pre123"}},
		revParseErrors:   map[string][]error{"main": {nil, nil, captureErr}},
	}
	events := &fakeEventAppender{}
	cleaner := successfulCleanup()
	detail := mergeReadyDetail("base123")
	detail.Dir = t.TempDir()
	detail.State.Plan.Review.Head = "tip-sha"

	err := (Service{Git: git, Cleaner: cleaner, Events: events}).Merge(context.Background(), detail, Options{NoVerify: true})
	if err == nil || !errors.Is(err, captureErr) {
		t.Fatalf("expected merged-SHA capture failure, got %v", err)
	}
	verificationEvent := events.requireSingle(t, plan.EventTypeMergeVerification)
	if verificationEvent.Result != "skipped" || events.count(plan.EventTypePlanMerged) != 0 || len(cleaner.calls) != 0 {
		t.Fatalf("failed capture should retain only the skipped verification event and not clean up: events=%#v cleanup=%#v", events.events, cleaner.calls)
	}
	wantSuffix := []string{"reset-hard pre123", "checkout main"}
	if got := git.calls[len(git.calls)-len(wantSuffix):]; !reflect.DeepEqual(got, wantSuffix) {
		t.Fatalf("rollback calls mismatch\nwant: %#v\n got: %#v", wantSuffix, git.calls)
	}
}

func TestMergeRecordRetryConsumesSettledJournalEvidence(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	if err := os.Mkdir(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := mergeReadyDetail("base123")
	base.Dir = planDir
	base.State.Schema = "tao.plan.state.v1"
	base.State.Status = plan.StatusReviewed
	base.Slices.Schema = "tao.plan.slices.v1"
	base.Slices.PlanID = "plan-a"
	writeMergeRestartJSON(t, filepath.Join(planDir, "state.json"), base.State)
	writeMergeRestartJSON(t, filepath.Join(planDir, "slices.json"), base.Slices)

	mergedAt := time.Date(2026, 7, 20, 18, 30, 0, 0, time.UTC)
	settledState := base.State
	settledState.Status = plan.StatusCompleted
	settledState.UpdatedAt = mergedAt
	settledState.Plan.Timing.LastActivityAt = &mergedAt
	event := plan.Event{Type: plan.EventTypePlanMerged, Timestamp: mergedAt, PlanID: "plan-a", MutationID: "restart-merge", Branch: "tao/plan-a", MergedDefaultSHA: "merged456", Message: "Plan merged into default branch"}
	writeMergeRestartJournal(t, planDir, settledState, event)

	service := Service{Now: func() time.Time { return mergedAt.Add(time.Minute) }}
	if err := service.AppendPlanMergedEvent(base, "tao/plan-a", "merged456"); err != nil {
		t.Fatalf("retry merge recording after journal settlement: %v", err)
	}
	reloaded, err := plan.NewFileRepository(plansDir).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	mergedEvents := 0
	for _, got := range reloaded.Events {
		if got.Type == plan.EventTypePlanMerged {
			mergedEvents++
		}
	}
	if mergedEvents != 1 || reloaded.State.Status != plan.StatusCompleted {
		t.Fatalf("settled merge evidence = status %q events %#v", reloaded.State.Status, reloaded.Events)
	}
	if len(base.Events) != 1 || base.Events[0].MergedDefaultSHA != "merged456" {
		t.Fatalf("merge consumer retained stale events: %#v", base.Events)
	}
	if _, statErr := os.Stat(filepath.Join(planDir, ".mutation.json")); !os.IsNotExist(statErr) {
		t.Fatalf("settled merge journal remains: %v", statErr)
	}
}

type mergeRestartPayload struct {
	Payload []byte `json:"payload"`
	SHA256  string `json:"sha256"`
}

func writeMergeRestartJournal(t *testing.T, planDir string, state plan.State, event plan.Event) {
	t.Helper()
	statePayload := mergeRestartJSONPayload(t, state, true)
	eventPayload := mergeRestartJSONPayload(t, event, false)
	journal := struct {
		Schema     string                `json:"schema"`
		MutationID string                `json:"mutation_id"`
		PlanID     string                `json:"plan_id"`
		CreatedAt  time.Time             `json:"created_at"`
		State      *mergeRestartPayload  `json:"state"`
		Events     []mergeRestartPayload `json:"events"`
	}{
		Schema: "tao.plan.mutation.v1", MutationID: "restart-merge", PlanID: "plan-a", CreatedAt: event.Timestamp,
		State: statePayload, Events: []mergeRestartPayload{*eventPayload},
	}
	writeMergeRestartJSON(t, filepath.Join(planDir, ".mutation.json"), journal)
}

func mergeRestartJSONPayload(t *testing.T, value any, indent bool) *mergeRestartPayload {
	t.Helper()
	var payload []byte
	var err error
	if indent {
		payload, err = json.MarshalIndent(value, "", "  ")
		payload = append(payload, '\n')
	} else {
		payload, err = json.Marshal(value)
	}
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	return &mergeRestartPayload{Payload: payload, SHA256: hex.EncodeToString(sum[:])}
}

func writeMergeRestartJSON(t *testing.T, path string, value any) {
	t.Helper()
	payload, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	payload = append(payload, '\n')
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMergeRetainsDurableIntentWhenPlanMergedEventFails(t *testing.T) {
	recordErr := errors.New("event store unavailable")
	git := &fakeGitClient{
		defaultBranch:    "main",
		mergeBase:        "base123",
		ancestors:        map[string]bool{"main..tao/plan-a": true},
		revParse:         map[string]string{"tao/plan-a": "tip-sha"},
		revParseSequence: map[string][]string{"main": {"pre123", "pre123", "merged456"}},
	}
	events := &fakeEventAppender{err: recordErr}
	cleaner := successfulCleanup()
	detail := mergeReadyDetail("base123")
	detail.Dir = t.TempDir()
	detail.State.Status = plan.StatusReviewed
	detail.State.Plan.Review.Head = "tip-sha"

	err := (Service{Git: git, Cleaner: cleaner, Events: events}).Merge(context.Background(), detail, Options{NoVerify: true})
	if err == nil || !errors.Is(err, recordErr) {
		t.Fatalf("expected event persistence failure, got %v", err)
	}
	if events.count(plan.EventTypeMergeVerification) != 0 || events.count(plan.EventTypePlanMerged) != 0 || len(cleaner.calls) != 0 {
		t.Fatalf("failed recording must not retain a merge event or clean up: events=%#v cleanup=%#v", events.events, cleaner.calls)
	}
	if len(events.stateWrites) != 2 || events.stateWrites[0].Plan.MergeCommitIntent == nil || events.stateWrites[1].Status != plan.StatusCompleted || events.stateWrites[1].Plan.MergeCommitIntent != nil {
		t.Fatalf("intent and merge evidence should remain installed for journal replay, got %#v", events.stateWrites)
	}
	if detail.State.Plan.MergeCommitIntent == nil || detail.State.Status != plan.StatusReviewed {
		t.Fatalf("loaded detail did not retain recoverable intent after event failure: %#v", detail.State)
	}
	wantSuffix := []string{"reset-hard pre123", "checkout main"}
	if got := git.calls[len(git.calls)-len(wantSuffix):]; !reflect.DeepEqual(got, wantSuffix) {
		t.Fatalf("rollback calls mismatch\nwant: %#v\n got: %#v", wantSuffix, git.calls)
	}
}

func TestMergeDirtyInCleanupGapPreservesRecordedStateAndRetries(t *testing.T) {
	fixture := newRealGitWorktree(t)
	commitRealPlanChange(t, fixture)
	cleaner := newRealManagedCleaner(t, fixture)
	dirtyPath := filepath.Join(fixture.worktreePath, "dirty-in-cleanup-gap.txt")
	dirtied := false
	cleaner.beforeClean = func() {
		if dirtied {
			return
		}
		dirtied = true
		if err := os.WriteFile(dirtyPath, []byte("late change\n"), 0o600); err != nil {
			t.Fatalf("dirty worktree after merge gate: %v", err)
		}
	}

	git := &fakeGitClient{
		defaultBranch:    "main",
		mergeBase:        "base123",
		ancestors:        map[string]bool{"main..tao/plan-a": true},
		revParse:         map[string]string{"tao/plan-a": "tip-sha"},
		revParseSequence: map[string][]string{"main": {"pre123", "pre123", "merged456"}},
	}
	detail := mergeReadyDetail("base123")
	detail.Dir = t.TempDir()
	detail.State.Status = plan.StatusReviewed
	detail.State.Plan.Review.Head = "tip-sha"
	detail.State.Repo.Root = fixture.repoRoot
	events := &fakeEventAppender{}
	service := Service{Git: git, Cleaner: cleaner, Events: events}

	err := service.Merge(context.Background(), detail, Options{NoVerify: true})
	if err == nil || !strings.Contains(err.Error(), "has uncommitted changes") {
		t.Fatalf("expected fresh-cleanliness cleanup refusal, got %v", err)
	}
	events.requireSingle(t, plan.EventTypeMergeVerification)
	events.requireSingle(t, plan.EventTypePlanMerged)
	if events.state == nil || events.state.Status != plan.StatusCompleted || detail.State.Status != plan.StatusCompleted {
		t.Fatalf("recorded completed state must survive refusal: stored=%#v detail=%q", events.state, detail.State.Status)
	}
	if _, statErr := os.Stat(fixture.worktreePath); statErr != nil {
		t.Fatalf("refused cleanup must leave worktree intact: %v", statErr)
	}

	if err := os.Remove(dirtyPath); err != nil {
		t.Fatal(err)
	}
	cleaner.beforeClean = nil
	result, err := service.Cleanup(context.Background(), detail, Options{allowNonAncestralCleanup: true})
	if err != nil {
		t.Fatalf("cleanup retry after cleaning worktree failed: %v", err)
	}
	if !result.Removed {
		t.Fatal("cleanup retry should report removal")
	}
	assertRealCleanupRemoved(t, fixture)
	if events.count(plan.EventTypePlanMerged) != 1 {
		t.Fatalf("cleanup retry must not duplicate merge recording, got %#v", events.events)
	}
}

// TestMergeRecordsMergeBeforeCleanup guards the recovery bug where the normal
// merge path ran cleanup (deleting the plan branch and worktree) before
// recording the merge: a failed record left a merged plan with no plan_merged
// event and no surviving refs to retry from. The merge must be recorded first,
// and a cleanup failure after recording must surface without unrecording.
func TestMergeRecordsMergeBeforeCleanup(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch:    "main",
		mergeBase:        "base123",
		ancestors:        map[string]bool{"main..tao/plan-a": true},
		revParse:         map[string]string{"tao/plan-a": "tip-sha"},
		revParseSequence: map[string][]string{"main": {"pre123", "pre123", "merged456"}},
	}
	detail := mergeReadyDetail("base123")
	detail.Dir = t.TempDir()
	detail.State.Plan.Review.Head = "tip-sha"
	cleaner := successfulCleanup()
	cleaner.cleanErr = errors.New("worktree busy")
	events := &fakeEventAppender{}
	var order []string
	cleaner.onCall = func(call string) { order = append(order, call) }
	events.onCall = func(call string) { order = append(order, call) }

	err := (Service{Git: git, Cleaner: cleaner, Events: events}).Merge(context.Background(), detail, Options{NoVerify: true})
	if err == nil {
		t.Fatal("expected cleanup failure to surface")
	}
	if !strings.Contains(err.Error(), "cleanup failed") {
		t.Fatalf("error %q should attribute the failure to cleanup", err.Error())
	}
	events.first(t, plan.EventTypeMergeVerification)
	events.first(t, plan.EventTypePlanMerged)
	recordIndex := indexOf(order, "append-event")
	cleanupIndex := indexOf(order, "plan-clean plan-a")
	if recordIndex < 0 || cleanupIndex < 0 || recordIndex > cleanupIndex {
		t.Fatalf("merge must be recorded before cleanup runs, order=%#v", order)
	}
}

// TestTryRecordExternalMergeRefusesDirtyPlanWorktree guards the data-loss bug
// where the external-merge record path checked only the repo-root worktree, so a
// plan whose separate worktree still held uncommitted work could be recorded as
// merged/completed and have its branch deleted.
func TestTryRecordExternalMergeRefusesDirtyPlanWorktree(t *testing.T) {
	// Real directories: hasSeparatePlanWorktree only trusts a worktree that
	// exists on disk.
	repoRoot := t.TempDir()
	worktreePath := t.TempDir()
	reg := newFakeGitRegistry()
	root := &fakeGitClient{
		defaultBranch: "main",
		ancestors: map[string]bool{
			"tao/plan-a..main":     true, // branch is already an ancestor: external merge
			"base-sha..tao/plan-a": true, // plan base precedes the branch (carries work)
			"tao/plan-a..base-sha": false,
		},
		revParse: map[string]string{"main": "merged-default-sha"},
	}
	reg.seed(repoRoot, root)
	reg.seed(worktreePath, &fakeGitClient{status: " M feature.go\n"})

	detail := mergeReadyDetail("base123")
	detail.State.Repo.Root = repoRoot
	detail.State.Workspace.BaseSHA = "base-sha"
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	detail.State.Workspace.Path = worktreePath

	cleaner := successfulCleanup()
	service := Service{
		Git:     reg.client(repoRoot),
		NewGit:  reg.newGit,
		Cleaner: cleaner,
	}

	recorded, err := service.tryRecordExternalMerge(context.Background(), reg.client(repoRoot), detail, Options{})
	if err == nil {
		t.Fatal("expected dirty plan-worktree error")
	}
	if !errors.Is(err, ErrDirtyWorktree) {
		t.Fatalf("expected ErrDirtyWorktree, got %v", err)
	}
	if recorded {
		t.Fatal("merge must not be recorded when the plan worktree is dirty")
	}
	if len(cleaner.calls) != 0 {
		t.Fatalf("cleanup must not run when the merge is refused, got calls %#v", cleaner.calls)
	}
}

func mergeReadyDetail(reviewBase string) *plan.PlanDetail {
	return &plan.PlanDetail{
		State: plan.State{
			Status: plan.StatusCompleted,
			Plan: plan.PlanState{
				ID: "plan-a",
				Review: &plan.PlanReview{
					Status:  plan.ReviewStatusCompleted,
					Verdict: plan.ReviewVerdictApprove,
					CommitMessage: &plan.ReviewCommitMessage{
						Subject: "feat(merge): use approved review message",
						Body:    "What:\nCreate the exact reviewed squash commit.\n\nWhy:\nAvoid a merge-time message session.",
					},
					Base: reviewBase,
				},
			},
			Workspace: &plan.Workspace{Branch: "tao/plan-a", BaseBranch: "main"},
		},
		Slices: plan.SlicesFile{Slices: []plan.Slice{{ID: "001-a", Status: plan.StatusCompleted}}},
	}
}

func notApprovedDetail() *plan.PlanDetail {
	detail := mergeReadyDetail("base123")
	detail.State.Status = plan.StatusInProgress
	detail.State.Plan.Review = nil
	return detail
}
