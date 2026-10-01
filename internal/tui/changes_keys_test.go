package tui

import (
	"context"
	"testing"

	"github.com/iamseth/tao/internal/term"
)

func TestChangesFocusEscapeAndNavigation(t *testing.T) {
	s := loopState{size: term.Size{Width: 140, Height: 30}, detail: &detailState{activeTab: detailTabChanges}}
	d := s.detail
	d.changes = DetailChangesModel{Snapshot: DetailChangesSnapshot{WorktreeMissing: true, Files: []DetailChangedFile{{Path: "a"}, {Path: "b"}}}, Diff: DetailFileDiff{Lines: make([]DetailDiffLine, 100), Hunks: []int{0, 30, 60}}}
	a := App{} // Viewer controls do not require Actions.
	key := func(r rune) {
		t.Helper()
		a.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyRune, Rune: r})
	}
	key('l')
	if d.changes.Focus != DetailChangesFocusDiff {
		t.Fatal("focus")
	}
	key(']')
	if d.changes.DiffOffset != 30 {
		t.Fatal("next hunk")
	}
	key('[')
	if d.changes.DiffOffset != 0 {
		t.Fatal("previous hunk")
	}
	key('G')
	if !d.changes.Pinned {
		t.Fatal("bottom not pinned")
	}
	key('k')
	if d.changes.Pinned {
		t.Fatal("up retained pin")
	}
	key('z')
	if !d.changes.Zoom {
		t.Fatal("wide zoom")
	}
	key('w')
	if d.changes.RefreshError != "worktree missing; branch delta only" {
		t.Fatal("missing worktree toggle")
	}
	a.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyEsc})
	if s.detail == nil || d.changes.Focus != DetailChangesFocusFiles || d.changes.Zoom {
		t.Fatal("diff escape closed detail")
	}
	key('n')
	key('n')
	if d.changes.FileIndex != 1 {
		t.Fatal("file navigation wrapped")
	}
	key('p')
	if d.changes.FileIndex != 0 {
		t.Fatal("previous file")
	}
	d.sliceOpen = true
	a.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyEsc})
	if s.detail == nil || d.sliceOpen {
		t.Fatal("slice escape order")
	}
	a.handleKey(context.Background(), &s, term.KeyEvent{Key: term.KeyEsc})
	if s.detail != nil {
		t.Fatal("files escape did not close")
	}
}

func TestChangesFittingDiffPinsOnlyExplicitlyAndNarrowDoesNotZoom(t *testing.T) {
	s := loopState{size: term.Size{Width: 60, Height: 30}, detail: &detailState{activeTab: detailTabChanges}}
	s.detail.changes.Focus = DetailChangesFocusDiff
	a := App{}
	a.handleChangesKey(&s, term.KeyEvent{Key: term.KeyRune, Rune: 'j'})
	if s.detail.changes.Pinned {
		t.Fatal("ordinary movement pinned fitting diff")
	}
	a.handleChangesKey(&s, term.KeyEvent{Key: term.KeyRune, Rune: 'G'})
	a.handleChangesKey(&s, term.KeyEvent{Key: term.KeyRune, Rune: 'z'})
	if !s.detail.changes.Pinned || s.detail.changes.Zoom {
		t.Fatal("explicit pin lost or narrow view zoomed")
	}
}
