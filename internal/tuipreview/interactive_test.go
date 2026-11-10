package tuipreview

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/tui"
)

type previewTestTerminal struct{}

func (previewTestTerminal) EnterRaw() error                              { return nil }
func (previewTestTerminal) Restore() error                               { return nil }
func (previewTestTerminal) PreserveBaseline() func() error               { return func() error { return nil } }
func (previewTestTerminal) Size() (term.Size, error)                     { return term.Size{Width: 80, Height: 24}, nil }
func (previewTestTerminal) ResizeEvents(context.Context) <-chan struct{} { return nil }

type previewTestTicker struct {
	updates chan time.Time
}

func (t *previewTestTicker) C() <-chan time.Time { return t.updates }
func (t *previewTestTicker) Stop()               {}

func TestNewInteractiveAppUsesOnlyFixtureBoundaries(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	ticker := &previewTestTicker{updates: make(chan time.Time)}
	app := NewInteractiveApp(scenario, strings.NewReader(""), io.Discard, previewTestTerminal{}, ticker)

	if app.NoteCreator != nil || app.NoteRepositories != nil || app.NoteEditor != nil || app.NoteActions != nil || app.NotePlanningLauncher != nil {
		t.Fatal("interactive fixture app has note mutation services")
	}
	if app.Actions != nil {
		t.Fatal("interactive fixture app has production actions")
	}
	if app.Terminal == nil || app.Ticker != ticker || app.Collector == nil || app.Notes == nil || app.Debug == nil || app.Trends == nil || app.Settings == nil || app.Details == nil || app.Changes == nil {
		t.Fatalf("interactive fixture app is incompletely wired: %+v", app)
	}
	if got := app.Now(); !got.Equal(scenario.Now) {
		t.Fatalf("interactive clock = %v, want %v", got, scenario.Now)
	}
	snapshot, err := app.Collector.Collect(context.Background())
	if err != nil || len(snapshot.Rows) != len(scenario.Snapshot.Rows) {
		t.Fatalf("fixture collector rows = %d, error = %v", len(snapshot.Rows), err)
	}
	detail, err := app.Details.ResolvePlan(context.Background(), scenario.Plans[0].PlanDir)
	if err != nil || detail.State.Plan.ID != scenario.Plans[0].Detail.State.Plan.ID {
		t.Fatalf("fixture detail = %#v, error = %v", detail, err)
	}
}

func TestInteractiveTrendsCohortsAndReadOnlyKeys(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	var output bytes.Buffer
	app := NewInteractiveApp(scenario, strings.NewReader("\tcramxp\rc cq"), &output, previewTestTerminal{}, &previewTestTicker{})
	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"monthly · overall", "monthly · pi", "monthly · claude"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("missing cohort %q: %s", want, output.String())
		}
	}
	if strings.Contains(output.String(), "PLAN DETAIL") || strings.Contains(output.String(), "Confirm") {
		t.Fatal("Trends opened a detail or mutation dialog")
	}
}

type failingPreviewTrends struct {
	snapshot tui.TrendsSnapshot
	calls    int
}

func (c *failingPreviewTrends) Collect(ctx context.Context) (tui.TrendsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tui.TrendsSnapshot{}, err
	}
	c.calls++
	if c.calls > 1 {
		return tui.TrendsSnapshot{}, errors.New("synthetic failure")
	}
	return cloneTrends(c.snapshot), nil
}

type trendFrameObserver struct {
	bytes.Buffer
	good, failed chan struct{}
}

func (w *trendFrameObserver) Write(p []byte) (int, error) {
	text := string(p)
	if strings.Contains(text, "n=20") {
		select {
		case w.good <- struct{}{}:
		default:
		}
	}
	if strings.Contains(text, "synthetic failure") {
		select {
		case w.failed <- struct{}{}:
		default:
		}
	}
	return w.Buffer.Write(p)
}

func TestInteractiveTrendsRetainsEvidenceOnLoaderError(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	input, keys := io.Pipe()
	defer func() { _ = input.Close(); _ = keys.Close() }()
	output := &trendFrameObserver{good: make(chan struct{}, 1), failed: make(chan struct{}, 1)}
	ticker := &previewTestTicker{updates: make(chan time.Time)}
	app := NewInteractiveApp(scenario, input, output, previewTestTerminal{}, ticker)
	app.Trends = &failingPreviewTrends{snapshot: scenario.Trends}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()
	if _, err := keys.Write([]byte("\t")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-output.good:
	case <-ctx.Done():
		t.Fatal("no fixture evidence")
	}
	select {
	case ticker.updates <- scenario.Now:
	case <-ctx.Done():
		t.Fatal("refresh blocked")
	}
	select {
	case <-output.failed:
	case <-ctx.Done():
		t.Fatal("no stale error")
	}
	if _, err := keys.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	last := output.String()[strings.LastIndex(output.String(), "Stale / collection failed:"):]
	if !strings.Contains(last, "n=20") {
		t.Fatal("stale frame lost evidence")
	}
}

func TestInteractiveSettingsEditing(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	for _, accept := range []string{"y", "n"} {
		var output bytes.Buffer
		app := NewInteractiveApp(scenario, strings.NewReader("Se\r\x1b[C\r"+accept+"\x1b\x1bq"), &output, previewTestTerminal{}, &previewTestTicker{})
		before, err := app.Settings.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := app.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		after, err := app.Settings.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "stored value") {
			t.Fatal("preview did not display confirmation")
		}
		changed := before.Repositories[0].Values[0].Stored != after.Repositories[0].Values[0].Stored
		if changed != (accept == "y") {
			t.Fatalf("confirmation %s changed=%t", accept, changed)
		}
	}
}

func TestInteractiveChangesWithNilActions(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	var output bytes.Buffer
	app := NewInteractiveApp(scenario, strings.NewReader("clhzuw\x1bq"), &output, previewTestTerminal{}, &previewTestTicker{})
	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Changes") {
		t.Fatal("changes tab not painted")
	}
}

func TestInteractiveNoteCreationIsUnavailable(t *testing.T) {
	scenario, _ := Lookup(ScenarioEmpty)
	var output bytes.Buffer
	app := NewInteractiveApp(scenario, strings.NewReader("\x1b[Znq"), &output, previewTestTerminal{}, &previewTestTicker{updates: make(chan time.Time)})
	if err := app.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Note creation is unavailable.") {
		t.Fatal("preview did not explain unavailable creation")
	}
}

func TestInteractiveNotePlanningIsUnavailable(t *testing.T) {
	scenario, _ := Lookup(ScenarioMixed)
	for _, keys := range []string{"\x1b[Zpq", "\x1b[Z\rpq"} {
		var output bytes.Buffer
		app := NewInteractiveApp(scenario, strings.NewReader(keys), &output, previewTestTerminal{}, &previewTestTicker{})
		if app.NotePlanningLauncher != nil {
			t.Fatal("preview must never launch a process")
		}
		if err := app.Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), "Note planning is unavailable.") {
			t.Fatal("missing unavailable feedback")
		}
	}
}

func TestRunInteractiveRejectsMissingOrNonTerminalFiles(t *testing.T) {
	scenario, _ := Lookup(ScenarioEmpty)
	if err := RunInteractive(context.Background(), scenario, nil, nil); err == nil {
		t.Fatal("RunInteractive accepted missing terminal files")
	}
	input, inputWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = inputWriter.Close()
	})
	outputReader, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = outputReader.Close()
		_ = output.Close()
	})
	if err := RunInteractive(context.Background(), scenario, input, output); err == nil || !strings.Contains(err.Error(), "use --plain") {
		t.Fatalf("RunInteractive non-terminal error = %v", err)
	}
}
