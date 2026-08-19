package note

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/taodata"
)

type catalogInventoryStub struct {
	entries []taodata.RepoInventoryEntry
	err     error
	called  bool
}

func (s *catalogInventoryStub) MetadataInventory() ([]taodata.RepoInventoryEntry, error) {
	s.called = true
	return s.entries, s.err
}

type catalogListerFunc func(context.Context, Filter) ([]Note, []Warning, error)

func (f catalogListerFunc) List(ctx context.Context, filter Filter) ([]Note, []Warning, error) {
	return f(ctx, filter)
}

func TestCollectorCollectsOpenNotesWarningsAndStableGlobalOrder(t *testing.T) {
	home := t.TempDir()
	updated := time.Date(2026, 8, 19, 4, 0, 0, 0, time.UTC)
	alphaDir := filepath.Join(home, "repo-alpha", "notes")
	betaDir := filepath.Join(home, "repo-beta", "notes")
	brokenDir := filepath.Join(home, "repo-broken", "notes")
	emptyDir := filepath.Join(home, "repo-empty", "notes")
	for _, dir := range []string{alphaDir, betaDir, brokenDir, emptyDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	writeCatalogTestNote(t, alphaDir, Note{ID: "note-z", Repo: RepoReference{ID: "repo-alpha"}, Text: "alpha z", Tags: []string{"z"}, CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, Status: StatusOpen})
	writeCatalogTestNote(t, alphaDir, Note{ID: "note-a", Repo: RepoReference{ID: "repo-alpha"}, Text: "alpha a", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, Status: StatusOpen})
	archivedPath := writeCatalogTestNote(t, alphaDir, Note{ID: "archived", Repo: RepoReference{ID: "repo-alpha"}, Text: "archived", CreatedAt: updated.Add(-2 * time.Hour), UpdatedAt: updated.Add(time.Hour), Status: StatusArchived, Archive: &ArchiveMetadata{ArchivedAt: updated.Add(time.Hour)}})
	if err := os.WriteFile(filepath.Join(alphaDir, "malformed.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeCatalogTestNote(t, betaDir, Note{ID: "newest", Repo: RepoReference{ID: "repo-beta"}, Text: "newest", CreatedAt: updated, UpdatedAt: updated.Add(time.Hour), Status: StatusOpen})
	writeCatalogTestNote(t, betaDir, Note{ID: "tie", Repo: RepoReference{ID: "repo-beta"}, Text: "beta tie", CreatedAt: updated.Add(-time.Hour), UpdatedAt: updated, Status: StatusOpen})
	writeCatalogTestNote(t, brokenDir, Note{ID: "must-not-load", Repo: RepoReference{ID: "repo-broken"}, Text: "hidden", CreatedAt: updated, UpdatedAt: updated.Add(2 * time.Hour), Status: StatusOpen})
	beforeArchived, err := os.ReadFile(archivedPath) //nolint:gosec // G304: helper returns a test-owned path rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}

	metadataErr := errors.New("malformed repository metadata")
	inventory := &catalogInventoryStub{entries: []taodata.RepoInventoryEntry{
		{Repo: taodata.Repo{ID: "repo-alpha", Name: "alpha", Root: "/alpha"}, NotesDir: alphaDir},
		{Repo: taodata.Repo{ID: "repo-beta", Name: "beta", Root: "/beta"}, NotesDir: betaDir},
		{Repo: taodata.Repo{ID: "repo-broken"}, NotesDir: brokenDir, MetadataError: metadataErr},
		{Repo: taodata.Repo{ID: "repo-empty", Name: "empty"}, NotesDir: emptyDir},
		{Repo: taodata.Repo{ID: "repo-missing", Name: "missing"}, NotesDir: filepath.Join(home, "missing", "notes")},
	}}

	snapshot, err := NewCollector(inventory).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, item := range snapshot.Notes {
		got = append(got, item.RepositoryID+"/"+item.ID)
	}
	want := []string{"repo-beta/newest", "repo-alpha/note-a", "repo-alpha/note-z", "repo-beta/tie"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered notes = %v, want %v", got, want)
	}
	if snapshot.Notes[1].RepositoryName != "alpha" || snapshot.Notes[1].RepositoryRoot != "/alpha" || !reflect.DeepEqual(snapshot.Notes[2].Tags, []string{"z"}) {
		t.Fatalf("catalog projection lost repository or note fields: %#v", snapshot.Notes)
	}
	if len(snapshot.Warnings) != 2 {
		t.Fatalf("warnings = %#v, want malformed record and repository metadata", snapshot.Warnings)
	}
	if snapshot.Warnings[0].Kind != CatalogWarningRecord || filepath.Base(snapshot.Warnings[0].Path) != "malformed.json" {
		t.Fatalf("record warning = %#v", snapshot.Warnings[0])
	}
	if snapshot.Warnings[1].Kind != CatalogWarningRepository || snapshot.Warnings[1].RepositoryID != "repo-broken" || !errors.Is(snapshot.Warnings[1].Err, metadataErr) {
		t.Fatalf("repository warning = %#v", snapshot.Warnings[1])
	}
	afterArchived, err := os.ReadFile(archivedPath) //nolint:gosec // G304: helper returns a test-owned path rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if string(afterArchived) != string(beforeArchived) {
		t.Fatal("collection mutated archived note lifecycle state")
	}
}

func TestCollectorRetainsStoreErrorsAsRepositoryWarnings(t *testing.T) {
	entry := taodata.RepoInventoryEntry{Repo: taodata.Repo{ID: "repo-a", Name: "alpha"}, NotesDir: "/unused"}
	inventory := &catalogInventoryStub{entries: []taodata.RepoInventoryEntry{entry}}
	storeErr := errors.New("store unavailable")
	collector := NewCollector(inventory)
	collector.NewLister = func(taodata.RepoInventoryEntry) Lister {
		return catalogListerFunc(func(context.Context, Filter) ([]Note, []Warning, error) {
			return nil, nil, storeErr
		})
	}

	snapshot, err := collector.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Notes) != 0 || len(snapshot.Warnings) != 1 || snapshot.Warnings[0].Kind != CatalogWarningRepository || !errors.Is(snapshot.Warnings[0].Err, storeErr) {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestCollectorHonorsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inventory := &catalogInventoryStub{entries: []taodata.RepoInventoryEntry{{Repo: taodata.Repo{ID: "repo-a"}, NotesDir: "/unused"}}}
	collector := NewCollector(inventory)
	collector.NewLister = func(taodata.RepoInventoryEntry) Lister {
		return catalogListerFunc(func(context.Context, Filter) ([]Note, []Warning, error) {
			cancel()
			return []Note{{ID: "should-not-return"}}, nil, nil
		})
	}

	snapshot, err := collector.Collect(ctx)
	if !errors.Is(err, context.Canceled) || len(snapshot.Notes) != 0 || len(snapshot.Warnings) != 0 {
		t.Fatalf("Collect canceled = %#v, %v", snapshot, err)
	}

	preCanceled, stop := context.WithCancel(context.Background())
	stop()
	unused := &catalogInventoryStub{}
	if _, err := NewCollector(unused).Collect(preCanceled); !errors.Is(err, context.Canceled) || unused.called {
		t.Fatalf("pre-canceled Collect err = %v, inventory called = %t", err, unused.called)
	}
}

func writeCatalogTestNote(t *testing.T, dir string, item Note) string {
	t.Helper()
	item.Schema = Schema
	content, err := json.MarshalIndent(item, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	path := filepath.Join(dir, item.ID+".json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
