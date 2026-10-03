package run

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
)

type sliceCompletionStore struct {
	failState    bool
	failSlicesAt int
	failEvent    bool
	stateWrites  int
	slicesWrites int
	eventWrites  int
	state        plan.State
	slices       plan.SlicesFile
	events       []plan.Event
}

func (s *sliceCompletionStore) WriteState(_ string, payload []byte) error {
	s.stateWrites++
	if s.failState {
		s.failState = false
		return errors.New("interrupted metadata write")
	}
	return json.Unmarshal(payload, &s.state)
}
func (s *sliceCompletionStore) WriteSlices(_ string, payload []byte) error {
	s.slicesWrites++
	if s.slicesWrites == s.failSlicesAt {
		return errors.New("interrupted slices write")
	}
	return json.Unmarshal(payload, &s.slices)
}
func (s *sliceCompletionStore) AppendEvent(_ string, event plan.Event) error {
	s.eventWrites++
	if s.failEvent {
		s.failEvent = false
		return errors.New("interrupted event append")
	}
	s.events = append(s.events, event)
	return nil
}

func TestExpectedPlanCommitPathsIncludesOnlyCompletedAndSelectedSlices(t *testing.T) {
	detail := &plan.PlanDetail{
		State: plan.State{Plan: plan.PlanState{CompletedSlices: []string{"completed"}}},
		Slices: plan.SlicesFile{Slices: []plan.Slice{
			{ID: "completed", ExpectedFiles: []string{"./README.md", "internal/run/*.go"}},
			{ID: "selected", ExpectedFiles: []string{"README.md", "./docs/**/*.md"}},
			{ID: "pending", ExpectedFiles: []string{"pending.go"}},
		}},
	}
	expected := expectedPlanCommitPaths(detail, "selected")
	for _, test := range []struct {
		path string
		want bool
	}{
		{"README.md", true},
		{"./README.md", true},
		{"internal/run/run.go", true},
		{"internal/run/sub/run.go", false},
		{"docs/guide.md", true},
		{"docs/sub/guide.md", true},
		{"pending.go", false},
		{"unrelated.go", false},
	} {
		if got := expected.Allows(test.path); got != test.want {
			t.Errorf("Allows(%q) = %v, want %v", test.path, got, test.want)
		}
	}
	if expectedPlanCommitPaths(detail).Allows("docs/guide.md") {
		t.Fatal("selected slice should require explicit inclusion before completion")
	}
}

func TestSliceCompletionRejectsAbandonedBeforeGitOrArtifactMutation(t *testing.T) {
	root := initSliceCompletionRepo(t)
	store := &sliceCompletionStore{}
	detail, record := sliceCompletionRecord(t, root, CommitPolicySlice, store)
	detail.State.Status = plan.StatusAbandoned
	detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypePlanAbandoned, Reason: "superseded by safer work"})
	if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	persistRunArtifacts(t, detail.Dir, detail)
	detailBefore, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	statusBefore := runCommitTestGitOutput(t, root, "status", "--short")
	var gitCalls []string
	err = (SliceCompletionService{CommandRunner: runGitFake(&gitCalls, nil)}).Complete(context.Background(), SliceCompletionRequest{
		Record: record, SliceID: "001-a", Notes: "must not complete", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC(),
	})
	if err == nil || !strings.Contains(err.Error(), "plan plan-a is abandoned") {
		t.Fatalf("completion error = %v, want abandonment refusal", err)
	}
	if len(gitCalls) != 0 {
		t.Fatalf("abandoned completion inspected or mutated Git: %v", gitCalls)
	}
	if store.stateWrites != 0 || store.slicesWrites != 0 || store.eventWrites != 0 {
		t.Fatalf("abandoned completion wrote artifacts: state=%d slices=%d events=%d", store.stateWrites, store.slicesWrites, store.eventWrites)
	}
	detailAfter, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(detailAfter, detailBefore) {
		t.Fatalf("abandoned completion mutated in-memory plan artifacts")
	}
	if headAfter := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); headAfter != headBefore {
		t.Fatalf("abandoned completion changed HEAD: %s -> %s", headBefore, headAfter)
	}
	if statusAfter := runCommitTestGitOutput(t, root, "status", "--short"); statusAfter != statusBefore {
		t.Fatalf("abandoned completion mutated worktree status:\nbefore:\n%safter:\n%s", statusBefore, statusAfter)
	}
}

func TestSliceCompletionRejectsAbandonmentSettledBeforeRefreshedMutation(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy CommitPolicy
	}{
		{name: "commit intent", policy: CommitPolicySlice},
		{name: "legacy completion", policy: CommitPolicyPlan},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := initSliceCompletionRepo(t)
			detail, _ := sliceCompletionRecord(t, root, test.policy, &sliceCompletionStore{})
			if err := os.MkdirAll(detail.Dir, 0o700); err != nil {
				t.Fatal(err)
			}
			record, err := plan.NewPlanRecord(detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			if err := record.PersistArtifacts(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			// The standalone service has already accepted this stale in-memory
			// detail when another process wins the durable abandonment mutation.
			if err := plan.RequireNotAbandoned(record.Detail()); err != nil {
				t.Fatalf("initial service gate: %v", err)
			}
			repo := plan.NewFileRepository(filepath.Dir(detail.Dir))
			abandonRecord, err := repo.ResolvePlanRecord(context.Background(), detail.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if err := abandonRecord.Abandon("superseded concurrently", time.Date(2026, 9, 1, 18, 0, 0, 0, time.UTC)); err != nil {
				t.Fatal(err)
			}
			if record.Detail().State.Status == plan.StatusAbandoned {
				t.Fatal("test requires the completion record to remain stale")
			}

			headBefore := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
			statusBefore := runCommitTestGitOutput(t, root, "status", "--short")
			err = (SliceCompletionService{}).Complete(context.Background(), SliceCompletionRequest{
				Record: record, SliceID: "001-a", Notes: "must not complete", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC(),
			})
			if err == nil || !strings.Contains(err.Error(), "plan plan-a is abandoned: superseded concurrently") {
				t.Fatalf("completion error = %v, want refreshed abandonment refusal", err)
			}

			persisted, err := repo.ResolvePlan(context.Background(), detail.Dir)
			if err != nil {
				t.Fatal(err)
			}
			slice := completionSlice(persisted, "001-a")
			if persisted.State.Status != plan.StatusAbandoned || slice == nil || slice.CommitIntent != nil || slice.Completion != nil || slice.Status != plan.StatusInProgress {
				t.Fatalf("artifacts after refused completion: status=%q slice=%#v", persisted.State.Status, slice)
			}
			if got := countPlanEvents(persisted.Events, plan.EventTypeSliceCompleted); got != 0 {
				t.Fatalf("slice_completed events = %d, want 0", got)
			}
			if headAfter := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); headAfter != headBefore {
				t.Fatalf("refused completion changed HEAD: %s -> %s", headBefore, headAfter)
			}
			if statusAfter := runCommitTestGitOutput(t, root, "status", "--short"); statusAfter != statusBefore {
				t.Fatalf("refused completion mutated Git status:\nbefore:\n%safter:\n%s", statusBefore, statusAfter)
			}
		})
	}
}

func TestSliceCompletionCommitsInterruptedTrackedStagedAndUntrackedWorkAtOriginalParent(t *testing.T) {
	fixture := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	record := fixture.Record
	root := record.Detail().Slices.Slices[0].ExecutionRoot
	originalHead := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
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
		CommitProposal:      sliceCompletionProposal(),
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
	if got := countPlanEvents(reloadVerifiedCompletion(t, fixture).Detail().Events, plan.EventTypeSliceCompleted); got != 1 {
		t.Fatalf("completion events = %d", got)
	}
	committed := strings.Fields(runCommitTestGitOutput(t, root, "show", "--name-only", "--format=", "HEAD"))
	if strings.Join(committed, ",") != "README.md,staged.go,untracked.go" {
		t.Fatalf("committed interrupted paths = %v", committed)
	}
}

func TestInterruptedSliceCompletionReloadBeforeParentExitIsRecoverable(t *testing.T) {
	fixture := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	record := fixture.Record
	detail := record.Detail()
	root := detail.Slices.Slices[0].ExecutionRoot
	if err := os.WriteFile(filepath.Join(root, "resumed.go"), []byte("package resumed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "resumed", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC()}
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

func TestHistoricalSliceCompletionCommitsAndRecoversInterruptedMetadata(t *testing.T) {
	root := initSliceCompletionRepo(t)
	detail, record := sliceCompletionRecord(t, root, CommitPolicySlice, &sliceCompletionStore{failState: true})
	if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{
		Record: record, SliceID: "001-a", Notes: "done",
		VerificationResults: []plan.VerificationRun{{Command: "go test ./internal/run", CWD: root, Result: "passed", Details: "ok"}},
		CommitProposal:      sliceCompletionProposal(),
		Now:                 time.Now().UTC(),
	}
	service := SliceCompletionService{}
	recordHistoricalTestIntent(t, request)
	if err := service.settleHistoricalCompletion(context.Background(), request); err == nil || !strings.Contains(err.Error(), "interrupted metadata write") {
		t.Fatalf("expected interrupted metadata error, got %v", err)
	}
	firstHead := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	if err := service.settleHistoricalCompletion(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); head != firstHead {
		t.Fatalf("retry created a second commit: first=%s second=%s", firstHead, head)
	}
	message := runCommitTestGitOutput(t, root, "log", "-1", "--format=%B")
	for _, want := range []string{"feat(run): accept slice commit proposals", "What:\n", "Why:\n", "Tao-Plan: plan-a", "Tao-Slice: 001-a"} {
		if !strings.Contains(message, want) {
			t.Fatalf("commit message missing %q:\n%s", want, message)
		}
	}
	if strings.Contains(message, "Verification:") {
		t.Fatalf("commit message retained verification rendering:\n%s", message)
	}
	completed := detail.Slices.Slices[0].Completion
	if completed == nil || completed.Outcome != plan.SliceCompletionCommitted || completed.CommitSHA != firstHead {
		t.Fatalf("unexpected completion outcome: %#v", completed)
	}
}

func TestHistoricalSliceCompletionRecoversCommitAfterStateOnlyCompletionWrite(t *testing.T) {
	root := initSliceCompletionRepo(t)
	store := &sliceCompletionStore{failSlicesAt: 2}
	_, record := sliceCompletionRecord(t, root, CommitPolicySlice, store)
	if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "done", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC()}
	service := SliceCompletionService{}
	recordHistoricalTestIntent(t, request)
	if err := service.settleHistoricalCompletion(context.Background(), request); err == nil || !strings.Contains(err.Error(), "interrupted slices write") {
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
	if err := service.settleHistoricalCompletion(context.Background(), request); err != nil {
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

func TestHistoricalSliceCompletionRecoversMissingCompletionEvent(t *testing.T) {
	root := initSliceCompletionRepo(t)
	store := &sliceCompletionStore{failEvent: true}
	_, record := sliceCompletionRecord(t, root, CommitPolicySlice, store)
	if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "done", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC()}
	service := SliceCompletionService{}
	recordHistoricalTestIntent(t, request)
	if err := service.settleHistoricalCompletion(context.Background(), request); err == nil || !strings.Contains(err.Error(), "interrupted event append") {
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
	if err := service.settleHistoricalCompletion(context.Background(), request); err != nil {
		t.Fatalf("recover completion event: %v", err)
	}
	if head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); head != committedHead {
		t.Fatalf("event recovery created another commit: %s -> %s", committedHead, head)
	}
	if len(store.events) != 1 || store.events[0].Type != plan.EventTypeSliceCompleted || store.events[0].SliceID != request.SliceID {
		t.Fatalf("recovered events = %#v", store.events)
	}
	if err := service.settleHistoricalCompletion(context.Background(), request); err != nil {
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
	hash, err := sliceCompletionHash("plan-a", "001-a", CommitPolicyNone.String(), notes, results, "")
	if err != nil {
		t.Fatal(err)
	}
	settled := cloneRunRestartDetail(t, base)
	settled.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Hash: hash, Policy: CommitPolicyNone.String(), StartingBranch: "tao/test", StartingHead: "base", CreatedAt: now}
	outcome := plan.SliceCompletionOutcome{Outcome: plan.SliceCompletionManualUncommitted}
	repository := plantest.NewPersistingRepository()
	repository.AddDetail(settled)
	settledRecord, err := repository.PlanRecord(settled)
	if err != nil {
		t.Fatal(err)
	}
	if err := settledRecord.CompleteSliceWithOutcome("001-a", notes, results, outcome, now); err != nil {
		t.Fatalf("prepare settled completion: %v", err)
	}
	settled, err = repository.GetPlan(context.Background(), settled.State.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	var completionEvent *plan.Event
	for _, event := range settled.Events {
		if event.Type == plan.EventTypeSliceCompleted && event.SliceID == "001-a" {
			completionEvent = &event
			break
		}
	}
	if completionEvent == nil {
		t.Fatal("missing slice_completed event")
	}
	writeRunRestartJournal(t, planDir, "restart-completion", &settled.State, &settled.Slices, []plan.Event{*completionEvent})

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
	fixture := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	record := fixture.Record
	detail := record.Detail()
	root := detail.Slices.Slices[0].ExecutionRoot
	detail.Slices.Slices[0].ExpectedFiles = []string{"declared.go"}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
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
		Record: record, SliceID: "001-a", Notes: "done", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC(),
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
	fixture := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	record := fixture.Record
	detail := record.Detail()
	root := detail.Slices.Slices[0].ExecutionRoot
	detail.Slices.Slices[0].ExpectedFiles = []string{".env.example"}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".env.example"), []byte("TOKEN=replace-me\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := (SliceCompletionService{}).Complete(context.Background(), SliceCompletionRequest{
		Record: record, SliceID: "001-a", Notes: "done", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC(),
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
			fixture := verifiedCompletionFixture(t, CommitPolicySlice, "true")
			record := fixture.Record
			detail := record.Detail()
			root := detail.Slices.Slices[0].ExecutionRoot
			if tc.tracked {
				if err := os.WriteFile(filepath.Join(root, tc.path), []byte("tracked fixture\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				runCommitTestGitCommand(t, root, "add", "-f", tc.path)
				runCommitTestGitCommand(t, root, "commit", "-m", "add tracked fixture")
			}
			detail.Slices.Slices[0].ExecutionStart.Head = strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
			detail.State.Workspace.HeadSHA = detail.Slices.Slices[0].ExecutionStart.Head
			// Build a fresh durable fixture at the updated baseline; execution_start
			// is immutable once recorded by a real run.
			detail.Dir = t.TempDir()
			var err error
			record, err = plan.NewPlanRecord(detail.Dir, detail)
			if err != nil {
				t.Fatal(err)
			}
			if err := record.PersistArtifacts(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, tc.path), []byte("unsafe completion input\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			headBefore := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
			statusBefore := runCommitTestGitOutput(t, root, "status", "--short")

			err = (SliceCompletionService{}).Complete(context.Background(), SliceCompletionRequest{
				Record: record, SliceID: "001-a", Notes: "done", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC(),
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

func TestSliceCompletionRejectsThenAcceptsProposalBeforeIntent(t *testing.T) {
	fixture := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	record := fixture.Record
	detail := record.Detail()
	root := detail.Slices.Slices[0].ExecutionRoot
	if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	proposal := sliceCompletionProposal()
	proposal.Summary = "Added invalid proposal"
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "done", CommitProposal: proposal, Now: time.Now().UTC()}
	if err := (SliceCompletionService{}).Complete(context.Background(), request); err == nil || !strings.Contains(err.Error(), "summary must be lowercase") {
		t.Fatalf("invalid proposal error = %v", err)
	}
	if detail.Slices.Slices[0].CommitIntent != nil {
		t.Fatalf("invalid proposal recorded intent: %#v", detail.Slices.Slices[0].CommitIntent)
	}
	if staged := runCommitTestGitOutput(t, root, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("invalid proposal staged paths: %q", staged)
	}
	if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); got != head {
		t.Fatalf("invalid proposal changed HEAD: %s -> %s", head, got)
	}

	request.CommitProposal = sliceCompletionProposal()
	if err := (SliceCompletionService{}).Complete(context.Background(), request); err != nil {
		t.Fatalf("repaired proposal: %v", err)
	}
	intent := reloadVerifiedCompletion(t, fixture).Detail().Slices.Slices[0].CommitIntent
	if intent == nil || intent.Message == "" || strings.Contains(intent.Message, "Verification:") {
		t.Fatalf("repaired proposal intent = %#v", intent)
	}
}

func TestSliceCompletionRejectsReservedTrailerBeforeIntent(t *testing.T) {
	fixture := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	record := fixture.Record
	detail := record.Detail()
	proposal := sliceCompletionProposal()
	proposal.Why += "\nTao-Plan: forged"
	err := (SliceCompletionService{}).Complete(context.Background(), SliceCompletionRequest{
		Record: record, SliceID: "001-a", Notes: "done", CommitProposal: proposal, Now: time.Now().UTC(),
	})
	if err == nil || !strings.Contains(err.Error(), "reserved Tao-*") {
		t.Fatalf("reserved trailer error = %v", err)
	}
	if detail.Slices.Slices[0].CommitIntent != nil {
		t.Fatalf("reserved trailer recorded intent: %#v", detail.Slices.Slices[0].CommitIntent)
	}
}

func TestHistoricalSliceCompletionConflictingProposalCannotReuseIntent(t *testing.T) {
	root := initSliceCompletionRepo(t)
	store := &sliceCompletionStore{failState: true}
	detail, record := sliceCompletionRecord(t, root, CommitPolicySlice, store)
	if err := os.WriteFile(filepath.Join(root, "work.go"), []byte("package work\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: "done", CommitProposal: sliceCompletionProposal(), Now: time.Now().UTC()}
	recordHistoricalTestIntent(t, request)
	if err := (SliceCompletionService{}).settleHistoricalCompletion(context.Background(), request); err == nil || !strings.Contains(err.Error(), "interrupted metadata write") {
		t.Fatalf("initial completion error = %v", err)
	}
	head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	intent := detail.Slices.Slices[0].CommitIntent
	if intent == nil {
		t.Fatal("initial completion did not persist intent before staging")
	}
	changed := sliceCompletionProposal()
	changed.Summary = "record a different slice proposal"
	request.CommitProposal = changed
	if err := (SliceCompletionService{}).settleHistoricalCompletion(context.Background(), request); err == nil || !strings.Contains(err.Error(), "conflicting commit proposal") {
		t.Fatalf("conflicting proposal error = %v", err)
	}
	if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); got != head {
		t.Fatalf("conflicting retry changed HEAD: %s -> %s", head, got)
	}
}

func TestHistoricalSliceCompletionRecoversLegacyIntentWithoutMessageValidation(t *testing.T) {
	root := initSliceCompletionRepo(t)
	detail, record := sliceCompletionRecord(t, root, CommitPolicySlice, &sliceCompletionStore{})
	parent := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "legacy.go"), []byte("package legacy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	legacyMessage := "legacy slice message\n\nVerification:\n- historical"
	notes := "recover historical intent"
	legacyHash, err := legacySliceCompletionHash("plan-a", "001-a", CommitPolicySlice.String(), notes, nil)
	if err != nil {
		t.Fatal(err)
	}
	detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{
		Hash: legacyHash, Policy: CommitPolicySlice.String(), StartingBranch: "tao/test", StartingHead: parent,
		Message: legacyMessage, CreatedAt: time.Now().UTC(),
	}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: notes, Now: time.Now().UTC()}
	if err := (SliceCompletionService{}).settleHistoricalCompletion(context.Background(), request); err != nil {
		t.Fatalf("recover legacy intent: %v", err)
	}
	completion := detail.Slices.Slices[0].Completion
	if completion == nil || completion.Outcome != plan.SliceCompletionCommitted {
		t.Fatalf("legacy recovery completion = %#v", completion)
	}
	if message := strings.TrimSpace(runCommitTestGitOutput(t, root, "log", "-1", "--format=%B")); message != legacyMessage {
		t.Fatalf("legacy message changed during recovery:\nwant: %q\ngot:  %q", legacyMessage, message)
	}
}

// Seed pre-rollout intent explicitly for the historical-store fault tests.
func recordHistoricalTestIntent(t *testing.T, request SliceCompletionRequest) {
	t.Helper()
	detail := request.Record.Detail()
	message, err := formatSliceCommitMessage(detail.State.Plan.ID, request.SliceID, *request.CommitProposal)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := sliceCompletionHash(detail.State.Plan.ID, request.SliceID, detail.State.Plan.LastRunCommitPolicy, request.Notes, request.VerificationResults, message)
	if err != nil {
		t.Fatal(err)
	}
	slice := completionSlice(detail, request.SliceID)
	intent := plan.SliceCommitIntent{Hash: hash, Policy: detail.State.Plan.LastRunCommitPolicy, StartingBranch: slice.ExecutionStart.Branch, StartingHead: slice.ExecutionStart.Head, Message: message, CreatedAt: request.Now}
	if err := persistSliceCommitIntent(request, intent); err != nil {
		t.Fatal(err)
	}
}

func sliceCompletionProposal() *commitcontract.Proposal {
	return &commitcontract.Proposal{
		Type: "feat", Scope: "run", Summary: "accept slice commit proposals",
		What: "Use the active implementation agent's structured proposal for the slice commit.",
		Why:  "Avoid a nested message session while retaining Tao-owned validation and Git authority.",
	}
}

func initSliceCompletionRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runCommitTestGitCommand(t, root, "init")
	disableRunGitMaintenance(t, root)
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
	detail.Dir = t.TempDir()
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

// isolateSliceStagingGitConfig keeps the developer's global ignore and system
// configuration from masking the porcelain states these tests construct.
func isolateSliceStagingGitConfig(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

func writeSliceStagingFile(t *testing.T, root string, name string, content string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sliceStagingFixture commits tracked baseline files on the verified worktree
// and rebuilds the durable record at the new execution_start head.
func sliceStagingFixture(t *testing.T, tracked map[string]string) (SliceCompletionRequest, string) {
	t.Helper()
	isolateSliceStagingGitConfig(t)
	fixture := verifiedCompletionFixture(t, CommitPolicySlice, "true")
	detail := fixture.Record.Detail()
	root := detail.Slices.Slices[0].ExecutionRoot
	for name, content := range tracked {
		writeSliceStagingFile(t, root, name, content)
		runCommitTestGitCommand(t, root, "add", "--", name)
	}
	runCommitTestGitCommand(t, root, "commit", "--allow-empty", "-m", "tracked baseline")
	detail.Slices.Slices[0].ExecutionStart.Head = strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	detail.State.Workspace.HeadSHA = detail.Slices.Slices[0].ExecutionStart.Head
	detail.Dir = t.TempDir()
	record, err := plan.NewPlanRecord(detail.Dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.PersistArtifacts(); err != nil {
		t.Fatal(err)
	}
	fixture.Record = record
	fixture.Notes = "staged by porcelain bucket"
	fixture.CommitProposal = sliceCompletionProposal()
	fixture.Now = time.Now().UTC()
	return fixture, root
}

func sliceStagingCommittedPaths(t *testing.T, root string) string {
	t.Helper()
	return strings.Join(strings.Fields(runCommitTestGitOutput(t, root, "show", "--name-only", "--format=", "HEAD")), ",")
}

func assertSliceStagingCommitted(t *testing.T, fixture SliceCompletionRequest, root string, originalHead string, wantPaths string) {
	t.Helper()
	head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	if head == originalHead {
		t.Fatalf("completion did not advance HEAD from %s", originalHead)
	}
	if parent := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", head+"^")); parent != originalHead {
		t.Fatalf("completion parent = %s, want execution_start HEAD %s", parent, originalHead)
	}
	if got := sliceStagingCommittedPaths(t, root); got != wantPaths {
		t.Fatalf("committed paths = %q, want %q", got, wantPaths)
	}
	reloaded := reloadVerifiedCompletion(t, fixture).Detail()
	completion := reloaded.Slices.Slices[0].Completion
	if completion == nil || completion.Outcome != plan.SliceCompletionCommitted || completion.CommitSHA != head {
		t.Fatalf("completion outcome = %#v, want committed %s", completion, head)
	}
	if got := countPlanEvents(reloaded.Events, plan.EventTypeSliceCompleted); got != 1 {
		t.Fatalf("completion events = %d", got)
	}
}

func TestSliceCompletionCommitsGitRmDeletionWithTrackedModification(t *testing.T) {
	fixture, root := sliceStagingFixture(t, map[string]string{"pkg/removed.go": "package pkg\n"})
	originalHead := fixture.Record.Detail().Slices.Slices[0].ExecutionStart.Head
	runCommitTestGitCommand(t, root, "rm", "--quiet", "--", "pkg/removed.go")
	writeSliceStagingFile(t, root, "README.md", "unrelated modification\n")
	status := runCommitTestGitOutput(t, root, "status", "--short")
	for _, want := range []string{"D  pkg/removed.go", " M README.md"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}

	if err := (SliceCompletionService{}).Complete(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}
	assertSliceStagingCommitted(t, fixture, root, originalHead, "README.md,pkg/removed.go")
	if diff := runCommitTestGitOutput(t, root, "show", "--name-status", "--format=", "HEAD"); !strings.Contains(diff, "D\tpkg/removed.go") {
		t.Fatalf("commit does not record the deletion:\n%s", diff)
	}
	if status := runCommitTestGitOutput(t, root, "status", "--short"); status != "" {
		t.Fatalf("final worktree is dirty:\n%s", status)
	}
}

func TestSliceCompletionStagesTrackedChangesUnderNewlyIgnoredDirectory(t *testing.T) {
	fixture, root := sliceStagingFixture(t, map[string]string{
		".gitignore":          "binary.dat\n",
		"pkg/cache/kept.go":   "package cache\n",
		"pkg/cache/gone.go":   "package cache\n",
		"pkg/cache/README.md": "cache\n",
	})
	originalHead := fixture.Record.Detail().Slices.Slices[0].ExecutionStart.Head
	writeSliceStagingFile(t, root, ".gitignore", "binary.dat\npkg/cache/\n")
	writeSliceStagingFile(t, root, "pkg/cache/kept.go", "package cache // modified\n")
	if err := os.Remove(filepath.Join(root, "pkg/cache/gone.go")); err != nil {
		t.Fatal(err)
	}
	writeSliceStagingFile(t, root, "pkg/cache/sibling.tmp", "ignored sibling\n")
	status := runCommitTestGitOutput(t, root, "status", "--short")
	for _, want := range []string{" M .gitignore", " M pkg/cache/kept.go", " D pkg/cache/gone.go"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}
	if strings.Contains(status, "sibling.tmp") {
		t.Fatalf("ignored sibling listed in status:\n%s", status)
	}

	if err := (SliceCompletionService{}).Complete(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}
	assertSliceStagingCommitted(t, fixture, root, originalHead, ".gitignore,pkg/cache/gone.go,pkg/cache/kept.go")
	if tracked := runCommitTestGitOutput(t, root, "ls-files", "--", "pkg/cache"); strings.Contains(tracked, "sibling.tmp") {
		t.Fatalf("ignored untracked sibling was force-added:\n%s", tracked)
	}
}

func TestSliceCompletionReplaysPartiallyStagedFailedAttemptAtSameIntent(t *testing.T) {
	fixture, root := sliceStagingFixture(t, map[string]string{"pkg/cache/kept.go": "package cache\n"})
	originalHead := fixture.Record.Detail().Slices.Slices[0].ExecutionStart.Head
	writeSliceStagingFile(t, root, "pkg/cache/kept.go", "package cache // modified\n")
	writeSliceStagingFile(t, root, "pkg/cache/new.go", "package cache\n")

	failingStage := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && len(args) >= 4 && args[2] == "add" && args[3] != "-f" {
			return errors.New("simulated untracked staging failure")
		}
		return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
	}}
	err := failingStage.Complete(context.Background(), fixture)
	if err == nil || !strings.Contains(err.Error(), "stage slice completion paths") {
		t.Fatalf("first attempt error = %v, want staging failure", err)
	}
	if staged := strings.TrimSpace(runCommitTestGitOutput(t, root, "diff", "--cached", "--name-only")); staged != "pkg/cache/kept.go" {
		t.Fatalf("partially staged paths = %q, want only the tracked change", staged)
	}
	if head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); head != originalHead {
		t.Fatalf("failed attempt moved HEAD to %s", head)
	}
	afterFailure := reloadVerifiedCompletion(t, fixture).Detail()
	intent := afterFailure.Slices.Slices[0].CommitIntent
	if intent == nil || intent.StartingHead != originalHead {
		t.Fatalf("intent after failed attempt = %#v, want recorded at %s", intent, originalHead)
	}
	if afterFailure.Slices.Slices[0].Completion != nil {
		t.Fatalf("failed attempt recorded completion %#v", afterFailure.Slices.Slices[0].Completion)
	}
	if got := countPlanEvents(afterFailure.Events, plan.EventTypeSliceCompleted); got != 0 {
		t.Fatalf("failed attempt recorded %d completion events", got)
	}

	replay := fixture
	replay.Record = reloadVerifiedCompletion(t, fixture)
	if err := (SliceCompletionService{}).Complete(context.Background(), replay); err != nil {
		t.Fatal(err)
	}
	assertSliceStagingCommitted(t, fixture, root, originalHead, "pkg/cache/kept.go,pkg/cache/new.go")
	settled := reloadVerifiedCompletion(t, fixture).Detail().Slices.Slices[0].CommitIntent
	if settled == nil || settled.Hash != intent.Hash || settled.Message != intent.Message || !settled.CreatedAt.Equal(intent.CreatedAt) {
		t.Fatalf("replay intent = %#v, want the original %#v", settled, intent)
	}
}

func TestSliceCompletionStagesCollapsedUntrackedDirectory(t *testing.T) {
	fixture, root := sliceStagingFixture(t, nil)
	originalHead := fixture.Record.Detail().Slices.Slices[0].ExecutionStart.Head
	writeSliceStagingFile(t, root, "pkg/cache/a.go", "package cache\n")
	writeSliceStagingFile(t, root, "pkg/cache/b.go", "package cache\n")
	if status := strings.TrimSpace(runCommitTestGitOutput(t, root, "status", "--short")); status != "?? pkg/" {
		t.Fatalf("status = %q, want one collapsed untracked directory", status)
	}

	if err := (SliceCompletionService{}).Complete(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}
	assertSliceStagingCommitted(t, fixture, root, originalHead, "pkg/cache/a.go,pkg/cache/b.go")
}

func TestSliceCompletionReaddsWorktreeCopyOfStagedDeletion(t *testing.T) {
	fixture, root := sliceStagingFixture(t, map[string]string{"pkg/cache/kept.go": "package cache\n", "pkg/cache/other.go": "package cache\n"})
	originalHead := fixture.Record.Detail().Slices.Slices[0].ExecutionStart.Head
	runCommitTestGitCommand(t, root, "rm", "--quiet", "--", "pkg/cache/kept.go")
	writeSliceStagingFile(t, root, "pkg/cache/kept.go", "package cache // recreated\n")
	status := runCommitTestGitOutput(t, root, "status", "--short")
	for _, want := range []string{"D  pkg/cache/kept.go", "?? pkg/cache/kept.go"} {
		if !strings.Contains(status, want) {
			t.Fatalf("status missing %q:\n%s", want, status)
		}
	}

	if err := (SliceCompletionService{}).Complete(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}
	assertSliceStagingCommitted(t, fixture, root, originalHead, "pkg/cache/kept.go")
	if diff := runCommitTestGitOutput(t, root, "show", "--name-status", "--format=", "HEAD"); !strings.Contains(diff, "M\tpkg/cache/kept.go") {
		t.Fatalf("commit does not record the worktree copy as a modification:\n%s", diff)
	}
	if content := runCommitTestGitOutput(t, root, "show", "HEAD:pkg/cache/kept.go"); !strings.Contains(content, "recreated") {
		t.Fatalf("committed content = %q, want the worktree copy", content)
	}
}

func TestSliceCompletionStagingFailureNamesExecutionRootAndMustChangeGuidance(t *testing.T) {
	fixture, root := sliceStagingFixture(t, map[string]string{"pkg/cache/kept.go": "package cache\n"})
	writeSliceStagingFile(t, root, "pkg/cache/kept.go", "package cache // modified\n")
	headBefore := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))

	cause := errors.New("simulated staging failure")
	service := SliceCompletionService{CommandRunner: func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		if name == "git" && len(args) >= 3 && args[2] == "add" {
			return cause
		}
		return commandrunner.DefaultLocal(ctx, cwd, name, args, stdout, stderr)
	}}
	err := service.Complete(context.Background(), fixture)
	if err == nil {
		t.Fatal("staging failure did not refuse completion")
	}
	for _, want := range []string{
		"stage slice completion paths: ",
		"the worktree under " + root + " must change before rerunning tao slice-complete",
		"inspect git status there; do not commit by hand",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want %q", err, want)
		}
	}
	if !errors.Is(err, cause) {
		t.Fatalf("error = %v does not wrap the staging cause", err)
	}
	if head := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD")); head != headBefore {
		t.Fatalf("staging failure moved HEAD to %s", head)
	}
	reloaded := reloadVerifiedCompletion(t, fixture).Detail()
	if reloaded.Slices.Slices[0].Completion != nil {
		t.Fatalf("staging failure recorded completion %#v", reloaded.Slices.Slices[0].Completion)
	}
	if got := countPlanEvents(reloaded.Events, plan.EventTypeSliceCompleted); got != 0 {
		t.Fatalf("staging failure recorded %d completion events", got)
	}
}
