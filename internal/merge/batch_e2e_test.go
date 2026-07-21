package merge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/workspace"
)

type batchE2EStore struct {
	dir              string
	latest           BatchState
	failLandedOnce   bool
	transitionCounts map[BatchStatus]int
}

func (s *batchE2EStore) Transition(state BatchState, _ string) (BatchState, error) {
	if s.failLandedOnce && state.Status == BatchStatusLanded {
		s.failLandedOnce = false
		return BatchState{}, errors.New("injected stop after default fast-forward")
	}
	state.LogSequence++
	s.latest = state
	if s.transitionCounts == nil {
		s.transitionCounts = make(map[BatchStatus]int)
	}
	s.transitionCounts[state.Status]++
	return state, nil
}

func (s *batchE2EStore) WriteAggregateReview(_ string, attempt int, output string) (string, error) {
	name := fmt.Sprintf("aggregate-review-%03d.md", attempt)
	return name, os.WriteFile(filepath.Join(s.dir, name), []byte(output), 0o600)
}

type batchE2EAgent struct {
	t             *testing.T
	conflictCalls int
	reviewCalls   int
	reworkCalls   int
}

func (a *batchE2EAgent) Resolve(_ context.Context, root, prompt string) (string, error) {
	a.t.Helper()
	switch {
	case strings.Contains(prompt, "aggregate-review"):
		a.reworkCalls++
		if err := os.WriteFile(filepath.Join(root, "aggregate-fixed.txt"), []byte("fixed\n"), 0o600); err != nil {
			a.t.Fatal(err)
		}
		return "fixed aggregate interaction", nil
	case strings.Contains(prompt, "tao-review-json") || strings.Contains(prompt, "Aggregate") || strings.Contains(prompt, "aggregate review"):
		a.reviewCalls++
		if a.reviewCalls == 1 {
			return reviewJSON("changes_requested", "combined cleanup event needs coverage", "cleanup event interaction"), nil
		}
		return reviewJSON("approve", "combined result approved", ""), nil
	default:
		a.conflictCalls++
		if err := os.WriteFile(filepath.Join(root, "shared.txt"), []byte("resolved autorework and overlap\n"), 0o600); err != nil {
			a.t.Fatal(err)
		}
		if a.conflictCalls > 1 {
			if err := os.Remove(filepath.Join(root, "verify.fail")); err != nil && !errors.Is(err, os.ErrNotExist) {
				a.t.Fatal(err)
			}
		}
		return fmt.Sprintf("conflict resolution attempt %d", a.conflictCalls), nil
	}
}

type batchE2ERepository struct {
	details map[string]*plan.PlanDetail
}

func (r *batchE2ERepository) ResolvePlan(_ context.Context, input string) (*plan.PlanDetail, error) {
	if detail := r.details[input]; detail != nil {
		return detail, nil
	}
	return nil, fmt.Errorf("plan %q not found", input)
}

type batchE2ECleaner struct {
	t         *testing.T
	repoRoot  string
	branches  map[string]string
	worktrees map[string]string
	cleaned   []string
}

func (c *batchE2ECleaner) PlanClean(_ context.Context, planID string) (workspace.CleanPlan, error) {
	return workspace.CleanPlan{PlanID: planID, Branch: c.branches[planID], Status: workspace.ManagedStatusClean, CanRemove: true}, nil
}

func (c *batchE2ECleaner) PlanManagedCleanup(context.Context) ([]workspace.ManagedCleanup, error) {
	items := make([]workspace.ManagedCleanup, 0, len(c.branches))
	for id, branch := range c.branches {
		items = append(items, workspace.ManagedCleanup{Branch: branch, WorktreePath: c.worktrees[id], Status: workspace.ManagedStatusUnmerged, CanRemove: false, Reason: "squash source"})
	}
	return items, nil
}

func (c *batchE2ECleaner) CleanManaged(_ context.Context, item workspace.ManagedCleanup, options workspace.CleanOptions) error {
	if !options.Force && !options.AllowNonAncestralBranch {
		return errors.New("squash cleanup was not guarded")
	}
	runBatchE2EGit(c.t, c.repoRoot, "worktree", "remove", item.WorktreePath)
	runBatchE2EGit(c.t, c.repoRoot, "branch", "-D", item.Branch)
	c.cleaned = append(c.cleaned, item.Branch)
	return nil
}

type batchE2ESettlementWorkspace struct {
	t               *testing.T
	repoRoot        string
	integrationRoot string
	removed         int
	cleared         int
}

func (w *batchE2ESettlementWorkspace) RemoveIntegration(_ context.Context, batchID string) error {
	runBatchE2EGit(w.t, w.repoRoot, "worktree", "remove", w.integrationRoot)
	runBatchE2EGit(w.t, w.repoRoot, "branch", "-D", "tao/integration/"+batchID)
	w.removed++
	return nil
}
func (w *batchE2ESettlementWorkspace) ClearActive(string) error { w.cleared++; return nil }

// TestMergeBatchEndToEnd exercises the motivating transaction shape in one
// real repository: low-overlap ordering, text and verification deferral,
// bounded agent resolution, aggregate rework, interrupted landing recovery,
// evidence recording, and guarded cleanup.
func TestMergeBatchEndToEnd(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runBatchE2EGit(t, root, "init", "-b", "main")
	runBatchE2EGit(t, root, "config", "user.name", "Tao E2E")
	runBatchE2EGit(t, root, "config", "user.email", "tao-e2e@example.invalid")
	writeBatchE2EFile(t, root, "shared.txt", "base\n")
	runBatchE2EGit(t, root, "add", ".")
	runBatchE2EGit(t, root, "commit", "-m", "initial")
	base := batchE2EGitOutput(t, root, "rev-parse", "main")

	ids := []string{"plan-a", "plan-b", "plan-c", "plan-d", "plan-e"}
	branches, worktrees, sourceHeads := map[string]string{}, map[string]string{}, map[string]string{}
	for _, id := range ids {
		branch := "tao/" + id
		worktreeRoot := filepath.Join(filepath.Dir(root), id)
		runBatchE2EGit(t, root, "worktree", "add", "-b", branch, worktreeRoot, base)
		branches[id], worktrees[id] = branch, worktreeRoot
		switch id {
		case "plan-a":
			writeBatchE2EFile(t, worktreeRoot, "shared.txt", "autorework result\n")
		case "plan-e":
			writeBatchE2EFile(t, worktreeRoot, "shared.txt", "cleanup event result\n")
			writeBatchE2EFile(t, worktreeRoot, "verify.fail", "verification-only interaction\n")
		default:
			writeBatchE2EFile(t, worktreeRoot, id+".txt", id+"\n")
		}
		runBatchE2EGit(t, worktreeRoot, "add", ".")
		runBatchE2EGit(t, worktreeRoot, "commit", "-m", "feat: "+id)
		sourceHeads[id] = batchE2EGitOutput(t, root, "rev-parse", branch)
	}

	planRoot := filepath.Join(filepath.Dir(root), "plans")
	repository := &batchE2ERepository{details: make(map[string]*plan.PlanDetail)}
	candidates := make([]BatchCandidate, 0, len(ids))
	for _, id := range ids {
		dir := filepath.Join(planRoot, id)
		detail := batchE2EDetail(id, dir, root, branches[id], worktrees[id], base, sourceHeads[id])
		repository.details[id], repository.details[dir] = detail, detail
		candidates = append(candidates, BatchCandidate{PlanID: id, PlanTitle: "Integrate " + id, PlanDir: dir, RepoRoot: root, Branch: branches[id], ReviewBase: base, ReviewHead: sourceHeads[id], ReviewSummary: "approved source", SourceTip: sourceHeads[id], DefaultBranch: "main", DefaultStartSHA: base})
	}

	service := NewService(root, nil)
	planning, err := service.PlanBatchCandidatesWithGit(ctx, candidates)
	if err != nil {
		t.Fatal(err)
	}
	order := make([]string, 0, len(planning.Ordered))
	for _, candidate := range planning.Ordered {
		order = append(order, candidate.PlanID)
	}
	if got := strings.Join(order, ","); got != "plan-a,plan-b,plan-c,plan-d,plan-e" {
		t.Fatalf("low-overlap order = %s", got)
	}

	batchID := "e2e"
	integrationRoot := filepath.Join(filepath.Dir(root), "integration")
	runBatchE2EGit(t, root, "worktree", "add", "-b", "tao/integration/"+batchID, integrationRoot, base)
	store := &batchE2EStore{dir: t.TempDir()}
	state := BatchState{Schema: BatchStateSchema, ID: batchID, Status: BatchStatusPlanned, RepoRoot: root, DefaultBranch: "main", DefaultStartSHA: base, Candidates: candidates, ChosenOrder: order, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	verifyLog := filepath.Join(t.TempDir(), "verify.log")
	verifyCommand := fmt.Sprintf("printf 'verify\\n' >> %q; test ! -e verify.fail", verifyLog)
	integrated, err := (BatchIntegrator{Store: store, Service: service}).Integrate(ctx, state, integrationRoot, BatchIntegrateOptions{VerifyCommand: verifyCommand})
	if err != nil {
		t.Fatal(err)
	}
	if len(integrated.Deferred) != 1 || integrated.Deferred[0].PlanID != "plan-e" || !strings.Contains(integrated.Deferred[0].Reason, "squash conflict") {
		t.Fatalf("expected inferred high-overlap deferral, got %#v", integrated.Deferred)
	}
	if got := batchE2EGitOutput(t, root, "rev-parse", "main"); got != base {
		t.Fatalf("default moved during staging: %s", got)
	}

	agent := &batchE2EAgent{t: t}
	resolved, err := (BatchAgentResolver{Store: store, Service: service, Agent: agent}).Resolve(ctx, integrated.State, integrationRoot, BatchResolveOptions{VerifyCommand: verifyCommand, MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	if agent.conflictCalls != 2 || len(resolved.Resolved) != 1 || resolved.State.Attempts.ConflictResolution != 2 {
		t.Fatalf("text/verification retries not exercised: agent=%+v state=%+v", agent, resolved.State.Attempts)
	}
	for _, item := range resolved.State.Integrations {
		message := batchE2EGitOutput(t, integrationRoot, "show", "-s", "--format=%B", item.IntegrationSHA)
		if strings.Count(message, "Tao-Plan: "+item.PlanID) != 1 || !strings.Contains(message, "Tao-Source-Head: "+sourceHeads[item.PlanID]) {
			t.Fatalf("invalid squash evidence for %s: %s", item.PlanID, message)
		}
	}

	reviewed, err := (BatchAggregateReviewer{Store: store, Service: service, Agent: agent}).Review(ctx, resolved.State, integrationRoot, BatchReviewOptions{VerifyCommand: verifyCommand, MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	state = reviewed.State
	if agent.reviewCalls != 2 || agent.reworkCalls != 1 || state.Review.Attempts != 2 || state.Attempts.AggregateRework != 1 || len(state.Review.ResolutionSHAs) != 1 {
		t.Fatalf("aggregate changes-requested/approve cycle missing: agent=%+v review=%+v", agent, state.Review)
	}
	resolutionMessage := batchE2EGitOutput(t, integrationRoot, "show", "-s", "--format=%B", state.Review.ResolutionSHAs[0])
	if !strings.Contains(resolutionMessage, "Tao-Merge-Batch: "+batchID) {
		t.Fatalf("aggregate resolution is not Tao-owned: %s", resolutionMessage)
	}
	if calls := strings.Count(readBatchE2EFile(t, verifyLog), "verify\n"); calls != 8 {
		t.Fatalf("full verification calls = %d, want 8", calls)
	}

	events := &fakeEventAppender{}
	cleaner := &batchE2ECleaner{t: t, repoRoot: root, branches: branches, worktrees: worktrees}
	service.Events, service.Cleaner = events, cleaner
	store.failLandedOnce = true
	lander := BatchLander{Store: store, Service: service, Repository: repository, Health: func(context.Context, plan.Repo) error { return nil }}
	_, err = lander.Land(ctx, state, integrationRoot)
	if err == nil || !strings.Contains(err.Error(), "injected stop") {
		t.Fatalf("expected interrupted landing, got %v", err)
	}
	if got := batchE2EGitOutput(t, root, "rev-parse", "main"); got != state.IntegrationHead {
		t.Fatalf("interrupted fast-forward lost default: got %s want %s", got, state.IntegrationHead)
	}
	for _, id := range ids {
		if got := batchE2EGitOutput(t, root, "rev-parse", branches[id]); got != sourceHeads[id] {
			t.Fatalf("interrupted landing lost %s: got %s want %s", id, got, sourceHeads[id])
		}
	}
	landed, err := lander.Land(ctx, store.latest, integrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := events.count(plan.EventTypePlanMerged); got != len(ids) {
		t.Fatalf("completed plan events = %d, want %d", got, len(ids))
	}

	settlementWorkspace := &batchE2ESettlementWorkspace{t: t, repoRoot: root, integrationRoot: integrationRoot}
	settled, err := (BatchSettler{Store: store, Service: service, Repository: repository, Workspace: settlementWorkspace}).Settle(ctx, landed.State)
	if err != nil {
		t.Fatal(err)
	}
	if settled.State.Status != BatchStatusCompleted || len(cleaner.cleaned) != len(ids) || settlementWorkspace.removed != 1 || settlementWorkspace.cleared != 1 {
		t.Fatalf("settlement incomplete: state=%s cleaned=%v integration=%d active=%d", settled.State.Status, cleaner.cleaned, settlementWorkspace.removed, settlementWorkspace.cleared)
	}
	for _, id := range ids {
		if out := batchE2EGitOutputAllowFailure(t, root, "branch", "--list", branches[id]); strings.TrimSpace(out) != "" {
			t.Fatalf("source branch %s survived safe cleanup: %s", branches[id], out)
		}
	}
	if got := batchE2EGitOutput(t, root, "rev-list", "--count", base+"..main"); got != "6" {
		t.Fatalf("atomic history has %s commits, want five squashes plus one aggregate resolution", got)
	}
}

func TestMergeBatchAutoEjectResolvesAndLandsAttributedReducedSet(t *testing.T) {
	ctx := context.Background()
	fixture, state, integrationRoot := batchEjectTestFixture(t)
	base := state.DefaultStartSHA
	planAHead := state.Candidates[0].SourceTip
	planB := &state.Candidates[1]
	planB.PlanDir = "plan-b-dir"
	detail := mergeReadyDetail(base)
	detail.Dir = planB.PlanDir
	detail.State.Repo = plan.Repo{Root: fixture.repoRoot, Branch: fixture.defaultBranch}
	detail.State.Plan.ID = planB.PlanID
	detail.State.Plan.Review.Head = planB.SourceTip
	detail.State.Workspace.Branch = planB.Branch
	detail.State.Workspace.BaseBranch = fixture.defaultBranch

	reviewCalls := 0
	resolutionCalls := 0
	agent := batchReviewAgentFunc(func(_ context.Context, root, prompt string) (string, error) {
		if strings.Contains(prompt, "Candidate: aggregate-review") {
			return "attempted fix", os.WriteFile(filepath.Join(root, "review-fix.txt"), []byte("fix\n"), 0o600)
		}
		if strings.Contains(prompt, "Candidate: plan-b") {
			resolutionCalls++
			return "fixed reduced-set verification", os.WriteFile(filepath.Join(root, "reduced-fixed.txt"), []byte("fixed\n"), 0o600)
		}
		reviewCalls++
		if reviewCalls == 3 {
			return reviewJSON("approve", "reduced batch is green", ""), nil
		}
		output := reviewJSON("changes_requested", "plan a keeps failing", fmt.Sprintf("round %d", reviewCalls))
		output = strings.Replace(output, `"file":"combined.txt"`, `"file":"plan-a.txt"`, 1)
		if reviewCalls == 2 {
			output = strings.Replace(output, `"severity":"major"`, `"severity":"critical"`, 1)
		}
		return output, nil
	})
	store := &batchReviewTestStore{dir: t.TempDir()}
	service := NewService(fixture.repoRoot, nil)
	verifyCommand := "test -f plan-a.txt || test -f reduced-fixed.txt"
	reviewer := BatchAggregateReviewer{Store: store, Service: service, Agent: agent}
	reviewed, err := reviewer.Review(ctx, state, integrationRoot, BatchReviewOptions{VerifyCommand: verifyCommand, MaxAttempts: 3, AutoEject: true})
	if err != nil {
		t.Fatal(err)
	}
	if reviewed.State.Status != BatchStatusResolving || !reviewed.ReenterPhases {
		t.Fatalf("auto-eject did not request resolver reentry: %+v", reviewed)
	}
	resolved, err := (BatchAgentResolver{Store: store, Service: service, Agent: agent}).Resolve(ctx, reviewed.State, integrationRoot, BatchResolveOptions{VerifyCommand: verifyCommand})
	if err != nil {
		t.Fatal(err)
	}
	reviewed, err = reviewer.Review(ctx, resolved.State, integrationRoot, BatchReviewOptions{VerifyCommand: verifyCommand, MaxAttempts: 3, AutoEject: true})
	if err != nil {
		t.Fatal(err)
	}
	if resolutionCalls != 1 {
		t.Fatalf("reduced-set resolution calls = %d, want 1", resolutionCalls)
	}

	events := &fakeEventAppender{}
	service.Events = events
	landed, err := (BatchLander{
		Store: store, Service: service,
		Repository: &batchE2ERepository{details: map[string]*plan.PlanDetail{detail.Dir: detail}},
		Health:     func(context.Context, plan.Repo) error { return nil },
	}).Land(ctx, reviewed.State, integrationRoot)
	if err != nil {
		t.Fatal(err)
	}
	if landed.State.Status != BatchStatusLanded || len(landed.State.Landing.Plans) != 1 || landed.State.Landing.Plans[0].PlanID != planB.PlanID {
		t.Fatalf("reduced batch did not land exactly plan-b: %+v", landed.State)
	}
	if len(events.events) != 1 || landed.State.Candidates[0].Deferred == nil || !strings.Contains(landed.State.Candidates[0].Deferred.Reason, "not converging") {
		t.Fatalf("ejected plan attribution was not retained: events=%d candidate=%+v", len(events.events), landed.State.Candidates[0])
	}
	if got := batchE2EGitOutput(t, fixture.repoRoot, "rev-parse", fixture.defaultBranch); got != landed.State.IntegrationHead {
		t.Fatalf("default = %s, want reduced integration %s", got, landed.State.IntegrationHead)
	}
	if got := batchE2EGitOutput(t, fixture.repoRoot, "rev-parse", fixture.planBranch); got != planAHead {
		t.Fatalf("ejected source moved: got %s want %s", got, planAHead)
	}
	if _, err := os.Stat(filepath.Join(fixture.repoRoot, "plan-a.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ejected file landed: %v", err)
	}
	if content := readBatchE2EFile(t, filepath.Join(fixture.repoRoot, "plan-b.txt")); content != "b\n" {
		t.Fatalf("remaining candidate content = %q", content)
	}
	if content := readBatchE2EFile(t, filepath.Join(fixture.repoRoot, "reduced-fixed.txt")); content != "fixed\n" {
		t.Fatalf("reduced-set resolution content = %q", content)
	}
}

func batchE2EDetail(id, dir, root, branch, worktreeRoot, base, head string) *plan.PlanDetail {
	return &plan.PlanDetail{Dir: dir, State: plan.State{Status: plan.StatusCompleted, Repo: plan.Repo{Root: root, Branch: "main"}, Plan: plan.PlanState{ID: id, Title: id, CompletedSlices: []string{"001"}, Review: &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Base: base, Head: head}}, Workspace: &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree, Root: root, Path: worktreeRoot, Branch: branch, BaseBranch: "main"}}, Slices: plan.SlicesFile{PlanID: id, Slices: []plan.Slice{{ID: "001", Status: plan.StatusCompleted}}}}
}

func writeBatchE2EFile(t *testing.T, root, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
func readBatchE2EFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path) //nolint:gosec // test-owned temporary path.
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
func runBatchE2EGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // fixed binary and test-controlled arguments.
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
func batchE2EGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // fixed binary and test-controlled arguments.
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
func batchE2EGitOutputAllowFailure(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) //nolint:gosec // fixed binary and test-controlled arguments.
	cmd.Dir = dir
	output, _ := cmd.CombinedOutput()
	return string(output)
}
