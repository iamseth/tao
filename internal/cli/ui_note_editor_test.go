package cli

import (
	"context"
	"io"
	"os"
	"slices"
	"testing"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/noteeditor"
	"github.com/iamseth/tao/internal/taodata"
)

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
