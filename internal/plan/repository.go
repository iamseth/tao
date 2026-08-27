package plan

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/iamseth/tao/internal/taodata"
)

// Repository exposes plan lookup without committing callers to file-backed artifacts.
type Repository interface {
	ListPlans(ctx context.Context, filter PlanFilter) ([]PlanSummary, error)
	GetPlan(ctx context.Context, id string) (*PlanDetail, error)
}

type PlanDeleter interface {
	DeletePlan(ctx context.Context, input string, opts DeletePlanOptions) (*DeletePlanResult, error)
}

type DeletePlanOptions struct {
	ConfirmInvalid bool
	AllowActive    bool
}

type DeletePlanResult struct {
	ID      string
	Dir     string
	Invalid bool
}

type LogAppender interface {
	OpenLogAppend(planDir string) (*os.File, error)
}

type LogReader interface {
	ReadLog(planDir string) (string, error)
}

type LogTailReader interface {
	ReadLogTail(planDir string, tail int) (string, error)
}

type LogFollower interface {
	FollowLog(ctx context.Context, planDir string, out io.Writer) error
}

type Resolver interface {
	ResolvePlan(ctx context.Context, input string) (*PlanDetail, error)
}

// ExactPlanResolver loads one plan by its complete repository-scoped ID. It
// deliberately excludes path, prefix, and slug resolution for runtime links.
type ExactPlanResolver interface {
	GetPlanExact(ctx context.Context, id string) (*PlanDetail, error)
}

// PlanRecordStore creates the preferred lifecycle mutation boundary for an
// already-loaded plan detail.
type PlanRecordStore interface {
	PlanRecord(detail *PlanDetail) (*PlanRecord, error)
}

// PlanRecordResolver combines user input resolution with the record boundary for
// commands that mutate exactly one plan.
type PlanRecordResolver interface {
	ResolvePlanRecord(ctx context.Context, input string) (*PlanRecord, error)
}

type EventAppender interface {
	AppendEvent(planDir string, event Event) error
}

// SliceRunStore is the run package's artifact boundary: it exposes telemetry
// writes plus a record factory for plan lifecycle mutations.
type SliceRunStore interface {
	LogAppender
	EventAppender
	PlanRecordStore
}

type SliceRunRepository interface {
	Resolver
	SliceRunStore
}

// repositoryArtifactOperations inventories artifact methods promoted from the
// shared helper to both repository implementations.
type repositoryArtifactOperations interface {
	AppendEvent(planDir string, event Event) error
}

// repositoryLogOperations inventories log methods promoted from the shared helper.
type repositoryLogOperations interface {
	LogAppender
	LogReader
	LogTailReader
	LogFollower
}

var (
	_ Repository                   = (*FileRepository)(nil)
	_ Resolver                     = (*FileRepository)(nil)
	_ ExactPlanResolver            = (*FileRepository)(nil)
	_ PlanRecordStore              = (*FileRepository)(nil)
	_ PlanRecordResolver           = (*FileRepository)(nil)
	_ PlanDeleter                  = (*FileRepository)(nil)
	_ repositoryArtifactOperations = (*FileRepository)(nil)
	_ repositoryLogOperations      = (*FileRepository)(nil)
)

// FileRepository is the package boundary between callers and on-disk plan
// artifacts. It keeps filesystem details behind artifactStore so loading,
// PlanRecord mutation, repository operations, and tests share one path.
type FileRepository struct {
	artifactOperations
	Dir   string
	Now   func() time.Time
	store artifactStore
}

// artifactStore is the internal seam for repository filesystem effects.
type artifactStore interface {
	artifactMutationStore
	writeSlices(planDir string, slices SlicesFile) error
	appendEvent(planDir string, event Event) error
	listDirs(ctx context.Context, root string) ([]planDirEntry, error)
	loadDir(dir string) (*PlanDetail, error)
	inputDir(path string) (string, bool, error)
	isDir(path string) bool
	openLogAppend(planDir string) (*os.File, error)
	readLog(planDir string) (string, error)
	readLogTail(planDir string, tail int) (string, error)
	followLog(ctx context.Context, planDir string, out io.Writer) error
	removeAll(path string) error
}

type artifactMutationStore interface {
	writeState(planDir string, state State) error
	withMutationLock(planDir string, operation func() error) error
	settleMutationLocked(planDir string, journal mutationJournal) error
	refreshMutationDetailLocked(planDir string, expectedPlanID string, force bool) (mutationDetailRefresh, error)
}

type planDirEntry struct {
	ID  string
	Dir string
}

type fileArtifactStore struct{}

// DefaultDir resolves to the centralized plans directory for the current source repository.
func DefaultDir() string {
	registry := taodata.NewRegistry("")
	if repo, err := registry.Current(context.Background()); err == nil && repo.ID != "" {
		return registry.PlansDir(repo)
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		if root, err := filepath.EvalSymlinks(cwd); err == nil {
			return registry.PlansDir(taodata.Repo{ID: taodata.RepoID(root), Root: root, Name: filepath.Base(root)})
		}
	}
	return filepath.Join(taodata.DataHome(), "repos", "unknown", "plans")
}

func NewFileRepository(dir string) *FileRepository {
	if dir == "" {
		dir = DefaultDir()
	}
	repo := &FileRepository{Dir: dir, Now: time.Now, store: fileArtifactStore{}}
	repo.artifactOperations = artifactOperations{store: &repo.store}
	return repo
}

func (r *FileRepository) PlanRecord(detail *PlanDetail) (*PlanRecord, error) {
	return r.record("", detail)
}

func (r *FileRepository) ResolvePlanRecord(ctx context.Context, input string) (*PlanRecord, error) {
	return resolvePlanRecord(ctx, r, r, input)
}

func resolvePlanRecord(ctx context.Context, resolver Resolver, store PlanRecordStore, input string) (*PlanRecord, error) {
	detail, err := resolver.ResolvePlan(ctx, input)
	if err != nil {
		return nil, err
	}
	return store.PlanRecord(detail)
}

func (r *FileRepository) ListPlans(ctx context.Context, filter PlanFilter) ([]PlanSummary, error) {
	entries, err := r.artifacts().listDirs(ctx, r.Dir)
	if err != nil {
		// A missing plans directory means no plans yet (fresh data home or a
		// repo that has never run a plan); report it as empty rather than
		// failing best-effort callers like status views.
		if os.IsNotExist(err) {
			return []PlanSummary{}, nil
		}
		return nil, err
	}

	summaries := make([]PlanSummary, 0, len(entries))
	for _, entry := range entries {
		detail, err := r.loadPlanDir(entry.Dir)
		if err != nil {
			summaries = append(summaries, invalidPlanSummary(entry.ID, entry.Dir, err))
			continue
		}

		summaries = appendPlanSummary(summaries, detail, filter, r.now())
	}

	sortPlanSummaries(summaries)

	return summaries, nil
}

func (r *FileRepository) loadPlanDir(dir string) (*PlanDetail, error) {
	return r.artifacts().loadDir(dir)
}

func (r *FileRepository) DeletePlan(ctx context.Context, input string, opts DeletePlanOptions) (*DeletePlanResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	dir, id, err := r.resolveDeletePlanDir(ctx, input)
	if err != nil {
		return nil, err
	}
	safeDir, err := r.safeDeletePlanDir(dir)
	if err != nil {
		return nil, err
	}

	result := &DeletePlanResult{ID: id, Dir: safeDir}
	detail, err := r.loadPlanDir(safeDir)
	if err != nil {
		if !opts.ConfirmInvalid {
			return nil, &classifiedError{
				msg:    fmt.Sprintf("plan %q is invalid; confirm invalid deletion: %s", id, err),
				causes: []error{ErrInvalid, err},
			}
		}
		result.Invalid = true
	} else {
		if detail.State.Plan.ID != "" {
			result.ID = detail.State.Plan.ID
		}
		if !opts.AllowActive && Derive(detail, r.now()).Active {
			return nil, classify(ErrActive, "plan %q is active and cannot be deleted", result.ID)
		}
	}

	if err := r.artifacts().removeAll(safeDir); err != nil {
		return nil, err
	}
	return result, nil
}

func (fileArtifactStore) listDirs(ctx context.Context, root string) ([]planDirEntry, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	dirs := make([]planDirEntry, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		dirs = append(dirs, planDirEntry{ID: entry.Name(), Dir: filepath.Join(root, entry.Name())})
	}
	return dirs, nil
}

func (fileArtifactStore) loadDir(dir string) (*PlanDetail, error) {
	files, err := loadPlanFiles(dir)
	if err != nil {
		return nil, err
	}
	return detailFromFiles(files), nil
}

func (fileArtifactStore) writeState(planDir string, state State) error {
	return writeState(planDir, state)
}

func (fileArtifactStore) writeSlices(planDir string, slices SlicesFile) error {
	return writeSlices(planDir, slices)
}

func (fileArtifactStore) appendEvent(planDir string, event Event) error {
	return AppendEvent(planDir, event)
}

func (fileArtifactStore) inputDir(path string) (string, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	if info.IsDir() {
		return path, true, nil
	}
	return filepath.Dir(path), true, nil
}

func (fileArtifactStore) isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func (s fileArtifactStore) openLogAppend(planDir string) (*os.File, error) {
	return os.OpenFile(LogPath(planDir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
}

func (s fileArtifactStore) readLog(planDir string) (string, error) {
	content, err := os.ReadFile(LogPath(planDir))
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func (s fileArtifactStore) readLogTail(planDir string, tail int) (string, error) {
	content, err := s.readLog(planDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("read agent log: %w", err)
	}
	if tail <= 0 {
		return content, nil
	}
	return lastLines(content, tail), nil
}

func (s fileArtifactStore) followLog(ctx context.Context, planDir string, out io.Writer) error {
	logFile, err := os.Open(LogPath(planDir))
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()

	if _, err := io.Copy(out, logFile); err != nil {
		return err
	}

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := io.Copy(out, logFile); err != nil {
				return err
			}
		}
	}
}

func (fileArtifactStore) removeAll(path string) error {
	return os.RemoveAll(path)
}

func detailFromFiles(files planFiles) *PlanDetail {
	detail := &PlanDetail{Dir: files.dir, State: files.state, Slices: files.slices, Events: files.events, PlanningSession: files.planningSession, PlanningBrief: files.planningBrief, Review: files.review, PlanNarrative: files.planNarrative, Warnings: files.warnings}
	stateBaseline := cloneState(detail.State)
	slicesBaseline := cloneSlicesFile(detail.Slices)
	detail.loadedStateBaseline = &stateBaseline
	detail.loadedSlicesBaseline = &slicesBaseline
	detail.Warnings = append(detail.Warnings, ValidateDetail(detail)...)
	return detail
}

func invalidPlanSummary(id string, dir string, err error) PlanSummary {
	return PlanSummary{ID: id, Dir: dir, Status: StatusInvalid, Warnings: []string{err.Error()}}
}

func appendPlanSummary(summaries []PlanSummary, detail *PlanDetail, filter PlanFilter, now time.Time) []PlanSummary {
	summary := Summarize(detail, now)
	if filter.ActiveOnly && !summary.Active() {
		return summaries
	}
	return append(summaries, summary)
}

func sortPlanSummaries(summaries []PlanSummary) {
	sort.Slice(summaries, func(i, j int) bool {
		left := summaries[i].LastActivityAt
		right := summaries[j].LastActivityAt
		if left == nil && right == nil {
			return summaries[i].ID > summaries[j].ID
		}
		if left == nil {
			return false
		}
		if right == nil {
			return true
		}
		return left.After(*right)
	})
}

func (r *FileRepository) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *FileRepository) artifacts() artifactStore {
	if r.store != nil {
		return r.store
	}
	return fileArtifactStore{}
}
