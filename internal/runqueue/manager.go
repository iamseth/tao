package runqueue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	reworkpkg "github.com/iamseth/tao/internal/rework"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

type Executor func(context.Context, run.Request) error

// PlanOwner keeps cross-process ownership of one plan while a queued entry
// executes and performs any automatic rework cycles.
type PlanOwner func(context.Context, run.Request, func(context.Context) error) error

type Validator func(context.Context, run.Request) error

// RecoveryInspection describes persisted plan state needed to establish an
// automatic-rework baseline and resume interrupted runs.
type RecoveryInspection struct {
	SlicesComplete             bool
	ReviewPending              bool
	TerminalReview             bool
	ReworkRound                int
	PreviousFindingFingerprint string
}

// RecoveryInspector inspects persisted plan state for queue execution and recovery.
type RecoveryInspector func(context.Context, string) (RecoveryInspection, error)

// RecoveryReviewer resumes the remaining finalization phases of an interrupted
// slice-complete run. The name is retained for compatibility with existing
// queue wiring.
type RecoveryReviewer func(context.Context, run.Request) error

type ConflictChecker func(context.Context, run.Request, []run.Request) string

// AutoReworker inspects the freshly persisted review and, when appropriate,
// applies one ordinary rework mutation. The integer arguments are the entry's
// baseline round, automatic attempts since that baseline, and maximum attempts.
// Decision.Round is the absolute deterministic rework round after the mutation.
type AutoReworker = reworkpkg.DecisionFunc

type RunStatus struct {
	Active       bool       `json:"active"`
	PlanID       string     `json:"plan_id"`
	Mode         string     `json:"mode"`
	CommitPolicy string     `json:"commit_policy,omitempty"`
	Continue     bool       `json:"continue,omitempty"`
	PullRequest  bool       `json:"pull_request,omitempty"`
	StartedAt    *time.Time `json:"started_at,omitempty"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Error        string     `json:"error,omitempty"`
}

type QueueStatus string

const (
	QueueStatusPending   QueueStatus = "pending"
	QueueStatusRunning   QueueStatus = "running"
	QueueStatusSucceeded QueueStatus = "succeeded"
	QueueStatusFailed    QueueStatus = "failed"
	QueueStatusSkipped   QueueStatus = "skipped"
)

type QueueEntry struct {
	PlanID     string      `json:"plan_id"`
	Status     QueueStatus `json:"status"`
	QueuedAt   time.Time   `json:"queued_at"`
	StartedAt  *time.Time  `json:"started_at,omitempty"`
	FinishedAt *time.Time  `json:"finished_at,omitempty"`
	Error      string      `json:"error,omitempty"`
	SkipReason string      `json:"skip_reason,omitempty"`
	WaitReason string      `json:"wait_reason,omitempty"`

	// RunOptions may use omitempty safely because the queue store writes whole-file
	// snapshots (with atomicWriteQueueFile) plus append-log records, never mergeJSON.
	RunOptions       *runtimeconfig.RunOptionsPatch  `json:"run_options,omitempty"`
	AutoReworkPolicy *runtimeconfig.AutoReworkPolicy `json:"auto_rework_policy,omitempty"`

	// Deprecated: legacy flat run options are decode-only; new queue records use RunOptions.
	Mode           run.Mode          `json:"-"`
	MaxSlices      int               `json:"-"`
	Continue       bool              `json:"-"`
	CommitPolicy   run.CommitPolicy  `json:"-"`
	ExecutionMode  run.ExecutionMode `json:"-"`
	Agent          run.AgentKind     `json:"-"`
	PullRequest    bool              `json:"-"`
	ReviewEnabled  *bool             `json:"-"`
	SessionTimeout *time.Duration    `json:"-"`

	// ReworkBaselineRound anchors per-entry automatic attempts independently of
	// manual rework history. The pointer distinguishes an explicit zero baseline
	// from queue snapshots written before baseline tracking was added.
	ReworkBaselineRound *int `json:"rework_baseline_round,omitempty"`

	// ReworkAttempts and PreviousFindingFingerprint are durable loop progress
	// after ReworkBaselineRound.
	ReworkAttempts             int    `json:"rework_attempts,omitempty"`
	PreviousFindingFingerprint string `json:"previous_finding_fingerprint,omitempty"`

	// RecoveryPending marks an interrupted run or deliberate stopped-plan
	// decision whose persisted state must be inspected before ordinary validation.
	RecoveryPending bool `json:"recovery_pending,omitempty"`

	request run.Request
}

// UnmarshalJSON retains read-only support for queue records written before
// run_options replaced the flat option fields.
func (entry *QueueEntry) UnmarshalJSON(data []byte) error {
	type queueEntryJSON QueueEntry
	decoded := struct {
		*queueEntryJSON
		Mode           run.Mode          `json:"mode,omitempty"`
		MaxSlices      int               `json:"max_slices,omitempty"`
		Continue       bool              `json:"continue,omitempty"`
		CommitPolicy   run.CommitPolicy  `json:"commit_policy,omitempty"`
		ExecutionMode  run.ExecutionMode `json:"execution_mode,omitempty"`
		Agent          run.AgentKind     `json:"agent,omitempty"`
		PullRequest    bool              `json:"pull_request,omitempty"`
		ReviewEnabled  *bool             `json:"review_enabled,omitempty"`
		SessionTimeout *time.Duration    `json:"session_timeout,omitempty"`
	}{queueEntryJSON: (*queueEntryJSON)(entry)}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	entry.Mode = decoded.Mode
	entry.MaxSlices = decoded.MaxSlices
	entry.Continue = decoded.Continue
	entry.CommitPolicy = decoded.CommitPolicy
	entry.ExecutionMode = decoded.ExecutionMode
	entry.Agent = decoded.Agent
	entry.PullRequest = decoded.PullRequest
	entry.ReviewEnabled = decoded.ReviewEnabled
	entry.SessionTimeout = decoded.SessionTimeout
	return nil
}

type QueueSnapshot struct {
	Entries []QueueEntry `json:"entries"`
}

// ManagerConfig is the complete production configuration for a durable queue
// scheduler and its synchronous entry driver.
type ManagerConfig struct {
	Context             context.Context
	Executor            Executor
	Clock               func() time.Time
	Store               Store
	Validator           Validator
	RecoveryInspector   RecoveryInspector
	RecoveryReviewer    RecoveryReviewer
	ConflictChecker     ConflictChecker
	AutoReworkPolicy    runtimeconfig.AutoReworkPolicy
	AutoReworker        AutoReworker
	PlanOwner           PlanOwner
	MaxParallelRuns     int
	StartDrainingPaused bool
}

type Manager struct {
	ctx                 context.Context
	execute             Executor
	now                 func() time.Time
	validateQueue       Validator
	inspectRecovery     RecoveryInspector
	reviewRecovery      RecoveryReviewer
	checkConflict       ConflictChecker
	entryMutationMu     sync.Mutex
	mu                  sync.Mutex
	statuses            map[string]*RunStatus
	activeRequests      map[string]run.Request
	queue               []*QueueEntry
	maxParallel         int
	autoReworkPolicy    runtimeconfig.AutoReworkPolicy
	autoReworkPolicySet bool
	autoReworker        AutoReworker
	planOwner           PlanOwner
	drainingPaused      bool
	drainErr            error
	drainFailed         chan struct{}
	dispatching         atomic.Bool
	drainRequested      atomic.Bool
	stopRequested       atomic.Bool
	store               Store
}

func New(ctx context.Context, execute Executor, now func() time.Time) *Manager {
	manager, _ := NewWithStore(ctx, execute, now, nil)
	return manager
}

// NewManager constructs a fully wired durable scheduler. New and NewWithStore
// remain available for focused tests and compatibility callers that install
// collaborators dynamically.
func NewManager(config ManagerConfig) (*Manager, error) {
	if config.Executor == nil {
		return nil, errors.New("queue manager requires executor")
	}
	if config.Clock == nil {
		return nil, errors.New("queue manager requires clock")
	}
	if config.Store == nil {
		return nil, errors.New("queue manager requires store")
	}
	if config.Validator == nil {
		return nil, errors.New("queue manager requires validator")
	}
	if config.RecoveryInspector == nil {
		return nil, errors.New("queue manager requires recovery inspector")
	}
	if config.RecoveryReviewer == nil {
		return nil, errors.New("queue manager requires recovery reviewer")
	}
	if config.PlanOwner == nil {
		return nil, errors.New("queue manager requires plan owner")
	}
	if config.MaxParallelRuns < 1 {
		return nil, errors.New("queue manager requires at least one parallel run")
	}
	policy, err := runtimeconfig.ResolveAutoReworkPolicy(config.AutoReworkPolicy.Enabled, config.AutoReworkPolicy.MaxAttempts, true)
	if err != nil {
		return nil, err
	}
	if policy.Enabled && config.AutoReworker == nil {
		return nil, errors.New("queue manager requires auto reworker when automatic rework is enabled")
	}

	manager, err := NewWithStore(config.Context, config.Executor, config.Clock, config.Store)
	if err != nil {
		return nil, err
	}
	manager.validateQueue = config.Validator
	manager.inspectRecovery = config.RecoveryInspector
	manager.reviewRecovery = config.RecoveryReviewer
	manager.checkConflict = config.ConflictChecker
	manager.autoReworker = config.AutoReworker
	manager.planOwner = config.PlanOwner
	manager.maxParallel = config.MaxParallelRuns
	manager.drainingPaused = config.StartDrainingPaused
	if err := manager.SetAutoReworkPolicy(policy); err != nil {
		return nil, err
	}
	return manager, nil
}

// NewWithStore creates a Manager backed by store. A nil store preserves the
// in-memory behavior of New, while a non-nil store is loaded before use.
func NewWithStore(ctx context.Context, execute Executor, now func() time.Time, store Store) (*Manager, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if now == nil {
		now = time.Now
	}
	manager := &Manager{
		ctx: ctx, execute: execute, now: now,
		statuses: make(map[string]*RunStatus), activeRequests: make(map[string]run.Request),
		maxParallel: 1, store: store, drainFailed: make(chan struct{}),
	}
	if store == nil {
		return manager, nil
	}
	snapshot, err := store.Load()
	if err != nil {
		return nil, err
	}
	manager.loadQueueSnapshot(snapshot)
	return manager, nil
}

func (m *Manager) Status(planID string) *RunStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.statuses[planID]
	if status == nil {
		return nil
	}
	copy := *status
	return &copy
}

func (m *Manager) ActiveStatuses() []*RunStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	statuses := make([]*RunStatus, 0, len(m.statuses))
	for _, status := range m.statuses {
		if !status.Active {
			continue
		}
		copy := *status
		statuses = append(statuses, &copy)
	}
	return statuses
}

func (m *Manager) Queue() QueueSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.queueSnapshotLocked()
}

// TransitionEntry implements EntryDriverHost. The entry-mutation mutex orders
// every durable queue mutation while Manager's state mutex is released for this
// transition's persistence. Once the append succeeds, queue and active-status
// projections are published together under the state mutex.
func (m *Manager) TransitionEntry(_ context.Context, transition EntryTransition) error {
	before, after := transition.Before, transition.After
	if before.PlanID == "" || after.PlanID != before.PlanID || !after.QueuedAt.Equal(before.QueuedAt) {
		return errors.New("queue entry transition changed entry identity")
	}

	m.entryMutationMu.Lock()
	defer m.entryMutationMu.Unlock()

	m.mu.Lock()
	var current *QueueEntry
	for _, entry := range m.queue {
		if entry.PlanID == before.PlanID && entry.QueuedAt.Equal(before.QueuedAt) {
			current = entry
			break
		}
	}
	if current == nil {
		m.mu.Unlock()
		return fmt.Errorf("queue entry %s is not available for transition", before.PlanID)
	}
	if current.Status != before.Status {
		status := current.Status
		m.mu.Unlock()
		return fmt.Errorf("queue entry %s status changed from %s to %s before transition", before.PlanID, before.Status, status)
	}
	m.mu.Unlock()

	persisted := after
	if err := m.persistEntryLocked(&persisted); err != nil {
		return fmt.Errorf("persist queue entry %s transition: %w", before.PlanID, err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	*current = after
	if before.Status == QueueStatusRunning && after.Status != QueueStatusRunning {
		finished := m.now()
		if after.FinishedAt != nil {
			finished = *after.FinishedAt
		}
		var resultErr error
		if after.Status == QueueStatusFailed && after.Error != "" {
			resultErr = errors.New(after.Error)
		}
		m.finishActiveEntryLocked(after.PlanID, finished, resultErr)
	}
	return nil
}

func (m *Manager) SetQueueValidator(validate Validator) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.validateQueue = validate
}

// SetRecoveryInspector installs the persisted-plan check used to establish
// rework baselines and recover entries after an interrupted run.
func (m *Manager) SetRecoveryInspector(inspect RecoveryInspector) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inspectRecovery = inspect
}

// SetRecoveryReviewer installs the finalization operation used when an
// interrupted run completed all slices.
func (m *Manager) SetRecoveryReviewer(review RecoveryReviewer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reviewRecovery = review
}

func (m *Manager) SetQueueConflictChecker(check ConflictChecker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.checkConflict = check
}

// SetAutoReworkPolicy configures queue-only automatic rework behavior. Entries
// retain the first policy assigned to them so interrupted drains resume with the
// same opt-in and cap even when the next process uses different flags.
func (m *Manager) SetAutoReworkPolicy(policy runtimeconfig.AutoReworkPolicy) error {
	resolved, err := runtimeconfig.ResolveAutoReworkPolicy(policy.Enabled, policy.MaxAttempts, true)
	if err != nil {
		return err
	}

	m.entryMutationMu.Lock()
	defer m.entryMutationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, entry := range m.queue {
		if entry.Status != QueueStatusPending && entry.Status != QueueStatusRunning {
			continue
		}
		effective := resolved
		if entry.AutoReworkPolicy != nil {
			effective = *entry.AutoReworkPolicy
		}
		if err := runtimeconfig.ValidateAutoReworkPolicy(effective, entry.request.ReviewEnabled); err != nil {
			return fmt.Errorf("auto-rework policy for queued plan %s: %w", entry.PlanID, err)
		}
	}

	for _, entry := range m.queue {
		if (entry.Status != QueueStatusPending && entry.Status != QueueStatusRunning) || entry.AutoReworkPolicy != nil {
			continue
		}
		assigned := resolved
		entry.AutoReworkPolicy = &assigned
		if err := m.persistEntryLocked(entry); err != nil {
			entry.AutoReworkPolicy = nil
			return fmt.Errorf("persist auto-rework policy for queued plan %s: %w", entry.PlanID, err)
		}
	}
	m.autoReworkPolicy = resolved
	m.autoReworkPolicySet = true
	return nil
}

// SetAutoReworker installs the repository mutation used between queue runs.
func (m *Manager) SetAutoReworker(reworker AutoReworker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.autoReworker = reworker
}

// SetPlanOwner installs the ownership boundary around a queued entry's initial
// run, automatic rework mutations, and follow-up runs.
func (m *Manager) SetPlanOwner(owner PlanOwner) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.planOwner = owner
}

func (m *Manager) SetMaxParallelRuns(max int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if max < 1 {
		max = 1
	}
	m.maxParallel = max
}

func (m *Manager) SetDrainingPaused(paused bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.drainingPaused = paused
}

func (m *Manager) RequestStop() {
	m.stopRequested.Store(true)
	m.SetDrainingPaused(true)
}

func (m *Manager) StopRequested() bool {
	return m.stopRequested.Load()
}

func (m *Manager) Drain() {
	m.drainQueue()
}

// RecoverInterruptedRuns returns entries left running by a previous process to
// the pending queue so a new drain can resume them. The recovery transition is
// persisted before the in-memory entry is changed.
func (m *Manager) RecoverInterruptedRuns() error {
	m.entryMutationMu.Lock()
	defer m.entryMutationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, entry := range m.queue {
		if entry.Status != QueueStatusRunning {
			continue
		}
		recovered := *entry
		recovered.Status = QueueStatusPending
		recovered.StartedAt = nil
		recovered.FinishedAt = nil
		recovered.Error = ""
		recovered.SkipReason = ""
		recovered.WaitReason = ""
		recovered.RecoveryPending = true
		if err := m.persistEntryLocked(&recovered); err != nil {
			return fmt.Errorf("recover interrupted queue run %s: %w", entry.PlanID, err)
		}
		*entry = recovered
	}
	return nil
}

func (m *Manager) Enqueue(request run.Request) (*QueueEntry, error) {
	return m.enqueue(request, false)
}

// EnqueueAutoReworkDecision queues a stopped reviewed plan through the durable
// recovery path so its restart guard or rework occurs before run validation.
func (m *Manager) EnqueueAutoReworkDecision(request run.Request) (*QueueEntry, error) {
	return m.enqueue(request, true)
}

func (m *Manager) enqueue(request run.Request, recoveryPending bool) (*QueueEntry, error) {
	if err := validateExecutableCommitPolicy(request.CommitPolicy); err != nil {
		return nil, err
	}
	m.entryMutationMu.Lock()
	m.mu.Lock()
	if m.planQueuedOrActiveLocked(request.Input) {
		m.mu.Unlock()
		m.entryMutationMu.Unlock()
		return nil, fmt.Errorf("plan run already queued or active for %s", request.Input)
	}
	entry := &QueueEntry{PlanID: request.Input, Status: QueueStatusPending, QueuedAt: m.now(), RecoveryPending: recoveryPending, request: request}
	if m.autoReworkPolicySet {
		if err := runtimeconfig.ValidateAutoReworkPolicy(m.autoReworkPolicy, request.ReviewEnabled); err != nil {
			m.mu.Unlock()
			m.entryMutationMu.Unlock()
			return nil, fmt.Errorf("auto-rework policy for queued plan %s: %w", request.Input, err)
		}
		assigned := m.autoReworkPolicy
		entry.AutoReworkPolicy = &assigned
	}
	entry.setPersistentRunOptions(request, m.store != nil)
	m.queue = append(m.queue, entry)
	if err := m.persistEntryLocked(entry); err != nil {
		m.queue = m.queue[:len(m.queue)-1]
		m.mu.Unlock()
		m.entryMutationMu.Unlock()
		return nil, err
	}
	initial := *entry
	m.mu.Unlock()
	m.entryMutationMu.Unlock()

	m.drainQueue()
	return &initial, nil
}

func (m *Manager) Dequeue(planID string) (*QueueEntry, error) {
	m.entryMutationMu.Lock()
	defer m.entryMutationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for i, entry := range m.queue {
		if entry.PlanID != planID {
			continue
		}
		found = true
		if entry.Status != QueueStatusPending {
			continue
		}
		finished := m.now()
		dequeued := *entry
		dequeued.Status = QueueStatusSkipped
		dequeued.FinishedAt = &finished
		dequeued.SkipReason = "dequeued"
		if err := m.persistRemovedEntryLocked(&dequeued); err != nil {
			return nil, err
		}
		*entry = dequeued
		m.queue = append(m.queue[:i], m.queue[i+1:]...)
		return &dequeued, nil
	}
	if found {
		return nil, fmt.Errorf("queued plan %s is not pending", planID)
	}
	return nil, fmt.Errorf("plan %s is not queued", planID)
}

func (m *Manager) drainQueue() {
	m.drainRequested.Store(true)
	if !m.dispatching.CompareAndSwap(false, true) {
		return
	}

	for {
		m.drainRequested.Store(false)
		for m.startNextQueuedRun() {
		}

		m.dispatching.Store(false)
		if !m.drainRequested.Load() || !m.dispatching.CompareAndSwap(false, true) {
			return
		}
	}
}

func (m *Manager) startNextQueuedRun() bool {
	m.mu.Lock()
	if m.drainErr != nil || m.drainingPaused || m.activeRunsLocked() >= m.maxParallel {
		m.mu.Unlock()
		return false
	}
	check := m.checkConflict
	active := m.activeRequestsLocked()
	candidates := make([]QueueEntry, 0, len(m.queue))
	for _, candidate := range m.queue {
		if candidate.Status == QueueStatusPending {
			candidates = append(candidates, *candidate)
		}
	}
	m.mu.Unlock()
	if len(candidates) == 0 {
		return false
	}

	driver := m.entryDriver()
	for _, candidate := range candidates {
		if err := validateExecutableCommitPolicy(candidate.request.CommitPolicy); err != nil {
			if applyErr := driver.ApplyResult(m.ctx, candidate, EntryResult{Outcome: EntryOutcomeSkipped, Err: err}); applyErr != nil {
				m.recordDriverError(candidate.PlanID, applyErr)
				return false
			}
			return true
		}

		if candidate.RecoveryPending {
			var claimed bool
			candidate, claimed = m.claimPendingEntry(candidate)
			if !claimed {
				continue
			}
		}
		preparation, err := driver.Prepare(m.ctx, candidate)
		if err != nil {
			m.recordDriverError(candidate.PlanID, err)
			return false
		}
		if preparation.Result.Outcome != EntryOutcomeReady {
			return true
		}

		callbackCtx := m.ctx
		if preparation.Ownership != nil {
			callbackCtx = preparation.Ownership.Context()
		}
		request := preparation.Entry.request
		if request.Input == "" {
			request = preparation.Entry.runRequest()
		}
		if check != nil {
			if reason := check(callbackCtx, request, active); reason != "" {
				if preparation.Entry.Status == QueueStatusPending && preparation.Entry.WaitReason == reason {
					continue
				}
				if err := driver.FinishPreparation(m.ctx, preparation, EntryResult{Outcome: EntryOutcomeWaiting, Reason: reason}); err != nil {
					m.recordDriverError(candidate.PlanID, err)
					return false
				}
				continue
			}
		}

		claimed := preparation.Entry
		if claimed.Status == QueueStatusPending {
			var ok bool
			claimed, ok = m.claimPendingEntry(claimed)
			if !ok {
				continue
			}
		} else if !m.claimedEntryRunnable(claimed) {
			if err := driver.FinishPreparation(m.ctx, preparation, EntryResult{Outcome: EntryOutcomeWaiting}); err != nil {
				m.recordDriverError(candidate.PlanID, err)
			}
			return false
		}
		m.driveClaimedEntry(driver, claimed, preparation.Ownership)
		return true
	}
	return false
}

func (m *Manager) claimPendingEntry(candidate QueueEntry) (QueueEntry, bool) {
	m.entryMutationMu.Lock()
	defer m.entryMutationMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drainingPaused || m.activeRunsLocked() >= m.maxParallel {
		return QueueEntry{}, false
	}
	for _, entry := range m.queue {
		if entry.PlanID != candidate.PlanID || !entry.QueuedAt.Equal(candidate.QueuedAt) || entry.Status != QueueStatusPending {
			continue
		}
		request := entry.request
		if request.Input == "" {
			request = entry.runRequest()
		}
		if m.planActiveLocked(request.Input) {
			return QueueEntry{}, false
		}
		now := m.now()
		claimed := *entry
		claimed.request = request
		claimed.Status = QueueStatusRunning
		claimed.StartedAt = &now
		claimed.WaitReason = ""
		if err := m.persistEntryLocked(&claimed); err != nil {
			return QueueEntry{}, false
		}
		*entry = claimed
		m.activateEntryLocked(request, now)
		return claimed, true
	}
	return QueueEntry{}, false
}

func (m *Manager) claimedEntryRunnable(candidate QueueEntry) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drainingPaused {
		return false
	}
	for _, entry := range m.queue {
		if entry.PlanID == candidate.PlanID && entry.QueuedAt.Equal(candidate.QueuedAt) && entry.Status == QueueStatusRunning {
			status := m.statuses[entry.PlanID]
			return status != nil && status.Active
		}
	}
	return false
}

func (m *Manager) activateEntryLocked(request run.Request, started time.Time) *RunStatus {
	status := &RunStatus{Active: true, PlanID: request.Input, Mode: request.Mode.String(), CommitPolicy: request.CommitPolicy.String(), Continue: request.Continue, PullRequest: request.PullRequest, StartedAt: &started}
	m.statuses[request.Input] = status
	if m.activeRequests == nil {
		m.activeRequests = make(map[string]run.Request)
	}
	m.activeRequests[request.Input] = request
	return status
}

func (m *Manager) finishActiveEntryLocked(planID string, finished time.Time, runErr error) {
	if status := m.statuses[planID]; status != nil && status.Active {
		status.Active = false
		status.FinishedAt = &finished
		if runErr != nil {
			status.Error = runErr.Error()
		}
	}
	delete(m.activeRequests, planID)
}

func (m *Manager) driveClaimedEntry(driver EntryDriver, claimed QueueEntry, ownership *EntryOwnership) {
	go func() {
		var (
			result   EntryResult
			driveErr error
		)
		if ownership != nil {
			result, driveErr = driver.DriveOwned(m.ctx, claimed, ownership)
		} else {
			result, driveErr = driver.Drive(m.ctx, claimed)
		}
		if driveErr != nil {
			m.recordDriverError(claimed.PlanID, driveErr)
			return
		}
		if result.Outcome != EntryOutcomeRequeuedAfterStop {
			m.drainQueue()
		}
	}()
}

func (m *Manager) recordDriverError(planID string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.drainErr != nil {
		return
	}
	m.drainErr = fmt.Errorf("drive queued plan %s: %w", planID, err)
	close(m.drainFailed)
}

func (m *Manager) drainError() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.drainErr
}

func (m *Manager) entryDriver() EntryDriver {
	m.mu.Lock()
	owner := m.planOwner
	validate := m.validateQueue
	inspect := m.inspectRecovery
	reviewer := m.reviewRecovery
	m.mu.Unlock()
	return EntryDriver{
		Host:             m,
		Own:              owner,
		Validate:         validate,
		InspectRecovery:  inspect,
		FinalizeRecovery: reviewer,
		Execute:          m.execute,
		StopRequested:    m.StopRequested,
		PolicyForEntry: func(entry QueueEntry) runtimeconfig.AutoReworkPolicy {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.autoReworkPolicyForEntryLocked(&entry)
		},
		ReworkForEntry: func(QueueEntry) AutoReworker {
			m.mu.Lock()
			defer m.mu.Unlock()
			return m.autoReworker
		},
		Now: m.now,
	}
}

func (m *Manager) autoReworkPolicyForEntryLocked(entry *QueueEntry) runtimeconfig.AutoReworkPolicy {
	if entry.AutoReworkPolicy != nil {
		return *entry.AutoReworkPolicy
	}
	if m.autoReworkPolicySet {
		return m.autoReworkPolicy
	}
	return runtimeconfig.AutoReworkPolicy{}
}

func (m *Manager) planQueuedOrActiveLocked(planID string) bool {
	if m.planActiveLocked(planID) {
		return true
	}
	for _, entry := range m.queue {
		if entry.PlanID == planID && (entry.Status == QueueStatusPending || entry.Status == QueueStatusRunning) {
			return true
		}
	}
	return false
}

func (m *Manager) planActiveLocked(planID string) bool {
	status := m.statuses[planID]
	return status != nil && status.Active
}

func (m *Manager) activeRunsLocked() int {
	active := 0
	for _, status := range m.statuses {
		if status.Active {
			active++
		}
	}
	return active
}

func (m *Manager) activeRequestsLocked() []run.Request {
	requests := make([]run.Request, 0, len(m.activeRequests))
	for _, request := range m.activeRequests {
		requests = append(requests, request)
	}
	return requests
}

func (m *Manager) queueSnapshotLocked() QueueSnapshot {
	entries := make([]QueueEntry, len(m.queue))
	for i, entry := range m.queue {
		entries[i] = *entry
	}
	return QueueSnapshot{Entries: entries}
}

func (m *Manager) loadQueueSnapshot(snapshot QueueSnapshot) {
	m.queue = make([]*QueueEntry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		loaded := entry
		if loaded.PlanID == "" {
			loaded.PlanID = loaded.request.Input
		}
		if loaded.request.Input == "" {
			loaded.request = loaded.runRequest()
		}
		m.queue = append(m.queue, &loaded)
	}
}

func (m *Manager) persistEntryLocked(entry *QueueEntry) error {
	if m.store == nil {
		return nil
	}
	persisted := *entry
	persisted.prepareForPersistence()
	return m.store.AppendTransition(QueueTransition{PlanID: persisted.PlanID, Entry: &persisted})
}

func (m *Manager) persistRemovedEntryLocked(entry *QueueEntry) error {
	if m.store == nil {
		return nil
	}
	persisted := *entry
	persisted.prepareForPersistence()
	return m.store.AppendTransition(QueueTransition{PlanID: persisted.PlanID, Entry: &persisted, Removed: true})
}

func (entry *QueueEntry) prepareForPersistence() {
	if entry.PlanID == "" {
		entry.PlanID = entry.request.Input
	}
	if entry.request.Input == "" {
		entry.request = entry.runRequest()
	}
	entry.setPersistentRunOptions(entry.request, true)
}

func (entry *QueueEntry) setPersistentRunOptions(request run.Request, enabled bool) {
	if !enabled {
		return
	}
	options := request.RunOptionsPatch()
	entry.RunOptions = &options
}

func (entry QueueEntry) runOptionsPatch() runtimeconfig.RunOptionsPatch {
	if entry.RunOptions != nil {
		return *entry.RunOptions
	}

	overrides := runtimeconfig.RunOptionsPatch{}.WithContinue(entry.Continue).WithPullRequest(entry.PullRequest)
	if entry.Mode != "" {
		overrides.Mode = entry.Mode
	}
	if entry.MaxSlices != 0 {
		overrides = overrides.WithMaxSlices(entry.MaxSlices)
	}
	if entry.CommitPolicy != "" {
		overrides.CommitPolicy = entry.CommitPolicy
	}
	if entry.ExecutionMode != "" {
		overrides.ExecutionMode = entry.ExecutionMode
	}
	if entry.Agent != "" {
		overrides.Agent = entry.Agent
	}
	if entry.ReviewEnabled != nil {
		overrides = overrides.WithReviewEnabled(*entry.ReviewEnabled)
	}
	if entry.SessionTimeout != nil {
		overrides = overrides.WithSessionTimeout(*entry.SessionTimeout)
	}
	return overrides
}

func (entry QueueEntry) runRequest() run.Request {
	overrides := entry.runOptionsPatch()
	entry.CommitPolicy = overrides.CommitPolicy
	options, err := runtimeconfig.ResolveRunOptions(runtimeconfig.DefaultRunOptionsPatch(), overrides)
	if err != nil {
		options, _ = runtimeconfig.ResolveRunOptions(runtimeconfig.DefaultRunOptionsPatch(), runtimeconfig.RunOptionsPatch{})
		// Keep removed historical policy semantics visible so the queue blocks the
		// entry actionably instead of silently executing it with today's default.
		if entry.CommitPolicy == run.CommitPolicyPlan {
			options.CommitPolicy = entry.CommitPolicy
		}
	}
	return run.Request{Input: entry.PlanID, ResolvedRunOptions: options}
}

func validateExecutableCommitPolicy(policy run.CommitPolicy) error {
	if policy == run.CommitPolicyPlan {
		return fmt.Errorf("commit policy plan was removed; use slice or none; replace or re-enqueue this run with slice")
	}
	return nil
}
