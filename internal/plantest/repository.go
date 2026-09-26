package plantest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

type runStartRecorder interface {
	StartSlice(sliceID string, request plan.SliceStartRequest) error
}

type runStartRepairer interface {
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
// Writes are no-ops by default. NewPersistingRepository opts into payload
// persistence and detached read snapshots.
// The zero value is not ready for use; call a constructor.
type Repository struct {
	mu        sync.Mutex
	details   map[string]*plan.PlanDetail   // key: plan ID
	persisted map[string]persistedArtifacts // key: cleaned plan directory; nil disables persistence
}

type persistedArtifacts struct {
	state  *plan.State
	slices *plan.SlicesFile
	events []plan.Event
}

// NewRepository returns an empty in-memory repository.
func NewRepository() *Repository {
	return &Repository{details: make(map[string]*plan.PlanDetail)}
}

// NewPersistingRepository returns an in-memory repository whose artifact writes
// survive reloads. Reads and records use detached snapshots, so mutations must
// be persisted through the artifact store to be visible to subsequent readers.
func NewPersistingRepository() *Repository {
	r := NewRepository()
	r.persisted = make(map[string]persistedArtifacts)
	return r
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
	if previous := r.details[detail.State.Plan.ID]; previous != nil {
		delete(r.persisted, filepath.Clean(previous.Dir))
	}
	r.details[detail.State.Plan.ID] = detail
}

// WriteState implements plan.ArtifactStore; only persisting repositories decode
// and retain the payload. Invalid payloads leave the previous state intact.
func (r *Repository) WriteState(dir string, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.persisted == nil {
		return nil
	}
	var state plan.State
	if err := json.Unmarshal(payload, &state); err != nil {
		return err
	}
	key := filepath.Clean(dir)
	artifacts := r.persisted[key]
	artifacts.state = &state
	r.persisted[key] = artifacts
	return nil
}

// WriteSlices implements plan.ArtifactStore, retaining decoded slices only in
// persisting mode.
func (r *Repository) WriteSlices(dir string, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.persisted == nil {
		return nil
	}
	var slices plan.SlicesFile
	if err := json.Unmarshal(payload, &slices); err != nil {
		return err
	}
	key := filepath.Clean(dir)
	artifacts := r.persisted[key]
	artifacts.slices = &slices
	r.persisted[key] = artifacts
	return nil
}

// AppendEvent implements plan.ArtifactStore, retaining an event snapshot only
// in persisting mode.
func (r *Repository) AppendEvent(dir string, event plan.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.persisted == nil {
		return nil
	}
	payload, err := json.Marshal(event)
	if err != nil {
		return err
	}
	var snapshot plan.Event
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return err
	}
	key := filepath.Clean(dir)
	artifacts := r.persisted[key]
	artifacts.events = append(artifacts.events, snapshot)
	r.persisted[key] = artifacts
	return nil
}

// reloadLocked overlays persisted artifacts on the fixture, then detaches the
// result so editing a loaded detail cannot silently mutate stored artifacts.
func (r *Repository) reloadLocked(detail *plan.PlanDetail) (*plan.PlanDetail, error) {
	if r.persisted == nil || detail == nil {
		return detail, nil
	}
	loaded := *detail
	artifacts := r.persisted[filepath.Clean(detail.Dir)]
	if artifacts.state != nil {
		loaded.State = *artifacts.state
	}
	if artifacts.slices != nil {
		loaded.Slices = *artifacts.slices
	}
	loaded.Events = append(append([]plan.Event(nil), detail.Events...), artifacts.events...)
	payload, err := json.Marshal(loaded)
	if err != nil {
		return nil, err
	}
	var snapshot plan.PlanDetail
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

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
		loaded, err := r.reloadLocked(detail)
		if err != nil {
			return nil, err
		}
		s := plan.Summarize(loaded, now)
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
	return r.reloadLocked(r.details[id])
}

// GetPlanExact performs the same exact-ID lookup as GetPlan; it never resolves
// prefixes, slugs, or paths.
func (r *Repository) GetPlanExact(ctx context.Context, id string) (*plan.PlanDetail, error) {
	return r.GetPlan(ctx, id)
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
		return r.reloadLocked(d)
	}

	// ID prefix.
	var matches []*plan.PlanDetail
	for id, d := range r.details {
		if strings.HasPrefix(id, input) {
			matches = append(matches, d)
		}
	}
	if len(matches) == 1 {
		return r.reloadLocked(matches[0])
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
		return r.reloadLocked(matches[0])
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("plan slug %q is ambiguous", input)
	}

	return nil, fmt.Errorf("plan %q not found", input)
}

// PlanRecord implements plan.PlanRecordStore.  The record is backed by this
// repository so mutations go to the in-memory store.
func (r *Repository) PlanRecord(detail *plan.PlanDetail) (*plan.PlanRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.persisted != nil {
		if stored := r.details[detail.State.Plan.ID]; stored != nil {
			detail = stored
		}
		var err error
		detail, err = r.reloadLocked(detail)
		if err != nil {
			return nil, err
		}
	}
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
	delete(r.persisted, filepath.Clean(dir))
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
