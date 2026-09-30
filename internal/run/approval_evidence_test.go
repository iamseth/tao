package run

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plantest"
)

func requireApprovalFacts(slice *plan.Slice) {
	slice.Approval = &plan.Approval{Required: true, Approved: true, Reason: "Approval must include factual observations."}
}

func assertApprovalEvidenceRefusal(t *testing.T, err error) {
	t.Helper()
	var evidence *plan.ApprovalEvidenceError
	if !errors.Is(err, ErrCannotStart) || !errors.As(err, &evidence) {
		t.Fatalf("error = %v, want typed approval evidence refusal", err)
	}
}

func TestApprovalEvidenceRefusesBeforeStart(t *testing.T) {
	detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
	detail.State.Repo.Root = t.TempDir()
	requireApprovalFacts(&detail.Slices.Slices[0])
	executor := &countingSliceExecutor{}
	var calls []string
	err := executeDetail(context.Background(), detail, func(context.Context, *plan.PlanDetail) (*plan.PlanDetail, error) {
		return nil, errors.New("unexpected provider handoff")
	}, io.Discard, testOptions(testDependencies(executor, runGitFake(&calls, nil))))
	assertApprovalEvidenceRefusal(t, err)
	if executor.calls != 0 || detail.Slices.Slices[0].Status != plan.StatusPending {
		t.Fatal("refusal launched or started slice")
	}
	assertNoApprovalAdmissionEvents(t, detail.Events)
}

func assertNoApprovalAdmissionEvents(t *testing.T, events []plan.Event) {
	t.Helper()
	for _, event := range events {
		switch event.Type {
		case plan.EventTypeSliceStarted, plan.EventTypeSliceResumeAttempted, plan.EventTypeSliceRestarted:
			t.Fatalf("unexpected admission event: %#v", event)
		}
	}
}

func TestApprovalEvidenceContinueAndResume(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "continue"}[blocked], func(t *testing.T) {
			root := t.TempDir()
			detail := interruptedServiceRunDetail(t, root)
			requireApprovalFacts(&detail.Slices.Slices[0])
			if blocked {
				detail.State.Status = plan.StatusBlocked
				detail.Slices.Slices[0].Status = plan.StatusBlocked
				detail.Slices.Slices[0].BlockerNote = "missing observations"
			}
			originalStatus := detail.Slices.Slices[0].Status
			originalEvents := append([]plan.Event(nil), detail.Events...)
			providerErr := errors.New("ordinary handoff")
			calls := 0
			var events []plan.Event
			service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
				CommandRunner:     interruptedServiceGitRunner(t, root, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
				PlanRecordFactory: memoryPlanRecordFactory,
				EventAppender:     eventAppenderFunc(func(_ string, e plan.Event) error { events = append(events, e); return nil }),
				SliceExecutor:     sliceExecutorFunc(func(context.Context, SliceRun) error { calls++; return providerErr }),
			}})
			request := Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{Continue: blocked, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}}
			for range 2 {
				assertApprovalEvidenceRefusal(t, service.Execute(context.Background(), request))
				if detail.Slices.Slices[0].Status != originalStatus || (blocked && detail.Slices.Slices[0].BlockerNote != "missing observations") {
					t.Fatal("refusal cleared blocked state")
				}
				assertNoApprovalAdmissionEvents(t, events)
				for _, kind := range []string{plan.EventTypeSliceStarted, plan.EventTypeSliceResumeAttempted, plan.EventTypeSliceRestarted} {
					if countPlanEvents(detail.Events, kind) != countPlanEvents(originalEvents, kind) {
						t.Fatalf("new %s event", kind)
					}
				}
				// Reapproval does not provide facts or remediate the contract.
				record, err := persistingPlanRecord(plantest.NewPersistingRepository(), detail)
				if err != nil {
					t.Fatal(err)
				}
				if err := record.ApproveSlice("001-a", "operator", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			if calls != 0 {
				t.Fatalf("provider calls = %d", calls)
			}
			if blocked {
				record, err := persistingPlanRecord(plantest.NewPersistingRepository(), detail)
				if err != nil {
					t.Fatal(err)
				}
				if err := record.AmendSlice("001-a", plan.SliceAmendmentRequest{Reason: "operator supplies observations", AddTasks: []string{"Use the observed production result: both requests succeeded."}}, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			} else {
				// Amendments may be recorded before this in-progress boundary.
				detail.Slices.Slices[0].Amendments = []plan.SliceAmendment{{Fields: []string{"goal"}}}
			}
			if err := service.Execute(context.Background(), request); !errors.Is(err, providerErr) {
				t.Fatalf("remediated admission: %v", err)
			}
			if calls != 1 {
				t.Fatalf("remediated calls = %d", calls)
			}
		})
	}
}

// Keep this symptom regression compatible with the pre-guard API so it can
// reproduce provider admission and blocked-state mutation on the parent commit.
func TestApprovalEvidenceAdmissionBaselineRegression(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "continue"}[blocked], func(t *testing.T) {
			root := t.TempDir()
			detail := interruptedServiceRunDetail(t, root)
			slice := &detail.Slices.Slices[0]
			slice.Approval = &plan.Approval{Required: true, Approved: true, Reason: "Approval must include factual observations."}
			if blocked {
				detail.State.Status = plan.StatusBlocked
				slice.Status = plan.StatusBlocked
				slice.BlockerNote = "missing observations"
			}
			originalPlanStatus, originalSliceStatus, originalBlocker := detail.State.Status, slice.Status, slice.BlockerNote
			originalEvents := append([]plan.Event(nil), detail.Events...)
			providerErr := errors.New("provider admitted without observations")
			calls := 0
			var events []plan.Event
			service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
				CommandRunner:     interruptedServiceGitRunner(t, root, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
				PlanRecordFactory: memoryPlanRecordFactory,
				EventAppender:     eventAppenderFunc(func(_ string, e plan.Event) error { events = append(events, e); return nil }),
				SliceExecutor:     sliceExecutorFunc(func(context.Context, SliceRun) error { calls++; return providerErr }),
			}})
			err := service.Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{
				Continue: blocked, ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice,
			}})
			if !errors.Is(err, ErrCannotStart) {
				t.Errorf("error = %v, want admission refusal", err)
			}
			if calls != 0 {
				t.Errorf("provider calls = %d, want 0", calls)
			}
			if detail.State.Status != originalPlanStatus || slice.Status != originalSliceStatus || slice.BlockerNote != originalBlocker {
				t.Errorf("admission mutated state: plan=%s slice=%s blocker=%q; want plan=%s slice=%s blocker=%q",
					detail.State.Status, slice.Status, slice.BlockerNote, originalPlanStatus, originalSliceStatus, originalBlocker)
			}
			for _, kind := range []string{plan.EventTypeSliceStarted, plan.EventTypeSliceResumeAttempted, plan.EventTypeSliceRestarted} {
				if countPlanEvents(events, kind) != 0 || countPlanEvents(detail.Events, kind) != countPlanEvents(originalEvents, kind) {
					t.Errorf("admission recorded new %s event", kind)
				}
			}
		})
	}
}

func TestApprovalEvidenceTransportReload(t *testing.T) {
	root := t.TempDir()
	initial := interruptedServiceRunDetail(t, root)
	reloaded := cloneRunRestartDetail(t, initial)
	requireApprovalFacts(&reloaded.Slices.Slices[0])
	calls := 0
	var events []plan.Event
	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{initial, initial, reloaded}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner:       interruptedServiceGitRunner(t, root, &[]string{}, func() string { return "" }, "tao/plan-a", "base"),
		EventAppender:       eventAppenderFunc(func(_ string, e plan.Event) error { events = append(events, e); return nil }),
		TransportRetryDelay: func(context.Context, time.Duration) error { return nil },
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error {
			calls++
			return retryableTransportTestError{errors.New("transport dropped")}
		}),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	assertApprovalEvidenceRefusal(t, err)
	if calls != 1 || countPlanEvents(events, plan.EventTypeSliceResumeAttempted) != 1 {
		t.Fatalf("calls=%d resume events=%d", calls, countPlanEvents(events, plan.EventTypeSliceResumeAttempted))
	}
}

func TestApprovalEvidenceSelectedSliceIsolation(t *testing.T) {
	for _, maxSlices := range []int{0, 1} {
		t.Run(strconv.Itoa(maxSlices), func(t *testing.T) {
			root := t.TempDir()
			first := runPlanDetail(plan.StatusPlanned, []string{"001-a", "002-b"}, nil, "001-a", plan.StatusPending, nil, nil)
			first.State.Repo.Root = root
			later := plan.Slice{ID: "002-b", Status: plan.StatusPending, Verification: plan.Verification{Commands: []string{"go test ."}}}
			requireApprovalFacts(&later)
			first.Slices.Slices = append(first.Slices.Slices, later)
			next := runPlanDetail(plan.StatusInProgress, []string{"002-b"}, []string{"001-a"}, "001-a", plan.StatusCompleted, nil, nil)
			next.State.Repo.Root = root
			next.Slices.Slices = append(next.Slices.Slices, later)
			calls := 0
			options := testOptions(testDependencies(sliceExecutorFunc(func(_ context.Context, run SliceRun) error {
				calls++
				if run.SliceID != "001-a" {
					t.Fatal("launched guarded later slice")
				}
				return nil
			}), runGitFake(&[]string{}, nil)))
			options.MaxSlices = maxSlices
			err := executeDetail(context.Background(), first, func(context.Context, *plan.PlanDetail) (*plan.PlanDetail, error) { return next, nil }, io.Discard, options)
			if maxSlices == 0 {
				assertApprovalEvidenceRefusal(t, err)
			} else if err != nil {
				t.Fatalf("max-slices must stop without admitting next slice: %v", err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want selected first slice only", calls)
			}
			assertNoApprovalAdmissionEvents(t, next.Events)
		})
	}
}

func TestApprovalEvidenceRequiredInputExecutionRoot(t *testing.T) {
	for _, kind := range []string{"missing", "directory", "invalid declaration", "file"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			detail := runPlanDetail(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending, nil, nil)
			detail.State.Repo.Root = t.TempDir()
			if err := os.WriteFile(filepath.Join(detail.State.Repo.Root, "facts.md"), []byte("not execution-root evidence"), 0o600); err != nil {
				t.Fatal(err)
			}
			slice := &detail.Slices.Slices[0]
			requireApprovalFacts(slice)
			slice.RequiredInputs = []plan.RequiredInput{{Path: "facts.md", Kind: plan.RequiredInputFile, Reason: "operator observations"}}
			switch kind {
			case "directory":
				if err := os.Mkdir(filepath.Join(root, "facts.md"), 0o700); err != nil {
					t.Fatal(err)
				}
			case "invalid declaration":
				slice.RequiredInputs[0].Kind = "unknown"
			case "file":
				if err := os.WriteFile(filepath.Join(root, "facts.md"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			handoff := errors.New("ordinary admission")
			var out bytes.Buffer
			err := executeDetail(context.Background(), detail, nil, &out, testOptions(testDependencies(sliceExecutorFunc(func(context.Context, SliceRun) error { calls++; return handoff }), runGitFake(&[]string{}, nil), func(d *RunDependencies) {
				d.WorkspacePreparer = func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) { return root, nil }
			}), func(o *Options) { o.ExecutionMode = ExecutionModeIsolated }))
			if kind == "file" {
				if !errors.Is(err, handoff) || calls != 1 {
					t.Fatalf("ordinary input route: %v, calls=%d", err, calls)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "failed verification preflight") || calls != 0 {
				t.Fatalf("preflight = %v, calls=%d, output=%s", err, calls, out.String())
			}
			assertNoApprovalAdmissionEvents(t, detail.Events)
		})
	}
}

func TestApprovalEvidencePreservesPostIntentRoute(t *testing.T) {
	detail := interruptedServiceRunDetail(t, t.TempDir())
	requireApprovalFacts(&detail.Slices.Slices[0])
	detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Hash: "intent", Policy: "slice"}
	calls := 0
	err := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner: func(context.Context, string, string, []string, io.Writer, io.Writer) error {
			t.Fatal("post-intent route inspected Git")
			return nil
		},
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error { calls++; return nil }),
	}}).Execute(context.Background(), Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}})
	var evidence *plan.ApprovalEvidenceError
	if err == nil || errors.As(err, &evidence) || !strings.Contains(err.Error(), "rerun tao slice-complete") || calls != 0 {
		t.Fatalf("post-intent route: %v, calls=%d", err, calls)
	}
}

func TestApprovalEvidenceRefusesBeforeBlockedRestart(t *testing.T) {
	repoRoot := t.TempDir()
	runRebaseRecoveryGit(t, repoRoot, "init", "-b", "main")
	disableRunGitMaintenance(t, repoRoot)
	runRebaseRecoveryGit(t, repoRoot, "config", "user.email", "tao@example.com")
	runRebaseRecoveryGit(t, repoRoot, "config", "user.name", "Tao Test")
	runRebaseRecoveryGit(t, repoRoot, "commit", "--allow-empty", "-m", "base")
	oldHead := rebaseRecoveryGitOutput(t, repoRoot, "rev-parse", "HEAD")
	root := filepath.Join(t.TempDir(), "worktree")
	runRebaseRecoveryGit(t, repoRoot, "worktree", "add", "-b", "tao/plan-a", root, oldHead)
	runRebaseRecoveryGit(t, repoRoot, "commit", "--allow-empty", "-m", "new baseline")
	detail := interruptedServiceRunDetail(t, root)
	detail.State.Repo.Root = repoRoot
	detail.State.Repo.Branch = "main"
	detail.State.Workspace.HeadSHA = oldHead
	detail.State.Status = plan.StatusBlocked
	slice := &detail.Slices.Slices[0]
	slice.Status = plan.StatusBlocked
	slice.BlockerNote = "waiting on observations"
	slice.ExecutionStart.Head = oldHead
	requireApprovalFacts(slice)
	before := cloneRunRestartDetail(t, detail)
	calls := 0
	prepared := 0
	var events []plan.Event
	service := NewService(&memoryRunRepository{details: []*plan.PlanDetail{detail}}, io.Discard, Options{RunDependencies: RunDependencies{
		CommandRunner:     defaultCommandRunner,
		PlanRecordFactory: memoryPlanRecordFactory,
		EventAppender:     eventAppenderFunc(func(_ string, e plan.Event) error { events = append(events, e); return nil }),
		WorkspacePreparer: func(context.Context, *plan.PlanDetail, WorkspaceResolverInput) (string, error) {
			prepared++
			return root, nil
		},
		SliceExecutor: sliceExecutorFunc(func(context.Context, SliceRun) error { calls++; return errors.New("unexpected handoff") }),
	}})
	request := Request{Input: "plan-a", ResolvedRunOptions: ResolvedRunOptions{ExecutionMode: ExecutionModeIsolated, CommitPolicy: CommitPolicySlice}, RecoveryMode: RecoveryMode{RestartBlocked: true}}
	for range 2 {
		assertApprovalEvidenceRefusal(t, service.Execute(context.Background(), request))
	}
	if calls != 0 || prepared != 0 || slice.Status != plan.StatusBlocked || slice.BlockerNote != before.Slices.Slices[0].BlockerNote || *slice.ExecutionStart != *before.Slices.Slices[0].ExecutionStart {
		t.Fatalf("restart mutated state: calls=%d prepared=%d slice=%#v", calls, prepared, slice)
	}
	assertNoApprovalAdmissionEvents(t, events)
	for _, kind := range []string{plan.EventTypeSliceStarted, plan.EventTypeSliceResumeAttempted, plan.EventTypeSliceRestarted} {
		if countPlanEvents(detail.Events, kind) != countPlanEvents(before.Events, kind) {
			t.Fatalf("new %s event", kind)
		}
	}
}

func TestApprovalEvidenceDoesNotGateExactIntentRecovery(t *testing.T) {
	root := initSliceCompletionRepo(t)
	detail, record := sliceCompletionRecord(t, root, CommitPolicySlice, &sliceCompletionStore{})
	requireApprovalFacts(&detail.Slices.Slices[0])
	parent := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, "recovery.go"), []byte("package recovery\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	const message = "historical exact message\n\nKeep this message verbatim."
	const notes = "recover frozen intent"
	hash, err := legacySliceCompletionHash("plan-a", "001-a", CommitPolicySlice.String(), notes, nil)
	if err != nil {
		t.Fatal(err)
	}
	detail.Slices.Slices[0].CommitIntent = &plan.SliceCommitIntent{Hash: hash, Policy: CommitPolicySlice.String(), StartingBranch: "tao/test", StartingHead: parent, Message: message, CreatedAt: time.Now().UTC()}
	request := SliceCompletionRequest{Record: record, SliceID: "001-a", Notes: notes, Now: time.Now().UTC()}
	if err := (SliceCompletionService{}).settleHistoricalCompletion(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "log", "-1", "--format=%B")); got != message {
		t.Fatalf("recovered message = %q", got)
	}
	if got := strings.TrimSpace(runCommitTestGitOutput(t, root, "rev-parse", "HEAD^")); got != parent {
		t.Fatalf("recovered parent = %q", got)
	}
	if completion := detail.Slices.Slices[0].Completion; completion == nil || completion.Outcome != plan.SliceCompletionCommitted {
		t.Fatalf("completion = %#v", completion)
	}
}
