// Package monitor builds cross-repository, read-only plan snapshots.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runstatus"
	"github.com/iamseth/tao/internal/taodata"
)

// RepositoryInventory is the metadata-only catalog boundary used by Collector.
type RepositoryInventory interface {
	MetadataInventory() ([]taodata.RepoInventoryEntry, error)
}

// PlanLister supplies one already-derived summary pass for a repository.
type PlanLister interface {
	ListPlans(context.Context, plan.PlanFilter) ([]plan.PlanSummary, error)
}

// RuntimeStatusReader reads best-effort operational state for one plan.
type RuntimeStatusReader interface {
	Read(planID string) (runstatus.Record, error)
}

// PlanListerFactory creates a summary source for one inventory entry.
type PlanListerFactory func(taodata.RepoInventoryEntry) PlanLister

// RuntimeStatusReaderFactory creates an operational status source for one inventory entry.
type RuntimeStatusReaderFactory func(taodata.RepoInventoryEntry) RuntimeStatusReader

// RunLockReader reads the operational ownership lock for one plan directory.
type RunLockReader func(string) (plan.RunLock, error)

// Liveness describes the presence and freshness of an operational run record.
type Liveness string

const (
	LivenessMissing Liveness = "missing"
	LivenessLive    Liveness = "live"
	LivenessStale   Liveness = "stale"
)

// RowKind distinguishes ordinary plans from catalog-level warning rows.
type RowKind string

const (
	RowKindPlan              RowKind = "plan"
	RowKindRepositoryWarning RowKind = "repository_warning"
)

// AttentionReason identifies one independently actionable plan condition.
type AttentionReason string

const (
	AttentionBlocked                AttentionReason = "blocked"
	AttentionChangesRequested       AttentionReason = "changes_requested"
	AttentionApprovalRequired       AttentionReason = "approval_required"
	AttentionSliceCompletionPending AttentionReason = "slice_completion_pending"
	AttentionReworkStopped          AttentionReason = "rework_stopped"
	AttentionRunCrashed             AttentionReason = "run_crashed"
)

// Row is one render-neutral monitor line. UpdatedAt is durable plan activity;
// invocation fields are operational observations and never lifecycle evidence.
type Row struct {
	Kind RowKind

	RepositoryID   string
	RepositoryName string
	RepositoryRoot string
	PlanID         string
	PlanTitle      string
	PlanDir        string
	Status         string

	Liveness            Liveness
	Phase               runstatus.Phase
	SliceID             string
	SliceTitle          string
	InvocationDuration  time.Duration
	HeartbeatAge        time.Duration
	RunLockPresent      bool
	RunLockProcessAlive bool

	Left                   int
	UpdatedAt              *time.Time
	OriginalCompletedCount int
	OriginalTotalCount     int
	ReworkCompletedCount   int
	ReworkTotalCount       int

	AttentionReasons []AttentionReason
	ApprovalSliceID  string
	ApprovalReason   string

	Warnings []string
}

// Snapshot is a deterministic observation collected at one instant.
type Snapshot struct {
	CollectedAt time.Time
	Rows        []Row
}

// Collector combines metadata inventory, plan summaries, and runtime records.
type Collector struct {
	Inventory              RepositoryInventory
	NewPlanLister          PlanListerFactory
	NewStatusReader        RuntimeStatusReaderFactory
	ReadRunLock            RunLockReader
	Now                    func() time.Time
	ShowInvalid            bool
	IncludeCompletedWithin time.Duration
}

// NewCollector returns a filesystem-backed collector without repository health probes.
func NewCollector(inventory RepositoryInventory) Collector {
	return Collector{
		Inventory: inventory,
		NewPlanLister: func(entry taodata.RepoInventoryEntry) PlanLister {
			return plan.NewFileRepository(entry.PlansDir)
		},
		NewStatusReader: func(entry taodata.RepoInventoryEntry) RuntimeStatusReader {
			return runstatus.NewStore(entry.RuntimeStatusDir, nil)
		},
		ReadRunLock: plan.ReadRunLock,
		Now:         time.Now,
	}
}

// Collect builds one snapshot. Repository damage is represented as warning
// rows, while invalid plan summaries are included only when ShowInvalid is set.
func (c Collector) Collect(ctx context.Context) (Snapshot, error) {
	if c.Inventory == nil {
		return Snapshot{}, errors.New("monitor repository inventory is required")
	}
	now := c.now()
	snapshot := Snapshot{CollectedAt: now}
	inventory, err := c.Inventory.MetadataInventory()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read monitor repository inventory: %w", err)
	}

	for _, entry := range inventory {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		if entry.MetadataError != nil {
			snapshot.Rows = append(snapshot.Rows, repositoryWarningRow(entry, entry.MetadataError))
			continue
		}

		lister := c.planLister(entry)
		if lister == nil {
			snapshot.Rows = append(snapshot.Rows, repositoryWarningRow(entry, errors.New("plan summary source is unavailable")))
			continue
		}
		summaries, err := lister.ListPlans(ctx, plan.PlanFilter{})
		if err != nil {
			snapshot.Rows = append(snapshot.Rows, repositoryWarningRow(entry, fmt.Errorf("list plans: %w", err)))
			continue
		}
		reader := c.statusReader(entry)
		for _, summary := range summaries {
			if err := ctx.Err(); err != nil {
				return Snapshot{}, err
			}
			if !c.includeSummary(summary, now) || (summary.Status == plan.StatusInvalid && !c.ShowInvalid) {
				continue
			}
			row := planRow(entry, summary)
			if summary.Status != plan.StatusInvalid {
				applyRuntimeStatus(&row, reader, entry.Repo.ID, now)
				applyCrashedRunAttention(&row, c.runLockReader(), planDir(entry, summary))
			}
			snapshot.Rows = append(snapshot.Rows, row)
		}
	}

	sortRows(snapshot.Rows)
	return snapshot, nil
}

func (c Collector) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c Collector) planLister(entry taodata.RepoInventoryEntry) PlanLister {
	if c.NewPlanLister != nil {
		return c.NewPlanLister(entry)
	}
	return plan.NewFileRepository(entry.PlansDir)
}

func (c Collector) statusReader(entry taodata.RepoInventoryEntry) RuntimeStatusReader {
	if c.NewStatusReader != nil {
		return c.NewStatusReader(entry)
	}
	return runstatus.NewStore(entry.RuntimeStatusDir, nil)
}

func (c Collector) runLockReader() RunLockReader {
	if c.ReadRunLock != nil {
		return c.ReadRunLock
	}
	return plan.ReadRunLock
}

func (c Collector) includeSummary(summary plan.PlanSummary, now time.Time) bool {
	if summary.Status != plan.StatusCompleted {
		return true
	}
	if c.IncludeCompletedWithin <= 0 {
		return false
	}
	completedAt := summary.CompletedAt
	if completedAt == nil {
		completedAt = summary.LastActivityAt
	}
	return completedAt != nil && !completedAt.Before(now.Add(-c.IncludeCompletedWithin))
}

func repositoryWarningRow(entry taodata.RepoInventoryEntry, err error) Row {
	name := strings.TrimSpace(entry.Repo.Name)
	if name == "" {
		name = entry.Repo.ID
	}
	return Row{
		Kind:           RowKindRepositoryWarning,
		RepositoryID:   entry.Repo.ID,
		RepositoryName: name,
		Status:         plan.StatusInvalid,
		Liveness:       LivenessMissing,
		Warnings:       []string{err.Error()},
	}
}

func planRow(entry taodata.RepoInventoryEntry, summary plan.PlanSummary) Row {
	warnings := append([]string(nil), summary.Warnings...)
	row := Row{
		Kind:                   RowKindPlan,
		RepositoryID:           entry.Repo.ID,
		RepositoryName:         entry.Repo.Name,
		RepositoryRoot:         entry.Repo.Root,
		PlanID:                 summary.ID,
		PlanTitle:              summary.Title,
		PlanDir:                planDir(entry, summary),
		Status:                 summary.Status,
		Liveness:               LivenessMissing,
		SliceID:                summary.CurrentSliceID,
		Left:                   summary.PendingCount,
		UpdatedAt:              cloneTime(summary.LastActivityAt),
		OriginalCompletedCount: summary.OriginalCompletedCount,
		OriginalTotalCount:     summary.OriginalTotalCount,
		ReworkCompletedCount:   summary.ReworkCompletedCount,
		ReworkTotalCount:       summary.ReworkTotalCount,
		ApprovalSliceID:        summary.Capabilities.ApprovalSliceID,
		ApprovalReason:         summary.Capabilities.ApprovalReason,
		Warnings:               warnings,
	}
	if summary.CurrentSlice != nil {
		row.SliceTitle = summary.CurrentSlice.Title
	}
	row.AttentionReasons = attentionReasons(summary)
	return row
}

func attentionReasons(summary plan.PlanSummary) []AttentionReason {
	var reasons []AttentionReason
	switch summary.Status {
	case plan.StatusBlocked:
		reasons = append(reasons, AttentionBlocked)
	case plan.StatusChangesRequested:
		reasons = append(reasons, AttentionChangesRequested)
	}
	if summary.Capabilities.NeedsApproval {
		reasons = append(reasons, AttentionApprovalRequired)
	}
	if summary.SliceCompletionPending {
		reasons = append(reasons, AttentionSliceCompletionPending)
	}
	if summary.UnresolvedReworkStop {
		reasons = append(reasons, AttentionReworkStopped)
	}
	return reasons
}

func applyRuntimeStatus(row *Row, reader RuntimeStatusReader, repoID string, now time.Time) {
	if reader == nil {
		row.Warnings = append(row.Warnings, "runtime status source is unavailable")
		return
	}
	record, err := reader.Read(row.PlanID)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			row.Warnings = append(row.Warnings, err.Error())
		}
		return
	}
	if record.RepoID != repoID {
		row.Warnings = append(row.Warnings, fmt.Sprintf("runtime status repository id %q does not match %q", record.RepoID, repoID))
		return
	}

	if runstatus.DeriveFreshness(record, now) == runstatus.FreshnessFresh {
		row.Liveness = LivenessLive
	} else {
		row.Liveness = LivenessStale
	}
	row.Phase = record.Phase
	if record.Slice != nil {
		row.SliceID = record.Slice.ID
		row.SliceTitle = record.Slice.Title
	}
	if now.After(record.InvocationStartedAt) {
		row.InvocationDuration = now.Sub(record.InvocationStartedAt)
	}
	if now.After(record.HeartbeatAt) {
		row.HeartbeatAge = now.Sub(record.HeartbeatAt)
	}
}

func applyCrashedRunAttention(row *Row, reader RunLockReader, planDir string) {
	if row.Liveness == LivenessLive || reader == nil {
		return
	}
	lock, err := reader(planDir)
	if err != nil {
		return
	}
	row.RunLockPresent = true
	row.RunLockProcessAlive = lock.ProcessAlive
	if !lock.ProcessAlive {
		row.AttentionReasons = append(row.AttentionReasons, AttentionRunCrashed)
	}
}

func planDir(entry taodata.RepoInventoryEntry, summary plan.PlanSummary) string {
	if strings.TrimSpace(summary.Dir) != "" {
		return summary.Dir
	}
	return filepath.Join(entry.PlansDir, summary.ID)
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func sortRows(rows []Row) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := rows[i], rows[j]
		if leftPriority, rightPriority := urgency(left), urgency(right); leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		if compared := compareActivity(left.UpdatedAt, right.UpdatedAt); compared != 0 {
			return compared < 0
		}
		if left.RepositoryName != right.RepositoryName {
			return left.RepositoryName < right.RepositoryName
		}
		if left.RepositoryID != right.RepositoryID {
			return left.RepositoryID < right.RepositoryID
		}
		return left.PlanID < right.PlanID
	})
}

func urgency(row Row) int {
	switch row.Liveness {
	case LivenessLive:
		return 0
	case LivenessStale:
		return 1
	}
	if row.Status == plan.StatusBlocked || row.Status == plan.StatusChangesRequested {
		return 2
	}
	return 3
}

func compareActivity(left, right *time.Time) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return 1
	}
	if right == nil {
		return -1
	}
	if left.After(*right) {
		return -1
	}
	if left.Before(*right) {
		return 1
	}
	return 0
}
