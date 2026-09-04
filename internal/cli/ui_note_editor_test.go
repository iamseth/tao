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
			return os.WriteFile(args[len(args)-1], []byte("tags:\ntier0\nbackend\n---\nnew text"), 0o600)
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
	if updated.Text != "new text" || !slices.Equal(updated.Tags, []string{"tier0", "backend"}) {
		t.Fatalf("updated note text=%q tags=%v", updated.Text, updated.Tags)
	}
}
