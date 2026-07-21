package run

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type sliceCompletionStore struct {
	failState    bool
	failSlicesAt int
	failEvent    bool
	slicesWrites int
	state        plan.State
	slices       plan.SlicesFile
	events       []plan.Event
}

func (s *sliceCompletionStore) WriteState(_ string, state plan.State) error {
	if s.failState {
		s.failState = false
		return errors.New("interrupted metadata write")
	}
	s.state = state
	return nil
}
func (s *sliceCompletionStore) WriteSlices(_ string, slices plan.SlicesFile) error {
	s.slicesWrites++
	if s.slicesWrites == s.failSlicesAt {
		return errors.New("interrupted slices write")
	}
	s.slices = slices
	return nil
}
func (s *sliceCompletionStore) AppendEvent(_ string, event plan.Event) error {
	if s.failEvent {
		s.failEvent = false
		return errors.New("interrupted event append")
	}
	s.events = append(s.events, event)
	return nil
}

func TestSliceCompletionCommitsInterruptedTrackedStagedAndUntrackedWorkAtOriginalParent(t *testing.T) {
	root := initSliceCompletionRepo(t)
	originalHead := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	store := &sliceCompletionStore{}
	_, record := sliceCompletionRecord(t, root, CommitPolicySlice, store)
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("preserved tracked edit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "staged.go"), []byte("package staged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCommitTestGitCommand(t, root, "add", "staged.go")
	if err := os.WriteFile(filepath.Join(root, "untracked.go"), []byte("package untracked\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusBefore := runCommitTestGitOutput(t, root, "status", "--short")
	for _, want := range []string{" M README.md", "A  staged.go", "?? untracked.go"} {
		if !strings.Contains(statusBefore, want) {
			t.Fatalf("interrupted status missing %q:\n%s", want, statusBefore)
		}
	}

	request := SliceCompletionRequest{
		Record: record, SliceID: "001-a", Notes: "resumed and completed",
		VerificationResults: []plan.VerificationRun{{Command: "go test ./internal/run", CWD: root, Result: "passed", Details: "ok"}},
		Now:                 time.Now().UTC(),
	}
	if err := (SliceCompletionService{}).Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	if parent := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", head+"^")); parent != originalHead {
		t.Fatalf("completion parent = %s, want original execution_start HEAD %s", parent, originalHead)
	}
	if status := runCommitTestGitOutput(t, root, "status", "--short"); status != "" {
		t.Fatalf("final worktree is dirty:\n%s", status)
	}
	message := runCommitTestGitOutput(t, root, "log", "-1", "--format=%B")
	for _, trailer := range []string{"Tao-Plan: plan-a", "Tao-Slice: 001-a"} {
		if !strings.Contains(message, trailer) {
			t.Fatalf("completion commit missing %q:\n%s", trailer, message)
		}
	}
	if len(store.events) != 1 || store.events[0].Type != plan.EventTypeSliceCompleted {
		t.Fatalf("completion events = %+v, want exactly one slice_completed", store.events)
	}
	committed := strings.Fields(runCommitTestGitOutput(t, root, "show", "--name-only", "--format=", "HEAD"))
	if strings.Join(committed, ",") != "README.md,staged.go,untracked.go" {
		t.Fatalf("committed interrupted paths = %v", committed)
	}
}

func TestInterruptedSliceCompletionReloadBeforeParentExitIsRecoverable(t *testing.T) {
	root := initSliceCompletionRepo(t)
	detail, _ := sliceCompletionRecord(t, root, CommitPolicySlice, &sliceCompletionStore{})
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(detail.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "resumed.go"), []byte("package resumed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "resumed", Now: time.Now().UTC()}
	service := SliceCompletionService{}
	if err := service.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}

	repo := plan.NewFileRepository(filepath.Dir(detail.Dir))
	reloaded, err := repo.ResolvePlan(context.Background(), detail.Dir)
	if err != nil {
		t.Fatalf("parent reload after child completion: %v", err)
	}
	if !plan.SliceCompleted(reloaded, request.SliceID) || reloaded.Slices.Slices[0].Completion == nil {
		t.Fatalf("reloaded slice did not retain completion: %+v", reloaded.Slices.Slices[0])
	}
	if got := countPlanEvents(reloaded.Events, plan.EventTypeSliceCompleted); got != 1 {
		t.Fatalf("slice_completed events after reload = %d, want 1", got)
	}

	reloadedRecord, err := repo.PlanRecord(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	request.Record = reloadedRecord
	if err := service.Complete(context.Background(), request); err != nil {
		t.Fatalf("parent completion re-entry after reload: %v", err)
	}
	final, err := repo.ResolvePlan(context.Background(), detail.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := countPlanEvents(final.Events, plan.EventTypeSliceCompleted); got != 1 {
		t.Fatalf("completion re-entry duplicated lifecycle event: %d", got)
	}
}

func countPlanEvents(events []plan.Event, eventType string) int {
	count := 0
	for _, event := range events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

func TestSliceCompletionCommitsAndRecoversInterruptedMetadata(t *testing.T) {
	root := initSliceCompletionRepo(t)
	detail, record := sliceCompletionRecord(t, root, CommitPolicySlice, &sliceCompletionStore{failState: true})
	if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{
		Record: record, SliceID: "001-a", Notes: "done",
		VerificationResults: []plan.VerificationRun{{Command: "go test ./internal/run", CWD: root, Result: "passed", Details: "ok"}},
		Now:                 time.Now().UTC(),
	}
	service := SliceCompletionService{}
	if err := service.Complete(context.Background(), request); err == nil || !strings.Contains(err.Error(), "interrupted metadata write") {
		t.Fatalf("expected interrupted metadata error, got %v", err)
	}
	firstHead := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	if err := service.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); head != firstHead {
		t.Fatalf("retry created a second commit: first=%s second=%s", firstHead, head)
	}
	message := runCommitTestGitOutput(t, root, "log", "-1", "--format=%B")
	for _, want := range []string{"chore(tao): complete 001-a — A", "Verification:", "Tao-Plan: plan-a", "Tao-Slice: 001-a"} {
		if !strings.Contains(message, want) {
			t.Fatalf("commit message missing %q:\n%s", want, message)
		}
	}
	completed := detail.Slices.Slices[0].Completion
	if completed == nil || completed.Outcome != plan.SliceCompletionCommitted || completed.CommitSHA != firstHead {
		t.Fatalf("unexpected completion outcome: %#v", completed)
	}
}

func TestSliceCompletionRecoversCommitAfterStateOnlyCompletionWrite(t *testing.T) {
	root := initSliceCompletionRepo(t)
	store := &sliceCompletionStore{failSlicesAt: 2}
	_, record := sliceCompletionRecord(t, root, CommitPolicySlice, store)
	if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "done", Now: time.Now().UTC()}
	service := SliceCompletionService{}
	if err := service.Complete(context.Background(), request); err == nil || !strings.Contains(err.Error(), "interrupted slices write") {
		t.Fatalf("expected state-only completion write, got %v", err)
	}
	committedHead := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))

	loaded := &plan.PlanDetail{Dir: record.Dir(), State: store.state, Slices: store.slices}
	lifecycle := plan.AnalyzeLifecycle(loaded)
	if lifecycle.Complete || lifecycle.Runnable || lifecycle.RunnableError == nil || !strings.Contains(lifecycle.RunnableError.Error(), "completion outcome is missing") {
		t.Fatalf("partially persisted completion advanced lifecycle: %+v", lifecycle)
	}
	reloadedRecord, err := plan.NewPlanRecordWithStore(store, loaded.Dir, loaded)
	if err != nil {
		t.Fatal(err)
	}
	request.Record = reloadedRecord
	if err := service.Complete(context.Background(), request); err != nil {
		t.Fatalf("recover recorded commit: %v", err)
	}
	if head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); head != committedHead {
		t.Fatalf("recovery created another commit: %s -> %s", committedHead, head)
	}
	if completion := loaded.Slices.Slices[0].Completion; completion == nil || completion.Outcome != plan.SliceCompletionCommitted || completion.CommitSHA != committedHead {
		t.Fatalf("recovered completion = %#v", completion)
	}
	if lifecycle := plan.AnalyzeLifecycle(loaded); !lifecycle.Complete {
		t.Fatalf("persisted recovered outcome did not settle plan: %+v", lifecycle)
	}
}

func TestSliceCompletionRecoversMissingCompletionEvent(t *testing.T) {
	root := initSliceCompletionRepo(t)
	store := &sliceCompletionStore{failEvent: true}
	_, record := sliceCompletionRecord(t, root, CommitPolicySlice, store)
	if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "done", Now: time.Now().UTC()}
	service := SliceCompletionService{}
	if err := service.Complete(context.Background(), request); err == nil || !strings.Contains(err.Error(), "interrupted event append") {
		t.Fatalf("expected interrupted event append, got %v", err)
	}
	committedHead := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	if store.slices.Slices[0].Completion == nil {
		t.Fatal("completion metadata was not persisted before event failure")
	}

	loaded := &plan.PlanDetail{Dir: record.Dir(), State: store.state, Slices: store.slices, Events: append([]plan.Event(nil), store.events...)}
	reloadedRecord, err := plan.NewPlanRecordWithStore(store, loaded.Dir, loaded)
	if err != nil {
		t.Fatal(err)
	}
	request.Record = reloadedRecord
	if err := service.Complete(context.Background(), request); err != nil {
		t.Fatalf("recover completion event: %v", err)
	}
	if head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); head != committedHead {
		t.Fatalf("event recovery created another commit: %s -> %s", committedHead, head)
	}
	if len(store.events) != 1 || store.events[0].Type != plan.EventTypeSliceCompleted || store.events[0].SliceID != request.SliceID {
		t.Fatalf("recovered events = %#v", store.events)
	}
	if err := service.Complete(context.Background(), request); err != nil {
		t.Fatalf("idempotent completion retry: %v", err)
	}
	if len(store.events) != 1 {
		t.Fatalf("completion retry duplicated event: %#v", store.events)
	}
}

func TestSliceCompletionRetryConsumesSettledJournalState(t *testing.T) {
	root := initSliceCompletionRepo(t)
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	if err := os.Mkdir(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	base, _ := sliceCompletionRecord(t, root, CommitPolicyNone, &sliceCompletionStore{})
	base.Dir = planDir
	base.State.Schema = "tao.plan.state.v1"
	base.Slices.Schema = "tao.plan.slices.v1"
	base.Slices.PlanID = "plan-a"
	persistRunArtifacts(t, planDir, base)

	now := time.Date(2026, 7, 20, 18, 10, 0, 0, time.UTC)
	notes := "settled before parent retry"
	results := []plan.VerificationRun{{Command: "go test ./internal/run", CWD: root, Result: "passed", Details: "ok"}}
	hash, err := sliceCompletionHash("plan-a", "001-a", CommitPolicyNone.String(), notes, results)
	if err != nil {
		t.Fatal(err)
	}
	settled := cloneRunRestartDetail(t, base)
	settled.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Hash: hash, Policy: CommitPolicyNone.String(), StartingBranch: "tao/test", StartingHead: "base", CreatedAt: now}
	outcome := plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionManualUncommitted}
	event, appendEvent, err := plan.MarkSliceCompletedWithOutcome(settled, "001-a", notes, results, &outcome, now)
	if err != nil || !appendEvent {
		t.Fatalf("prepare settled completion: append=%t err=%v", appendEvent, err)
	}
	writeRunRestartJournal(t, planDir, "restart-completion", &settled.State, &settled.Slices, []plan.Event{event})

	reloaded, err := plan.NewFileRepository(plansDir).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := plan.NewFileRepository(plansDir).PlanRecord(reloaded)
	if err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: notes, VerificationResults: results, Now: now.Add(time.Minute)}
	if err := (SliceCompletionService{}).Complete(context.Background(), request); err != nil {
		t.Fatalf("completion retry after journal settlement: %v", err)
	}

	final, err := plan.NewFileRepository(plansDir).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := countPlanEvents(final.Events, plan.EventTypeSliceCompleted); got != 1 {
		t.Fatalf("slice_completed events = %d, want one", got)
	}
	if completion := final.Slices.Slices[0].Completion; completion == nil || completion.Outcome != plan.SliceCompletionManualUncommitted {
		t.Fatalf("settled completion = %#v", completion)
	}
}

func TestReviewStateReadSettlesPendingJournal(t *testing.T) {
	plansDir := t.TempDir()
	planDir := filepath.Join(plansDir, "plan-a")
	if err := os.Mkdir(planDir, 0o700); err != nil {
		t.Fatal(err)
	}
	base := completedReviewPlanDetail(planDir)
	base.State.Schema = "tao.plan.state.v1"
	base.State.Plan.ID = "plan-a"
	base.Slices.Schema = "tao.plan.slices.v1"
	base.Slices.PlanID = "plan-a"
	persistRunArtifacts(t, planDir, base)

	reviewedAt := time.Date(2026, 7, 20, 18, 20, 0, 0, time.UTC)
	settled := cloneRunRestartDetail(t, base)
	settled.State.Status = plan.StatusReviewed
	settled.State.Plan.Review = &plan.PlanReview{Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictApprove, Summary: "approved after restart", ReviewedAt: reviewedAt}
	event := plan.Event{Type: plan.EventTypePlanReviewed, Timestamp: reviewedAt, PlanID: "plan-a", Agent: "pi", Review: settled.State.Plan.Review, Message: "Plan reviewed"}
	writeRunRestartJournal(t, planDir, "restart-review", &settled.State, nil, []plan.Event{event})

	state, err := reviewState(planDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != plan.StatusReviewed || state.Plan.Review == nil || state.Plan.Review.Verdict != plan.ReviewVerdictApprove {
		t.Fatalf("review consumer read stale state: %#v", state)
	}
	reloaded, err := plan.NewFileRepository(plansDir).ResolvePlan(context.Background(), planDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := countPlanEvents(reloaded.Events, plan.EventTypePlanReviewed); got != 1 {
		t.Fatalf("plan_reviewed events = %d, want one", got)
	}
}

func TestSliceCompletionCommitsUndeclaredSafePathsWithWarning(t *testing.T) {
	root := initSliceCompletionRepo(t)
	detail, record := sliceCompletionRecord(t, root, CommitPolicySlice, &sliceCompletionStore{})
	detail.Slices.Slices[0].ExpectedFiles = []string{"declared.go"}
	for name, content := range map[string]string{
		"declared.go": "package work\n",
		"extra.go":    "package work\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	err := (SliceCompletionService{Output: &out}).Complete(context.Background(), SliceCompletionRequest{
		Record: record, SliceID: "001-a", Notes: "done", Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	committed := strings.Fields(runCommitTestGitOutput(t, root, "show", "--name-only", "--format=", "HEAD"))
	if strings.Join(committed, ",") != "declared.go,extra.go" {
		t.Fatalf("committed paths = %v, want declared and undeclared safe paths", committed)
	}
	warning := out.String()
	if !strings.Contains(warning, "committed path(s) outside completed slice expected_files") || !strings.Contains(warning, "extra.go") {
		t.Fatalf("warning = %q, want unexpected extra.go advisory", warning)
	}
	if strings.Contains(warning, "declared.go") {
		t.Fatalf("warning = %q, declared path must not be reported", warning)
	}
}

func TestSliceCompletionCommitsEnvExampleTemplate(t *testing.T) {
	root := initSliceCompletionRepo(t)
	detail, record := sliceCompletionRecord(t, root, CommitPolicySlice, &sliceCompletionStore{})
	detail.Slices.Slices[0].ExpectedFiles = []string{".env.example"}
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("TOKEN=replace-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (SliceCompletionService{}).Complete(context.Background(), SliceCompletionRequest{
		Record: record, SliceID: "001-a", Notes: "done", Now: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	committed := strings.TrimSpace(runCommitTestGitOutput(t, root, "show", "--name-only", "--format=", "HEAD"))
	if committed != ".env.example" {
		t.Fatalf("committed paths = %q, want .env.example", committed)
	}
}

func TestSliceCompletionRefusesUnsafePathsBeforeStagingOrCommit(t *testing.T) {
	for _, tc := range []struct {
		name          string
		path          string
		tracked       bool
		wantErrorText string
	}{
		{name: "suspected secret", path: ".env.local", wantErrorText: "suspected secret path: .env.local"},
		{name: "generated artifact", path: "coverage.out", tracked: true, wantErrorText: "generated artifact path: coverage.out"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initSliceCompletionRepo(t)
			if tc.tracked {
				if err := os.WriteFile(filepath.Join(root, tc.path), []byte("tracked fixture\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runCommitTestGitCommand(t, root, "add", "-f", tc.path)
				runCommitTestGitCommand(t, root, "commit", "-m", "add tracked fixture")
			}
			detail, record := sliceCompletionRecord(t, root, CommitPolicySlice, &sliceCompletionStore{})
			if err := os.WriteFile(filepath.Join(root, tc.path), []byte("unsafe completion input\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			headBefore := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
			statusBefore := runCommitTestGitOutput(t, root, "status", "--short")

			err := (SliceCompletionService{}).Complete(context.Background(), SliceCompletionRequest{
				Record: record, SliceID: "001-a", Notes: "done", Now: time.Now().UTC(),
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantErrorText) {
				t.Fatalf("completion error = %v, want refusal containing %q", err, tc.wantErrorText)
			}
			if staged := runCommitTestGitOutput(t, root, "diff", "--cached", "--name-only"); staged != "" {
				t.Fatalf("completion staged unsafe path(s): %q", staged)
			}
			if headAfter := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); headAfter != headBefore {
				t.Fatalf("completion committed unsafe path: HEAD changed from %s to %s", headBefore, headAfter)
			}
			if statusAfter := runCommitTestGitOutput(t, root, "status", "--short"); statusAfter != statusBefore {
				t.Fatalf("completion mutated worktree status:\nbefore:\n%safter:\n%s", statusBefore, statusAfter)
			}
			if completion := detail.Slices.Slices[0].Completion; completion != nil {
				t.Fatalf("completion metadata recorded after safety refusal: %#v", completion)
			}
		})
	}
}

func TestSliceCompletionRecordsNoChangesAndNonePolicy(t *testing.T) {
	for _, tc := range []struct {
		name, policy, outcome string
	}{
		{"no changes", CommitPolicySlice.String(), plan.SliceCompletionNoChanges},
		{"manual none", CommitPolicyNone.String(), plan.SliceCompletionManualUncommitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := initSliceCompletionRepo(t)
			detail, record := sliceCompletionRecord(t, root, CommitPolicy(tc.policy), &sliceCompletionStore{})
			head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
			err := (SliceCompletionService{}).Complete(context.Background(), SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "done", Now: time.Now().UTC()})
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); got != head {
				t.Fatalf("completion unexpectedly mutated HEAD: %s -> %s", head, got)
			}
			if completion := detail.Slices.Slices[0].Completion; completion == nil || completion.Outcome != tc.outcome {
				t.Fatalf("completion = %#v, want %s", completion, tc.outcome)
			}
		})
	}
}

func initSliceCompletionRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runCommitTestGitCommand(t, root, "init")
	runCommitTestGitCommand(t, root, "config", "user.email", "tao@example.com")
	runCommitTestGitCommand(t, root, "config", "user.name", "Tao Test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runCommitTestGitCommand(t, root, "add", "README.md")
	runCommitTestGitCommand(t, root, "commit", "-m", "base")
	runCommitTestGitCommand(t, root, "checkout", "-b", "tao/test")
	return root
}

func sliceCompletionRecord(t *testing.T, root string, policy CommitPolicy, store plan.ArtifactStore) (*plan.PlanDetail, *plan.PlanRecord) {
	t.Helper()
	started := time.Now().UTC().Add(-time.Minute)
	detail := runPlanDetail(plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress, &started, nil)
	detail.Dir = filepath.Join(root, "plan-metadata")
	detail.State.Repo.Root = root
	detail.State.Plan.LastRunCommitPolicy = policy.String()
	detail.State.Plan.CurrentSlice = new("001-a")
	detail.Slices.Slices[0].Title = "A"
	detail.Slices.Slices[0].ExecutionRoot = root
	detail.Slices.Slices[0].ExecutionStart = &plan.SliceExecutionStart{
		Branch: strings.TrimSpace(runCommitTestGitOutput(t, root, "branch", "--show-current")),
		Head:   strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")),
	}
	detail.Slices.Slices[0].Timing.StartedAt = &started
	record, err := plan.NewPlanRecordWithStore(store, detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	return detail, record
}
