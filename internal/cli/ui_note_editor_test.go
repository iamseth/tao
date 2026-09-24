package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/noteeditor"
	"github.com/iamseth/tao/internal/taodata"
)

func TestUINoteRepositoryInventoryIsMetadataOnly(t *testing.T) {
	registry := taodata.NewRegistry(t.TempDir())
	repo := taodata.Repo{Schema: taodata.RepoSchema, ID: "no-notes", Name: "Empty", Root: "/missing/checkout"}
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	broken := repo
	broken.ID = "broken"
	broken.Schema = "unsupported"
	if err := registry.WriteRepo(broken); err != nil {
		t.Fatal(err)
	}
	registry.Runner = func(context.Context, string, string, []string, io.Writer, io.Writer) error {
		t.Fatal("inventory probed health")
		return nil
	}
	editor := newUINoteEditor(App{Registry: func() NoteRegistry { return registry }}, nil, nil)
	items, err := editor.ListNoteRepositories(context.Background())
	if err != nil || len(items) != 1 || items[0].ID != repo.ID || items[0].Name != repo.Name {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := editor.ListNoteRepositories(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled inventory: %v", err)
	}
	editor.app.Registry = func() NoteRegistry { return &fakeNoteRegistry{} }
	if _, err := editor.ListNoteRepositories(context.Background()); err == nil {
		t.Fatal("missing inventory accepted")
	}
}

type uiCreationStore struct {
	NoteRepository
	calls int
	err   error
}

func (store *uiCreationStore) Create(ctx context.Context, text string, tags []string) (note.Note, error) {
	store.calls++
	if store.err != nil {
		return note.Note{}, store.err
	}
	return store.NoteRepository.Create(ctx, text, tags)
}

type uiCreationRegistry struct {
	NoteRegistry
	read func(string) (taodata.Repo, error)
}

func (registry uiCreationRegistry) ReadRepo(id string) (taodata.Repo, error) {
	return registry.read(id)
}

func TestUINoteCreateRoutesExactRepositoryToDataHome(t *testing.T) {
	ctx := context.Background()
	first := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-1", Name: "same name", Root: "/first"}
	target := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-2", Name: "same name", Root: "/target"}
	app, _, _ := noteTestApp(t, nil, first, target)
	// Use the real registry to cover data-home routing independently of CWD.
	registry := taodata.NewRegistry(t.TempDir())
	for _, repo := range []taodata.Repo{first, target} {
		if err := registry.WriteRepo(repo); err != nil {
			t.Fatal(err)
		}
	}
	app.Registry = func() NoteRegistry { return registry }
	app.RepoHealthCheck = func(context.Context, taodata.Repo) taodata.RepoHealth {
		t.Fatal("creation must not probe execution health")
		return taodata.RepoHealth{}
	}
	store := &uiCreationStore{NoteRepository: note.NewRepository(registry.NotesDir(target), note.RepoReference{ID: target.ID, Root: target.Root})}
	factoryCalls := 0
	app.NoteRepository = func(dir string, ref note.RepoReference) NoteRepository {
		factoryCalls++
		if dir != registry.NotesDir(target) || ref.ID != target.ID || ref.Root != target.Root {
			t.Fatalf("wrong destination: %q %+v", dir, ref)
		}
		return store
	}
	editor := &uiNoteEditor{app: app, session: noteeditor.Session{TempDir: t.TempDir(), Runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
		if factoryCalls != 0 || store.calls != 0 {
			t.Fatal("store opened before composition")
		}
		if _, err := os.Stat(registry.NotesDir(target)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("placeholder store exists: %v", err)
		}
		content, err := os.ReadFile(args[len(args)-1])
		if err != nil {
			return err
		}
		if !strings.HasPrefix(string(content), "# Destination: same name (repo-2)\n") {
			t.Fatalf("missing canonical destination: %q", content)
		}
		// Informational header edits must never retarget persistence.
		return os.WriteFile(args[len(args)-1], []byte("# Destination: repo-1\ntags:\nTIER1\nbackend\n---\nnew note\n"), 0o600)
	}}}
	created, ok, err := editor.Create(ctx, target.ID)
	if err != nil || !ok || created.ID == "" || created.RepositoryID != target.ID || created.RepositoryRoot != target.Root || created.RepositoryName != target.Name {
		t.Fatalf("Create = %+v %t %v", created, ok, err)
	}
	if factoryCalls != 1 || store.calls != 1 {
		t.Fatalf("factory=%d Create=%d", factoryCalls, store.calls)
	}
	notes, warnings, err := store.List(ctx, note.Filter{All: true})
	if err != nil || len(warnings) != 0 || len(notes) != 1 {
		t.Fatalf("persisted notes = %+v %v %v", notes, warnings, err)
	}
	persisted := notes[0]
	if persisted.ID != created.ID || persisted.Status != note.StatusOpen || persisted.Text != "new note\n" || !slices.Equal(persisted.Tags, []string{"tier1", "backend"}) {
		t.Fatalf("persisted = %+v", persisted)
	}
	if _, err := os.Stat(registry.NotesDir(first)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("wrong repository store exists: %v", err)
	}
}

func TestUINoteCreateSkipsPersistenceUnlessCompositionSucceeds(t *testing.T) {
	for _, test := range []struct {
		name, buffer string
		failure      error
		wantError    bool
	}{
		{name: "unchanged"},
		{name: "tags only", buffer: "tags:\ntier1\n---\n \t"},
		{name: "malformed", buffer: "text", wantError: true},
		{name: "editor failed", failure: errors.New("editor failed"), wantError: true},
		{name: "editor cancelled", failure: context.Canceled, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-1", Name: "repo", Root: "/repo"}
			app, _, _ := noteTestApp(t, nil, target)
			app.NoteRepository = func(string, note.RepoReference) NoteRepository {
				t.Fatal("must not open note store")
				return nil
			}
			dir := t.TempDir()
			editor := &uiNoteEditor{app: app, session: noteeditor.Session{TempDir: dir, Runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
				if test.failure != nil {
					return test.failure
				}
				if test.buffer == "" {
					return nil
				}
				return os.WriteFile(args[len(args)-1], []byte(test.buffer), 0o600)
			}}}
			created, ok, err := editor.Create(context.Background(), target.ID)
			if (err != nil) != test.wantError || ok || created.ID != "" {
				t.Fatalf("Create = %+v %t %v", created, ok, err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("buffer cleanup = %v %v", entries, err)
			}
			if _, err := os.Stat(app.registry().NotesDir(target)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("placeholder store exists: %v", err)
			}
		})
	}
}

func TestUINoteCreateValidatesCanonicalIdentityBeforeAndAfterEditor(t *testing.T) {
	target := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-1", Name: "repo", Root: "/repo"}
	for _, phase := range []string{"before", "after"} {
		for _, damage := range []string{"missing", "mismatched ID", "schema", "empty name", "empty root", "changed root"} {
			if phase == "before" && damage == "changed root" {
				continue
			}
			t.Run(phase+"/"+damage, func(t *testing.T) {
				app, _, _ := noteTestApp(t, nil, target)
				registry := app.registry()
				reads, runs := 0, 0
				app.Registry = func() NoteRegistry {
					return uiCreationRegistry{NoteRegistry: registry, read: func(id string) (taodata.Repo, error) {
						reads++
						if id != target.ID {
							t.Fatalf("read unexpected ID %q", id)
						}
						if phase == "after" && reads == 1 {
							return target, nil
						}
						broken := target
						switch damage {
						case "missing":
							return taodata.Repo{}, os.ErrNotExist
						case "mismatched ID":
							broken.ID = "other"
						case "schema":
							broken.Schema = "unknown"
						case "empty name":
							broken.Name = " "
						case "empty root":
							broken.Root = " "
						case "changed root":
							broken.Root = "/other"
						}
						return broken, nil
					}}
				}
				app.NoteRepository = func(string, note.RepoReference) NoteRepository {
					t.Fatal("must not open store for invalid target")
					return nil
				}
				editor := &uiNoteEditor{app: app, session: noteeditor.Session{TempDir: t.TempDir(), Runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
					runs++
					return os.WriteFile(args[len(args)-1], []byte("tags:\n---\nnew note"), 0o600)
				}}}
				created, ok, err := editor.Create(context.Background(), target.ID)
				if err == nil || ok || created.ID != "" || (phase == "before" && (runs != 0 || reads != 1)) || (phase == "after" && (runs != 1 || reads != 2)) {
					t.Fatalf("Create = %+v %t %v, reads=%d runs=%d", created, ok, err, reads, runs)
				}
			})
		}
	}
}

func TestUINoteCreateRejectsSelectorsAndUnsafeIDs(t *testing.T) {
	target := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-1", Name: "project", Root: "/repo"}
	for _, id := range []string{"", " ", "repo-", "project", "missing", "../repo-1", "/repo-1", "repo-1/..", "repo-1\\..", "repo-1\n"} {
		t.Run(id, func(t *testing.T) {
			app, _, _ := noteTestApp(t, nil, target)
			editor := &uiNoteEditor{app: app, session: noteeditor.Session{Runner: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
				t.Fatal("editor launched for invalid selector")
				return nil
			}}}
			if _, ok, err := editor.Create(context.Background(), id); err == nil || ok {
				t.Fatalf("Create(%q) = %t %v", id, ok, err)
			}
		})
	}
}

func TestUINoteCreatePersistenceFailureDoesNotRetry(t *testing.T) {
	target := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-1", Name: "repo", Root: "/repo"}
	app, _, _ := noteTestApp(t, nil, target)
	failure := errors.New("write failed")
	store := &uiCreationStore{err: failure}
	app.NoteRepository = func(string, note.RepoReference) NoteRepository { return store }
	dir := t.TempDir()
	editor := &uiNoteEditor{app: app, session: noteeditor.Session{TempDir: dir, Runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
		return os.WriteFile(args[len(args)-1], []byte("tags:\n---\nnew note"), 0o600)
	}}}
	created, ok, err := editor.Create(context.Background(), target.ID)
	if !errors.Is(err, failure) || ok || created.ID != "" || store.calls != 1 {
		t.Fatalf("Create = %+v %t %v, calls=%d", created, ok, err, store.calls)
	}
	buffers, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil || len(buffers) != 0 {
		t.Fatalf("buffer cleanup = %v %v", buffers, err)
	}
}

func TestUINoteEditorPersistsTextAndTags(t *testing.T) {
	registered := taodata.Repo{ID: "repo-1", Name: "repo", Root: "/repo"}
	app, _, _ := noteTestApp(t, nil, registered)
	repo := app.noteRepository(registered)
	created, err := repo.Create(context.Background(), "old text", []string{"old"})
	if err != nil {
		t.Fatal(err)
	}
	editor := &uiNoteEditor{app: app, session: noteeditor.Session{
		TempDir: t.TempDir(),
		Runner: func(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) error {
			return os.WriteFile(args[len(args)-1], []byte("tags:\ntier0\ntier9\nbackend\n---\nnew text"), 0o600)
		},
	}}
	changed, err := editor.Edit(context.Background(), note.CatalogNote{RepositoryID: registered.ID, ID: created.ID})
	if err != nil || !changed {
		t.Fatalf("Edit() changed=%t error=%v", changed, err)
	}
	updated, err := repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Text != "new text" || !slices.Equal(updated.Tags, []string{"tier0", "tier9", "backend"}) {
		t.Fatalf("updated note text=%q tags=%v", updated.Text, updated.Tags)
	}

	changed, err = editor.SetTier(context.Background(), note.CatalogNote{RepositoryID: registered.ID, ID: created.ID}, 2)
	if err != nil || !changed {
		t.Fatalf("SetTier() changed=%t error=%v", changed, err)
	}
	updated, err = repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(updated.Tags, []string{"tier2", "backend"}) {
		t.Fatalf("tiered note tags=%v", updated.Tags)
	}
	changed, err = editor.SetTier(context.Background(), note.CatalogNote{RepositoryID: registered.ID, ID: created.ID}, 2)
	if err != nil || changed {
		t.Fatalf("idempotent SetTier() changed=%t error=%v", changed, err)
	}

	if err := editor.Delete(context.Background(), note.CatalogNote{RepositoryID: registered.ID, ID: created.ID}); err != nil {
		t.Fatal(err)
	}
	updated, err = repo.Get(context.Background(), created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != note.StatusArchived || updated.Archive == nil || updated.Archive.Reason != "deleted from Tao UI" {
		t.Fatalf("deleted note status=%q archive=%+v", updated.Status, updated.Archive)
	}
}
