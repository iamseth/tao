package runqueue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

type recordingEntryDriverHost struct {
	transitions []EntryTransition
	err         error
}

func (h *recordingEntryDriverHost) TransitionEntry(_ context.Context, transition EntryTransition) error {
	h.transitions = append(h.transitions, transition)
	return h.err
}

func TestEntryDriverConfigurationErrors(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	entry := QueueEntry{PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: now}
	execute := func(context.Context, run.Request) error { return nil }
	host := &recordingEntryDriverHost{}

	tests := []struct {
		name   string
		driver EntryDriver
		want   string
	}{
		{name: "missing host", driver: EntryDriver{Execute: execute, Now: func() time.Time { return now }}, want: "requires host"},
		{name: "missing executor", driver: EntryDriver{Host: host, Now: func() time.Time { return now }}, want: "requires executor"},
		{name: "missing clock", driver: EntryDriver{Host: host, Execute: execute}, want: "requires clock"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.driver.Drive(context.Background(), entry); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Drive() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestEntryDriverAppliesOutcomes(t *testing.T) {
	started := time.Date(2026, 7, 21, 11, 0, 0, 0, time.UTC)
	finished := started.Add(time.Hour)
	failure := errors.New("run failed")

	tests := []struct {
		name             string
		result           EntryResult
		wantStatus       QueueStatus
		wantError        string
		wantSkipReason   string
		wantWaitReason   string
		wantFinished     bool
		wantRecovery     bool
		wantTransitioned bool
	}{
		{name: "succeeded", result: EntryResult{Outcome: EntryOutcomeSucceeded}, wantStatus: QueueStatusSucceeded, wantFinished: true, wantTransitioned: true},
		{name: "failed", result: EntryResult{Outcome: EntryOutcomeFailed, Err: failure}, wantStatus: QueueStatusFailed, wantError: failure.Error(), wantFinished: true, wantTransitioned: true},
		{name: "skipped", result: EntryResult{Outcome: EntryOutcomeSkipped, Reason: "no longer runnable"}, wantStatus: QueueStatusSkipped, wantSkipReason: "no longer runnable", wantFinished: true, wantTransitioned: true},
		{name: "waiting", result: EntryResult{Outcome: EntryOutcomeWaiting, Reason: "conflicts with plan-b"}, wantStatus: QueueStatusPending, wantWaitReason: "conflicts with plan-b", wantRecovery: true, wantTransitioned: true},
		{name: "ready", result: EntryResult{Outcome: EntryOutcomeReady}, wantStatus: QueueStatusRunning, wantRecovery: true},
		{name: "requeued after stop", result: EntryResult{Outcome: EntryOutcomeRequeuedAfterStop}, wantStatus: QueueStatusPending, wantRecovery: true, wantTransitioned: true},
		{name: "retained running", result: EntryResult{Outcome: EntryOutcomeRetainedRunning}, wantStatus: QueueStatusRunning, wantRecovery: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := &recordingEntryDriverHost{}
			driver := EntryDriver{Host: host, Now: func() time.Time { return finished }}
			entry := QueueEntry{
				PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: started.Add(-time.Minute), StartedAt: &started,
				Error: "old error", SkipReason: "old skip", WaitReason: "old wait", RecoveryPending: true,
			}

			if err := driver.ApplyResult(context.Background(), entry, tt.result); err != nil {
				t.Fatal(err)
			}
			if !tt.wantTransitioned {
				if len(host.transitions) != 0 {
					t.Fatalf("retained result unexpectedly transitioned entry: %+v", host.transitions)
				}
				if entry.Status != tt.wantStatus {
					t.Fatalf("caller entry mutated to %s", entry.Status)
				}
				return
			}
			if len(host.transitions) != 1 {
				t.Fatalf("host transitions = %d, want 1", len(host.transitions))
			}
			got := host.transitions[0].After
			if got.Status != tt.wantStatus || got.Error != tt.wantError || got.SkipReason != tt.wantSkipReason || got.WaitReason != tt.wantWaitReason {
				t.Fatalf("applied entry = %+v", got)
			}
			if (got.FinishedAt != nil) != tt.wantFinished {
				t.Fatalf("FinishedAt = %v, want present %v", got.FinishedAt, tt.wantFinished)
			}
			if got.FinishedAt != nil && !got.FinishedAt.Equal(finished) {
				t.Fatalf("FinishedAt = %v, want %v", *got.FinishedAt, finished)
			}
			if got.RecoveryPending != tt.wantRecovery {
				t.Fatalf("RecoveryPending = %v, want %v", got.RecoveryPending, tt.wantRecovery)
			}
			if got.Status == QueueStatusPending && got.StartedAt != nil {
				t.Fatalf("pending result retained StartedAt: %v", got.StartedAt)
			}
			if entry.Status != QueueStatusRunning || entry.Error != "old error" {
				t.Fatalf("driver mutated caller-owned entry: %+v", entry)
			}
		})
	}
}

func TestEntryDriverPreparesPendingEntryBeforeClaim(t *testing.T) {
	now := time.Date(2026, 7, 21, 11, 30, 0, 0, time.UTC)
	host := &recordingEntryDriverHost{}
	validated := false
	driver := EntryDriver{
		Host: host,
		InspectRecovery: func(context.Context, string) (RecoveryInspection, error) {
			return RecoveryInspection{ReworkRound: 3}, nil
		},
		Validate: func(_ context.Context, request run.Request) error {
			validated = request.Input == "plan-a" && len(host.transitions) == 1
			return nil
		},
		PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
			return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 2}
		},
		Now: func() time.Time { return now },
	}
	entry := QueueEntry{PlanID: "plan-a", Status: QueueStatusPending, QueuedAt: now, request: run.Request{Input: "plan-a"}}

	preparation, err := driver.Prepare(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Result.Outcome != EntryOutcomeReady || !validated {
		t.Fatalf("Prepare() = (%+v, validated %v), want ready after validation", preparation.Result, validated)
	}
	if preparation.Entry.Status != QueueStatusPending || preparation.Entry.ReworkBaselineRound == nil || *preparation.Entry.ReworkBaselineRound != 3 {
		t.Fatalf("prepared entry = %+v", preparation.Entry)
	}
	if len(host.transitions) != 1 || host.transitions[0].After.Status != QueueStatusPending {
		t.Fatalf("preparation transitions = %+v", host.transitions)
	}
}

func TestEntryDriverPreparationAppliesValidationFailure(t *testing.T) {
	now := time.Date(2026, 7, 21, 11, 45, 0, 0, time.UTC)
	host := &recordingEntryDriverHost{}
	driver := EntryDriver{
		Host:     host,
		Validate: func(context.Context, run.Request) error { return errors.New("plan is no longer runnable") },
		Now:      func() time.Time { return now },
	}
	entry := QueueEntry{PlanID: "plan-a", Status: QueueStatusPending, QueuedAt: now, request: run.Request{Input: "plan-a"}}

	preparation, err := driver.Prepare(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if preparation.Result.Outcome != EntryOutcomeSkipped || len(host.transitions) != 1 {
		t.Fatalf("Prepare() = (%+v, transitions %+v)", preparation.Result, host.transitions)
	}
	if got := host.transitions[0].After; got.Status != QueueStatusSkipped || got.SkipReason != "plan is no longer runnable" {
		t.Fatalf("validation transition = %+v", got)
	}
}

func TestEntryDriverDriveIsSynchronous(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	host := &recordingEntryDriverHost{}
	executed := false
	driver := EntryDriver{
		Host: host,
		Execute: func(_ context.Context, request run.Request) error {
			executed = request.Input == "plan-a"
			return nil
		},
		Now: func() time.Time { return now },
	}
	entry := QueueEntry{PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: now.Add(-time.Minute)}

	result, err := driver.Drive(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if !executed || result.Outcome != EntryOutcomeSucceeded || len(host.transitions) != 1 {
		t.Fatalf("Drive() = (%+v, executed %v, transitions %d)", result, executed, len(host.transitions))
	}
}

func TestEntryDriverStopAfterClaimStillExecutesInitialRun(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	host := &recordingEntryDriverHost{}
	ownerEntered := make(chan struct{})
	continueOwnership := make(chan struct{})
	stopRequested := make(chan struct{})
	executed := make(chan struct{}, 1)
	driver := EntryDriver{
		Host: host,
		Own: func(ctx context.Context, _ run.Request, operation func(context.Context) error) error {
			close(ownerEntered)
			<-continueOwnership
			return operation(ctx)
		},
		Execute: func(context.Context, run.Request) error {
			executed <- struct{}{}
			return nil
		},
		StopRequested: func() bool {
			select {
			case <-stopRequested:
				return true
			default:
				return false
			}
		},
		Now: func() time.Time { return now },
	}
	entry := QueueEntry{PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: now.Add(-time.Minute)}

	type driveResult struct {
		result EntryResult
		err    error
	}
	driven := make(chan driveResult, 1)
	go func() {
		result, err := driver.Drive(context.Background(), entry)
		driven <- driveResult{result: result, err: err}
	}()

	<-ownerEntered
	close(stopRequested)
	close(continueOwnership)

	drive := <-driven
	if drive.err != nil {
		t.Fatal(drive.err)
	}
	if drive.result.Outcome != EntryOutcomeSucceeded || len(host.transitions) != 1 {
		t.Fatalf("Drive() = (%+v, transitions %d), want successful initial execution", drive.result, len(host.transitions))
	}
	select {
	case <-executed:
	default:
		t.Fatal("stop requested after claim prevented initial execution")
	}
}

type failingEntryTransitionStore struct {
	snapshot QueueSnapshot
	fail     bool
}

func (s *failingEntryTransitionStore) Load() (QueueSnapshot, error)     { return s.snapshot, nil }
func (s *failingEntryTransitionStore) SaveSnapshot(QueueSnapshot) error { return nil }
func (s *failingEntryTransitionStore) AppendTransition(QueueTransition) error {
	if s.fail {
		return errors.New("disk full")
	}
	return nil
}

type recoveryEntryDriverHost struct {
	entry       QueueEntry
	transitions []EntryTransition
	failAt      int
	calls       int
}

func (h *recoveryEntryDriverHost) TransitionEntry(_ context.Context, transition EntryTransition) error {
	h.calls++
	if h.failAt == h.calls {
		return errors.New("recovery store unavailable")
	}
	h.transitions = append(h.transitions, transition)
	h.entry = transition.After
	return nil
}

func recoveryPendingEntry() QueueEntry {
	started := time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)
	return QueueEntry{
		PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: started.Add(-time.Minute),
		StartedAt: &started, RecoveryPending: true,
	}
}

func TestEntryDriverRecoverRetainsOwnershipForExecution(t *testing.T) {
	type ownershipKey struct{}
	entry := recoveryPendingEntry()
	host := &recoveryEntryDriverHost{entry: entry}
	released := make(chan error, 1)
	driver := EntryDriver{
		Host: host,
		Own: func(ctx context.Context, _ run.Request, operation func(context.Context) error) error {
			err := operation(context.WithValue(ctx, ownershipKey{}, true))
			released <- err
			return err
		},
		InspectRecovery: func(ctx context.Context, _ string) (RecoveryInspection, error) {
			if owned, _ := ctx.Value(ownershipKey{}).(bool); !owned {
				t.Fatal("recovery inspection ran outside plan ownership")
			}
			return RecoveryInspection{}, nil
		},
		Now: time.Now,
	}

	recovery, err := driver.Recover(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Result.Outcome != EntryOutcomeRetainedRunning || recovery.Ownership == nil {
		t.Fatalf("Recover() = %+v, want retained-running ownership", recovery)
	}
	if host.entry.RecoveryPending {
		t.Fatalf("recovered entry still marked pending: %+v", host.entry)
	}
	select {
	case <-released:
		t.Fatal("plan ownership released before execution continuation")
	default:
	}
	if err := recovery.Ownership.Finish(nil); err != nil {
		t.Fatal(err)
	}
	if err := <-released; err != nil {
		t.Fatalf("owner completed with %v", err)
	}
}

func TestEntryDriverRecoverInspectionAndFinalizationFailures(t *testing.T) {
	failure := errors.New("phase failed")
	tests := []struct {
		name      string
		inspect   RecoveryInspector
		finalize  RecoveryReviewer
		wantError string
	}{
		{
			name: "initial inspection",
			inspect: func(context.Context, string) (RecoveryInspection, error) {
				return RecoveryInspection{}, failure
			},
			wantError: "inspect interrupted queue run: phase failed",
		},
		{
			name: "missing finalizer",
			inspect: func(context.Context, string) (RecoveryInspection, error) {
				return RecoveryInspection{SlicesComplete: true}, nil
			},
			wantError: "recovery finalizer unavailable",
		},
		{
			name: "finalizer",
			inspect: func(context.Context, string) (RecoveryInspection, error) {
				return RecoveryInspection{SlicesComplete: true}, nil
			},
			finalize:  func(context.Context, run.Request) error { return failure },
			wantError: "resume interrupted plan finalization: phase failed",
		},
		{
			name: "post-finalization inspection",
			inspect: func() RecoveryInspector {
				calls := 0
				return func(context.Context, string) (RecoveryInspection, error) {
					calls++
					if calls == 1 {
						return RecoveryInspection{SlicesComplete: true}, nil
					}
					return RecoveryInspection{}, failure
				}
			}(),
			finalize:  func(context.Context, run.Request) error { return nil },
			wantError: "inspect interrupted queue run after finalization: phase failed",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := recoveryPendingEntry()
			host := &recoveryEntryDriverHost{entry: entry}
			driver := EntryDriver{Host: host, InspectRecovery: test.inspect, FinalizeRecovery: test.finalize, Now: time.Now}
			recovery, err := driver.Recover(context.Background(), entry)
			if err != nil {
				t.Fatal(err)
			}
			if recovery.Result.Outcome != EntryOutcomeFailed || !strings.Contains(recovery.Result.Err.Error(), test.wantError) {
				t.Fatalf("Recover() result = %+v, want failure containing %q", recovery.Result, test.wantError)
			}
			if host.entry.Status != QueueStatusFailed || host.entry.RecoveryPending {
				t.Fatalf("failed recovery entry = %+v", host.entry)
			}
		})
	}
}

func TestEntryDriverRecoverStopCanRestartFinalization(t *testing.T) {
	entry := recoveryPendingEntry()
	host := &recoveryEntryDriverHost{entry: entry}
	stopped := true
	reviewed := false
	driver := EntryDriver{
		Host: host,
		InspectRecovery: func(context.Context, string) (RecoveryInspection, error) {
			return RecoveryInspection{SlicesComplete: true, TerminalReview: reviewed}, nil
		},
		FinalizeRecovery: func(context.Context, run.Request) error {
			reviewed = true
			return nil
		},
		StopRequested: func() bool { return stopped },
		Now:           time.Now,
	}

	first, err := driver.Recover(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if first.Result.Outcome != EntryOutcomeRequeuedAfterStop || host.entry.Status != QueueStatusPending || !host.entry.RecoveryPending {
		t.Fatalf("stopped Recover() = %+v, entry %+v", first.Result, host.entry)
	}

	restarted := host.entry
	restarted.Status = QueueStatusRunning
	restarted.StartedAt = entry.StartedAt
	host.entry = restarted
	stopped = false
	second, err := driver.Recover(context.Background(), restarted)
	if err != nil {
		t.Fatal(err)
	}
	if second.Result.Outcome != EntryOutcomeSucceeded || host.entry.Status != QueueStatusSucceeded || !reviewed {
		t.Fatalf("restarted Recover() = %+v, entry %+v, reviewed %v", second.Result, host.entry, reviewed)
	}
}

func TestEntryDriverRecoverReconcilesReworkBudget(t *testing.T) {
	tests := []struct {
		name         string
		baseline     *int
		attempts     int
		previous     string
		inspection   RecoveryInspection
		wantBaseline int
		wantAttempts int
		wantPrevious string
	}{
		{
			name: "legacy snapshot", inspection: RecoveryInspection{TerminalReview: true, ReworkRound: 5, PreviousFindingFingerprint: "finding-1"},
			wantBaseline: 4, wantAttempts: 1, wantPrevious: "finding-1",
		},
		{
			name: "explicit baseline", baseline: new(3), inspection: RecoveryInspection{TerminalReview: true, ReworkRound: 5, PreviousFindingFingerprint: "finding-2"},
			wantBaseline: 3, wantAttempts: 2, wantPrevious: "finding-2",
		},
		{
			name: "stale round preserves fingerprint", baseline: new(3), attempts: 2, previous: "finding-old", inspection: RecoveryInspection{TerminalReview: true, ReworkRound: 4, PreviousFindingFingerprint: "finding-stale"},
			wantBaseline: 3, wantAttempts: 2, wantPrevious: "finding-old",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := recoveryPendingEntry()
			entry.ReworkBaselineRound = test.baseline
			entry.ReworkAttempts = test.attempts
			entry.PreviousFindingFingerprint = test.previous
			host := &recoveryEntryDriverHost{entry: entry}
			driver := EntryDriver{
				Host: host,
				InspectRecovery: func(context.Context, string) (RecoveryInspection, error) {
					return test.inspection, nil
				},
				PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
					return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}
				},
				Now: time.Now,
			}
			recovery, err := driver.Recover(context.Background(), entry)
			if err != nil {
				t.Fatal(err)
			}
			if recovery.Result.Outcome != EntryOutcomeSucceeded || host.entry.ReworkBaselineRound == nil || *host.entry.ReworkBaselineRound != test.wantBaseline || host.entry.ReworkAttempts != test.wantAttempts || host.entry.PreviousFindingFingerprint != test.wantPrevious {
				t.Fatalf("reconciled entry = %+v, result %+v", host.entry, recovery.Result)
			}
		})
	}
}

func TestEntryDriverRecoverBaselinePersistenceFailureIsTerminal(t *testing.T) {
	entry := recoveryPendingEntry()
	host := &recoveryEntryDriverHost{entry: entry, failAt: 1}
	driver := EntryDriver{
		Host: host,
		InspectRecovery: func(context.Context, string) (RecoveryInspection, error) {
			return RecoveryInspection{ReworkRound: 1}, nil
		},
		PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
			return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}
		},
		Now: time.Now,
	}

	recovery, err := driver.Recover(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Result.Outcome != EntryOutcomeFailed || !strings.Contains(recovery.Result.Err.Error(), "persist recovered automatic rework progress") || host.entry.Status != QueueStatusFailed {
		t.Fatalf("Recover() = %+v, entry %+v", recovery.Result, host.entry)
	}
}

func TestEntryDriverRecoverTerminalReworkBranches(t *testing.T) {
	tests := []struct {
		name        string
		decision    reworkpkg.Decision
		reworkErr   error
		wantOutcome EntryOutcome
		wantError   string
		wantRetain  bool
	}{
		{name: "mutation failure", reworkErr: errors.New("mutation failed"), wantOutcome: EntryOutcomeFailed, wantError: "automatic rework mutation failed"},
		{name: "stopped findings", decision: reworkpkg.Decision{Round: 1, StopReason: "stalled"}, wantOutcome: EntryOutcomeFailed, wantError: "stalled"},
		{name: "terminal review", decision: reworkpkg.Decision{}, wantOutcome: EntryOutcomeSucceeded},
		{name: "reworked", decision: reworkpkg.Decision{Reworked: true, Round: 1, Fingerprint: "finding-1"}, wantOutcome: EntryOutcomeRetainedRunning, wantRetain: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := recoveryPendingEntry()
			baseline := 0
			entry.ReworkBaselineRound = &baseline
			host := &recoveryEntryDriverHost{entry: entry}
			driver := EntryDriver{
				Host: host,
				InspectRecovery: func(context.Context, string) (RecoveryInspection, error) {
					return RecoveryInspection{TerminalReview: true}, nil
				},
				Rework: func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
					return test.decision, test.reworkErr
				},
				PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
					return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}
				},
				Now: time.Now,
			}
			recovery, err := driver.Recover(context.Background(), entry)
			if err != nil {
				t.Fatal(err)
			}
			if recovery.Result.Outcome != test.wantOutcome || (recovery.Ownership != nil) != test.wantRetain {
				t.Fatalf("Recover() = %+v", recovery)
			}
			if test.wantError != "" && !strings.Contains(recovery.Result.Err.Error(), test.wantError) {
				t.Fatalf("Recover() error = %v, want containing %q", recovery.Result.Err, test.wantError)
			}
			if recovery.Ownership != nil {
				if err := recovery.Ownership.Finish(nil); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestEntryDriverRecoverStopsRecurringFilesWithoutAnotherRun(t *testing.T) {
	detail, findings := recurringFileRecoveryPlan()
	repo := recoveryRepositoryFunc(func(context.Context, string) (*plan.PlanDetail, error) { return detail, nil })
	entry := recoveryPendingEntry()
	baseline := 0
	entry.ReworkBaselineRound = &baseline
	entry.ReworkAttempts = 1
	entry.PreviousFindingFingerprint = reworkpkg.ReworkFindingsFingerprint([]plan.ReviewFinding{findings[1]})
	host := &recoveryEntryDriverHost{entry: entry}
	now := time.Date(2026, 7, 29, 20, 0, 0, 0, time.UTC)
	var appended []plan.Event
	domainDriver := reworkpkg.Driver{
		Resolve: repo.ResolvePlan,
		Now:     func() time.Time { return now },
		AppendEvent: func(_ string, event plan.Event) error {
			appended = append(appended, event)
			detail.Events = append(detail.Events, event)
			return nil
		},
	}
	decisions := 0
	finalizations := 0
	executions := 0
	driver := EntryDriver{
		Host:            host,
		InspectRecovery: NewRecoveryInspector(repo),
		FinalizeRecovery: func(context.Context, run.Request) error {
			finalizations++
			return nil
		},
		Execute: func(context.Context, run.Request) error {
			executions++
			return nil
		},
		Rework: func(ctx context.Context, planID string, baseline, attempts int, previous string, maxAttempts int) (reworkpkg.Decision, error) {
			decisions++
			return domainDriver.Decide(ctx, planID, baseline, attempts, previous, maxAttempts)
		},
		PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
			return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 5}
		},
		Now: func() time.Time { return now },
	}

	recovery, err := driver.Recover(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Result.Outcome != EntryOutcomeFailed || recovery.Result.Err == nil {
		t.Fatalf("Recover() = %+v, want terminal recurring-file failure", recovery)
	}
	wantReason := "automatic rework stalled on files recurring across three consecutive reviews: [\"internal/runqueue/recovery.go\"]"
	wantDecision := reworkpkg.Decision{
		StopKind:       reworkpkg.StopKindRecurringFiles,
		StopReason:     wantReason,
		RecurringFiles: []string{"internal/runqueue/recovery.go"},
		Findings:       []plan.ReviewFinding{findings[2]},
	}
	if got, want := recovery.Result.Err.Error(), reworkpkg.FormatStopMessage(wantDecision); got != want {
		t.Fatalf("recovered stop output = %q, want %q", got, want)
	}
	if finalizations != 1 || decisions != 1 || executions != 0 {
		t.Fatalf("recovered convergence calls = (%d finalizations, %d decisions, %d executions), want (1, 1, 0)", finalizations, decisions, executions)
	}
	if host.entry.Status != QueueStatusFailed || host.entry.RecoveryPending || host.entry.ReworkAttempts != 2 || host.entry.PreviousFindingFingerprint != entry.PreviousFindingFingerprint {
		t.Fatalf("failed recovered queue entry = %+v", host.entry)
	}
	if detail.State.Status != plan.StatusChangesRequested || detail.State.Plan.Review == nil || len(detail.State.Plan.Review.Findings) != 1 || detail.State.Plan.Review.Findings[0] != findings[2] || reworkpkg.RoundCount(detail) != 2 {
		t.Fatalf("recurring stop mutated latest review or reopened plan: %+v", detail)
	}
	stopEvents := 0
	for _, event := range appended {
		if event.Type != plan.EventTypeReworkStopped {
			continue
		}
		stopEvents++
		if event.Round != 2 || event.Attempts != 2 || event.Reason != wantReason || reworkpkg.StopKindForPersistedReason(event.Reason) != reworkpkg.StopKindRecurringFiles {
			t.Fatalf("recovered rework_stopped event = %+v", event)
		}
	}
	if stopEvents != 1 {
		t.Fatalf("recovered recurring review produced %d stop observations, want one", stopEvents)
	}
}

func TestEntryDriverRecoverDoesNotDuplicatePersistedRecurringFileStop(t *testing.T) {
	detail, findings := recurringFileRecoveryPlan()
	reason := "automatic rework stalled on files recurring across three consecutive reviews: [\"internal/runqueue/recovery.go\"]"
	detail.Events = append(detail.Events, plan.Event{Type: plan.EventTypeReworkStopped, Round: 2, Attempts: 2, Reason: reason, Message: reason})
	repo := recoveryRepositoryFunc(func(context.Context, string) (*plan.PlanDetail, error) { return detail, nil })
	entry := recoveryPendingEntry()
	baseline := 0
	entry.ReworkBaselineRound = &baseline
	entry.ReworkAttempts = 2
	entry.PreviousFindingFingerprint = reworkpkg.ReworkFindingsFingerprint([]plan.ReviewFinding{findings[1]})
	host := &recoveryEntryDriverHost{entry: entry}
	guardCalls := 0
	finalizations := 0
	executions := 0
	driver := EntryDriver{
		Host:            host,
		InspectRecovery: NewRecoveryInspector(repo),
		FinalizeRecovery: func(context.Context, run.Request) error {
			finalizations++
			return nil
		},
		Execute: func(context.Context, run.Request) error {
			executions++
			return nil
		},
		Rework: func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
			guardCalls++
			_, _, err := reworkpkg.GuardAutoReworkRestart(detail, false)
			return reworkpkg.Decision{}, err
		},
		PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
			return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 5}
		},
		Now: time.Now,
	}

	recovery, err := driver.Recover(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Result.Outcome != EntryOutcomeFailed || recovery.Result.Err == nil {
		t.Fatalf("Recover() = %+v, want persisted-stop failure", recovery)
	}
	for _, want := range []string{"THE SAME FILES KEEP RECURRING", "- internal/runqueue/recovery.go", findings[2].Message, findings[2].Suggestion, "--rework-restart"} {
		if !strings.Contains(recovery.Result.Err.Error(), want) {
			t.Errorf("persisted stop output %q does not contain %q", recovery.Result.Err, want)
		}
	}
	if finalizations != 1 || guardCalls != 1 || executions != 0 || host.entry.Status != QueueStatusFailed || host.entry.RecoveryPending {
		t.Fatalf("persisted-stop recovery = (%d finalizations, %d guard calls, %d executions, entry %+v)", finalizations, guardCalls, executions, host.entry)
	}
	stopEvents := 0
	for _, event := range detail.Events {
		if event.Type == plan.EventTypeReworkStopped {
			stopEvents++
		}
	}
	if stopEvents != 1 || reworkpkg.RoundCount(detail) != 2 {
		t.Fatalf("resume duplicated the recurring observation or reopened work: stop events=%d rounds=%d", stopEvents, reworkpkg.RoundCount(detail))
	}
}

func TestEntryDriverReworkInitialExecutionFailure(t *testing.T) {
	entry := recoveryPendingEntry()
	entry.RecoveryPending = false
	host := &recoveryEntryDriverHost{entry: entry}
	reworkCalls := 0
	driver := EntryDriver{
		Host: host,
		Execute: func(context.Context, run.Request) error {
			return errors.New("initial execution failed")
		},
		Rework: func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
			reworkCalls++
			return reworkpkg.Decision{}, nil
		},
		PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
			return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}
		},
		Now: time.Now,
	}

	result, err := driver.Drive(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != EntryOutcomeFailed || !strings.Contains(result.Err.Error(), "initial execution failed") || reworkCalls != 0 {
		t.Fatalf("Drive() = %+v, rework calls %d", result, reworkCalls)
	}
	if host.entry.Status != QueueStatusFailed {
		t.Fatalf("failed entry = %+v", host.entry)
	}
}

func TestEntryDriverReworkRepeatsUnderOneOwnership(t *testing.T) {
	type ownershipKey struct{}
	entry := recoveryPendingEntry()
	entry.RecoveryPending = false
	baseline := 0
	entry.ReworkBaselineRound = &baseline
	host := &recoveryEntryDriverHost{entry: entry}
	runs := 0
	reworkCalls := 0
	ownerReturned := false
	driver := EntryDriver{
		Host: host,
		Own: func(ctx context.Context, _ run.Request, operation func(context.Context) error) error {
			err := operation(context.WithValue(ctx, ownershipKey{}, true))
			ownerReturned = true
			return err
		},
		Execute: func(ctx context.Context, _ run.Request) error {
			if owned, _ := ctx.Value(ownershipKey{}).(bool); !owned {
				t.Fatal("execution ran outside plan ownership")
			}
			if ownerReturned {
				t.Fatal("ownership released before follow-up execution")
			}
			runs++
			return nil
		},
		Rework: func(ctx context.Context, _ string, _ int, attempts int, previous string, _ int) (reworkpkg.Decision, error) {
			if owned, _ := ctx.Value(ownershipKey{}).(bool); !owned {
				t.Fatal("rework ran outside plan ownership")
			}
			if ownerReturned {
				t.Fatal("ownership released before rework")
			}
			reworkCalls++
			if attempts >= 2 {
				return reworkpkg.Decision{}, nil
			}
			if attempts > 0 && previous != "finding-1" {
				t.Fatalf("prior fingerprint = %q, want finding-1", previous)
			}
			return reworkpkg.Decision{Reworked: true, Round: attempts + 1, Fingerprint: "finding-" + fmt.Sprint(attempts+1)}, nil
		},
		PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
			return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}
		},
		Now: time.Now,
	}

	result, err := driver.Drive(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != EntryOutcomeSucceeded || runs != 3 || reworkCalls != 3 || !ownerReturned {
		t.Fatalf("Drive() = %+v, runs %d, reworks %d, owner returned %v", result, runs, reworkCalls, ownerReturned)
	}
	if host.entry.Status != QueueStatusSucceeded || host.entry.ReworkAttempts != 2 || host.entry.PreviousFindingFingerprint != "finding-2" {
		t.Fatalf("persisted entry = %+v", host.entry)
	}
	if len(host.transitions) != 3 {
		t.Fatalf("transitions = %d, want two progress updates and terminal result", len(host.transitions))
	}
}

func TestEntryDriverReworkBoundedStopsAreTerminal(t *testing.T) {
	tests := []struct {
		name     string
		decision reworkpkg.Decision
		want     string
	}{
		{name: "cap", decision: reworkpkg.Decision{Round: 3, StopKind: reworkpkg.StopKindCapExhausted, StopReason: "automatic rework cap exhausted after 3 cycles"}, want: "attempt cap reached"},
		{name: "stall", decision: reworkpkg.Decision{Round: 2, StopKind: reworkpkg.StopKindFindingsStalled, StopReason: "automatic rework stalled on equivalent consecutive findings"}, want: "THE LOOP IS GOING IN CIRCLES"},
		{name: "recurring files", decision: reworkpkg.Decision{Round: 2, StopKind: reworkpkg.StopKindRecurringFiles, StopReason: "automatic rework stalled on files recurring across three consecutive reviews: [\"internal/runqueue/entry_driver.go\"]", RecurringFiles: []string{"internal/runqueue/entry_driver.go"}}, want: "THE SAME FILES KEEP RECURRING"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := recoveryPendingEntry()
			entry.RecoveryPending = false
			baseline := 0
			entry.ReworkBaselineRound = &baseline
			host := &recoveryEntryDriverHost{entry: entry}
			runs := 0
			driver := EntryDriver{
				Host: host,
				Execute: func(context.Context, run.Request) error {
					runs++
					return nil
				},
				Rework: func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
					return test.decision, nil
				},
				PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
					return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}
				},
				Now: time.Now,
			}

			result, err := driver.Drive(context.Background(), entry)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != EntryOutcomeFailed || !strings.Contains(result.Err.Error(), test.want) || runs != 1 {
				t.Fatalf("Drive() = %+v, runs %d", result, runs)
			}
			if host.entry.Status != QueueStatusFailed {
				t.Fatalf("bounded stop entry = %+v", host.entry)
			}
		})
	}
}

func TestEntryDriverReworkStopAfterReopenRequeues(t *testing.T) {
	entry := recoveryPendingEntry()
	entry.RecoveryPending = false
	baseline := 0
	entry.ReworkBaselineRound = &baseline
	host := &recoveryEntryDriverHost{entry: entry}
	stopped := false
	runs := 0
	driver := EntryDriver{
		Host: host,
		Execute: func(context.Context, run.Request) error {
			runs++
			return nil
		},
		Rework: func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
			stopped = true
			return reworkpkg.Decision{Reworked: true, Round: 1, Fingerprint: "finding-1"}, nil
		},
		StopRequested: func() bool { return stopped },
		PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
			return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}
		},
		Now: time.Now,
	}

	result, err := driver.Drive(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != EntryOutcomeRequeuedAfterStop || runs != 1 {
		t.Fatalf("Drive() = %+v, runs %d", result, runs)
	}
	if host.entry.Status != QueueStatusPending || host.entry.ReworkAttempts != 1 || host.entry.PreviousFindingFingerprint != "finding-1" {
		t.Fatalf("requeued entry = %+v", host.entry)
	}
}

func TestEntryDriverReworkPersistenceFailureReleasesOwnership(t *testing.T) {
	entry := recoveryPendingEntry()
	entry.RecoveryPending = false
	baseline := 0
	entry.ReworkBaselineRound = &baseline
	host := &recoveryEntryDriverHost{entry: entry, failAt: 1}
	ownerReturned := false
	driver := EntryDriver{
		Host: host,
		Own: func(ctx context.Context, _ run.Request, operation func(context.Context) error) error {
			err := operation(ctx)
			ownerReturned = true
			return err
		},
		Execute: func(context.Context, run.Request) error { return nil },
		Rework: func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
			return reworkpkg.Decision{Reworked: true, Round: 1, Fingerprint: "finding-1"}, nil
		},
		PolicyForEntry: func(QueueEntry) runtimeconfig.AutoReworkPolicy {
			return runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}
		},
		Now: time.Now,
	}

	result, err := driver.Drive(context.Background(), entry)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != EntryOutcomeFailed || !strings.Contains(result.Err.Error(), "persist automatic rework progress") || !ownerReturned {
		t.Fatalf("Drive() = %+v, owner returned %v", result, ownerReturned)
	}
	if host.entry.Status != QueueStatusFailed || host.entry.ReworkAttempts != 1 || host.entry.PreviousFindingFingerprint != "" {
		t.Fatalf("entry after progress persistence failure = %+v", host.entry)
	}
}

func TestEntryDriverManagerHostPersistsBeforePublishing(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := &failingEntryTransitionStore{
		snapshot: QueueSnapshot{Entries: []QueueEntry{{PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: now.Add(-time.Minute)}}},
		fail:     true,
	}
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { return nil }, func() time.Time { return now }, store)
	if err != nil {
		t.Fatal(err)
	}
	driver := EntryDriver{Host: manager, Now: func() time.Time { return now }}
	entry := manager.Queue().Entries[0]

	if err := driver.ApplyResult(context.Background(), entry, EntryResult{Outcome: EntryOutcomeSucceeded}); err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("ApplyResult() error = %v, want persistence failure", err)
	}
	if got := manager.Queue().Entries[0].Status; got != QueueStatusRunning {
		t.Fatalf("published status after persistence failure = %s", got)
	}

	store.fail = false
	if err := driver.ApplyResult(context.Background(), entry, EntryResult{Outcome: EntryOutcomeSucceeded}); err != nil {
		t.Fatal(err)
	}
	if got := manager.Queue().Entries[0].Status; got != QueueStatusSucceeded {
		t.Fatalf("published status after persistence = %s", got)
	}
}
