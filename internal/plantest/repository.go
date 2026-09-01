package plantest

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type runStartRecorder interface {
	StartSliceWithRunCommitPolicy(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, now time.Time) error
	StartSliceWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary plan.SliceExecutionStart, now time.Time) error
}

type runStartRepairer interface {
	RepairSliceStartWithRunBoundary(sliceID string, executionRoot string, commitPolicy string, startingDirtyPaths []string, boundary plan.SliceExecutionStart, startedAt time.Time) error
	RepairMissingSliceStartedEvent(sliceID string, startedAt time.Time) error
}

type finalVerificationRecorder interface {
	RecordFinalVerification(verification plan.FinalVerification) error
}

var (
	_ runStartRecorder          = (*plan.PlanRecord)(nil)
	_ runStartRepairer          = (*plan.PlanRecord)(nil)
	_ finalVerificationRecorder = (*plan.PlanRecord)(nil)
)

// Repository is a fully in-memory implementation of the plan repository
// interfaces for use in tests. Details, states, slices, and events are stored
// in maps; PlanRecord supplies the same operation capabilities used by run and
// CLI tests, and no filesystem access occurs during lifecycle mutations.
//
// The zero value is not ready for use; call NewRepository.
type Repository struct {
	mu      sync.Mutex
	details map[string]*plan.PlanDetail // key: plan ID
}

// NewRepository returns an empty in-memory repository.
func NewRepository() *Repository {
	return &Repository{details: make(map[string]*plan.PlanDetail)}
}

// AddDetail registers detail in the repository.  If detail.Dir is empty a
// synthetic path "/plantest/<id>" is assigned so PlanRecord can bind a
// directory without creating real filesystem entries.
func (r *Repository) AddDetail(detail *plan.PlanDetail) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if detail.Dir == "" {
		detail.Dir = "/plantest/" + detail.State.Plan.ID
	}
	r.details[detail.State.Plan.ID] = detail
}

// WriteState implements plan.ArtifactStore. PlanRecord publishes settled state
// to its bound PlanDetail after this adapter succeeds; Repository retains that
// same pointer, so no separate payload write is needed.
func (r *Repository) WriteState(_ string, _ []byte) error { return nil }

// WriteSlices implements plan.ArtifactStore. PlanRecord publishes settled slices
// to its bound PlanDetail after this adapter succeeds; Repository retains that
// same pointer, so no separate payload write is needed.
func (r *Repository) WriteSlices(_ string, _ []byte) error { return nil }

// AppendEvent implements plan.ArtifactStore. PlanRecord publishes settled events
// to its bound PlanDetail after this adapter succeeds; Repository retains that
// same pointer, so no separate append is needed.
func (r *Repository) AppendEvent(_ string, _ plan.Event) error { return nil }

// ListPlans implements plan.Repository.  It summarizes all registered details
// and applies the same sort order as FileRepository (most-recently-active
// first; ties broken by ID descending).
func (r *Repository) ListPlans(ctx context.Context, filter plan.PlanFilter) ([]plan.PlanSummary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	summaries := make([]plan.PlanSummary, 0, len(r.details))
	for _, detail := range r.details {
		s := plan.Summarize(detail, now)
		if filter.ActiveOnly && !s.Active() {
			continue
		}
		summaries = append(summaries, s)
	}
	sortSummaries(summaries)
	return summaries, nil
}

func sortSummaries(summaries []plan.PlanSummary) {
	sort.Slice(summaries, func(i, j int) bool {
		l, ri := summaries[i].LastActivityAt, summaries[j].LastActivityAt
		if l == nil && ri == nil {
			return summaries[i].ID > summaries[j].ID
		}
		if l == nil {
			return false
		}
		if ri == nil {
			return true
		}
		return l.After(*ri)
	})
}

// GetPlan implements plan.Repository.  It returns the detail registered under
// id, or nil when not found.
func (r *Repository) GetPlan(ctx context.Context, id string) (*plan.PlanDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.details[id], nil
}

// ResolvePlan implements plan.Resolver.  Resolution order: exact ID, ID
// prefix, slug prefix.  Ambiguous inputs return an error.
func (r *Repository) ResolvePlan(ctx context.Context, input string) (*plan.PlanDetail, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	// Exact match.
	if d, ok := r.details[input]; ok {
		return d, nil
	}

	// ID prefix.
	var matches []*plan.PlanDetail
	for id, d := range r.details {
		if strings.HasPrefix(id, input) {
			matches = append(matches, d)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("plan id %q is ambiguous", input)
	}

	// Slug prefix.
	for id, d := range r.details {
		if slug, ok := plan.PlanSlug(id); ok && strings.HasPrefix(slug, input) {
			matches = append(matches, d)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("plan slug %q is ambiguous", input)
	}

	return nil, fmt.Errorf("plan %q not found", input)
}

// PlanRecord implements plan.PlanRecordStore.  The record is backed by this
// repository so mutations go to the in-memory store.
func (r *Repository) PlanRecord(detail *plan.PlanDetail) (*plan.PlanRecord, error) {
	dir := detail.Dir
	if dir == "" {
		dir = "/plantest/" + detail.State.Plan.ID
		detail.Dir = dir
	}
	return plan.NewPlanRecordWithStore(r, dir, detail)
}

// ResolvePlanRecord implements plan.PlanRecordResolver.
func (r *Repository) ResolvePlanRecord(ctx context.Context, input string) (*plan.PlanRecord, error) {
	detail, err := r.ResolvePlan(ctx, input)
	if err != nil {
		return nil, err
	}
	return r.PlanRecord(detail)
}

// DeletePlan implements plan.PlanDeleter.
func (r *Repository) DeletePlan(ctx context.Context, input string, _ plan.DeletePlanOptions) (*plan.DeletePlanResult, error) {
	detail, err := r.ResolvePlan(ctx, input)
	if err != nil {
		return nil, err
	}
	if detail == nil {
		return nil, fmt.Errorf("plan %q not found", input)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	id := detail.State.Plan.ID
	dir := detail.Dir
	delete(r.details, id)
	return &plan.DeletePlanResult{ID: id, Dir: dir}, nil
}

// OpenLogAppend implements plan.LogAppender by opening the system null device
// so callers receive a valid writable *os.File without touching real log paths.
func (r *Repository) OpenLogAppend(_ string) (*os.File, error) {
	return os.OpenFile(os.DevNull, os.O_WRONLY, 0)
}

// ReadLog implements plan.LogReader.
func (r *Repository) ReadLog(_ string) (string, error) { return "", nil }

// ReadLogTail implements plan.LogTailReader.
func (r *Repository) ReadLogTail(_ string, _ int) (string, error) { return "", nil }

// FollowLog implements plan.LogFollower.
func (r *Repository) FollowLog(ctx context.Context, _ string, _ io.Writer) error {
	return ctx.Err()
}
