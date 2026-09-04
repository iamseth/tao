package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

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

func (editor *uiNoteEditor) Delete(ctx context.Context, item note.CatalogNote) error {
	registered, err := editor.app.registry().ReadRepo(item.RepositoryID)
	if err != nil {
		return fmt.Errorf("resolve note repository: %w", err)
	}
	repo := editor.app.noteRepository(registered)
	_, err = editor.app.mutateOpenNote(ctx, registered, item.ID, "TUI note delete", func() (note.Note, error) {
		current, getErr := repo.Get(ctx, item.ID)
		if getErr != nil {
			return note.Note{}, getErr
		}
		if current.Status != note.StatusOpen {
			return note.Note{}, fmt.Errorf("note %s is no longer open", current.ID)
		}
		return repo.Archive(ctx, current.ID, "deleted from Tao UI")
	})
	if err != nil {
		return fmt.Errorf("delete note: %w", err)
	}
	return nil
}

func (editor *uiNoteEditor) SetTier(ctx context.Context, item note.CatalogNote, tier int) (bool, error) {
	if tier < 0 || tier > 3 {
		return false, fmt.Errorf("unsupported note tier %d", tier)
	}
	registered, err := editor.app.registry().ReadRepo(item.RepositoryID)
	if err != nil {
		return false, fmt.Errorf("resolve note repository: %w", err)
	}
	repo := editor.app.noteRepository(registered)
	changed := false
	_, err = editor.app.mutateOpenNote(ctx, registered, item.ID, "TUI note tier", func() (note.Note, error) {
		current, getErr := repo.Get(ctx, item.ID)
		if getErr != nil {
			return note.Note{}, getErr
		}
		if current.Status != note.StatusOpen {
			return note.Note{}, fmt.Errorf("note %s is no longer open", current.ID)
		}
		target := fmt.Sprintf("tier%d", tier)
		tags := make([]string, 0, len(current.Tags)+1)
		tierAdded := false
		for _, tag := range current.Tags {
			if isUITierTag(tag) {
				if !tierAdded {
					tags = append(tags, target)
					tierAdded = true
				}
				continue
			}
			tags = append(tags, tag)
		}
		if !tierAdded {
			tags = append(tags, target)
		}
		if slices.Equal(tags, current.Tags) {
			return current, nil
		}
		changed = true
		return repo.Edit(ctx, current.ID, current.Text, tags)
	})
	if err != nil {
		return false, fmt.Errorf("set note tier: %w", err)
	}
	return changed, nil
}

func isUITierTag(tag string) bool {
	rest, found := strings.CutPrefix(tag, "tier")
	if !found || rest == "" {
		return false
	}
	for _, value := range rest {
		if value < '0' || value > '9' {
			return false
		}
	}
	return true
}
