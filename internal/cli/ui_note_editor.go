package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/noteeditor"
)

type uiNoteEditor struct {
	app     App
	session noteeditor.Session
}

func newUINoteEditor(app App, input io.Reader, output io.Writer) *uiNoteEditor {
	return &uiNoteEditor{app: app, session: noteeditor.Session{
		Input: input, Output: output, Error: app.noteErrorOutput(),
	}}
}

func (editor *uiNoteEditor) Edit(ctx context.Context, item note.CatalogNote) (bool, error) {
	registered, err := editor.app.registry().ReadRepo(item.RepositoryID)
	if err != nil {
		return false, fmt.Errorf("resolve note repository: %w", err)
	}
	repo := editor.app.noteRepository(registered)
	current, err := repo.Get(ctx, item.ID)
	if err != nil {
		return false, fmt.Errorf("load note: %w", err)
	}
	if current.Status != note.StatusOpen {
		return false, fmt.Errorf("note %s is no longer open", current.ID)
	}
	text, tags, changed, err := editor.session.Edit(ctx, current)
	if err != nil || !changed {
		return changed, err
	}
	_, err = editor.app.mutateOpenNote(ctx, registered, current.ID, "TUI note edit", func() (note.Note, error) {
		return repo.Edit(ctx, current.ID, text, tags)
	})
	if err != nil {
		return false, fmt.Errorf("persist note edit: %w", err)
	}
	return true, nil
}
