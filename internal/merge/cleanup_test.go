package merge

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

type fakeWorkspaceCleaner struct {
	cleanPlan      workspace.CleanPlan
	managed        []workspace.ManagedCleanup
	planCleanErr   error
	planManagedErr error
	cleanErr       error
	calls          []string
	cleaned        []workspace.ManagedCleanup
	cleanOptions   []workspace.CleanOptions
	onCall         func(string)
}

func successfulCleanup() *fakeWorkspaceCleaner {
	return &fakeWorkspaceCleaner{
		cleanPlan: workspace.CleanPlan{PlanID: "plan-a", Branch: "tao/plan-a", Status: "clean", CanRemove: true},
		managed:   []workspace.ManagedCleanup{{Branch: "tao/plan-a", Status: workspace.ManagedStatusClean, CanRemove: true, Reason: "merged into main", WorktreePath: "/repo/.tao/workspaces/plan-a"}},
	}
}

func (f *fakeWorkspaceCleaner) PlanClean(ctx context.Context, planID string) (workspace.CleanPlan, error) {
	_ = ctx
	f.record("plan-clean " + planID)
	if f.planCleanErr != nil {
		return workspace.CleanPlan{}, f.planCleanErr
	}
	return f.cleanPlan, nil
}

func (f *fakeWorkspaceCleaner) PlanManagedCleanup(ctx context.Context) ([]workspace.ManagedCleanup, error) {
	_ = ctx
	f.record("plan-managed-cleanup")
	if f.planManagedErr != nil {
		return nil, f.planManagedErr
	}
	return append([]workspace.ManagedCleanup(nil), f.managed...), nil
}

func (f *fakeWorkspaceCleaner) CleanManaged(ctx context.Context, item workspace.ManagedCleanup, options workspace.CleanOptions) error {
	_ = ctx
	f.record("clean-managed " + item.Branch)
	f.cleaned = append(f.cleaned, item)
	f.cleanOptions = append(f.cleanOptions, options)
	return f.cleanErr
}

func (f *fakeWorkspaceCleaner) record(call string) {
	f.calls = append(f.calls, call)
	if f.onCall != nil {
		f.onCall(call)
	}
}

type realManagedCleaner struct {
	manager       *workspace.Manager
	cleanPlan     workspace.CleanPlan
	forceUnmerged bool
	beforeClean   func()
}

func newRealManagedCleaner(t *testing.T, fixture realGitWorktree) *realManagedCleaner {
	t.Helper()
	manager, err := workspace.NewManager(workspace.Options{RepoRoot: fixture.repoRoot})
	if err != nil {
		t.Fatal(err)
	}
	return &realManagedCleaner{
		manager: manager,
		cleanPlan: workspace.CleanPlan{
			PlanID: "plan-a", Branch: fixture.planBranch, Status: "clean", CanRemove: true,
			Path: fixture.worktreePath, BranchExists: true,
		},
	}
}

func (c *realManagedCleaner) PlanClean(context.Context, string) (workspace.CleanPlan, error) {
	return c.cleanPlan, nil
}

func (c *realManagedCleaner) PlanManagedCleanup(ctx context.Context) ([]workspace.ManagedCleanup, error) {
	items, err := c.manager.PlanManagedCleanup(ctx)
	if err != nil {
		return nil, err
	}
	if c.forceUnmerged {
		for i := range items {
			if items[i].Branch == c.cleanPlan.Branch {
				items[i].Status = workspace.ManagedStatusUnmerged
				items[i].CanRemove = false
				items[i].MergedNonAncestral = false
				items[i].Reason = "not merged into main"
			}
		}
	}
	return items, nil
}

func (c *realManagedCleaner) CleanManaged(ctx context.Context, item workspace.ManagedCleanup, options workspace.CleanOptions) error {
	if c.beforeClean != nil {
		c.beforeClean()
	}
	return c.manager.CleanManaged(ctx, item, options)
}

func commitRealPlanChange(t *testing.T, fixture realGitWorktree) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.worktreePath, "feature.txt"), []byte("planned change\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runRealGit(t, fixture.worktreePath, "add", "feature.txt")
	runRealGit(t, fixture.worktreePath, "commit", "-m", "feat: planned change")
}

func assertRealCleanupRemoved(t *testing.T, fixture realGitWorktree) {
	t.Helper()
	if _, err := os.Stat(fixture.worktreePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("worktree should be removed, stat error=%v", err)
	}
	if branches := realGitOutput(t, fixture.repoRoot, "branch", "--list", fixture.planBranch); branches != "" {
		t.Fatalf("branch should be removed, got %q", branches)
	}
}

type fakeEventAppender struct {
	planDirs    []string
	events      []plan.Event
	state       *plan.State
	stateWrites []plan.State
	err         error
	onCall      func(string)
}

func (f *fakeEventAppender) AppendEvent(planDir string, event plan.Event) error {
	if f.onCall != nil {
		f.onCall("append-event")
	}
	if f.err != nil {
		return f.err
	}
	f.planDirs = append(f.planDirs, planDir)
	f.events = append(f.events, event)
	return nil
}

func (f *fakeEventAppender) count(eventType string) int {
	count := 0
	for _, event := range f.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func (f *fakeEventAppender) first(t testing.TB, eventType string) plan.Event {
	t.Helper()
	for _, event := range f.events {
		if event.Type == eventType {
			return event
		}
	}
	t.Fatalf("expected event %q, got %#v", eventType, f.events)
	return plan.Event{}
}

func (f *fakeEventAppender) requireSingle(t testing.TB, eventType string) plan.Event {
	t.Helper()
	if count := f.count(eventType); count != 1 {
		t.Fatalf("expected exactly one event %q, got %d in %#v", eventType, count, f.events)
	}
	return f.first(t, eventType)
}

// WriteState and WriteSlices make fakeEventAppender a full plan.ArtifactStore so
// recorded merges persist state through it (in memory) instead of the
// filesystem, keeping AppendPlanMergedEvent's state/event persistence unified.
func (f *fakeEventAppender) WriteState(_ string, payload []byte) error {
	var state plan.State
	if err := json.Unmarshal(payload, &state); err != nil {
		return err
	}
	f.state = &state
	f.stateWrites = append(f.stateWrites, state)
	return nil
}

func (f *fakeEventAppender) WriteSlices(_ string, _ []byte) error {
	return nil
}

func TestMergeCleanupRunsOnlyAfterSuccessfulVerify(t *testing.T) {
	reg := newFakeGitRegistry()
	reg.seed(mergeVerifyRoot, &fakeGitClient{
		defaultBranch:    "main",
		mergeBase:        "base123",
		revParse:         map[string]string{"main": "pre123", "tao/plan-a": "head123"},
		ancestors:        map[string]bool{"main..tao/plan-a": true},
		revParseSequence: map[string][]string{"main": {"pre123", "pre123", "merged456"}},
	})
	git := reg.client(mergeVerifyRoot)
	detail := mergeVerifyDetail()
	cleaner := successfulCleanup()
	events := &fakeEventAppender{}
	var order []string
	cleaner.onCall = func(call string) { order = append(order, call) }
	events.onCall = func(call string) { order = append(order, call) }
	runner := func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		_ = ctx
		_ = cwd
		_ = name
		_ = args
		_ = stderr
		order = append(order, "verify")
		_, _ = stdout.Write([]byte("ok\n"))
		return nil
	}

	err := (Service{Git: git, Runner: runner, Cleaner: cleaner, Events: events}).Merge(context.Background(), detail, Options{VerifyCommand: "go test ./internal/merge"})
	if err != nil {
		t.Fatal(err)
	}
	verifyIndex := indexOf(order, "verify")
	cleanupIndex := indexOf(order, "plan-clean plan-a")
	if verifyIndex < 0 || cleanupIndex < 0 || cleanupIndex < verifyIndex {
		t.Fatalf("cleanup should run after verify, order=%#v", order)
	}
	if len(cleaner.cleaned) != 1 || cleaner.cleaned[0].Branch != "tao/plan-a" || cleaner.cleanOptions[0].Force || cleaner.cleanOptions[0].AllowNonAncestralBranch {
		t.Fatalf("expected one ordinary clean managed removal, cleaned=%#v options=%#v", cleaner.cleaned, cleaner.cleanOptions)
	}
}

func TestCleanupRemovesRecordedSquashThroughEvidenceGate(t *testing.T) {
	fixture := newRealGitWorktree(t)
	commitRealPlanChange(t, fixture)
	runRealGit(t, fixture.repoRoot, "merge", "--squash", fixture.planBranch)
	runRealGit(t, fixture.repoRoot, "commit", "-m", "Plan A\n\nTao-Plan: plan-a")

	cleaner := newRealManagedCleaner(t, fixture)
	// Model the merge cleanup decision before generic patch-equivalence
	// detection: the branch is non-ancestral, while the just-recorded Tao
	// squash is the evidence that authorizes deleting it.
	cleaner.forceUnmerged = true
	detail := mergeVerifyDetail()
	detail.State.Repo.Root = fixture.repoRoot
	detail.Events = []plan.Event{{Type: plan.EventTypePlanMerged, Branch: fixture.planBranch}}

	result, err := (Service{Cleaner: cleaner}).Cleanup(context.Background(), detail, Options{allowNonAncestralCleanup: true})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Removed {
		t.Fatal("recorded squash cleanup should report removal")
	}
	assertRealCleanupRemoved(t, fixture)
}

func TestCleanupDeclinesUnmergedBranchWithoutEvidenceOrForce(t *testing.T) {
	fixture := newRealGitWorktree(t)
	commitRealPlanChange(t, fixture)
	cleaner := newRealManagedCleaner(t, fixture)
	detail := mergeVerifyDetail()
	detail.State.Repo.Root = fixture.repoRoot

	_, err := (Service{Cleaner: cleaner}).Cleanup(context.Background(), detail, Options{})
	if err == nil || !errors.Is(err, ErrCleanupDeclined) {
		t.Fatalf("expected unmerged cleanup decline, got %v", err)
	}
	if _, statErr := os.Stat(fixture.worktreePath); statErr != nil {
		t.Fatalf("declined cleanup must leave worktree intact: %v", statErr)
	}
	if branches := realGitOutput(t, fixture.repoRoot, "branch", "--list", fixture.planBranch); !strings.Contains(branches, fixture.planBranch) {
		t.Fatalf("declined cleanup must leave branch intact, got %q", branches)
	}
}

func TestCleanupRespectsUnmergedAndDirtyDecisionsWithoutForce(t *testing.T) {
	tests := []struct {
		name   string
		status string
		reason string
	}{
		{name: "unmerged", status: workspace.ManagedStatusUnmerged, reason: "not merged into main"},
		{name: "dirty", status: workspace.ManagedStatusDirty, reason: "worktree has uncommitted changes"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaner := successfulCleanup()
			cleaner.managed = []workspace.ManagedCleanup{{Branch: "tao/plan-a", Status: tt.status, Reason: tt.reason}}

			_, err := (Service{Cleaner: cleaner}).Cleanup(context.Background(), mergeVerifyDetail(), Options{})
			if err == nil {
				t.Fatal("expected cleanup decline")
			}
			if !errors.Is(err, ErrCleanupDeclined) {
				t.Fatalf("expected ErrCleanupDeclined, got %v", err)
			}
			if len(cleaner.cleaned) != 0 {
				t.Fatalf("declined cleanup should not remove, cleaned=%#v", cleaner.cleaned)
			}
		})
	}
}

func TestMergeRecordsAlreadyMergedPlanWithoutIntegrating(t *testing.T) {
	reg := newFakeGitRegistry()
	reg.seed(mergeVerifyRoot, &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "merged789"},
		// tao/plan-a is merged into main and carries plan work beyond the review
		// base (base123 leads the plan branch), so the external merge is recorded.
		ancestors: map[string]bool{"tao/plan-a..main": true, "base123..tao/plan-a": true},
	})
	git := reg.client(mergeVerifyRoot)
	detail := mergeVerifyDetail()
	cleaner := successfulCleanup()
	events := &fakeEventAppender{}

	if err := (Service{Git: git, Cleaner: cleaner, Events: events}).Merge(context.Background(), detail, Options{}); err != nil {
		t.Fatal(err)
	}
	event := events.requireSingle(t, plan.EventTypePlanMerged)
	if event.MergedDefaultSHA != "merged789" || event.Branch != "tao/plan-a" {
		t.Fatalf("expected recorded external merge event, got %#v", event)
	}
	for _, call := range git.calls {
		if strings.HasPrefix(call, "checkout ") || strings.HasPrefix(call, "merge-ff-only ") || strings.HasPrefix(call, "rebase ") {
			t.Fatalf("already-merged plan should not integrate again, calls=%#v", git.calls)
		}
	}
	if len(cleaner.cleaned) != 1 || cleaner.cleaned[0].Branch != "tao/plan-a" {
		t.Fatalf("expected cleanup after recording external merge, got %#v", cleaner.cleaned)
	}
}

// TestMergeDoesNotRecordAncestorRefWithoutPlanWork guards the external-merge
// false positive: a plan branch that is an ancestor of the default branch but
// carries no commits beyond the plan base (e.g. a zero-diff run) must not be
// recorded as merged, which would otherwise complete the plan and delete its
// branch.
func TestMergeDoesNotRecordAncestorRefWithoutPlanWork(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "main123"},
		// tao/plan-a is an ancestor of main, but base123 does NOT lead it: the
		// branch never advanced past the base, so there is no plan work merged.
		ancestors: map[string]bool{"tao/plan-a..main": true},
	}
	events := &fakeEventAppender{}
	err := (Service{Git: git, Cleaner: successfulCleanup(), Events: events}).Merge(context.Background(), mergeVerifyDetail(), Options{RecordOnly: true})
	if err == nil || !strings.Contains(err.Error(), "not already merged") {
		t.Fatalf("expected record-only refusal for ancestor ref without plan work, got %v", err)
	}
	if events.count(plan.EventTypePlanMerged) != 0 {
		t.Fatalf("no merge event should be recorded, got %#v", events.events)
	}
}

func TestMergeRecordOnlyForceRecordsWithoutAncestryProof(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "squash456"},
		ancestors:     map[string]bool{},
	}
	detail := mergeVerifyDetail()
	detail.State.Plan.Review = nil
	events := &fakeEventAppender{}

	if err := (Service{Git: git, Cleaner: successfulCleanup(), Events: events}).Merge(context.Background(), detail, Options{RecordOnly: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	event := events.requireSingle(t, plan.EventTypePlanMerged)
	if event.MergedDefaultSHA != "squash456" || event.Branch != "tao/plan-a" {
		t.Fatalf("expected forced record-only merge event, got %#v", event)
	}
}

func TestMergeRecordOnlyRefusesWhenNotMergedWithoutForce(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "main123"},
		ancestors:     map[string]bool{},
	}
	err := (Service{Git: git, Cleaner: successfulCleanup(), Events: &fakeEventAppender{}}).Merge(context.Background(), mergeVerifyDetail(), Options{RecordOnly: true})
	if err == nil || !strings.Contains(err.Error(), "not already merged") {
		t.Fatalf("expected record-only not-merged refusal, got %v", err)
	}
}

func TestMergeAppendsPlanMergedEvent(t *testing.T) {
	reg := newFakeGitRegistry()
	reg.seed(mergeVerifyRoot, &fakeGitClient{
		defaultBranch:    "main",
		mergeBase:        "base123",
		revParse:         map[string]string{"main": "pre123", "tao/plan-a": "head123"},
		ancestors:        map[string]bool{"main..tao/plan-a": true},
		revParseSequence: map[string][]string{"main": {"pre123", "pre123", "merged456"}},
	})
	git := reg.client(mergeVerifyRoot)
	detail := mergeVerifyDetail()
	cleaner := successfulCleanup()
	events := &fakeEventAppender{}
	now := time.Date(2026, 6, 28, 21, 30, 0, 0, time.UTC)

	err := (Service{Git: git, Cleaner: cleaner, Events: events, Now: func() time.Time { return now }}).Merge(context.Background(), detail, Options{NoVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(events.planDirs, []string{detail.Dir, detail.Dir}) {
		t.Fatalf("plan dirs mismatch: %#v", events.planDirs)
	}
	// Intentional ordering assertion: verification must precede merge completion.
	if len(events.events) != 2 || events.events[0].Type != plan.EventTypeMergeVerification {
		t.Fatalf("expected verification and plan merged events, got %#v", events.events)
	}
	event := events.events[1]
	if event.Type != plan.EventTypePlanMerged || event.PlanID != "plan-a" || event.Branch != "tao/plan-a" || event.MergedDefaultSHA != "merged456" || !event.Timestamp.Equal(now) {
		t.Fatalf("unexpected plan_merged event: %#v", event)
	}
}

// TestMergeRecordsExternalMergeWhenNothingLeftToClean guards the spurious
// failure on first-time external-merge recording: a cleanup decline that
// reports the branch is not managed at all (already removed manually or by a
// prior clean, or a current-mode plan whose commits landed directly on
// default) means there is nothing left to do, so the command must succeed the
// way the recorded-retry path already does — not exit nonzero after recording
// a fully successful merge, which makes unattended queues mark the completed
// plan as failed.
func TestMergeRecordsExternalMergeWhenNothingLeftToClean(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "merged789"},
		ancestors:     map[string]bool{"tao/plan-a..main": true, "base123..tao/plan-a": true},
	}
	cleaner := successfulCleanup()
	cleaner.managed = nil // branch not in the managed-cleanup list: nothing to clean
	events := &fakeEventAppender{}

	if err := (Service{Git: git, Cleaner: cleaner, Events: events}).Merge(context.Background(), mergeVerifyDetail(), Options{}); err != nil {
		t.Fatalf("nothing-left-to-clean decline must be success on first-time recording: %v", err)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
}

// TestRecordedMergeCleanupRetrySaysForceForUnmergedBranch distinguishes a
// locally verified Tao squash from a --record-only --force squash/cherry-pick.
// Only a recorded default commit whose trailers match the plan and reviewed
// source head may authorize non-ancestral deletion; other unmerged branches
// still require --force.
func TestRecordedMergeCleanupRetrySaysForceForUnmergedBranch(t *testing.T) {
	const (
		mergedSHA  = "squash456"
		sourceHead = "reviewed-head"
	)
	newDetail := func() *plan.PlanDetail {
		detail := mergeVerifyDetail()
		detail.State.Plan.Review.Head = sourceHead
		detail.Events = []plan.Event{{Type: plan.EventTypePlanMerged, MergedDefaultSHA: mergedSHA}}
		return detail
	}
	verifiedSquashGit := func() *fakeGitClient {
		return &fakeGitClient{commitMessages: map[string]string{
			mergedSHA: "Plan A\n\nTao-Plan: plan-a\nTao-Source-Head: " + sourceHead,
		}}
	}
	declinedCleanup := func(status string, reason string) *fakeWorkspaceCleaner {
		cleaner := successfulCleanup()
		cleaner.managed = []workspace.ManagedCleanup{{Branch: "tao/plan-a", Status: status, Reason: reason}}
		return cleaner
	}

	t.Run("unmerged decline explains --force", func(t *testing.T) {
		cleaner := declinedCleanup(workspace.ManagedStatusUnmerged, "not merged into main")
		err := (Service{Git: verifiedSquashGit(), Cleaner: cleaner}).Merge(context.Background(), newDetail(), Options{NoSquash: true})
		if err == nil || !strings.Contains(err.Error(), "rerun with --force") {
			t.Fatalf("expected --force guidance for permanently-declined unmerged branch, got %v", err)
		}
	})

	t.Run("verified Tao squash retry cleans unmerged branch", func(t *testing.T) {
		cleaner := declinedCleanup(workspace.ManagedStatusUnmerged, "not merged into main")
		if err := (Service{Git: verifiedSquashGit(), Cleaner: cleaner}).Merge(context.Background(), newDetail(), Options{}); err != nil {
			t.Fatal(err)
		}
		if len(cleaner.cleaned) != 1 || cleaner.cleanOptions[0].Force || !cleaner.cleanOptions[0].AllowNonAncestralBranch {
			t.Fatalf("expected verified squash cleanup retry to allow only the non-ancestral branch, cleaned=%#v options=%#v", cleaner.cleaned, cleaner.cleanOptions)
		}
	})

	t.Run("plain retry without matching Tao squash evidence declines", func(t *testing.T) {
		for name, message := range map[string]string{
			"record-only merge without trailers": "Host-owned squash merge",
			"different source head":              "Plan A\n\nTao-Plan: plan-a\nTao-Source-Head: other-head",
			"different plan":                     "Plan A\n\nTao-Plan: other-plan\nTao-Source-Head: " + sourceHead,
		} {
			t.Run(name, func(t *testing.T) {
				cleaner := declinedCleanup(workspace.ManagedStatusUnmerged, "not merged into main")
				git := &fakeGitClient{commitMessages: map[string]string{mergedSHA: message}}
				err := (Service{Git: git, Cleaner: cleaner}).Merge(context.Background(), newDetail(), Options{})
				if err == nil || !strings.Contains(err.Error(), "rerun with --force") {
					t.Fatalf("expected unsafe cleanup retry to require --force, got %v", err)
				}
				if len(cleaner.cleaned) != 0 {
					t.Fatalf("cleanup must not remove an unmerged branch without verified squash evidence, got %#v", cleaner.cleaned)
				}
			})
		}
	})

	t.Run("dirty decline keeps plain retry guidance", func(t *testing.T) {
		cleaner := declinedCleanup(workspace.ManagedStatusDirty, "worktree has uncommitted changes")
		err := (Service{Git: verifiedSquashGit(), Cleaner: cleaner}).Merge(context.Background(), newDetail(), Options{})
		if err == nil || strings.Contains(err.Error(), "rerun with --force") {
			t.Fatalf("dirty decline is retryable after committing/stashing; got %v", err)
		}
	})

	t.Run("force retry cleans the unmerged branch", func(t *testing.T) {
		cleaner := declinedCleanup(workspace.ManagedStatusUnmerged, "not merged into main")
		if err := (Service{Git: &fakeGitClient{}, Cleaner: cleaner}).Merge(context.Background(), newDetail(), Options{Force: true}); err != nil {
			t.Fatal(err)
		}
		if len(cleaner.cleaned) != 1 || cleaner.cleaned[0].Branch != "tao/plan-a" {
			t.Fatalf("expected forced cleanup to remove the recorded branch, got %#v", cleaner.cleaned)
		}
	})
}

// TestDetectExternalMergeSkipsPlanOnDefaultBranch guards the false positive
// for current-mode plans whose workspace branch IS the default branch: every
// commit of default's own history is trivially an ancestor of default, and
// "carries plan work" is satisfied by unrelated commits merely advancing
// default past the plan base. Detection must not record such a plan as merged
// while its actual work may sit uncommitted or stashed; the explicit
// --record-only --force override remains the escape hatch.
func TestDetectExternalMergeSkipsPlanOnDefaultBranch(t *testing.T) {
	newGit := func() *fakeGitClient {
		return &fakeGitClient{
			defaultBranch: "main",
			revParse:      map[string]string{"main": "advanced789"},
			// Without the guard these would "prove" the merge: main contains
			// itself, and main advanced past the plan base.
			ancestors: map[string]bool{"main..main": true, "base123..main": true},
		}
	}
	newDetail := func() *plan.PlanDetail {
		detail := mergeVerifyDetail()
		detail.State.Workspace.Strategy = plan.WorkspaceStrategyCurrent
		detail.State.Workspace.Branch = "main"
		detail.State.Workspace.BaseSHA = "base123"
		return detail
	}

	git := newGit()
	_, ok, err := (Service{Git: git}).detectExternalMerge(context.Background(), git, newDetail(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("default's own advance must not be detected as an external merge of a default-branch plan")
	}

	// The deliberate no-proof override still records.
	git = newGit()
	events := &fakeEventAppender{}
	detail := newDetail()
	detail.Dir = t.TempDir()
	if err := (Service{Git: git, Cleaner: successfulCleanup(), Events: events}).Merge(context.Background(), detail, Options{RecordOnly: true, Force: true}); err != nil {
		t.Fatal(err)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
}

// TestMergeRecordsExternalMergeWhenWorktreeDirectoryGone guards the recording
// path against a recorded-but-deleted plan worktree: the clean-worktrees gate
// must not run git against a directory that no longer exists (there is nothing
// uncommitted in it), consistent with Cleanup treating a missing worktree as
// already settled. Without the fallback an actually-merged plan errors on
// every `tao merge` retry and never reaches completed.
func TestMergeRecordsExternalMergeWhenWorktreeDirectoryGone(t *testing.T) {
	git := &fakeGitClient{
		defaultBranch: "main",
		revParse:      map[string]string{"main": "merged789"},
		ancestors:     map[string]bool{"tao/plan-a..main": true, "base123..tao/plan-a": true},
	}
	detail := mergeVerifyDetail()
	detail.Dir = t.TempDir()
	detail.State.Workspace.Strategy = plan.WorkspaceStrategyWorktree
	detail.State.Workspace.Path = filepath.Join(t.TempDir(), "removed-worktree") // recorded, never created
	events := &fakeEventAppender{}
	service := Service{
		Git: git,
		NewGit: func(dir string) GitClient {
			t.Fatalf("worktree git client must not be constructed for missing directory %q", dir)
			return nil
		},
		Cleaner: successfulCleanup(),
		Events:  events,
	}

	if err := service.Merge(context.Background(), detail, Options{}); err != nil {
		t.Fatal(err)
	}
	events.requireSingle(t, plan.EventTypePlanMerged)
}

// TestAppendPlanMergedEventRefreshesStateFromDisk guards against clobbering
// concurrent plan-state updates: the rebase/verify window between plan load
// and merge recording can span minutes, and RecordMerged persists the whole
// state snapshot. A review recorded by another process during that window must
// survive the merge write instead of silently reverting to the stale
// in-memory copy.
func TestAppendPlanMergedEventRefreshesStateFromDisk(t *testing.T) {
	planDir := t.TempDir()
	detail := mergeVerifyDetail()
	detail.Dir = planDir

	// Another process persisted a fresh review after this merge command loaded
	// its (review-less) snapshot.
	onDisk := detail.State
	onDisk.Plan.Review = &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Summary: "concurrent review"}
	data, err := json.Marshal(onDisk)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "state.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	detail.State.Plan.Review = nil

	svc := Service{Now: func() time.Time { return time.Date(2026, 7, 7, 12, 0, 0, 0, time.UTC) }}
	if err := svc.AppendPlanMergedEvent(detail, "tao/plan-a", "merged456"); err != nil {
		t.Fatal(err)
	}

	state, err := plan.ReadState(planDir)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != plan.StatusCompleted {
		t.Fatalf("recorded merge must persist status %q, got %q", plan.StatusCompleted, state.Status)
	}
	if state.Plan.Review == nil || state.Plan.Review.Summary != "concurrent review" {
		t.Fatalf("concurrent review must survive the merge write, got %#v", state.Plan.Review)
	}
	if detail.State.Plan.Review == nil {
		t.Fatal("in-memory detail must adopt the refreshed state")
	}
}

func indexOf(values []string, want string) int {
	for i, value := range values {
		if value == want {
			return i
		}
	}
	return -1
}
