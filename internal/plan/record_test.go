package plan

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRestartBlockedSliceSupersedesExactBoundaryWithDurableEvidence(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	started := time.Date(2026, 8, 27, 1, 0, 0, 0, time.UTC)
	boundary := SliceExecutionStart{Branch: "feature/plan-a", Head: "old-head", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree}
	if err := record.StartSliceWithRunBoundary("001-a", "/worktrees/plan-a", "slice", nil, boundary, started); err != nil {
		t.Fatal(err)
	}
	if err := record.BlockSlice("001-a", "waiting for dependency", started.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	restarted := started.Add(2 * time.Minute)
	if err := record.RestartBlockedSlice(BlockedSliceRestartRequest{
		SliceID: "001-a", PriorRoot: "/worktrees/plan-a", PriorBoundary: boundary,
		BaselineBranch: "main", BaselineHead: "new-head", Reason: "dependency landed", RestartedAt: restarted,
	}); err != nil {
		t.Fatal(err)
	}

	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := &PlanDetail{Dir: dir, State: files.state, Slices: files.slices, Events: files.events}
	slice := reloaded.Slices.Slices[0]
	if reloaded.State.Status != StatusInProgress || reloaded.State.Plan.CurrentSlice != nil || slice.Status != StatusPending || slice.ExecutionRoot != "" || slice.ExecutionStart != nil || slice.BlockerNote != "" {
		t.Fatalf("restart lifecycle = state:%#v slice:%#v", reloaded.State, slice)
	}
	var event *Event
	for i := range reloaded.Events {
		if reloaded.Events[i].Type == EventTypeSliceRestarted {
			event = &reloaded.Events[i]
		}
	}
	if event == nil || event.PriorRoot != "/worktrees/plan-a" || event.PriorBranch != boundary.Branch || event.PriorHead != boundary.Head || event.BaselineBranch != "main" || event.BaselineHead != "new-head" || event.Reason != "dependency landed" {
		t.Fatalf("restart event = %#v", event)
	}
	before := reloaded
	staleRecord := testRecord(dir, before)
	if err := staleRecord.RestartBlockedSlice(BlockedSliceRestartRequest{SliceID: "001-a", PriorRoot: "/worktrees/plan-a", PriorBoundary: boundary, BaselineBranch: "main", BaselineHead: "other", RestartedAt: restarted}); err == nil {
		t.Fatal("expected retry against superseded boundary to fail")
	}
}

func TestRestartBlockedSliceSettlesJournalOnExactRetry(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	boundary := SliceExecutionStart{Branch: "feature/plan-a", Head: "old-head", CommitPolicy: "slice", WorkspaceStrategy: WorkspaceStrategyWorktree}
	started := time.Date(2026, 8, 27, 2, 0, 0, 0, time.UTC)
	setup := testRecord(dir, detail)
	if err := setup.StartSliceWithRunBoundary("001-a", "/worktrees/plan-a", "slice", nil, boundary, started); err != nil {
		t.Fatal(err)
	}
	if err := setup.BlockSlice("001-a", "dependency", started.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	original := clonePlanDetail(detail)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "state"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	record, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	request := BlockedSliceRestartRequest{SliceID: "001-a", PriorRoot: "/worktrees/plan-a", PriorBoundary: boundary, BaselineBranch: "main", BaselineHead: "new-head", RestartedAt: started.Add(2 * time.Minute)}
	if err := record.RestartBlockedSlice(request); err == nil || !strings.Contains(err.Error(), "injected state failure") {
		t.Fatalf("restart error = %v, want injected failure", err)
	}
	if !reflect.DeepEqual(detail, original) {
		t.Fatal("failed restart published its postimage")
	}
	ioStore.failOperation = ""
	if err := record.RestartBlockedSlice(request); err != nil {
		t.Fatalf("settle exact restart retry: %v", err)
	}
	if detail.State.Plan.CurrentSlice != nil || detail.Slices.Slices[0].ExecutionStart != nil {
		t.Fatalf("settled restart = state:%#v slice:%#v", detail.State, detail.Slices.Slices[0])
	}
	count := 0
	for _, event := range detail.Events {
		if event.Type == EventTypeSliceRestarted {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("restart events = %d, want 1", count)
	}
}

func TestRecordWorkspacePreparationTransitionsPreserveAndClearDependencyEvidence(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	intent := WorkspaceRebaseIntent{
		Branch: "tao/plan-a", BaseBranch: "main", OldHeadSHA: "abc1234", OldBaseSHA: "def1234",
		NewBaseSHA: "fed1234", CommitSeriesFingerprint: "v1:series", CreatedAt: time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC),
	}
	detail.State.Workspace = &Workspace{
		DependencyFailure: "old failure", DependencyFingerprint: "successful-lockfile", RebaseIntent: &intent,
	}
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	createdAt := time.Date(2026, 8, 25, 9, 0, 0, 0, time.FixedZone("test", 2*60*60))

	if err := record.RecordWorkspacePreparing(WorkspacePreparingRequest{
		Strategy: WorkspaceStrategyWorktree, Root: "/worktrees", Path: "/worktrees/plan-a", Branch: "tao/plan-a",
		BaseBranch: "main", BaseSHA: "def1234", BaseCurrentSHA: "def1234", BaseStatus: WorkspaceBaseStatusCurrent,
		HeadSHA: "abc1234", RefreshStatus: WorkspaceRefreshStatusNotNeeded, RebaseStatus: WorkspaceRebaseStatusNotNeeded,
		Created: true, RecordedAt: createdAt,
	}); err != nil {
		t.Fatal(err)
	}
	workspace := detail.State.Workspace
	if workspace.LifecycleStatus != WorkspaceStatusPreparing || workspace.Path != "/worktrees/plan-a" || workspace.HeadSHA != "abc1234" {
		t.Fatalf("preparing workspace = %#v", workspace)
	}
	if workspace.DependencyFailure != "old failure" || workspace.DependencyFingerprint != "successful-lockfile" || workspace.RebaseIntent == nil || *workspace.RebaseIntent != intent {
		t.Fatalf("preparing mutation replaced unrelated evidence: %#v", workspace)
	}
	if workspace.Timing.CreatedAt == nil || !workspace.Timing.CreatedAt.Equal(createdAt.UTC()) || workspace.Timing.LastActivityAt == nil || !workspace.Timing.LastActivityAt.Equal(createdAt.UTC()) {
		t.Fatalf("preparing timing = %#v", workspace.Timing)
	}

	startedAt := createdAt.Add(time.Minute).UTC()
	completedAt := startedAt.Add(time.Minute)
	if err := record.RecordWorkspaceDependencyFailure(WorkspaceDependencyFailureRequest{
		Status: DependencyPreparationStatusFailed, Command: "npm ci", StartedAt: &startedAt, CompletedAt: &completedAt, Failure: "registry unavailable",
	}); err != nil {
		t.Fatal(err)
	}
	workspace = detail.State.Workspace
	if workspace.LifecycleStatus != WorkspaceStatusFailed || workspace.DependencyFailure != "registry unavailable" || workspace.DependencyFingerprint != "successful-lockfile" {
		t.Fatalf("failed workspace = %#v", workspace)
	}

	readyAt := completedAt.Add(time.Minute)
	if err := record.RecordWorkspaceReady(WorkspaceReadyRequest{
		DependencyStatus: DependencyPreparationStatusFailed, DependencyCommand: "npm ci", DependencyStartedAt: &startedAt,
		DependencyCompletedAt: &completedAt, DependencyFailure: "registry unavailable", PreparedAt: readyAt,
	}); err != nil {
		t.Fatal(err)
	}
	workspace = detail.State.Workspace
	if workspace.LifecycleStatus != WorkspaceStatusReady || workspace.DependencyFailure != "registry unavailable" || workspace.DependencyFingerprint != "successful-lockfile" {
		t.Fatalf("reused-failure ready workspace = %#v", workspace)
	}

	successAt := readyAt.Add(time.Minute)
	if err := record.RecordWorkspaceReady(WorkspaceReadyRequest{
		DependencyStatus: DependencyPreparationStatusReady, DependencyCommand: "npm ci", DependencyStartedAt: &startedAt,
		DependencyCompletedAt: &completedAt, ClearDependencyFailure: true, ClearDependencyFingerprint: true, PreparedAt: successAt,
	}); err != nil {
		t.Fatal(err)
	}
	workspace = detail.State.Workspace
	if workspace.DependencyFailure != "" || workspace.DependencyFingerprint != "" || workspace.LifecycleStatus != WorkspaceStatusReady {
		t.Fatalf("successful ready workspace = %#v", workspace)
	}
	if workspace.RebaseIntent == nil || *workspace.RebaseIntent != intent {
		t.Fatalf("workspace rebase intent changed: %#v", workspace.RebaseIntent)
	}
	persisted := readStateFile(t, dir)
	if persisted.Workspace == nil || persisted.Workspace.DependencyFailure != "" || persisted.Workspace.DependencyFingerprint != "" {
		t.Fatalf("persisted successful ready workspace = %#v", persisted.Workspace)
	}
}

func TestRecordWorkspacePreparingRefreshesStaleDisjointWriter(t *testing.T) {
	dir := t.TempDir()
	initial := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, initial)
	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	stale := detailFromFiles(files)
	lifecycleRecord := testRecord(dir, detailFromFiles(files))
	startedAt := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	if err := lifecycleRecord.StartSlice("001-a", startedAt); err != nil {
		t.Fatal(err)
	}

	workspaceRecord := testRecord(dir, stale)
	if err := workspaceRecord.RecordWorkspacePreparing(WorkspacePreparingRequest{
		Strategy: WorkspaceStrategyWorktree, Path: "/worktrees/plan-a", Branch: "tao/plan-a", RecordedAt: startedAt.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	persisted := readStateFile(t, dir)
	if persisted.Status != StatusInProgress || persisted.Plan.CurrentSlice == nil || *persisted.Plan.CurrentSlice != "001-a" {
		t.Fatalf("workspace writer erased concurrent lifecycle state: %#v", persisted.Plan)
	}
	if persisted.Workspace == nil || persisted.Workspace.Path != "/worktrees/plan-a" || persisted.Workspace.LifecycleStatus != WorkspaceStatusPreparing {
		t.Fatalf("workspace mutation was not persisted: %#v", persisted.Workspace)
	}
	if stale.State.Status != StatusInProgress || stale.Slices.Slices[0].Status != StatusInProgress {
		t.Fatalf("workspace record did not publish refreshed settled detail: state=%#v slices=%#v", stale.State, stale.Slices)
	}
}

func TestRecordWorkspaceReadyRetryRecoversJournalWithoutPublishingFailedPostimage(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	detail.State.Workspace = &Workspace{
		Strategy: WorkspaceStrategyWorktree, LifecycleStatus: WorkspaceStatusPreparing,
		DependencyFailure: "install failed", DependencyFingerprint: "old-lockfile",
	}
	writeStartSliceArtifacts(t, dir, detail)
	original := clonePlanDetail(detail)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "state"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	record, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	request := WorkspaceReadyRequest{
		DependencyStatus: DependencyPreparationStatusReady, ClearDependencyFailure: true,
		DependencyFingerprint: "new-lockfile", PreparedAt: time.Date(2026, 8, 25, 11, 0, 0, 0, time.UTC),
	}

	if err := record.RecordWorkspaceReady(request); err == nil || !strings.Contains(err.Error(), "injected state failure") {
		t.Fatalf("ready error = %v, want injected state failure", err)
	}
	if !reflect.DeepEqual(detail, original) {
		t.Fatalf("failed ready mutation published postimage:\n got: %#v\nwant: %#v", detail, original)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); err != nil {
		t.Fatalf("pending journal missing: %v", err)
	}

	if err := record.RecordWorkspaceReady(request); err != nil {
		t.Fatalf("retry ready mutation: %v", err)
	}
	if detail.State.Workspace == nil || detail.State.Workspace.LifecycleStatus != WorkspaceStatusReady || detail.State.Workspace.DependencyFailure != "" || detail.State.Workspace.DependencyFingerprint != "new-lockfile" {
		t.Fatalf("retried ready workspace = %#v", detail.State.Workspace)
	}
	persisted := readStateFile(t, dir)
	if persisted.Workspace == nil || !reflect.DeepEqual(persisted.Workspace, detail.State.Workspace) {
		t.Fatalf("persisted retry workspace = %#v, in-memory = %#v", persisted.Workspace, detail.State.Workspace)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !os.IsNotExist(err) {
		t.Fatalf("settled retry journal remains: %v", err)
	}
}

func TestAdvanceWorkspaceHeadAdvancesExactBoundaryAndPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	detail.State.Workspace = &Workspace{Branch: "feature/plan-a", HeadSHA: "old-head", BaseSHA: "base-head"}
	writeStartSliceArtifacts(t, dir, detail)

	var raw map[string]any
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	raw["unknown_state_field"] = "keep"
	raw["workspace"].(map[string]any)["unknown_workspace_field"] = "keep"
	payload, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	slicesBefore, err := os.ReadFile(filepath.Join(dir, "slices.json")) // #nosec G304 -- path is under a test-owned temporary plan directory.
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord(dir, detail)

	if err := record.AdvanceWorkspaceHead("feature/plan-a", "old-head", "new-head"); err != nil {
		t.Fatal(err)
	}
	if detail.State.Workspace == nil || detail.State.Workspace.HeadSHA != "new-head" || detail.State.Workspace.BaseSHA != "base-head" {
		t.Fatalf("advanced workspace = %#v", detail.State.Workspace)
	}
	firstState, err := os.ReadFile(filepath.Join(dir, "state.json")) // #nosec G304 -- path is under a test-owned temporary plan directory.
	if err != nil {
		t.Fatal(err)
	}
	if err := record.AdvanceWorkspaceHead("feature/plan-a", "old-head", "new-head"); err != nil {
		t.Fatalf("exact postimage retry: %v", err)
	}
	secondState, err := os.ReadFile(filepath.Join(dir, "state.json")) // #nosec G304 -- path is under a test-owned temporary plan directory.
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(secondState, firstState) {
		t.Fatal("exact postimage retry rewrote state")
	}
	slicesAfter, err := os.ReadFile(filepath.Join(dir, "slices.json")) // #nosec G304 -- path is under a test-owned temporary plan directory.
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(slicesAfter, slicesBefore) {
		t.Fatal("workspace head advance rewrote slices")
	}
	readJSONFile(t, filepath.Join(dir, "state.json"), &raw)
	workspace := raw["workspace"].(map[string]any)
	if raw["unknown_state_field"] != "keep" || workspace["unknown_workspace_field"] != "keep" || workspace["head_sha"] != "new-head" {
		t.Fatalf("advanced state did not preserve unknown fields: %#v", raw)
	}
}

func TestAdvanceWorkspaceHeadRejectsInvalidBoundary(t *testing.T) {
	t.Run("missing expected branch", func(t *testing.T) {
		dir := t.TempDir()
		detail := startSliceDetail(dir)
		detail.State.Workspace = &Workspace{Branch: "feature/plan-a", HeadSHA: "old-head"}
		writeStartSliceArtifacts(t, dir, detail)
		err := testRecord(dir, detail).AdvanceWorkspaceHead("", "old-head", "new-head")
		if err == nil || !strings.Contains(err.Error(), "expected branch") {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("missing workspace", func(t *testing.T) {
		dir := t.TempDir()
		detail := startSliceDetail(dir)
		writeStartSliceArtifacts(t, dir, detail)
		err := testRecord(dir, detail).AdvanceWorkspaceHead("feature/plan-a", "old-head", "new-head")
		if err == nil || !strings.Contains(err.Error(), "no workspace") {
			t.Fatalf("error = %v", err)
		}
	})

	for _, test := range []struct {
		name           string
		branch, head   string
		expectedBranch string
		expectedHead   string
		newHead        string
		wantError      string
	}{
		{name: "branch conflict before idempotence", branch: "other", head: "new-head", expectedBranch: "feature/plan-a", expectedHead: "old-head", newHead: "new-head", wantError: "branch changed"},
		{name: "head conflict", branch: "feature/plan-a", head: "other-head", expectedBranch: "feature/plan-a", expectedHead: "old-head", newHead: "new-head", wantError: "head changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			detail := startSliceDetail(dir)
			detail.State.Workspace = &Workspace{Branch: test.branch, HeadSHA: test.head}
			writeStartSliceArtifacts(t, dir, detail)
			original := clonePlanDetail(detail)
			err := testRecord(dir, detail).AdvanceWorkspaceHead(test.expectedBranch, test.expectedHead, test.newHead)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			if !reflect.DeepEqual(detail, original) || readStateFile(t, dir).Workspace.HeadSHA != test.head {
				t.Fatalf("conflict mutated workspace: %#v", detail.State.Workspace)
			}
		})
	}
}

func TestAdvanceWorkspaceHeadUsesExactBoundedValues(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	detail.State.Workspace = &Workspace{Branch: " feature/plan-a ", HeadSHA: " old head "}
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	if err := record.AdvanceWorkspaceHead(" feature/plan-a ", " old head ", " new head "); err != nil {
		t.Fatalf("exact non-SHA boundary: %v", err)
	}
	if detail.State.Workspace.HeadSHA != " new head " {
		t.Fatalf("head was normalized to %q", detail.State.Workspace.HeadSHA)
	}
	oversized := strings.Repeat("x", maxWorkspaceHeadAdvanceValueBytes+1)
	for label, values := range map[string][3]string{
		"branch": {oversized, " new head ", "next"},
		"head":   {" feature/plan-a ", oversized, "next"},
		"new":    {" feature/plan-a ", " new head ", oversized},
	} {
		if err := record.AdvanceWorkspaceHead(values[0], values[1], values[2]); err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("oversized %s error = %v", label, err)
		}
	}
}

func TestAdvanceWorkspaceHeadRejectsStaleConcurrentBoundaryChange(t *testing.T) {
	dir := t.TempDir()
	initial := startSliceDetail(dir)
	initial.State.Workspace = &Workspace{Branch: "feature/plan-a", HeadSHA: "old-head"}
	writeStartSliceArtifacts(t, dir, initial)
	files, err := loadPlanFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	stale := detailFromFiles(files)
	concurrent := detailFromFiles(files)
	if err := testRecord(dir, concurrent).AdvanceWorkspaceHead("feature/plan-a", "old-head", "concurrent-head"); err != nil {
		t.Fatal(err)
	}

	err = testRecord(dir, stale).AdvanceWorkspaceHead("feature/plan-a", "old-head", "requested-head")
	if err == nil || !strings.Contains(err.Error(), "head changed") {
		t.Fatalf("stale advance error = %v", err)
	}
	if stale.State.Workspace.HeadSHA != "old-head" {
		t.Fatalf("conflicting postimage published to stale detail: %#v", stale.State.Workspace)
	}
	if got := readStateFile(t, dir).Workspace.HeadSHA; got != "concurrent-head" {
		t.Fatalf("stale advance replaced concurrent head with %q", got)
	}
}

func TestAdvanceWorkspaceHeadRetryRecoversJournalWithoutPublishingFailedPostimage(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	detail.State.Workspace = &Workspace{Branch: "feature/plan-a", HeadSHA: "old-head"}
	writeStartSliceArtifacts(t, dir, detail)
	original := clonePlanDetail(detail)
	ioStore := &failingMutationJournalIO{delegate: fileMutationJournalIO{}, failOperation: "state"}
	store := journalArtifactMutationStore{fileArtifactStore: fileArtifactStore{}, journalIO: ioStore}
	record, err := newPlanRecord(store, dir, detail)
	if err != nil {
		t.Fatal(err)
	}

	if err := record.AdvanceWorkspaceHead("feature/plan-a", "old-head", "new-head"); err == nil || !strings.Contains(err.Error(), "injected state failure") {
		t.Fatalf("advance error = %v, want injected state failure", err)
	}
	if !reflect.DeepEqual(detail, original) {
		t.Fatalf("failed advance published postimage:\n got: %#v\nwant: %#v", detail, original)
	}
	if got := readStateFile(t, dir).Workspace.HeadSHA; got != "old-head" {
		t.Fatalf("failed advance persisted head %q", got)
	}
	journalData, err := os.ReadFile(filepath.Join(dir, mutationJournalFile)) // #nosec G304 -- path is under a test-owned temporary plan directory.
	if err != nil {
		t.Fatalf("read pending journal: %v", err)
	}
	journal, err := decodeMutationJournal(journalData, detail.State.Plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if journal.State == nil || journal.Slices != nil || len(journal.Events) != 0 {
		t.Fatalf("head advance journal is not state-only: %#v", journal)
	}

	if err := record.AdvanceWorkspaceHead("feature/plan-a", "old-head", "new-head"); err != nil {
		t.Fatalf("recovered retry: %v", err)
	}
	if detail.State.Workspace.HeadSHA != "new-head" || readStateFile(t, dir).Workspace.HeadSHA != "new-head" {
		t.Fatalf("recovered retry workspace = %#v", detail.State.Workspace)
	}
	if _, err := os.Stat(filepath.Join(dir, mutationJournalFile)); !os.IsNotExist(err) {
		t.Fatalf("settled retry journal remains: %v", err)
	}
}

func TestRecordPRFeedbackTriageWritesStateAndEvent(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	triagedAt := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	result := PRFeedbackTriageResult{
		"PRRT_change":   {Kind: "change", Rationale: "Requests a concrete correction."},
		"PRRT_question": {Kind: "question", Rationale: "Asks about the chosen behavior."},
	}

	if err := record.RecordPRFeedbackTriage(result, triagedAt); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackTriage, result) {
		t.Fatalf("persisted triage = %#v, want %#v", state.Plan.PRFeedbackTriage, result)
	}
	if !state.UpdatedAt.Equal(triagedAt) || state.Plan.Timing.LastActivityAt == nil || !state.Plan.Timing.LastActivityAt.Equal(triagedAt) {
		t.Fatalf("triage activity timestamps = updated %v, last activity %v", state.UpdatedAt, state.Plan.Timing.LastActivityAt)
	}
	events := readRecordTestEvents(t, dir)
	event := requireRecordTestTriageEvents(t, events, 1)[0]
	if event.MutationID == "" || !event.Timestamp.Equal(triagedAt) || !reflect.DeepEqual(event.PRFeedbackTriage, result) {
		t.Fatalf("triage event = %#v", event)
	}
}

func TestRecordPRFeedbackTriageIsIdempotentForUnchangedThreadSet(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	firstAt := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	first := PRFeedbackTriageResult{"PRRT_1": {Kind: "change", Rationale: "Keep the binding."}}
	if err := record.RecordPRFeedbackTriage(first, firstAt); err != nil {
		t.Fatal(err)
	}

	reclassified := PRFeedbackTriageResult{"PRRT_1": {Kind: "question", Rationale: "A later agent answered differently."}}
	if err := record.RecordPRFeedbackTriage(reclassified, firstAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackTriage, first) {
		t.Fatalf("idempotent triage = %#v, want original %#v", state.Plan.PRFeedbackTriage, first)
	}
	if !state.UpdatedAt.Equal(firstAt) {
		t.Fatalf("idempotent repeat updated timestamp to %v, want %v", state.UpdatedAt, firstAt)
	}
	requireRecordTestTriageEvents(t, readRecordTestEvents(t, dir), 1)
}

func TestRecordPRFeedbackTriageSupersedesChangedThreadSet(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	firstAt := time.Date(2026, 8, 13, 16, 0, 0, 0, time.UTC)
	if err := record.RecordPRFeedbackTriage(PRFeedbackTriageResult{
		"PRRT_1": {Kind: "change", Rationale: "First request."},
	}, firstAt); err != nil {
		t.Fatal(err)
	}
	secondAt := firstAt.Add(time.Hour)
	superseding := PRFeedbackTriageResult{
		"PRRT_1": {Kind: "change", Rationale: "First request."},
		"PRRT_2": {Kind: "scope", Rationale: "New unrelated request."},
	}
	if err := record.RecordPRFeedbackTriage(superseding, secondAt); err != nil {
		t.Fatal(err)
	}

	state := readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackTriage, superseding) {
		t.Fatalf("superseding triage = %#v, want %#v", state.Plan.PRFeedbackTriage, superseding)
	}
	events := requireRecordTestTriageEvents(t, readRecordTestEvents(t, dir), 2)
	latest := events[len(events)-1]
	if !latest.Timestamp.Equal(secondAt) || !reflect.DeepEqual(latest.PRFeedbackTriage, superseding) {
		t.Fatalf("superseding triage event = %#v", latest)
	}
}

func TestReopenFromPullRequestConsumesThreadAcrossCompletedPRCycle(t *testing.T) {
	dir := t.TempDir()
	detail := completedReopenDetail()
	detail.State.Plan.PullRequest = &PullRequest{Number: 17, URL: "https://github.com/owner/repo/pull/17", CreatedAt: detail.State.UpdatedAt, Branch: "feature", HeadSHA: "old-head"}
	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "old-head", ReviewedAt: detail.State.UpdatedAt}
	detail.State.Plan.PRFeedbackTriage = PRFeedbackTriageResult{
		"PRRT_change":   {Kind: "change", Rationale: "Requests a lifecycle fix."},
		"PRRT_question": {Kind: "question", Rationale: "Asks why this is needed."},
	}
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	reopenedAt := time.Date(2026, 8, 13, 17, 0, 0, 0, time.UTC)
	newSlices := []Slice{newReopenSlice("002-pr-fix", "Fix pull request feedback", reopenedAt)}

	if err := record.ReopenFromPullRequest(newSlices, []string{"PRRT_change"}, reopenedAt); err != nil {
		t.Fatal(err)
	}
	// Retrying the same atomic transaction is a no-op rather than a duplicate
	// consumption or reopen.
	if err := record.ReopenFromPullRequest(newSlices, []string{"PRRT_change"}, reopenedAt); err != nil {
		t.Fatalf("retry atomic reopen: %v", err)
	}

	state := readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackConsumedThreadIDs, []string{"PRRT_change"}) {
		t.Fatalf("consumed thread IDs = %#v, want PRRT_change", state.Plan.PRFeedbackConsumedThreadIDs)
	}
	if state.Status != StatusInProgress || !reflect.DeepEqual(state.Plan.PendingSlices, []string{"002-pr-fix"}) {
		t.Fatalf("reopened state = status %q pending %v", state.Status, state.Plan.PendingSlices)
	}
	var reopenEvents []Event
	for _, event := range readRecordTestEvents(t, dir) {
		if event.Type == EventTypePlanReopened {
			reopenEvents = append(reopenEvents, event)
		}
	}
	if len(reopenEvents) != 1 || reopenEvents[0].MutationID == "" || !reopenEvents[0].Timestamp.Equal(reopenedAt) {
		t.Fatalf("reopen events = %#v", reopenEvents)
	}

	// Complete the generated work, approve its new head, and refresh the
	// recorded pull request just as a full pull-request rework cycle does.
	startedAt := reopenedAt.Add(time.Minute)
	if err := record.StartSlice("002-pr-fix", startedAt); err != nil {
		t.Fatal(err)
	}
	completedAt := startedAt.Add(time.Minute)
	if err := record.CompleteSlice("002-pr-fix", "fixed", nil, completedAt); err != nil {
		t.Fatal(err)
	}
	reviewedAt := completedAt.Add(time.Minute)
	if err := record.RecordReviewCompleted(PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "new-head", ReviewedAt: reviewedAt}, "pi"); err != nil {
		t.Fatal(err)
	}
	prAt := reviewedAt.Add(time.Minute)
	if err := record.RecordPullRequest(PullRequest{Number: 17, URL: "https://github.com/owner/repo/pull/17", CreatedAt: prAt}, "feature", "new-head"); err != nil {
		t.Fatal(err)
	}
	if record.Detail().State.Status != StatusCompleted {
		t.Fatalf("completed pull-request cycle status = %q, want completed", record.Detail().State.Status)
	}

	// The same unresolved thread remains consumed while a newly arrived thread
	// can be triaged and converted in a later invocation.
	refreshed := PRFeedbackTriageResult{
		"PRRT_change": {Kind: "change", Rationale: "Requests a lifecycle fix."},
		"PRRT_new":    {Kind: "change", Rationale: "Requests a newly arrived fix."},
	}
	if err := record.RecordPRFeedbackTriage(refreshed, prAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	beforeSlices := len(record.Detail().Slices.Slices)
	err := record.ReopenFromPullRequest([]Slice{newReopenSlice("003-duplicate", "Duplicate pull request feedback", prAt.Add(2*time.Minute))}, []string{"PRRT_change"}, prAt.Add(2*time.Minute))
	if err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("second conversion error = %v, want consumed-thread refusal", err)
	}
	if len(record.Detail().Slices.Slices) != beforeSlices || record.Detail().State.Status != StatusCompleted {
		t.Fatal("consumed-thread refusal mutated the completed plan")
	}

	if err := record.ReopenFromPullRequest([]Slice{newReopenSlice("003-new", "Fix new pull request feedback", prAt.Add(3*time.Minute))}, []string{"PRRT_new"}, prAt.Add(3*time.Minute)); err != nil {
		t.Fatalf("reopen for newly arrived thread: %v", err)
	}
	state = readStateFile(t, dir)
	if !reflect.DeepEqual(state.Plan.PRFeedbackConsumedThreadIDs, []string{"PRRT_change", "PRRT_new"}) {
		t.Fatalf("consumed thread IDs after new thread = %#v", state.Plan.PRFeedbackConsumedThreadIDs)
	}
}

func TestReopenFromPullRequestRejectsUntriagedThreadWithoutMutation(t *testing.T) {
	dir := t.TempDir()
	detail := completedReopenDetail()
	detail.State.Plan.PRFeedbackTriage = PRFeedbackTriageResult{
		"PRRT_change": {Kind: "change", Rationale: "Requests a lifecycle fix."},
	}
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	beforeState, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // test-controlled temporary plan path
	if err != nil {
		t.Fatal(err)
	}
	beforeSlices, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // test-controlled temporary plan path
	if err != nil {
		t.Fatal(err)
	}
	reopenedAt := time.Date(2026, 8, 13, 17, 0, 0, 0, time.UTC)

	err = record.ReopenFromPullRequest(
		[]Slice{newReopenSlice("002-pr-fix", "Fix pull request feedback", reopenedAt)},
		[]string{"PRRT_change", "PRRT_missing"},
		reopenedAt,
	)
	if err == nil || !strings.Contains(err.Error(), "PRRT_missing") {
		t.Fatalf("reopen error = %v, want missing triage refusal", err)
	}
	afterState, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // test-controlled temporary plan path
	if err != nil {
		t.Fatal(err)
	}
	afterSlices, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // test-controlled temporary plan path
	if err != nil {
		t.Fatal(err)
	}
	if string(afterState) != string(beforeState) || string(afterSlices) != string(beforeSlices) {
		t.Fatal("refused pull-request reopen changed persisted artifacts")
	}
	if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("refused pull-request reopen created events: %v", err)
	}
}

func TestLegacyPlanStateOmitsAbsentPRFeedbackTriage(t *testing.T) {
	var state State
	if err := json.Unmarshal([]byte(`{"schema":"tao.plan.state.v1","plan":{"id":"legacy"}}`), &state); err != nil {
		t.Fatal(err)
	}
	if state.Plan.PRFeedbackTriage != nil || state.Plan.PRFeedbackConsumedThreadIDs != nil {
		t.Fatalf("legacy pull-request feedback state = triage %#v consumed %#v, want nil", state.Plan.PRFeedbackTriage, state.Plan.PRFeedbackConsumedThreadIDs)
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "pr_feedback_triage") || strings.Contains(string(encoded), "pr_feedback_consumed_thread_ids") {
		t.Fatalf("legacy state unexpectedly added pull-request feedback fields: %s", encoded)
	}
}

func readRecordTestEvents(t *testing.T, dir string) []Event {
	t.Helper()
	events, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("read event warnings: %v", warnings)
	}
	return events
}

func requireRecordTestTriageEvents(t *testing.T, events []Event, want int) []Event {
	t.Helper()
	var triageEvents []Event
	for _, event := range events {
		if event.Type == EventTypePRFeedbackTriaged {
			triageEvents = append(triageEvents, event)
		}
	}
	if len(triageEvents) != want {
		t.Fatalf("pr_feedback_triaged events = %d, want %d: %#v", len(triageEvents), want, events)
	}
	return triageEvents
}

func TestRecordReviewRejectsUnsettledWorkWithoutMutation(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*PlanRecord, PlanReview) error
	}{
		{
			name: "completed",
			apply: func(record *PlanRecord, review PlanReview) error {
				return record.RecordReviewCompleted(review, "pi")
			},
		},
		{
			name: "error",
			apply: func(record *PlanRecord, review PlanReview) error {
				return record.RecordReviewError(review, "pi")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			detail := startSliceDetail(dir)
			current := "001-a"
			detail.State.Status = StatusInProgress
			detail.State.Plan.CurrentSlice = &current
			detail.Slices.Slices[0].Status = StatusInProgress
			detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested, Summary: "keep this actionable review"}
			detail.State.Plan.MergeCommitIntent = &SingleMergeCommitIntent{PlanID: "plan-a", SourceHead: "old-head", DefaultBranch: "main", DefaultParent: "base", Message: "fix(plan): old\n\nWhat:\nKeep it.\n\nWhy:\nIt is reviewed.", CreatedAt: time.Date(2026, 8, 5, 3, 0, 0, 0, time.UTC)}
			writeStartSliceArtifacts(t, dir, detail)
			record := testRecord(dir, detail)
			beforeDetail := clonePlanDetail(detail)
			beforeState, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // G304: test-controlled temporary plan path
			if err != nil {
				t.Fatal(err)
			}
			beforeSlices, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // G304: test-controlled temporary plan path
			if err != nil {
				t.Fatal(err)
			}

			review := PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "replacement", Head: "new-head", ReviewedAt: time.Date(2026, 8, 5, 4, 0, 0, 0, time.UTC)}
			err = tt.apply(record, review)
			if err == nil || !strings.Contains(err.Error(), "001-a") || !strings.Contains(err.Error(), "tao run plan-a") {
				t.Fatalf("review record error = %v, want actionable unsettled-work refusal", err)
			}
			if !reflect.DeepEqual(detail, beforeDetail) {
				t.Fatalf("refused review changed in-memory detail:\n got: %#v\nwant: %#v", detail, beforeDetail)
			}
			afterState, err := os.ReadFile(filepath.Join(dir, "state.json")) //nolint:gosec // G304: test-controlled temporary plan path
			if err != nil {
				t.Fatal(err)
			}
			afterSlices, err := os.ReadFile(filepath.Join(dir, "slices.json")) //nolint:gosec // G304: test-controlled temporary plan path
			if err != nil {
				t.Fatal(err)
			}
			if string(afterState) != string(beforeState) || string(afterSlices) != string(beforeSlices) {
				t.Fatal("refused review changed persisted plan artifacts")
			}
			if _, err := os.Stat(filepath.Join(dir, "events.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("refused review created an event artifact: %v", err)
			}
		})
	}
}
