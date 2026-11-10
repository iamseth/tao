package tui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
)

// Exercise routing through Run so each continuing key must resume the reader.
func TestRunForegroundDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, keys   string
		calls        int
		fail, cancel bool
	}{
		{name: "success", keys: "\x1b[Zppq", calls: 2},
		{name: "failure resumes", keys: "\x1b[Zppq", calls: 2, fail: true},
		{name: "cancellation exits", keys: "\x1b[Zppq", calls: 1, cancel: true},
		{name: "sort", keys: "\x1b[Zsnpx\x07q"},
		{name: "filter", keys: "\x1b[Zfnpx\x07q"},
		{name: "overlay", keys: "\x1b[Z?npx\x07q"},
		{name: "confirmation", keys: "\x1b[Zdpx\x07qq"},
		{name: "picker", keys: "\x1b[Znpx\x07q"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			terminal := &fakeTerminal{size: term.Size{Width: 100, Height: 24}}
			var output bytes.Buffer
			calls := 0
			app := App{Input: strings.NewReader(tc.keys), Output: &output, Terminal: terminal,
				Ticker: &fakeTicker{channel: make(chan time.Time)}, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{}}},
				Notes:       &fakeNoteCollector{snapshots: []note.Snapshot{{Notes: []note.CatalogNote{{RepositoryID: "repo", ID: "selected", Text: "body"}}}}},
				NoteActions: &fakeNoteActions{},
				NoteCreator: noteCreatorFunc(func(context.Context, string) (note.CatalogNote, bool, error) {
					t.Fatal("creation leaked")
					return note.CatalogNote{}, false, nil
				}),
				NoteRepositories: noteRepositoriesFunc(func(context.Context) ([]NoteRepository, error) {
					return []NoteRepository{{ID: "one"}, {ID: "two"}}, nil
				}),
				NotePlanningLauncher: notePlanningFunc(func(context.Context, note.CatalogNote) error {
					calls++
					if tc.cancel {
						cancel()
					}
					if tc.fail {
						return errors.New("launcher failure")
					}
					return nil
				}),
			}
			if err := app.Run(ctx); err != nil {
				t.Fatal(err)
			}
			if calls != tc.calls {
				t.Fatalf("launches=%d want %d", calls, tc.calls)
			}
			if terminal.enterCalls != calls+1 || terminal.restoreCalls != calls+1 {
				t.Fatalf("terminal enter=%d restore=%d", terminal.enterCalls, terminal.restoreCalls)
			}
			if tc.fail && !strings.Contains(output.String(), "launcher failure") {
				t.Fatal("missing failure feedback")
			}
		})
	}
}
