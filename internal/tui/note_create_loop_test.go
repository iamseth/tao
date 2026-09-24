package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
)

type noteCreatorFunc func(context.Context, string) (note.CatalogNote, bool, error)

func (f noteCreatorFunc) Create(ctx context.Context, id string) (note.CatalogNote, bool, error) {
	return f(ctx, id)
}

type noteRepositoriesFunc func(context.Context) ([]NoteRepository, error)

func (f noteRepositoriesFunc) ListNoteRepositories(ctx context.Context) ([]NoteRepository, error) {
	return f(ctx)
}

type noteCollectorFunc func(context.Context) (note.Snapshot, error)

func (f noteCollectorFunc) Collect(ctx context.Context) (note.Snapshot, error) { return f(ctx) }

func TestRunNoteCreationDispatchAndExclusiveEditorInput(t *testing.T) {
	for _, tc := range []struct {
		name, keys, target string
		calls              int
		message            string
	}{
		{"empty picker", "\x1b[Zn\rXq", "repo-a", 1, "Created note created."},
		{"choose other repo", "\x1b[Znj\rXq", "repo-b", 1, "Created note created."},
		{"focused", "\x1b[ZfnXq", "repo-a", 1, "Created note created."},
		{"focused filtered empty", "\x1b[Zf/absent\rnXq", "repo-a", 1, "Not visible under current filters"},
		{"filtered empty picker", "\x1b[Z/absent\rnj\rXq", "repo-b", 1, "Not visible under current filters"},
		{"picker isolates actions", "\x1b[Zn\x07ndDcf03?\t/\rXq", "repo-a", 1, "Created note created."},
		{"cancel picker", "\x1b[Zn\x7fq", "", 0, "Note creation cancelled."},
		{"quit picker", "\x1b[Znq", "", 0, "Choose note repository"},
		{"ctrl c picker", "\x1b[Zn\x03", "", 0, "Choose note repository"},
		{"search owns n", "\x1b[Z/n\rq", "", 0, ""},
		{"help owns n", "\x1b[Z?n?q", "", 0, ""},
		{"detail owns n", "\x1b[Z\rnq", "", 0, ""},
		{"confirmation owns n", "\x1b[Zdnq", "", 0, ""},
		{"plans ignore n", "nq", "", 0, ""},
		{"settings ignore n", "\tnq", "", 0, ""},
		{"debug ignores n", "\t\tnq", "", 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input, writer := io.Pipe()
			defer func() { _ = input.Close() }()
			defer func() { _ = writer.Close() }()
			editorStarted := make(chan struct{})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			go func() {
				before, after, hasEditor := strings.Cut(tc.keys, "X")
				_, _ = io.WriteString(writer, before)
				if hasEditor {
					select {
					case <-editorStarted:
						_, _ = io.WriteString(writer, "X"+after)
					case <-ctx.Done():
					}
				}
			}()
			terminal := &fakeTerminal{size: term.Size{Width: 140, Height: 35}, resizes: make(chan struct{})}
			initial := note.CatalogNote{RepositoryID: "repo-a", ID: "old", Text: "existing"}
			snapshot := note.Snapshot{Notes: []note.CatalogNote{initial}}
			if tc.name == "empty picker" {
				snapshot = note.Snapshot{}
			}
			calls, listings, refreshes, edits := 0, 0, 0, 0
			actions := &fakeNoteActions{}
			var output bytes.Buffer
			err := (App{
				Input: input, Output: &output, Terminal: terminal, Ticker: &fakeTicker{channel: make(chan time.Time)},
				Collector: &fakeCollector{snapshots: []monitor.Snapshot{{}}},
				Notes:     noteCollectorFunc(func(context.Context) (note.Snapshot, error) { refreshes++; return snapshot, nil }),
				NoteRepositories: noteRepositoriesFunc(func(context.Context) ([]NoteRepository, error) {
					listings++
					return []NoteRepository{{ID: "repo-b", Name: "Beta"}, {ID: "repo-a", Name: "Alpha"}}, nil
				}),
				NoteCreator: noteCreatorFunc(func(_ context.Context, id string) (note.CatalogNote, bool, error) {
					calls++
					if id != tc.target {
						t.Fatalf("destination = %q, want %q", id, tc.target)
					}
					if terminal.enterCalls != 1 || terminal.restoreCalls != 1 {
						t.Fatal("editor did not receive restored terminal")
					}
					close(editorStarted)
					var b [1]byte
					if _, err := io.ReadFull(input, b[:]); err != nil || b[0] != 'X' {
						t.Fatalf("dashboard reader stole editor input: %q %v", b, err)
					}
					created := note.CatalogNote{RepositoryID: id, ID: "created", Text: "new note"}
					snapshot.Notes = append(snapshot.Notes, created)
					return created, true, nil
				}),
				NoteEditor:  noteEditorFunc(func(context.Context, note.CatalogNote) (bool, error) { edits++; return false, nil }),
				NoteActions: actions,
			}).Run(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if calls != tc.calls || refreshes != 1+tc.calls || edits != 0 || len(actions.deleted) > 0 || len(actions.tiers) > 0 {
				t.Fatalf("creates=%d refreshes=%d edits=%d actions=%+v", calls, refreshes, edits, actions)
			}
			if tc.calls > 0 && listings != 1 {
				t.Fatalf("listings=%d", listings)
			}
			if terminal.enterCalls != 1+calls || terminal.restoreCalls != 1+calls {
				t.Fatalf("terminal: %+v", terminal)
			}
			if !strings.Contains(output.String(), tc.message) {
				t.Fatalf("missing %q", tc.message)
			}
		})
	}
}

func TestNoteCreationFeedbackAndIdentitySelection(t *testing.T) {
	created := note.CatalogNote{RepositoryID: "repo-b", ID: "same-id", Text: "created", Tags: []string{"tier2"}}
	other := note.CatalogNote{RepositoryID: "repo-a", ID: created.ID, Text: "other", Tags: []string{"tier0"}}
	for _, tc := range []struct {
		name                  string
		saved                 bool
		createErr, refreshErr error
		query, message        string
	}{
		{name: "saved tier sorted", saved: true, message: "Created note same-id."},
		{name: "hidden", saved: true, query: "absent", message: "Not visible under current filters"},
		{name: "blank cancelled", message: "Note creation cancelled."},
		{name: "editor failed", createErr: errors.New("editor\x1b[31m\nfailed"), message: "Note creation failed: editor failed"},
		{name: "persistence failed", createErr: errors.New("persist new note: failed"), message: "Note creation failed: persist new note: failed"},
		{name: "refresh failed", saved: true, refreshErr: errors.New("disk\x1b[31m\nfailed"), message: "Created note same-id. Refresh failed: disk failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal := &fakeTerminal{}
			calls, refreshes := 0, 0
			app := App{Terminal: terminal, Output: io.Discard,
				NoteCreator: noteCreatorFunc(func(context.Context, string) (note.CatalogNote, bool, error) {
					calls++
					return created, tc.saved, tc.createErr
				}),
				Notes: noteCollectorFunc(func(context.Context) (note.Snapshot, error) {
					refreshes++
					return note.Snapshot{Notes: []note.CatalogNote{created, other}}, tc.refreshErr
				}),
			}
			state := loopState{page: PageNotes, searchQuery: tc.query}
			if err := app.createNote(context.Background(), &state, NoteRepository{ID: "repo-b"}); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || terminal.restoreCalls != 1 || terminal.enterCalls != 1 {
				t.Fatalf("calls=%d terminal=%+v", calls, terminal)
			}
			wantRefresh := 0
			if tc.saved {
				wantRefresh = 1
			}
			if refreshes != wantRefresh || !strings.Contains(state.noteEditMessage, tc.message) || strings.Contains(state.noteEditMessage, "\x1b") {
				t.Fatalf("refreshes=%d message=%q", refreshes, state.noteEditMessage)
			}
			if state.searchQuery != tc.query {
				t.Fatal("search changed")
			}
			if tc.name == "saved tier sorted" {
				selected, ok := state.selectedNote()
				if !ok || noteIdentity(selected) != noteIdentity(created) || state.selected != 1 {
					t.Fatalf("selected=%+v index=%d", selected, state.selected)
				}
			}
		})
	}
}

func TestNoteCreationUnavailableCatalog(t *testing.T) {
	for _, tc := range []struct {
		name, focus, want   string
		items               []NoteRepository
		err                 error
		noCreator, noLister bool
	}{
		{name: "no creator", noCreator: true, want: "unavailable"},
		{name: "no lister", noLister: true, want: "unavailable"},
		{name: "empty", want: "tao init"},
		{name: "error", err: errors.New(strings.Repeat("private\x1b", 500)), want: "tao repo list"},
		{name: "stale focus", focus: "missing", items: []NoteRepository{{ID: "repo"}}, want: "clear repository focus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := App{NoteCreator: noteCreatorFunc(func(context.Context, string) (note.CatalogNote, bool, error) {
				t.Fatal("unexpected create")
				return note.CatalogNote{}, false, nil
			}), NoteRepositories: noteRepositoriesFunc(func(context.Context) ([]NoteRepository, error) { return tc.items, tc.err })}
			if tc.noCreator {
				app.NoteCreator = nil
			}
			if tc.noLister {
				app.NoteRepositories = nil
			}
			state := loopState{page: PageNotes, focusRepositoryID: tc.focus}
			handled, quit, err := app.handleNoteCreation(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'n'})
			if !handled || quit || err != nil || state.notePicker != nil || !strings.Contains(state.noteEditMessage, tc.want) || len(state.noteEditMessage) > 250 {
				t.Fatalf("handled=%v quit=%v err=%v state=%+v", handled, quit, err, state)
			}
		})
	}
}

type failingCreationTerminal struct {
	fakeTerminal
	failEnter, failRestore int
}

func (t *failingCreationTerminal) EnterRaw() error {
	_ = t.fakeTerminal.EnterRaw()
	if t.enterCalls == t.failEnter {
		return errors.New("raw failure")
	}
	return nil
}
func (t *failingCreationTerminal) Restore() error {
	_ = t.fakeTerminal.Restore()
	if t.restoreCalls == t.failRestore {
		return errors.New("restore failure")
	}
	return nil
}

func TestRunNoteCreationFatalTerminalFailures(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		enter, restore, calls int
		want                  string
	}{
		{"suspend", 0, 1, 0, "suspend dashboard"},
		{"resume", 2, 0, 1, "resume dashboard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal := &failingCreationTerminal{fakeTerminal: fakeTerminal{size: term.Size{Width: 80, Height: 20}}, failEnter: tc.enter, failRestore: tc.restore}
			calls := 0
			err := (App{Input: strings.NewReader("\x1b[Zn\rq"), Output: io.Discard, Terminal: terminal, Ticker: &fakeTicker{channel: make(chan time.Time)}, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{}}}, NoteRepositories: noteRepositoriesFunc(func(context.Context) ([]NoteRepository, error) { return []NoteRepository{{ID: "repo"}}, nil }), NoteCreator: noteCreatorFunc(func(context.Context, string) (note.CatalogNote, bool, error) {
				calls++
				return note.CatalogNote{}, false, nil
			})}).Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls != tc.calls || terminal.restoreCalls != 2 {
				t.Fatalf("err=%v calls=%d restores=%d", err, calls, terminal.restoreCalls)
			}
		})
	}
}
