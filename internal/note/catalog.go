package note

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/iamseth/tao/internal/taodata"
)

// RepositoryInventory is the metadata-only catalog boundary used by Collector.
type RepositoryInventory interface {
	MetadataInventory() ([]taodata.RepoInventoryEntry, error)
}

// Lister supplies notes and record-level warnings from one repository store.
type Lister interface {
	List(context.Context, Filter) ([]Note, []Warning, error)
}

// ListerFactory creates a note source for one valid inventory entry.
type ListerFactory func(taodata.RepoInventoryEntry) Lister

// CatalogNote is the read-only, open-note projection used by cross-repository
// consumers. Repository fields come from validated catalog metadata.
type CatalogNote struct {
	RepositoryID   string
	RepositoryName string
	RepositoryRoot string
	ID             string
	Text           string
	Tags           []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// CatalogWarningKind distinguishes damaged repository metadata or stores from
// malformed individual note records.
type CatalogWarningKind string

const (
	CatalogWarningRepository CatalogWarningKind = "repository"
	CatalogWarningRecord     CatalogWarningKind = "record"
)

// CatalogWarning retains a non-fatal cross-repository collection problem.
type CatalogWarning struct {
	Kind           CatalogWarningKind
	RepositoryID   string
	RepositoryName string
	Path           string
	Err            error
}

func (w CatalogWarning) Error() string {
	if w.Path != "" {
		return fmt.Sprintf("%s: %v", w.Path, w.Err)
	}
	return fmt.Sprintf("repository %s: %v", w.RepositoryID, w.Err)
}

// Snapshot is a deterministic projection of open notes and collection warnings.
type Snapshot struct {
	Notes    []CatalogNote
	Warnings []CatalogWarning
}

// Collector builds read-only note snapshots without repository health probes.
type Collector struct {
	Inventory RepositoryInventory
	NewLister ListerFactory
}

// NewCollector returns a filesystem-backed cross-repository note collector.
func NewCollector(inventory RepositoryInventory) Collector {
	return Collector{
		Inventory: inventory,
		NewLister: func(entry taodata.RepoInventoryEntry) Lister {
			return NewRepository(entry.NotesDir, RepoReference{ID: entry.Repo.ID, Root: entry.Repo.Root})
		},
	}
}

// Collect lists open notes from every valid inventory entry. Missing stores are
// empty; damaged repositories, stores, and records are retained as warnings.
func (c Collector) Collect(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	if c.Inventory == nil {
		return Snapshot{}, errors.New("note repository inventory is required")
	}
	inventory, err := c.Inventory.MetadataInventory()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read note repository inventory: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}

	var snapshot Snapshot
	for _, entry := range inventory {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		if entry.MetadataError != nil {
			snapshot.Warnings = append(snapshot.Warnings, catalogRepositoryWarning(entry, entry.MetadataError))
			continue
		}
		lister := c.lister(entry)
		if lister == nil {
			snapshot.Warnings = append(snapshot.Warnings, catalogRepositoryWarning(entry, errors.New("note store is unavailable")))
			continue
		}
		notes, warnings, listErr := lister.List(ctx, Filter{})
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		if listErr != nil {
			snapshot.Warnings = append(snapshot.Warnings, catalogRepositoryWarning(entry, fmt.Errorf("list notes: %w", listErr)))
			continue
		}
		for _, warning := range warnings {
			snapshot.Warnings = append(snapshot.Warnings, CatalogWarning{
				Kind:           CatalogWarningRecord,
				RepositoryID:   entry.Repo.ID,
				RepositoryName: entry.Repo.Name,
				Path:           warning.Path,
				Err:            warning.Err,
			})
		}
		for _, item := range notes {
			snapshot.Notes = append(snapshot.Notes, catalogNote(entry, item))
		}
	}

	sort.SliceStable(snapshot.Notes, func(i, j int) bool {
		left, right := snapshot.Notes[i], snapshot.Notes[j]
		if !left.UpdatedAt.Equal(right.UpdatedAt) {
			return left.UpdatedAt.After(right.UpdatedAt)
		}
		if left.RepositoryID != right.RepositoryID {
			return left.RepositoryID < right.RepositoryID
		}
		return left.ID < right.ID
	})
	return snapshot, nil
}

func (c Collector) lister(entry taodata.RepoInventoryEntry) Lister {
	if c.NewLister != nil {
		return c.NewLister(entry)
	}
	return NewRepository(entry.NotesDir, RepoReference{ID: entry.Repo.ID, Root: entry.Repo.Root})
}

func catalogNote(entry taodata.RepoInventoryEntry, item Note) CatalogNote {
	return CatalogNote{
		RepositoryID:   entry.Repo.ID,
		RepositoryName: entry.Repo.Name,
		RepositoryRoot: entry.Repo.Root,
		ID:             item.ID,
		Text:           item.Text,
		Tags:           append([]string(nil), item.Tags...),
		CreatedAt:      item.CreatedAt,
		UpdatedAt:      item.UpdatedAt,
	}
}

func catalogRepositoryWarning(entry taodata.RepoInventoryEntry, err error) CatalogWarning {
	return CatalogWarning{
		Kind:           CatalogWarningRepository,
		RepositoryID:   entry.Repo.ID,
		RepositoryName: entry.Repo.Name,
		Err:            err,
	}
}
