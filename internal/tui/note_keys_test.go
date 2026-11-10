package tui

import (
	"context"
	"testing"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
)

func TestNoteActionRoutingParity(t *testing.T) {
	for _, detail := range []bool{false, true} {
		for _, r := range "cdD0123" {
			item := note.CatalogNote{RepositoryID: "repo", ID: "target", Text: "text", Tags: []string{"tier3", "backend"}}
			clipboard := &fakeClipboard{}
			actions := &fakeNoteActions{changed: true}
			collector := &fakeNoteCollector{snapshots: []note.Snapshot{{Notes: []note.CatalogNote{item}}}}
			app := App{Clipboard: clipboard, NoteActions: actions, Notes: collector}
			other := note.CatalogNote{RepositoryID: "repo", ID: "other", Text: "other"}
			s := loopState{page: PageNotes, noteSnapshot: note.Snapshot{Notes: []note.CatalogNote{other, item}}}
			for index, candidate := range s.visibleNotes() {
				if candidate.ID == item.ID {
					s.selected = index
				}
			}
			if detail {
				s.noteDetail = &item
				s.noteSnapshot = note.Snapshot{Notes: []note.CatalogNote{other}}
				s.selected = 0
			}
			if app.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyRune, Rune: r}) {
				t.Fatal("note action quit")
			}
			switch r {
			case 'c':
				if len(clipboard.values) != 1 || clipboard.values[0] != item.ID {
					t.Fatalf("copy detail=%v: %v", detail, clipboard.values)
				}
			case 'd':
				if s.confirm == nil || len(actions.deleted) != 0 {
					t.Fatal("delete must confirm")
				}
				app.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyRune, Rune: 'y'})
				if len(actions.deleted) != 1 || actions.deleted[0].ID != item.ID {
					t.Fatal("confirmed wrong note")
				}
			case 'D':
				if s.confirm != nil || len(actions.deleted) != 1 || actions.deleted[0].ID != item.ID {
					t.Fatal("immediate delete wrong note")
				}
			default:
				if len(actions.tiers) != 1 || actions.tiers[0] != int(r-'0') || len(actions.tierNotes) != 1 || actions.tierNotes[0].ID != item.ID {
					t.Fatalf("tier detail=%v: %v", detail, actions.tiers)
				}
			}
		}
	}
}

func TestNoteKeysUnavailableAndUnknown(t *testing.T) {
	app := App{Clipboard: &fakeClipboard{}, NoteActions: &fakeNoteActions{}}
	s := loopState{page: PageNotes}
	for _, r := range "cdD0123~" {
		if app.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyRune, Rune: r}) || s.confirm != nil || s.noteDetail != nil {
			t.Fatalf("unavailable key %q", r)
		}
	}
	item := note.CatalogNote{ID: "detail"}
	s.noteDetail = &item
	app.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyTab})
	app.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyRune, Rune: '?'})
	if s.page != PageNotes || s.showShortcuts || s.noteDetail != &item {
		t.Fatal("unknown detail keys escaped")
	}
}
