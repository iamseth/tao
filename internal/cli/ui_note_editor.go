package cli

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/noteeditor"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/tui"
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

func (editor *uiNoteEditor) ListNoteRepositories(ctx context.Context) ([]tui.NoteRepository, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	inventory, ok := editor.app.registry().(note.RepositoryInventory)
	if !ok {
		return nil, fmt.Errorf("note repository inventory is unavailable")
	}
	entries, err := inventory.MetadataInventory()
	if err != nil {
		return nil, err
	}
	repositories := make([]tui.NoteRepository, 0, len(entries))
	for _, entry := range entries {
		if entry.MetadataError != nil {
			continue
		}
		repositories = append(repositories, tui.NoteRepository{ID: entry.Repo.ID, Name: entry.Repo.Name})
	}
	return repositories, ctx.Err()
}

// Create returns the persisted identity, or false for a cancelled composition.
// It never resolves a prefix, name, current checkout, or selected-note fallback.
func (editor *uiNoteEditor) Create(ctx context.Context, repositoryID string) (note.CatalogNote, bool, error) {
	if err := ctx.Err(); err != nil {
		return note.CatalogNote{}, false, err
	}
	registered, err := editor.creationRepository(repositoryID)
	if err != nil {
		return note.CatalogNote{}, false, err
	}
	text, tags, ready, err := editor.session.Compose(ctx, registered.Name+" ("+registered.ID+")")
	if err != nil || !ready {
		return note.CatalogNote{}, false, err
	}
	current, err := editor.creationRepository(repositoryID)
	if err != nil {
		return note.CatalogNote{}, false, err
	}
	if current.Root != registered.Root {
		return note.CatalogNote{}, false, fmt.Errorf("note repository changed during composition")
	}
	created, err := editor.app.noteRepository(current).Create(ctx, text, tags)
	if err != nil {
		return note.CatalogNote{}, false, fmt.Errorf("persist new note: %w", err)
	}
	return note.CatalogNote{
		RepositoryID: current.ID, RepositoryName: current.Name, RepositoryRoot: current.Root,
		ID: created.ID, Text: created.Text, Tags: created.Tags,
		CreatedAt: created.CreatedAt, UpdatedAt: created.UpdatedAt,
	}, true, nil
}

var uiNoteRepositoryID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

func (editor *uiNoteEditor) creationRepository(id string) (taodata.Repo, error) {
	if !uiNoteRepositoryID.MatchString(id) {
		return taodata.Repo{}, fmt.Errorf("an exact registered repository ID is required")
	}
	registered, err := editor.app.registry().ReadRepo(id)
	if err != nil {
		return taodata.Repo{}, fmt.Errorf("resolve note repository: %w", err)
	}
	if registered.Schema != taodata.RepoSchema || registered.ID != id ||
		strings.TrimSpace(registered.Name) == "" || strings.TrimSpace(registered.Root) == "" {
		return taodata.Repo{}, fmt.Errorf("invalid note repository metadata")
	}
	return registered, nil
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
