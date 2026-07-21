package runqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

func TestNewManagerValidatesProductionConfiguration(t *testing.T) {
	valid := ManagerConfig{
		Context:           context.Background(),
		Executor:          func(context.Context, run.Request) error { return nil },
		Clock:             time.Now,
		Store:             NewFileStore(t.TempDir()),
		Validator:         func(context.Context, run.Request) error { return nil },
		RecoveryInspector: func(context.Context, string) (RecoveryInspection, error) { return RecoveryInspection{}, nil },
		RecoveryReviewer:  func(context.Context, run.Request) error { return nil },
		PlanOwner: func(ctx context.Context, _ run.Request, operation func(context.Context) error) error {
			return operation(ctx)
		},
		MaxParallelRuns:     1,
		StartDrainingPaused: true,
	}
	tests := []struct {
		name   string
		mutate func(*ManagerConfig)
		want   string
	}{
		{name: "executor", mutate: func(config *ManagerConfig) { config.Executor = nil }, want: "requires executor"},
		{name: "clock", mutate: func(config *ManagerConfig) { config.Clock = nil }, want: "requires clock"},
		{name: "store", mutate: func(config *ManagerConfig) { config.Store = nil }, want: "requires store"},
		{name: "validator", mutate: func(config *ManagerConfig) { config.Validator = nil }, want: "requires validator"},
		{name: "recovery inspector", mutate: func(config *ManagerConfig) { config.RecoveryInspector = nil }, want: "requires recovery inspector"},
		{name: "recovery reviewer", mutate: func(config *ManagerConfig) { config.RecoveryReviewer = nil }, want: "requires recovery reviewer"},
		{name: "plan owner", mutate: func(config *ManagerConfig) { config.PlanOwner = nil }, want: "requires plan owner"},
		{name: "parallelism", mutate: func(config *ManagerConfig) { config.MaxParallelRuns = 0 }, want: "requires at least one parallel run"},
		{
			name: "enabled automatic rework",
			mutate: func(config *ManagerConfig) {
				config.AutoReworkPolicy = runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 1}
				config.AutoReworker = nil
			},
			want: "requires auto reworker",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			if _, err := NewManager(config); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("NewManager() error = %v, want containing %q", err, test.want)
			}
		})
	}

	manager, err := NewManager(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	if entry := manager.Queue().Entries[0]; entry.Status != QueueStatusPending {
		t.Fatalf("paused production queue entry = %+v", entry)
	}
	manager.SetDrainingPaused(false)
	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
}

func TestNewManagerRejectsLegacyEntryWithoutCompatiblePolicy(t *testing.T) {
	store := NewFileStore(t.TempDir())
	legacy := `{"entries":[{"plan_id":"legacy-plan","status":"pending","queued_at":"2026-07-21T00:00:00Z","review_enabled":false}]}`
	if err := os.WriteFile(store.snapshotPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := NewManager(ManagerConfig{
		Executor:          func(context.Context, run.Request) error { return nil },
		Clock:             time.Now,
		Store:             store,
		Validator:         func(context.Context, run.Request) error { return nil },
		RecoveryInspector: func(context.Context, string) (RecoveryInspection, error) { return RecoveryInspection{}, nil },
		RecoveryReviewer:  func(context.Context, run.Request) error { return nil },
		AutoReworkPolicy:  runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 1},
		AutoReworker: func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
			return reworkpkg.Decision{}, nil
		},
		PlanOwner: func(ctx context.Context, _ run.Request, operation func(context.Context) error) error {
			return operation(ctx)
		},
		MaxParallelRuns: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "queued plan legacy-plan") || !strings.Contains(err.Error(), "review") {
		t.Fatalf("NewManager() legacy policy error = %v", err)
	}
}

func TestManagerAutoReworkLoopsInOneQueueSlot(t *testing.T) {
	var mu sync.Mutex
	runs := 0
	manager := New(context.Background(), func(context.Context, run.Request) error {
		mu.Lock()
		runs++
		mu.Unlock()
		return nil
	}, nil)
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	manager.SetAutoReworker(func(_ context.Context, _ string, _ int, attempts int, _ string, _ int) (reworkpkg.Decision, error) {
		if attempts >= 2 {
			return reworkpkg.Decision{}, nil
		}
		return reworkpkg.Decision{Reworked: true, Round: attempts + 1, Fingerprint: fmt.Sprintf("finding-%d", attempts+1)}, nil
	})
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
	mu.Lock()
	defer mu.Unlock()
	if runs != 3 {
		t.Fatalf("runs = %d, want initial run plus two rework cycles", runs)
	}
	entry := manager.Queue().Entries[0]
	if entry.ReworkAttempts != 2 || entry.PreviousFindingFingerprint != "finding-2" {
		t.Fatalf("unexpected durable progress: %+v", entry)
	}
}

func TestManagerAutoReworkUsesReplacementInstalledDuringInitialRun(t *testing.T) {
	tests := []struct {
		name             string
		configureInitial bool
	}{
		{name: "replaces configured reworker", configureInitial: true},
		{name: "enables initially nil reworker", configureInitial: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			runs, initialCalls, replacementCalls := 0, 0, 0
			var manager *Manager
			manager = New(context.Background(), func(context.Context, run.Request) error {
				mu.Lock()
				runs++
				mu.Unlock()
				manager.SetAutoReworker(func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
					mu.Lock()
					replacementCalls++
					mu.Unlock()
					return reworkpkg.Decision{}, nil
				})
				return nil
			}, nil)
			if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}); err != nil {
				t.Fatal(err)
			}
			if tt.configureInitial {
				manager.SetAutoReworker(func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
					mu.Lock()
					initialCalls++
					mu.Unlock()
					return reworkpkg.Decision{}, nil
				})
			}
			if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
				t.Fatal(err)
			}
			waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
				return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
			})

			mu.Lock()
			defer mu.Unlock()
			if runs != 1 || initialCalls != 0 || replacementCalls != 1 {
				t.Fatalf("replacement during initial run = (%d runs, %d initial calls, %d replacement calls), want (1, 0, 1)", runs, initialCalls, replacementCalls)
			}
		})
	}
}

func TestManagerStopDuringAutoReworkPreservesCreatedRoundForResume(t *testing.T) {
	store := NewFileStore(t.TempDir())
	runs := make(chan string, 3)
	var manager *Manager
	manager, err := NewWithStore(context.Background(), func(_ context.Context, request run.Request) error {
		runs <- request.Input
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	manager.SetAutoReworker(func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
		manager.RequestStop()
		return reworkpkg.Decision{Reworked: true, Round: 1, Fingerprint: "finding-1"}, nil
	})
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusPending && snapshot.Entries[0].ReworkAttempts == 1
	})

	entry := manager.Queue().Entries[0]
	if entry.StartedAt != nil || entry.FinishedAt != nil || entry.RecoveryPending || entry.PreviousFindingFingerprint != "finding-1" {
		t.Fatalf("stopped rework round is not resumable: %+v", entry)
	}
	if got := <-runs; got != "plan-a" {
		t.Fatalf("initial run = %q, want plan-a", got)
	}
	select {
	case got := <-runs:
		t.Fatalf("stop requested during rework still started another run for %q", got)
	default:
	}

	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Entries) != 1 || persisted.Entries[0].Status != QueueStatusPending || persisted.Entries[0].ReworkAttempts != 1 || persisted.Entries[0].PreviousFindingFingerprint != "finding-1" {
		t.Fatalf("persisted stopped rework round is not resumable: %+v", persisted)
	}

	resumed, err := NewWithStore(context.Background(), func(_ context.Context, request run.Request) error {
		runs <- request.Input
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := resumed.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	progress := make(chan struct {
		attempts int
		previous string
	}, 1)
	resumed.SetAutoReworker(func(_ context.Context, _ string, _ int, attempts int, previous string, _ int) (reworkpkg.Decision, error) {
		progress <- struct {
			attempts int
			previous string
		}{attempts: attempts, previous: previous}
		return reworkpkg.Decision{}, nil
	})
	resumed.Drain()
	waitForManagerQueue(t, resumed, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
	if got := <-runs; got != "plan-a" {
		t.Fatalf("resumed run = %q, want plan-a", got)
	}
	gotProgress := <-progress
	if gotProgress.attempts != 1 || gotProgress.previous != "finding-1" {
		t.Fatalf("resumed rework progress = (%d, %q), want (1, finding-1)", gotProgress.attempts, gotProgress.previous)
	}
}

func TestManagerQueueResumeUsesPersistedExecutionOptionsInsidePlanOwner(t *testing.T) {
	type ownershipKey struct{}
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	sessionTimeout := 37 * time.Minute
	options := runtimeconfig.RunOptionsPatch{
		Mode: run.ModeRun, CommitPolicy: run.CommitPolicySlice,
		ExecutionMode: run.ExecutionModeIsolated, Agent: run.AgentCodex,
	}.WithMaxSlices(1).WithContinue(true).WithPullRequest(false).WithReviewEnabled(false).WithSessionTimeout(sessionTimeout)
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
		PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt, StartedAt: &startedAt, RunOptions: &options,
	}}}); err != nil {
		t.Fatal(err)
	}

	executed := make(chan run.Request, 1)
	manager, err := NewWithStore(context.Background(), func(ctx context.Context, request run.Request) error {
		if owned, _ := ctx.Value(ownershipKey{}).(bool); !owned {
			return errors.New("queued recovery executed outside plan ownership")
		}
		executed <- request
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	ownerCalls := 0
	manager.SetPlanOwner(func(ctx context.Context, request run.Request, operation func(context.Context) error) error {
		ownerCalls++
		if request.Input != "plan-a" {
			return fmt.Errorf("owned plan = %q, want plan-a", request.Input)
		}
		return operation(context.WithValue(ctx, ownershipKey{}, true))
	})
	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})

	request := <-executed
	if request.Input != "plan-a" || request.Mode != run.ModeRun || request.MaxSlices != 1 || !request.Continue || request.CommitPolicy != run.CommitPolicySlice || request.ExecutionMode != run.ExecutionModeIsolated || request.Agent != run.AgentCodex || request.PullRequest || request.ReviewEnabled || request.SessionTimeout != sessionTimeout {
		t.Fatalf("recovered request lost effective execution options: %+v", request)
	}
	if ownerCalls != 1 {
		t.Fatalf("plan owner calls = %d, want one lock spanning recovered execution", ownerCalls)
	}
}

func TestManagerAutoReworkCountsAttemptsAfterPersistedBaseline(t *testing.T) {
	var mu sync.Mutex
	runs := 0
	manager := New(context.Background(), func(context.Context, run.Request) error {
		mu.Lock()
		runs++
		mu.Unlock()
		return nil
	}, nil)
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	manager.SetRecoveryInspector(func(context.Context, string) (RecoveryInspection, error) {
		return RecoveryInspection{ReworkRound: 4}, nil
	})
	reworkCalls := 0
	manager.SetAutoReworker(func(_ context.Context, _ string, baseline int, attempts int, _ string, _ int) (reworkpkg.Decision, error) {
		mu.Lock()
		defer mu.Unlock()
		reworkCalls++
		if baseline != 4 {
			t.Errorf("baseline = %d, want existing manual round 4", baseline)
		}
		if reworkCalls == 1 {
			if attempts != 0 {
				t.Errorf("initial automatic attempts = %d, want 0", attempts)
			}
			return reworkpkg.Decision{Reworked: true, Round: 5, Fingerprint: "finding-1"}, nil
		}
		if attempts != 1 {
			t.Errorf("automatic attempts after round 5 = %d, want 1", attempts)
		}
		return reworkpkg.Decision{}, nil
	})
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})

	mu.Lock()
	defer mu.Unlock()
	entry := manager.Queue().Entries[0]
	if runs != 2 || reworkCalls != 2 || entry.ReworkBaselineRound == nil || *entry.ReworkBaselineRound != 4 || entry.ReworkAttempts != 1 {
		t.Fatalf("baseline loop = (%d runs, %d reworks), entry=%+v", runs, reworkCalls, entry)
	}
}

func TestManagerRestoresPersistedAutoReworkPolicyOnRestart(t *testing.T) {
	store := NewFileStore(t.TempDir())
	persistedPolicy := runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 2}
	first, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	first.SetDrainingPaused(true)
	if err := first.SetAutoReworkPolicy(persistedPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}

	runs := 0
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error {
		runs++
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{MaxAttempts: 9}); err != nil {
		t.Fatal(err)
	}
	reworkCalls := 0
	manager.SetAutoReworker(func(_ context.Context, _ string, _ int, _ int, _ string, maxAttempts int) (reworkpkg.Decision, error) {
		reworkCalls++
		if maxAttempts != 2 {
			t.Errorf("restored max attempts = %d, want 2", maxAttempts)
		}
		return reworkpkg.Decision{}, nil
	})
	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})

	entry := manager.Queue().Entries[0]
	if runs != 1 || reworkCalls != 1 || entry.AutoReworkPolicy == nil || *entry.AutoReworkPolicy != persistedPolicy {
		t.Fatalf("restarted queue did not retain policy: runs=%d reworks=%d entry=%+v", runs, reworkCalls, entry)
	}
}

func TestManagerAutoReworkValidatesPersistedReviewOption(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	options := runtimeconfig.RunOptionsPatch{}.WithReviewEnabled(false)
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
		PlanID: "plan-a", Status: QueueStatusPending, QueuedAt: time.Date(2026, 7, 12, 11, 0, 0, 0, time.UTC), RunOptions: &options,
	}}}); err != nil {
		t.Fatal(err)
	}
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}

	err = manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3})
	if err == nil || !strings.Contains(err.Error(), "plan-a") || !strings.Contains(err.Error(), "requires automatic review") {
		t.Fatalf("persisted review validation error = %v", err)
	}
	entry := manager.Queue().Entries[0]
	if entry.AutoReworkPolicy != nil || entry.RunOptions == nil || entry.RunOptions.ReviewEnabled == nil || *entry.RunOptions.ReviewEnabled {
		t.Fatalf("invalid policy mutated persisted queue entry: %+v", entry)
	}
}

func TestManagerAutoReworkValidatesNewRequestReviewOption(t *testing.T) {
	manager := New(context.Background(), func(context.Context, run.Request) error { return nil }, nil)
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	request := queueTestRequest("plan-a")
	request.ReviewEnabled = false
	if _, err := manager.Enqueue(request); err == nil || !strings.Contains(err.Error(), "requires automatic review") {
		t.Fatalf("new request validation error = %v", err)
	}
	if len(manager.Queue().Entries) != 0 {
		t.Fatalf("invalid request was queued: %+v", manager.Queue())
	}
}

func TestManagerRecoversInterruptedRunBeforeAutoReworkMutation(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 7, 11, 18, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
		PlanID:        "plan-a",
		Status:        QueueStatusRunning,
		QueuedAt:      queuedAt,
		StartedAt:     &startedAt,
		Mode:          run.ModeRun,
		CommitPolicy:  run.CommitPolicySlice,
		ExecutionMode: run.ExecutionModeIsolated,
		Agent:         run.AgentPi,
	}}}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	runs := 0
	reworkCalls := 0
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error {
		mu.Lock()
		runs++
		mu.Unlock()
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	assertRecoveredQueueEntry(t, store, 0, "")

	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	manager.SetAutoReworker(func(_ context.Context, _ string, _ int, attempts int, previous string, _ int) (reworkpkg.Decision, error) {
		mu.Lock()
		defer mu.Unlock()
		reworkCalls++
		if reworkCalls == 1 {
			if attempts != 0 || previous != "" {
				t.Errorf("first recovered rework progress = (%d, %q), want zero values", attempts, previous)
			}
			return reworkpkg.Decision{Reworked: true, Round: 1, Fingerprint: "finding-1"}, nil
		}
		if attempts != 1 || previous != "finding-1" {
			t.Errorf("second recovered rework progress = (%d, %q), want (1, finding-1)", attempts, previous)
		}
		return reworkpkg.Decision{}, nil
	})
	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})

	mu.Lock()
	defer mu.Unlock()
	if runs != 2 || reworkCalls != 2 {
		t.Fatalf("recovered loop calls = (%d runs, %d reworks), want (2, 2)", runs, reworkCalls)
	}
	entry := manager.Queue().Entries[0]
	if entry.ReworkAttempts != 1 || entry.PreviousFindingFingerprint != "finding-1" {
		t.Fatalf("unexpected recovered loop progress: %+v", entry)
	}
}

func TestManagerRecoversInterruptedRunAfterAutoReworkMutation(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 7, 11, 19, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
		PlanID:                     "plan-a",
		Status:                     QueueStatusRunning,
		QueuedAt:                   queuedAt,
		StartedAt:                  &startedAt,
		Mode:                       run.ModeRun,
		CommitPolicy:               run.CommitPolicySlice,
		ExecutionMode:              run.ExecutionModeIsolated,
		Agent:                      run.AgentPi,
		ReworkAttempts:             1,
		PreviousFindingFingerprint: "finding-1",
	}}}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	runs := 0
	progressChecked := false
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error {
		mu.Lock()
		runs++
		mu.Unlock()
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	assertRecoveredQueueEntry(t, store, 1, "finding-1")

	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	manager.SetAutoReworker(func(_ context.Context, _ string, _ int, attempts int, previous string, _ int) (reworkpkg.Decision, error) {
		mu.Lock()
		defer mu.Unlock()
		progressChecked = true
		if attempts != 1 || previous != "finding-1" {
			t.Errorf("recovered rework progress = (%d, %q), want (1, finding-1)", attempts, previous)
		}
		return reworkpkg.Decision{}, nil
	})
	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})

	mu.Lock()
	defer mu.Unlock()
	if runs != 1 || !progressChecked {
		t.Fatalf("recovered loop = (%d runs, progress checked %v), want (1, true)", runs, progressChecked)
	}
	entry := manager.Queue().Entries[0]
	if entry.ReworkAttempts != 1 || entry.PreviousFindingFingerprint != "finding-1" {
		t.Fatalf("unexpected recovered loop progress: %+v", entry)
	}
}

func TestManagerRecoveredTerminalReviewAutoReworksBeforeValidation(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 7, 11, 20, 0, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
		PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt, StartedAt: &startedAt,
	}}}); err != nil {
		t.Fatal(err)
	}

	mutated := false
	runs := 0
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error {
		runs++
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	manager.SetRecoveryInspector(func(context.Context, string) (RecoveryInspection, error) {
		return RecoveryInspection{TerminalReview: true}, nil
	})
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	manager.SetAutoReworker(func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
		if mutated {
			return reworkpkg.Decision{}, nil
		}
		mutated = true
		return reworkpkg.Decision{Reworked: true, Round: 1, Fingerprint: "finding-1"}, nil
	})
	manager.SetQueueValidator(func(context.Context, run.Request) error {
		if !mutated {
			return errors.New("terminal plan validated before recovery rework")
		}
		return nil
	})
	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})

	entry := manager.Queue().Entries[0]
	if runs != 1 || entry.ReworkAttempts != 1 || entry.PreviousFindingFingerprint != "finding-1" || entry.RecoveryPending {
		t.Fatalf("unexpected recovered rework result: runs=%d entry=%+v", runs, entry)
	}
}

func TestManagerRecoveredAutoReworkRejectsCompetingRunnerUntilRerunCompletes(t *testing.T) {
	store := NewFileStore(t.TempDir())
	queuedAt := time.Date(2026, 7, 11, 20, 10, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	reviewEnabled := true
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
		PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt, StartedAt: &startedAt, ReviewEnabled: &reviewEnabled,
	}}}); err != nil {
		t.Fatal(err)
	}

	type ownershipKey struct{}
	errPlanOwned := errors.New("competing runner rejected")
	var ownerMu sync.Mutex
	ownerHeld := false
	owner := func(ctx context.Context, _ run.Request, operation func(context.Context) error) error {
		ownerMu.Lock()
		if ownerHeld {
			ownerMu.Unlock()
			return errPlanOwned
		}
		ownerHeld = true
		ownerMu.Unlock()
		defer func() {
			ownerMu.Lock()
			ownerHeld = false
			ownerMu.Unlock()
		}()
		return operation(context.WithValue(ctx, ownershipKey{}, true))
	}

	var runs atomic.Int32
	var unownedPhases atomic.Int32
	manager, err := NewWithStore(context.Background(), func(ctx context.Context, _ run.Request) error {
		runs.Add(1)
		if owned, _ := ctx.Value(ownershipKey{}).(bool); !owned {
			unownedPhases.Add(1)
		}
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	manager.SetPlanOwner(owner)
	manager.SetRecoveryInspector(func(ctx context.Context, _ string) (RecoveryInspection, error) {
		if owned, _ := ctx.Value(ownershipKey{}).(bool); !owned {
			unownedPhases.Add(1)
		}
		return RecoveryInspection{TerminalReview: true}, nil
	})
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	competingResult := make(chan error, 1)
	var reworkCalls atomic.Int32
	manager.SetAutoReworker(func(ctx context.Context, _ string, _ int, _ int, _ string, _ int) (reworkpkg.Decision, error) {
		if owned, _ := ctx.Value(ownershipKey{}).(bool); !owned {
			unownedPhases.Add(1)
		}
		if reworkCalls.Add(1) == 1 {
			competingResult <- owner(context.Background(), run.Request{Input: "plan-a"}, func(context.Context) error { return nil })
			return reworkpkg.Decision{Reworked: true, Round: 1, Fingerprint: "finding-1"}, nil
		}
		return reworkpkg.Decision{}, nil
	})

	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
	if err := <-competingResult; !errors.Is(err, errPlanOwned) {
		t.Fatalf("competing runner during recovered rework error = %v, want ownership contention", err)
	}
	if got := unownedPhases.Load(); got != 0 {
		t.Fatalf("recovery phases outside plan ownership = %d, want 0", got)
	}
	if got := runs.Load(); got != 1 {
		t.Fatalf("recovered reruns = %d, want 1", got)
	}
	if got := reworkCalls.Load(); got != 2 {
		t.Fatalf("automatic rework calls = %d, want mutation and post-run inspection", got)
	}
}

func TestManagerRecoveredCreatedReworkRoundRestoresPriorFingerprint(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 7, 11, 20, 15, 0, 0, time.UTC)
	startedAt := queuedAt.Add(time.Minute)
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
		PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt, StartedAt: &startedAt,
	}}}); err != nil {
		t.Fatal(err)
	}

	runs := 0
	reworkCalls := 0
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error {
		runs++
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	manager.SetRecoveryInspector(func(context.Context, string) (RecoveryInspection, error) {
		return RecoveryInspection{ReworkRound: 1, PreviousFindingFingerprint: "finding-1"}, nil
	})
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 3}); err != nil {
		t.Fatal(err)
	}
	manager.SetAutoReworker(func(_ context.Context, _ string, _ int, attempts int, previous string, _ int) (reworkpkg.Decision, error) {
		reworkCalls++
		if attempts != 1 || previous != "finding-1" {
			t.Errorf("recovered rework progress = (%d, %q), want (1, finding-1)", attempts, previous)
		}
		return reworkpkg.Decision{Round: 1, Fingerprint: "finding-1", StopKind: reworkpkg.StopKindFindingsStalled, StopReason: "automatic rework stalled on equivalent consecutive findings"}, nil
	})
	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusFailed
	})

	entry := manager.Queue().Entries[0]
	if runs != 1 || reworkCalls != 1 {
		t.Fatalf("recovered loop = (%d runs, %d reworks), want one run and one comparison", runs, reworkCalls)
	}
	if entry.ReworkAttempts != 1 || entry.PreviousFindingFingerprint != "finding-1" || entry.RecoveryPending {
		t.Fatalf("unexpected recovered progress: %+v", entry)
	}
}

func TestManagerRecoveredApprovedReviewSucceedsWithoutRerun(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 7, 11, 20, 30, 0, 0, time.UTC)
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt}}}); err != nil {
		t.Fatal(err)
	}

	runs := 0
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { runs++; return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	manager.SetRecoveryInspector(func(context.Context, string) (RecoveryInspection, error) {
		return RecoveryInspection{TerminalReview: true}, nil
	})
	manager.SetQueueValidator(func(context.Context, run.Request) error { return errors.New("completed plan is not runnable") })
	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
	if runs != 0 {
		t.Fatalf("recovered approved plan ran %d times, want 0", runs)
	}
}

func TestManagerRecoveredPendingReviewRunsBeforeTerminalHandling(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 7, 11, 20, 45, 0, 0, time.UTC)
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt}}}); err != nil {
		t.Fatal(err)
	}

	runs := 0
	reviewed := false
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { runs++; return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}
	manager.SetRecoveryInspector(func(context.Context, string) (RecoveryInspection, error) {
		if reviewed {
			return RecoveryInspection{SlicesComplete: true, TerminalReview: true}, nil
		}
		return RecoveryInspection{SlicesComplete: true, ReviewPending: true}, nil
	})
	reviews := 0
	manager.SetRecoveryReviewer(func(context.Context, run.Request) error {
		reviews++
		reviewed = true
		return nil
	})
	manager.SetQueueValidator(func(context.Context, run.Request) error { return errors.New("slice-complete plan is not runnable") })
	manager.Drain()
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
	if runs != 0 || reviews != 1 {
		t.Fatalf("recovered pending review calls = (%d runs, %d reviews), want (0, 1)", runs, reviews)
	}
}

func TestManagerQueueStopDuringRecoveryInspectionKeepsFinalizationResumable(t *testing.T) {
	tests := []struct {
		name        string
		inspection  RecoveryInspection
		pullRequest bool
	}{
		{name: "pending review", inspection: RecoveryInspection{SlicesComplete: true, ReviewPending: true}},
		{name: "terminal review pending pull request", inspection: RecoveryInspection{SlicesComplete: true, TerminalReview: true}, pullRequest: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewFileStore(t.TempDir())
			queuedAt := time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC)
			startedAt := queuedAt.Add(time.Minute)
			options := runtimeconfig.RunOptionsPatch{}.WithReviewEnabled(true).WithPullRequest(test.pullRequest)
			if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
				PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt, StartedAt: &startedAt, RunOptions: &options,
			}}}); err != nil {
				t.Fatal(err)
			}

			var runs atomic.Int32
			manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error {
				runs.Add(1)
				return nil
			}, nil, store)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.RecoverInterruptedRuns(); err != nil {
				t.Fatal(err)
			}
			inspections := 0
			manager.SetRecoveryInspector(func(context.Context, string) (RecoveryInspection, error) {
				inspections++
				manager.RequestStop()
				return test.inspection, nil
			})
			reviews := 0
			manager.SetRecoveryReviewer(func(context.Context, run.Request) error {
				reviews++
				return nil
			})

			manager.Drain()

			snapshot := manager.Queue()
			if len(snapshot.Entries) != 1 {
				t.Fatalf("stopped recovery queue = %+v, want one entry", snapshot)
			}
			entry := snapshot.Entries[0]
			if entry.Status != QueueStatusPending || !entry.RecoveryPending || entry.StartedAt != nil || entry.FinishedAt != nil {
				t.Fatalf("stopped recovery entry is not resumable: %+v", entry)
			}
			if inspections != 1 || reviews != 0 || runs.Load() != 0 {
				t.Fatalf("stopped recovery calls = (%d inspections, %d finalizations, %d runs), want (1, 0, 0)", inspections, reviews, runs.Load())
			}
			if active := manager.ActiveStatuses(); len(active) != 0 {
				t.Fatalf("stopped recovery remained active: %+v", active)
			}
			assertRecoveredQueueEntry(t, store, 0, "")

			resumed, err := NewWithStore(context.Background(), func(context.Context, run.Request) error {
				runs.Add(1)
				return nil
			}, nil, store)
			if err != nil {
				t.Fatal(err)
			}
			finalized := false
			resumed.SetRecoveryInspector(func(context.Context, string) (RecoveryInspection, error) {
				if finalized {
					return RecoveryInspection{SlicesComplete: true, TerminalReview: true}, nil
				}
				return test.inspection, nil
			})
			resumedReviews := 0
			resumed.SetRecoveryReviewer(func(_ context.Context, request run.Request) error {
				resumedReviews++
				if request.PullRequest != test.pullRequest {
					t.Errorf("resumed pull request option = %v, want %v", request.PullRequest, test.pullRequest)
				}
				finalized = true
				return nil
			})

			resumed.Drain()
			waitForManagerQueue(t, resumed, func(snapshot QueueSnapshot) bool {
				return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
			})
			if resumedReviews != 1 || runs.Load() != 0 {
				t.Fatalf("resumed recovery calls = (%d finalizations, %d runs), want (1, 0)", resumedReviews, runs.Load())
			}
		})
	}
}

func TestManagerAutoReworkConcurrentRecoveryClaimsOneActiveSlot(t *testing.T) {
	store := NewFileStore(t.TempDir())
	queuedAt := time.Date(2026, 7, 11, 20, 50, 0, 0, time.UTC)
	reviewEnabled := true
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
		PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt, ReviewEnabled: &reviewEnabled,
	}}}); err != nil {
		t.Fatal(err)
	}

	var runs atomic.Int32
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error {
		runs.Add(1)
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RecoverInterruptedRuns(); err != nil {
		t.Fatal(err)
	}

	var reviewed atomic.Bool
	manager.SetRecoveryInspector(func(context.Context, string) (RecoveryInspection, error) {
		if reviewed.Load() {
			return RecoveryInspection{SlicesComplete: true, TerminalReview: true}, nil
		}
		return RecoveryInspection{SlicesComplete: true, ReviewPending: true}, nil
	})
	reviewStarted := make(chan struct{})
	releaseReview := make(chan struct{})
	defer func() {
		select {
		case <-releaseReview:
		default:
			close(releaseReview)
		}
	}()
	var reviews atomic.Int32
	manager.SetRecoveryReviewer(func(context.Context, run.Request) error {
		if reviews.Add(1) == 1 {
			close(reviewStarted)
		}
		<-releaseReview
		reviewed.Store(true)
		return nil
	})

	firstDrainDone := make(chan struct{})
	go func() {
		manager.Drain()
		close(firstDrainDone)
	}()
	select {
	case <-reviewStarted:
	case <-time.After(time.Second):
		t.Fatal("recovered review did not start")
	}

	snapshot := manager.Queue()
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != QueueStatusRunning || !snapshot.Entries[0].RecoveryPending {
		t.Fatalf("recovered entry was not durably claimed: %+v", snapshot)
	}
	active := manager.ActiveStatuses()
	if len(active) != 1 || active[0].PlanID != "plan-a" {
		t.Fatalf("recovery did not occupy one active queue slot: %+v", active)
	}
	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Entries) != 1 || persisted.Entries[0].Status != QueueStatusRunning || !persisted.Entries[0].RecoveryPending {
		t.Fatalf("recovered claim was not persisted before review: %+v", persisted)
	}

	secondDrainDone := make(chan struct{})
	go func() {
		manager.Drain()
		close(secondDrainDone)
	}()
	select {
	case <-secondDrainDone:
	case <-time.After(time.Second):
		t.Fatal("concurrent drain blocked on duplicate recovery work")
	}
	if got := reviews.Load(); got != 1 {
		t.Fatalf("recovery reviews = %d, want 1", got)
	}

	close(releaseReview)
	select {
	case <-firstDrainDone:
	case <-time.After(time.Second):
		t.Fatal("first drain did not finish after recovered review")
	}
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
	if got := runs.Load(); got != 0 {
		t.Fatalf("recovered completed plan ran %d times, want 0", got)
	}
}

func TestManagerRecoveredSliceCompleteReviewOutcomesRemainSuccessful(t *testing.T) {
	tests := []struct {
		name          string
		reviewEnabled bool
		inspection    RecoveryInspection
		wantReviews   int
	}{
		{name: "review disabled", reviewEnabled: false, inspection: RecoveryInspection{SlicesComplete: true, ReviewPending: true}, wantReviews: 1},
		{name: "recorded review error", reviewEnabled: true, inspection: RecoveryInspection{SlicesComplete: true}, wantReviews: 1},
		{name: "persisted terminal review", reviewEnabled: true, inspection: RecoveryInspection{SlicesComplete: true, TerminalReview: true}, wantReviews: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			store := NewFileStore(dir)
			queuedAt := time.Date(2026, 7, 11, 21, 0, 0, 0, time.UTC)
			if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{PlanID: "plan-a", Status: QueueStatusRunning, QueuedAt: queuedAt, ReviewEnabled: new(test.reviewEnabled)}}}); err != nil {
				t.Fatal(err)
			}
			runs := 0
			reviews := 0
			manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { runs++; return nil }, nil, store)
			if err != nil {
				t.Fatal(err)
			}
			if err := manager.RecoverInterruptedRuns(); err != nil {
				t.Fatal(err)
			}
			manager.SetRecoveryInspector(func(context.Context, string) (RecoveryInspection, error) { return test.inspection, nil })
			manager.SetRecoveryReviewer(func(context.Context, run.Request) error {
				reviews++
				return nil
			})
			manager.SetQueueValidator(func(context.Context, run.Request) error { return errors.New("slice-complete plan is not runnable") })
			manager.Drain()
			waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
				return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
			})
			if runs != 0 || reviews != test.wantReviews {
				t.Fatalf("recovered outcome calls = (%d runs, %d reviews), want (0, %d)", runs, reviews, test.wantReviews)
			}
		})
	}
}

func TestManagerAutoReworkStopReasonFailsWithoutAnotherRun(t *testing.T) {
	runs := 0
	manager := New(context.Background(), func(context.Context, run.Request) error { runs++; return nil }, nil)
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	manager.SetAutoReworker(func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
		return reworkpkg.Decision{StopKind: reworkpkg.StopKindCapExhausted, StopReason: "automatic rework cap exhausted after 1 cycles"}, nil
	})
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusFailed
	})
	if runs != 1 {
		t.Fatalf("runs = %d, want 1", runs)
	}
	if got := manager.Queue().Entries[0].Error; !strings.Contains(got, "cap exhausted") {
		t.Fatalf("error = %q", got)
	}
}

func TestManagerAutoReworkStopBannerIncludesDecisionFindings(t *testing.T) {
	finding := plan.ReviewFinding{
		Severity:   "major",
		File:       "internal/runqueue/manager.go",
		Line:       807,
		Message:    "preserve findings across the queue seam",
		Suggestion: "return the rework decision directly",
	}
	manager := New(context.Background(), func(context.Context, run.Request) error { return nil }, nil)
	if err := manager.SetAutoReworkPolicy(runtimeconfig.AutoReworkPolicy{Enabled: true, MaxAttempts: 2}); err != nil {
		t.Fatal(err)
	}
	manager.SetAutoReworker(func(context.Context, string, int, int, string, int) (reworkpkg.Decision, error) {
		return reworkpkg.Decision{
			StopKind:   reworkpkg.StopKindFindingsStalled,
			StopReason: "automatic rework stalled on equivalent consecutive findings",
			Findings:   []plan.ReviewFinding{finding},
		}, nil
	})
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	waitForManagerQueue(t, manager, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusFailed
	})

	got := manager.Queue().Entries[0].Error
	for _, want := range []string{"THE LOOP IS GOING IN CIRCLES", "internal/runqueue/manager.go:807", finding.Message, finding.Suggestion} {
		if !strings.Contains(got, want) {
			t.Errorf("queue stop banner %q does not contain %q", got, want)
		}
	}
}

func TestManagerDrainUsesInjectedClockForStatusTimestamps(t *testing.T) {
	const planID = "20260427-1810-example"
	startedAt := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
	finishedAt := startedAt.Add(time.Minute)
	queuedAt := startedAt.Add(-time.Minute)
	var clockCalls atomic.Int32
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		<-release
		return nil
	}, func() time.Time {
		switch clockCalls.Add(1) {
		case 1:
			return queuedAt
		case 2:
			return startedAt
		default:
			return finishedAt
		}
	})

	if _, err := runs.Enqueue(queueTestRequest(planID)); err != nil {
		t.Fatal(err)
	}
	status := runs.Status(planID)
	if status == nil || status.StartedAt == nil || !status.StartedAt.Equal(startedAt) {
		t.Fatalf("expected deterministic started_at %s, got %+v", startedAt, status)
	}
	close(release)
	waitForManagerQueue(t, runs, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
	status = runs.Status(planID)
	if status == nil || status.FinishedAt == nil || !status.FinishedAt.Equal(finishedAt) {
		t.Fatalf("expected deterministic finished_at %s, got %+v", finishedAt, status)
	}
}

func TestManagerQueueStartsInFIFOOrder(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		<-release
		return nil
	}, nil)

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != "plan-a" {
		t.Fatalf("expected plan-a to start first, got %q", got)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if got := <-started; got != "plan-b" {
		t.Fatalf("expected plan-b to start second, got %q", got)
	}
}

func TestManagerQueueDedupePendingAndActivePlans(t *testing.T) {
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		<-release
		return nil
	}, nil)
	t.Cleanup(func() { close(release) })

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err == nil {
		t.Fatal("expected active plan dedupe error")
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err == nil {
		t.Fatal("expected pending plan dedupe error")
	}
}

func TestManagerRequestStopMarksStopAndPausesDrain(t *testing.T) {
	started := make(chan string, 1)
	manager := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		return nil
	}, nil)

	manager.RequestStop()
	if !manager.StopRequested() {
		t.Fatal("expected stop request to be recorded")
	}
	if _, err := manager.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	snapshot := manager.Queue()
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Status != QueueStatusPending {
		t.Fatalf("expected stopped manager to keep entry pending, got %+v", snapshot)
	}
	select {
	case got := <-started:
		t.Fatalf("expected stopped manager not to start queued run, got %q", got)
	default:
	}
}

func TestManagerDequeueRemovesPendingPlan(t *testing.T) {
	started := make(chan string, 1)
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		<-release
		return nil
	}, nil)

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	dequeued, err := runs.Dequeue("plan-b")
	if err != nil {
		t.Fatal(err)
	}
	if dequeued.Status != QueueStatusSkipped || dequeued.SkipReason != "dequeued" {
		t.Fatalf("expected skipped dequeue entry, got %+v", dequeued)
	}
	close(release)
	waitForManagerQueue(t, runs, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].PlanID == "plan-a" && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
}

func TestManagerSerializesTransitionAndDequeuePersistence(t *testing.T) {
	queuedAt := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	store := &blockingTransitionQueueStore{
		recordingQueueStore: recordingQueueStore{snapshot: QueueSnapshot{Entries: []QueueEntry{{
			PlanID: "plan-a", Status: QueueStatusPending, QueuedAt: queuedAt,
		}}}},
		transitionBlocked: make(chan struct{}),
		releaseTransition: make(chan struct{}),
		removalAppended:   make(chan struct{}),
	}
	manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}

	entry := manager.Queue().Entries[0]
	transition, apply, err := entryTransitionForResult(entry, EntryResult{Outcome: EntryOutcomeFailed, Err: errors.New("failed")}, queuedAt.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !apply {
		t.Fatal("expected failed result to require a transition")
	}

	transitionDone := make(chan error, 1)
	go func() { transitionDone <- manager.TransitionEntry(context.Background(), transition) }()
	<-store.transitionBlocked

	dequeueDone := make(chan error, 1)
	go func() {
		_, dequeueErr := manager.Dequeue("plan-a")
		dequeueDone <- dequeueErr
	}()

	removalInterleaved := false
	select {
	case <-store.removalAppended:
		removalInterleaved = true
	case <-time.After(100 * time.Millisecond):
	}
	close(store.releaseTransition)

	if err := <-transitionDone; err != nil {
		t.Fatalf("TransitionEntry() error = %v", err)
	}
	if err := <-dequeueDone; err == nil || !strings.Contains(err.Error(), "not pending") {
		t.Fatalf("Dequeue() error = %v, want not pending", err)
	}
	if removalInterleaved {
		t.Fatal("dequeue removal was appended while the entry transition was awaiting persistence")
	}

	persisted, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	inMemory := manager.Queue()
	if len(persisted.Entries) != 1 || persisted.Entries[0].Status != QueueStatusFailed {
		t.Fatalf("persisted queue = %+v, want one failed entry", persisted)
	}
	if len(inMemory.Entries) != 1 || inMemory.Entries[0].Status != QueueStatusFailed {
		t.Fatalf("in-memory queue = %+v, want one failed entry", inMemory)
	}
}

func TestManagerDequeueRejectsActivePlan(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		close(started)
		<-release
		return nil
	}, nil)
	t.Cleanup(func() { close(release) })

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := runs.Dequeue("plan-a"); err == nil {
		t.Fatal("expected active queued plan to reject dequeue")
	}
}

func TestManagerQueueExecutesOneAtATime(t *testing.T) {
	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		if request.Input == "plan-a" {
			<-releaseFirst
		}
		return nil
	}, nil)

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != "plan-a" {
		t.Fatalf("expected plan-a to start first, got %q", got)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-started:
		t.Fatalf("expected plan-b to wait, got started %q", got)
	default:
	}
	close(releaseFirst)
	if got := <-started; got != "plan-b" {
		t.Fatalf("expected plan-b after plan-a finishes, got %q", got)
	}
}

func TestManagerQueueCanRunMultiplePlansWhenConfigured(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		<-release
		return nil
	}, nil)
	runs.SetMaxParallelRuns(2)
	t.Cleanup(func() { close(release) })

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}

	got := map[string]bool{<-started: true, <-started: true}
	if !got["plan-a"] || !got["plan-b"] {
		t.Fatalf("expected plan-a and plan-b to start, got %+v", got)
	}
	snapshot := runs.Queue()
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Status != QueueStatusRunning || snapshot.Entries[1].Status != QueueStatusRunning {
		t.Fatalf("expected two running queue entries, got %+v", snapshot)
	}
}

func TestManagerQueueWaitsOnConflictingPlan(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		<-release
		return nil
	}, nil)
	runs.SetMaxParallelRuns(2)
	runs.SetQueueConflictChecker(func(ctx context.Context, candidate run.Request, active []run.Request) string {
		if candidate.Input == "plan-b" && len(active) > 0 {
			return "expected files overlap with active plan"
		}
		return ""
	})
	t.Cleanup(func() { close(release) })

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != "plan-a" {
		t.Fatalf("expected plan-a to start, got %q", got)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-started:
		t.Fatalf("expected plan-b to wait, got started %q", got)
	default:
	}

	snapshot := runs.Queue()
	if len(snapshot.Entries) != 2 || snapshot.Entries[1].Status != QueueStatusPending || !strings.Contains(snapshot.Entries[1].WaitReason, "expected files overlap") {
		t.Fatalf("expected plan-b to wait on overlap, got %+v", snapshot)
	}
}

func TestManagerQueueStartsNonConflictingPlans(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		<-release
		return nil
	}, nil)
	runs.SetMaxParallelRuns(2)
	runs.SetQueueConflictChecker(func(ctx context.Context, candidate run.Request, active []run.Request) string {
		return ""
	})
	t.Cleanup(func() { close(release) })

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{<-started: true, <-started: true}
	if !got["plan-a"] || !got["plan-b"] {
		t.Fatalf("expected both non-conflicting plans to start, got %+v", got)
	}
}

func TestManagerPreparesCandidatesOutsideMutexBeforeClaim(t *testing.T) {
	started := make(chan struct{})
	manager := New(context.Background(), func(context.Context, run.Request) error {
		close(started)
		return nil
	}, nil)
	validatorCalled := false
	conflictCalled := false
	manager.SetQueueValidator(func(context.Context, run.Request) error {
		snapshot := manager.Queue()
		validatorCalled = len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusPending
		return nil
	})
	manager.SetQueueConflictChecker(func(context.Context, run.Request, []run.Request) string {
		snapshot := manager.Queue()
		conflictCalled = len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusPending
		return ""
	})

	done := make(chan error, 1)
	go func() {
		_, err := manager.Enqueue(queueTestRequest("plan-a"))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("candidate callbacks blocked on manager mutex")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("prepared queue entry did not start")
	}
	if !validatorCalled || !conflictCalled {
		t.Fatalf("callbacks observed claimed entry: validator=%v conflict=%v", validatorCalled, conflictCalled)
	}
}

func TestManagerRerunsDrainAfterConcurrentEnqueueDuringConflictCheck(t *testing.T) {
	conflictEntered := make(chan struct{})
	releaseConflict := make(chan struct{})
	started := make(chan string, 1)
	manager := New(context.Background(), func(_ context.Context, request run.Request) error {
		started <- request.Input
		return nil
	}, nil)
	var enteredOnce sync.Once
	manager.SetQueueConflictChecker(func(_ context.Context, candidate run.Request, _ []run.Request) string {
		if candidate.Input != "plan-a" {
			return ""
		}
		enteredOnce.Do(func() { close(conflictEntered) })
		<-releaseConflict
		return "waiting for conflict"
	})

	firstEnqueue := make(chan error, 1)
	go func() {
		_, err := manager.Enqueue(queueTestRequest("plan-a"))
		firstEnqueue <- err
	}()
	select {
	case <-conflictEntered:
	case <-time.After(time.Second):
		t.Fatal("conflict checker was not called")
	}

	if _, err := manager.Enqueue(queueTestRequest("plan-b")); err != nil {
		t.Fatal(err)
	}
	close(releaseConflict)

	select {
	case err := <-firstEnqueue:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial drain did not finish")
	}
	select {
	case got := <-started:
		if got != "plan-b" {
			t.Fatalf("expected concurrently enqueued plan-b to start, got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrently enqueued plan remained pending without a drain")
	}
}

func TestManagerQueueSkipsNoLongerRunnablePlanAtDispatch(t *testing.T) {
	started := make(chan string, 3)
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		if request.Input == "plan-a" {
			<-release
		}
		return nil
	}, nil)
	runs.SetQueueValidator(func(ctx context.Context, request run.Request) error {
		if request.Input == "plan-b" {
			return errors.New("plan is no longer runnable")
		}
		return nil
	})

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != "plan-a" {
		t.Fatalf("expected plan-a to start first, got %q", got)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-c", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if got := <-started; got != "plan-c" {
		t.Fatalf("expected dispatch skip to bypass plan-b and run plan-c, got %q", got)
	}
	waitForManagerQueue(t, runs, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) >= 3 && snapshot.Entries[1].PlanID == "plan-b" && snapshot.Entries[1].Status == QueueStatusSkipped
	})
}

func TestManagerQueueRejectsDuplicateActivePlanWithParallelRuns(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		close(started)
		<-release
		return nil
	}, nil)
	runs.SetMaxParallelRuns(2)
	t.Cleanup(func() { close(release) })

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	<-started
	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err == nil {
		t.Fatal("expected duplicate active plan to be rejected")
	}
}

func TestManagerQueueContinuesAfterFailure(t *testing.T) {
	started := make(chan string, 2)
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		if request.Input == "plan-a" {
			return errors.New("boom")
		}
		return nil
	}, nil)

	if _, err := runs.Enqueue(run.Request{Input: "plan-a", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runs.Enqueue(run.Request{Input: "plan-b", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: runtimeconfig.CommitPolicySlice}}); err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != "plan-a" {
		t.Fatalf("expected plan-a first, got %q", got)
	}
	if got := <-started; got != "plan-b" {
		t.Fatalf("expected plan-b after failure, got %q", got)
	}
	waitForManagerQueue(t, runs, func(snapshot QueueSnapshot) bool {
		if len(snapshot.Entries) != 2 {
			return false
		}
		return snapshot.Entries[0].Status == QueueStatusFailed && snapshot.Entries[0].Error == "boom" && snapshot.Entries[1].Status == QueueStatusSucceeded
	})
}

func TestManagerNilStorePreservesQueueLifecycle(t *testing.T) {
	started := make(chan string, 2)
	releaseFirst := make(chan struct{})
	runs := New(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		if request.Input == "plan-a" {
			<-releaseFirst
		}
		return nil
	}, nil)

	if _, err := runs.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != "plan-a" {
		t.Fatalf("expected plan-a to start first, got %q", got)
	}
	if _, err := runs.Enqueue(queueTestRequest("plan-b")); err != nil {
		t.Fatal(err)
	}
	snapshot := runs.Queue()
	if len(snapshot.Entries) != 2 || snapshot.Entries[0].Mode != "" || snapshot.Entries[1].Mode != "" {
		t.Fatalf("expected nil-store queue entries to preserve legacy run-option shape, got %+v", snapshot)
	}
	close(releaseFirst)
	if got := <-started; got != "plan-b" {
		t.Fatalf("expected plan-b to start after plan-a, got %q", got)
	}
	waitForManagerQueue(t, runs, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 2 && snapshot.Entries[0].Status == QueueStatusSucceeded && snapshot.Entries[1].Status == QueueStatusSucceeded
	})
}

func TestManagerPersistsQueueTransitions(t *testing.T) {
	store := &recordingQueueStore{}
	started := make(chan string, 3)
	releaseFirst := make(chan struct{})
	runs, err := NewWithStore(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request.Input
		if request.Input == "plan-a" {
			<-releaseFirst
			return errors.New("boom")
		}
		return nil
	}, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	runs.SetMaxParallelRuns(2)
	runs.SetQueueConflictChecker(func(ctx context.Context, candidate run.Request, active []run.Request) string {
		if candidate.Input == "plan-b" && len(active) > 0 {
			return "expected files overlap with active plan"
		}
		return ""
	})

	if _, err := runs.Enqueue(queueTestRequest("plan-a")); err != nil {
		t.Fatal(err)
	}
	if got := <-started; got != "plan-a" {
		t.Fatalf("expected plan-a to start first, got %q", got)
	}
	if _, err := runs.Enqueue(queueTestRequest("plan-b")); err != nil {
		t.Fatal(err)
	}
	waitForStoreTransitions(t, store, func(transitions []QueueTransition) bool {
		return len(transitions) >= 4 && transitions[3].Entry != nil && transitions[3].Entry.PlanID == "plan-b" && strings.Contains(transitions[3].Entry.WaitReason, "expected files overlap")
	})
	if _, err := runs.Dequeue("plan-b"); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	waitForManagerQueue(t, runs, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].PlanID == "plan-a" && snapshot.Entries[0].Status == QueueStatusFailed
	})
	if _, err := runs.Enqueue(queueTestRequest("plan-c")); err != nil {
		t.Fatal(err)
	}
	waitForManagerQueue(t, runs, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 2 && snapshot.Entries[0].Status == QueueStatusFailed && snapshot.Entries[1].Status == QueueStatusSucceeded
	})

	transitions := waitForStoreTransitions(t, store, func(transitions []QueueTransition) bool { return len(transitions) == 9 })
	want := []struct {
		planID     string
		status     QueueStatus
		removed    bool
		waitReason string
		skipReason string
		runError   string
	}{
		{planID: "plan-a", status: QueueStatusPending},
		{planID: "plan-a", status: QueueStatusRunning},
		{planID: "plan-b", status: QueueStatusPending},
		{planID: "plan-b", status: QueueStatusPending, waitReason: "expected files overlap"},
		{planID: "plan-b", status: QueueStatusSkipped, removed: true, skipReason: "dequeued"},
		{planID: "plan-a", status: QueueStatusFailed, runError: "boom"},
		{planID: "plan-c", status: QueueStatusPending},
		{planID: "plan-c", status: QueueStatusRunning},
		{planID: "plan-c", status: QueueStatusSucceeded},
	}
	for i, wantTransition := range want {
		got := transitions[i]
		if got.Removed != wantTransition.removed {
			t.Fatalf("transition %d removed = %v, want %v", i, got.Removed, wantTransition.removed)
		}
		if got.Entry == nil {
			t.Fatalf("transition %d missing entry", i)
		}
		if got.Entry.PlanID != wantTransition.planID || got.Entry.Status != wantTransition.status {
			t.Fatalf("transition %d = %+v, want plan %s status %s", i, got.Entry, wantTransition.planID, wantTransition.status)
		}
		if wantTransition.waitReason != "" && !strings.Contains(got.Entry.WaitReason, wantTransition.waitReason) {
			t.Fatalf("transition %d wait reason = %q, want %q", i, got.Entry.WaitReason, wantTransition.waitReason)
		}
		if got.Entry.SkipReason != wantTransition.skipReason {
			t.Fatalf("transition %d skip reason = %q, want %q", i, got.Entry.SkipReason, wantTransition.skipReason)
		}
		if got.Entry.Error != wantTransition.runError {
			t.Fatalf("transition %d error = %q, want %q", i, got.Entry.Error, wantTransition.runError)
		}
		if got.Entry.RunOptions == nil || got.Entry.RunOptions.Mode != run.ModeRun || got.Entry.RunOptions.CommitPolicy != run.CommitPolicySlice {
			t.Fatalf("transition %d missing persisted run options: %+v", i, got.Entry)
		}
	}
}

func TestManagerLoadsQueueFromDisk(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	queuedAt := time.Date(2026, 6, 28, 3, 0, 0, 0, time.UTC)
	options := runtimeconfig.RunOptionsPatch{
		Mode: run.ModeRun, CommitPolicy: run.CommitPolicySlice,
		ExecutionMode: run.ExecutionModeCurrent, Agent: run.AgentCodex,
	}.WithContinue(true).WithPullRequest(true)
	if err := store.SaveSnapshot(QueueSnapshot{Entries: []QueueEntry{{
		PlanID: "plan-a", Status: QueueStatusPending, QueuedAt: queuedAt, RunOptions: &options,
	}}}); err != nil {
		t.Fatal(err)
	}

	started := make(chan run.Request, 1)
	runs, err := NewWithStore(context.Background(), func(ctx context.Context, request run.Request) error {
		started <- request
		return nil
	}, nil, NewFileStore(dir))
	if err != nil {
		t.Fatal(err)
	}
	loaded := runs.Queue()
	if len(loaded.Entries) != 1 || loaded.Entries[0].PlanID != "plan-a" || loaded.Entries[0].Status != QueueStatusPending {
		t.Fatalf("expected pending plan loaded from disk, got %+v", loaded)
	}

	runs.drainQueue()
	request := <-started
	if request.Input != "plan-a" || request.Mode != run.ModeRun || !request.Continue || request.CommitPolicy != run.CommitPolicySlice || request.ExecutionMode != run.ExecutionModeCurrent || request.Agent != run.AgentCodex || !request.PullRequest {
		t.Fatalf("unexpected hydrated request from disk: %+v", request)
	}
	waitForManagerQueue(t, runs, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSucceeded
	})
}

func TestQueueEntryRunRequestDecodesLegacyFlatFixture(t *testing.T) {
	fixtureDir := filepath.Join("testdata", "legacy-flat-queue")
	store := NewFileStorePaths(filepath.Join(fixtureDir, queueSnapshotFilename), filepath.Join(fixtureDir, queueEventLogFilename))
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 3 {
		t.Fatalf("legacy fixture entries = %d, want 3", len(snapshot.Entries))
	}

	expected := map[string]runtimeconfig.ResolvedRunOptions{
		"legacy-all-options": {
			Mode: run.ModeRun, MaxSlices: 3, Continue: true, CommitPolicy: run.CommitPolicySlice,
			ExecutionMode: run.ExecutionModeCurrent, Agent: run.AgentCodex, PullRequest: true,
			ReviewEnabled: false, SessionTimeout: 45 * time.Minute,
		},
		"legacy-pointer-options-unset": {
			Mode: run.ModeStep, MaxSlices: 1, CommitPolicy: run.CommitPolicyNone,
			ExecutionMode: run.ExecutionModeIsolated, Agent: run.AgentPi,
			ReviewEnabled: true, SessionTimeout: runtimeconfig.DefaultSessionTimeout,
		},
		"legacy-plan-policy": {
			Mode: run.ModeRun, CommitPolicy: run.CommitPolicyPlan,
			ExecutionMode: run.ExecutionModeIsolated, Agent: run.AgentPi,
			ReviewEnabled: true, SessionTimeout: runtimeconfig.DefaultSessionTimeout,
		},
	}
	for _, entry := range snapshot.Entries {
		if entry.RunOptions != nil {
			t.Fatalf("legacy entry %s unexpectedly decoded nested options: %+v", entry.PlanID, entry.RunOptions)
		}
		got := entry.runRequest()
		want, ok := expected[entry.PlanID]
		if !ok {
			t.Fatalf("unexpected legacy fixture entry %q", entry.PlanID)
		}
		if got.Input != entry.PlanID || got.ResolvedRunOptions != want {
			t.Fatalf("legacy request %s changed\n got: %+v\nwant: %+v", entry.PlanID, got.ResolvedRunOptions, want)
		}
	}
}

func TestQueueEntryRunRequestRestoresDefaultRuntimeConfig(t *testing.T) {
	config, err := runtimeconfig.NewConfigFromStages(runtimeconfig.DefaultRunOptionsPatch(), runtimeconfig.RunOptionsPatch{})
	if err != nil {
		t.Fatal(err)
	}
	original := run.Request{Input: "plan-a", ResolvedRunOptions: config.ResolvedOptions()}
	entry := QueueEntry{Status: QueueStatusPending, QueuedAt: time.Date(2026, 7, 9, 21, 30, 0, 0, time.UTC), request: original}
	entry.prepareForPersistence()

	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var restored QueueEntry
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	restoredRequest := restored.runRequest()

	if restoredRequest.Input != original.Input || restoredRequest.ResolvedRunOptions != original.ResolvedRunOptions {
		t.Fatalf("unexpected restored request\n got: %+v\nwant: %+v", restoredRequest, original)
	}
	if !restoredRequest.ReviewEnabled || restoredRequest.SessionTimeout != runtimeconfig.DefaultSessionTimeout {
		t.Fatalf("expected restored defaults to include review and session timeout, got %+v", restoredRequest.ResolvedRunOptions)
	}

	stepRequest := QueueEntry{PlanID: "plan-step", Mode: run.ModeStep}.runRequest()
	if stepRequest.MaxSlices != 1 {
		t.Fatalf("expected restored step-mode entry to derive max slices from mode, got %+v", stepRequest.ResolvedRunOptions)
	}
}

func TestQueueEntryRunRequestRoundTripsNestedOptionsThroughStore(t *testing.T) {
	cases := []struct {
		name      string
		planID    string
		overrides runtimeconfig.RunOptionsPatch
	}{
		{name: "unset pointers use defaults", planID: "plan-defaults"},
		{
			name:      "explicit false and zero",
			planID:    "plan-review-disabled",
			overrides: runtimeconfig.RunOptionsPatch{}.WithReviewEnabled(false).WithSessionTimeout(0),
		},
		{
			name:      "representative nonzero options",
			planID:    "plan-custom-timeout",
			overrides: runtimeconfig.RunOptionsPatch{Mode: run.ModeRun, Agent: run.AgentCodex}.WithContinue(true).WithPullRequest(true).WithSessionTimeout(45 * time.Minute),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := runtimeconfig.NewConfigFromStages(runtimeconfig.DefaultRunOptionsPatch(), tc.overrides)
			if err != nil {
				t.Fatal(err)
			}
			original := run.Request{Input: tc.planID, ResolvedRunOptions: config.ResolvedOptions()}
			dir := t.TempDir()
			store := NewFileStore(dir)
			manager, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { return nil }, nil, store)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.Enqueue(original); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveSnapshot(manager.Queue()); err != nil {
				t.Fatal(err)
			}

			reloaded, err := NewFileStore(dir).Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(reloaded.Entries) != 1 || reloaded.Entries[0].RunOptions == nil {
				t.Fatalf("reloaded queue missing nested run options: %+v", reloaded)
			}
			restored := reloaded.Entries[0]
			if restored.RunOptions.ReviewEnabled == nil || *restored.RunOptions.ReviewEnabled != original.ReviewEnabled || restored.RunOptions.SessionTimeout == nil || *restored.RunOptions.SessionTimeout != original.SessionTimeout {
				t.Fatalf("pointer options did not round trip: %+v", restored.RunOptions)
			}
			if got := restored.runRequest(); got.Input != original.Input || got.ResolvedRunOptions != original.ResolvedRunOptions {
				t.Fatalf("unexpected restored request\n got: %+v\nwant: %+v", got, original)
			}

			body, err := os.ReadFile(filepath.Join(dir, queueSnapshotFilename)) // #nosec G304 -- test reads its own temporary queue snapshot.
			if err != nil {
				t.Fatal(err)
			}
			var persisted struct {
				Entries []map[string]json.RawMessage `json:"entries"`
			}
			if err := json.Unmarshal(body, &persisted); err != nil {
				t.Fatal(err)
			}
			if _, ok := persisted.Entries[0]["run_options"]; !ok {
				t.Fatalf("snapshot missing run_options: %s", body)
			}
			for _, legacy := range []string{"mode", "max_slices", "continue", "commit_policy", "execution_mode", "agent", "pull_request", "review_enabled", "session_timeout"} {
				if _, ok := persisted.Entries[0][legacy]; ok {
					t.Fatalf("snapshot wrote legacy field %q: %s", legacy, body)
				}
			}
		})
	}
}

func TestQueueEntryJSONOmitsQueuedRequest(t *testing.T) {
	entry := QueueEntry{PlanID: "plan-a", Status: QueueStatusPending, QueuedAt: time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC), request: run.Request{Input: "secret-plan"}}
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, want := range []string{`"plan_id":"plan-a"`, `"status":"pending"`, `"queued_at":"2026-05-24T12:00:00Z"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected queue entry JSON to include %s, got %s", want, body)
		}
	}
	if strings.Contains(body, "secret-plan") || strings.Contains(body, "request") {
		t.Fatalf("expected queued request to remain unexported from JSON, got %s", body)
	}
}

func TestManagerRejectsRemovedPlanPolicyForNewAndPersistedRuns(t *testing.T) {
	manager := New(context.Background(), func(context.Context, run.Request) error { return nil }, nil)
	_, err := manager.Enqueue(run.Request{Input: "new-plan", ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{CommitPolicy: run.CommitPolicyPlan}})
	if err == nil || !strings.Contains(err.Error(), "plan was removed; use slice or none") {
		t.Fatalf("expected enqueue migration error, got %v", err)
	}

	dir := t.TempDir()
	store := NewFileStore(dir)
	legacySnapshot := `{"entries":[{"plan_id":"legacy-plan","status":"pending","queued_at":"2026-07-20T01:03:00Z","commit_policy":"plan"}]}`
	if err := os.WriteFile(filepath.Join(dir, queueSnapshotFilename), []byte(legacySnapshot), 0o600); err != nil {
		t.Fatal(err)
	}
	executions := 0
	loaded, err := NewWithStore(context.Background(), func(context.Context, run.Request) error { executions++; return nil }, nil, store)
	if err != nil {
		t.Fatal(err)
	}
	loaded.Drain()
	waitForManagerQueue(t, loaded, func(snapshot QueueSnapshot) bool {
		return len(snapshot.Entries) == 1 && snapshot.Entries[0].Status == QueueStatusSkipped
	})
	entry := loaded.Queue().Entries[0]
	if executions != 0 || entry.runRequest().CommitPolicy != run.CommitPolicyPlan || !strings.Contains(entry.SkipReason, "replace or re-enqueue") {
		t.Fatalf("legacy queue entry was not preserved and blocked actionably: executions=%d entry=%+v", executions, entry)
	}
}

func waitForManagerQueue(t *testing.T, runs *Manager, done func(QueueSnapshot) bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if done(runs.Queue()) {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("queue did not reach expected state: %+v", runs.Queue())
}

func queueTestRequest(planID string) run.Request {
	return run.Request{Input: planID, ResolvedRunOptions: runtimeconfig.ResolvedRunOptions{Mode: run.ModeRun, CommitPolicy: run.CommitPolicySlice, ExecutionMode: run.ExecutionModeIsolated, Agent: run.AgentPi, ReviewEnabled: true}}
}

func assertRecoveredQueueEntry(t *testing.T, store Store, attempts int, fingerprint string) {
	t.Helper()
	snapshot, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 {
		t.Fatalf("recovered snapshot has %d entries, want 1", len(snapshot.Entries))
	}
	entry := snapshot.Entries[0]
	if entry.Status != QueueStatusPending || entry.StartedAt != nil || entry.FinishedAt != nil {
		t.Fatalf("recovered entry is not resumable: %+v", entry)
	}
	if entry.ReworkAttempts != attempts || entry.PreviousFindingFingerprint != fingerprint {
		t.Fatalf("recovered progress = (%d, %q), want (%d, %q)", entry.ReworkAttempts, entry.PreviousFindingFingerprint, attempts, fingerprint)
	}
	if !entry.RecoveryPending {
		t.Fatalf("recovered entry is missing its recovery phase: %+v", entry)
	}
}

type blockingTransitionQueueStore struct {
	recordingQueueStore
	blockOnce         sync.Once
	removalOnce       sync.Once
	transitionBlocked chan struct{}
	releaseTransition chan struct{}
	removalAppended   chan struct{}
}

func (s *blockingTransitionQueueStore) AppendTransition(transition QueueTransition) error {
	if !transition.Removed && transition.Entry != nil && transition.Entry.Status == QueueStatusFailed {
		s.blockOnce.Do(func() {
			close(s.transitionBlocked)
			<-s.releaseTransition
		})
	}
	if err := s.recordingQueueStore.AppendTransition(transition); err != nil {
		return err
	}
	if transition.Removed {
		s.removalOnce.Do(func() { close(s.removalAppended) })
	}
	return nil
}

type recordingQueueStore struct {
	mu          sync.Mutex
	snapshot    QueueSnapshot
	transitions []QueueTransition
	appendCalls int
	failAt      int
	appendErr   error
}

func (s *recordingQueueStore) Load() (QueueSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot, nil
}

func (s *recordingQueueStore) SaveSnapshot(snapshot QueueSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = snapshot
	return nil
}

func (s *recordingQueueStore) AppendTransition(transition QueueTransition) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.appendCalls++
	if s.appendCalls == s.failAt {
		return s.appendErr
	}
	cloned := cloneQueueTransitionForTest(transition)
	s.transitions = append(s.transitions, cloned)
	applyQueueTransition(&s.snapshot, cloned)
	return nil
}

func (s *recordingQueueStore) recordedTransitions() []QueueTransition {
	s.mu.Lock()
	defer s.mu.Unlock()
	transitions := make([]QueueTransition, len(s.transitions))
	for i, transition := range s.transitions {
		transitions[i] = cloneQueueTransitionForTest(transition)
	}
	return transitions
}

func waitForStoreTransitions(t *testing.T, store *recordingQueueStore, done func([]QueueTransition) bool) []QueueTransition {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		transitions := store.recordedTransitions()
		if done(transitions) {
			return transitions
		}
		runtime.Gosched()
	}
	transitions := store.recordedTransitions()
	t.Fatalf("queue store did not reach expected transitions: %+v", transitions)
	return nil
}

func cloneQueueTransitionForTest(transition QueueTransition) QueueTransition {
	cloned := transition
	if transition.Entry != nil {
		entry := *transition.Entry
		cloned.Entry = &entry
	}
	return cloned
}
