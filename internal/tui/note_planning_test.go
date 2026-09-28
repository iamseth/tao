package tui

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/term"
)

func TestRunNotePlanningPreservesSettingsP(t *testing.T) {
	settings := &fakeSettingsService{snapshot: SettingsSnapshot{Repositories: []RepositorySetting{{ID: "repo", Name: "repo", Health: "ok"}}}}
	err := (App{Input: strings.NewReader("\tpyq"), Output: io.Discard, Terminal: &fakeTerminal{size: term.Size{Width: 80, Height: 24}}, Ticker: &fakeTicker{}, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{}}}, Settings: settings, NotePlanningLauncher: notePlanningFunc(func(context.Context, note.CatalogNote) error { t.Fatal("settings p launched planning"); return nil })}).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.calls != 1 || settings.snapshot.Repositories[0].PullRequest == nil || !*settings.snapshot.Repositories[0].PullRequest {
		t.Fatal("settings p did not retain its meaning")
	}
}

func TestNotePlanningGuardsAndFeedback(t *testing.T) {
	item := note.CatalogNote{RepositoryID: "repo", ID: "note", Text: "unchanged"}
	for _, guard := range []string{"shortcuts", "search", "confirm", "detail", "picker", "filter", "settings", "empty", "unavailable", "failure", "success"} {
		t.Run(guard, func(t *testing.T) {
			state := loopState{page: PageNotes, noteSnapshot: note.Snapshot{Notes: []note.CatalogNote{item}}, size: term.Size{Width: 80, Height: 24}, filter: repositoryFilter("repo")}
			switch guard {
			case "shortcuts":
				state.showShortcuts = true
			case "search":
				state.searchActive = true
			case "confirm":
				state.confirm = &confirmPrompt{}
			case "detail":
				state.detail = &detailState{}
			case "picker":
				state.notePicker = &noteRepositoryPicker{}
			case "filter":
				state.filterMenu = &filterMenu{}
			case "settings":
				state.page = PageSettings
			case "empty":
				state.noteSnapshot = note.Snapshot{}
			}
			terminal := &fakeTerminal{size: term.Size{Width: 42, Height: 15}}
			calls := 0
			app := App{Terminal: terminal, Output: io.Discard, NotePlanningLauncher: notePlanningFunc(func(context.Context, note.CatalogNote) error {
				calls++
				if guard == "failure" {
					return errors.New("bad\x1b]52;c;secret\a\n\r\t" + strings.Repeat("界", 300))
				}
				return nil
			})}
			if guard == "unavailable" {
				app.NotePlanningLauncher = nil
			}
			before := state.noteSnapshot
			filter := state.filter
			handled, err := app.planSelectedNote(context.Background(), &state, term.KeyEvent{Key: term.KeyRune, Rune: 'p'})
			if err != nil {
				t.Fatal(err)
			}
			want := guard == "success" || guard == "failure" || guard == "unavailable"
			if handled != want || (calls == 1) != (want && guard != "unavailable") {
				t.Fatalf("handled=%t calls=%d", handled, calls)
			}
			if !reflect.DeepEqual(before, state.noteSnapshot) || !reflect.DeepEqual(filter, state.filter) {
				t.Fatal("planning changed notes or filters")
			}
			if calls == 1 && state.size != terminal.size {
				t.Fatalf("size not reconciled: %+v", state.size)
			}
			if guard == "unavailable" && state.noteEditMessage != "Note planning is unavailable." {
				t.Fatal(state.noteEditMessage)
			}
			if guard == "success" && state.noteEditMessage != "" {
				t.Fatalf("success must not claim lifecycle changes: %q", state.noteEditMessage)
			}
			if guard == "failure" {
				if !strings.HasPrefix(state.noteEditMessage, "Note planning failed: bad") || strings.Contains(state.noteEditMessage, "secret") || len([]rune(state.noteEditMessage)) > 270 {
					t.Fatal(state.noteEditMessage)
				}
				for _, r := range state.noteEditMessage {
					if unicode.IsControl(r) {
						t.Fatalf("control in feedback: %q", state.noteEditMessage)
					}
				}
			}
		})
	}
}

type planningFaultTerminal struct {
	fakeTerminal
	fail          string
	baselineCalls int
}

func (t *planningFaultTerminal) PreserveBaseline() func() error {
	return func() error {
		t.baselineCalls++
		if t.fail == "baseline" {
			return errors.New("baseline failure")
		}
		return nil
	}
}

func (t *planningFaultTerminal) EnterRaw() error {
	_ = t.fakeTerminal.EnterRaw()
	if t.enterCalls == 2 && t.baselineCalls != 1 {
		return errors.New("resumed before restoring baseline")
	}
	if t.fail == "resume" && t.enterCalls == 2 {
		return errors.New("raw failure")
	}
	return nil
}
func (t *planningFaultTerminal) Restore() error {
	_ = t.fakeTerminal.Restore()
	if t.fail == "suspend" && t.restoreCalls == 1 {
		return errors.New("restore failure")
	}
	return nil
}

func TestNotePlanningFailureCleanup(t *testing.T) {
	for _, failure := range []string{"suspend", "resume", "panic", "cancel", "baseline"} {
		t.Run(failure, func(t *testing.T) {
			terminal := &planningFaultTerminal{fakeTerminal: fakeTerminal{size: term.Size{Width: 80, Height: 24}}, fail: failure}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var output bytes.Buffer
			calls := 0
			app := App{Input: strings.NewReader("\x1b[Zp"), Output: &output, Terminal: terminal, Ticker: &fakeTicker{}, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{}}}, Notes: &fakeNoteCollector{snapshots: []note.Snapshot{{Notes: []note.CatalogNote{{ID: "note"}}}}}, NotePlanningLauncher: notePlanningFunc(func(ctx context.Context, _ note.CatalogNote) error {
				calls++
				if failure == "panic" {
					panic("child panic")
				}
				if failure == "cancel" {
					cancel()
					<-ctx.Done()
					return ctx.Err()
				}
				return nil
			})}
			var err error
			var caught any
			func() { defer func() { caught = recover() }(); err = app.Run(ctx) }()
			if failure == "panic" && caught != "child panic" {
				t.Fatalf("panic=%v", caught)
			}
			if (failure == "suspend" || failure == "resume") && (err == nil || !strings.Contains(err.Error(), failure+" dashboard")) {
				t.Fatalf("error=%v", err)
			}
			if terminal.baselineCalls != 1 {
				t.Fatalf("baseline restores=%d", terminal.baselineCalls)
			}
			if failure == "baseline" && (err == nil || !strings.Contains(err.Error(), "baseline failure") || terminal.enterCalls != 1) {
				t.Fatalf("baseline failure must stop before resume: err=%v enters=%d", err, terminal.enterCalls)
			}
			if failure == "cancel" && err != nil {
				t.Fatal(err)
			}
			if (calls == 0) != (failure == "suspend") || terminal.restoreCalls != 2 {
				t.Fatalf("calls=%d restores=%d", calls, terminal.restoreCalls)
			}
			if !strings.HasSuffix(output.String(), "\x1b[?1049l") {
				t.Fatalf("alternate screen not cleaned up: %q", output.String())
			}
		})
	}
}

type notePlanningFunc func(context.Context, note.CatalogNote) error

func (f notePlanningFunc) Launch(ctx context.Context, item note.CatalogNote) error {
	return f(ctx, item)
}

func TestRunNotePlanningDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, keys string
		empty      bool
		calls      int
	}{
		{name: "list repeated", keys: "\x1b[Zppq", calls: 2},
		{name: "detail", keys: "\x1b[Z\rpq", calls: 1},
		{name: "empty", keys: "\x1b[Zpq", empty: true},
		{name: "search", keys: "\x1b[Z/p\rq"},
		{name: "shortcuts", keys: "\x1b[Z?pq"},
		{name: "plans", keys: "pq"},
		{name: "uppercase", keys: "\x1b[ZPq"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			terminal := &fakeTerminal{size: term.Size{Width: 80, Height: 24}}
			item := note.CatalogNote{RepositoryID: "repo", ID: "note", Text: "unchanged"}
			snapshot := note.Snapshot{}
			if !tc.empty {
				snapshot.Notes = []note.CatalogNote{item}
			}
			notes := &fakeNoteCollector{snapshots: []note.Snapshot{snapshot}}
			calls := 0
			err := (App{Input: strings.NewReader(tc.keys), Output: io.Discard, Terminal: terminal, Ticker: &fakeTicker{channel: make(chan time.Time)}, Collector: &fakeCollector{snapshots: []monitor.Snapshot{{}}}, Notes: notes,
				NotePlanningLauncher: notePlanningFunc(func(_ context.Context, got note.CatalogNote) error {
					calls++
					if got.ID != item.ID || terminal.restoreCalls != calls || terminal.enterCalls != calls {
						t.Fatalf("handoff item=%+v terminal=%+v", got, terminal)
					}
					return nil
				}),
			}).Run(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if calls != tc.calls || terminal.enterCalls != tc.calls+1 || terminal.restoreCalls != tc.calls+1 || notes.callCount() != 1 {
				t.Fatalf("calls=%d enter=%d restore=%d collects=%d", calls, terminal.enterCalls, terminal.restoreCalls, notes.callCount())
			}
		})
	}
}
