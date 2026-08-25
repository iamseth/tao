package tuipreview

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/term"
)

type previewTestTerminal struct{}

func (previewTestTerminal) EnterRaw() error                              { return nil }
func (previewTestTerminal) Restore() error                               { return nil }
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

	if app.Actions != nil {
		t.Fatal("interactive fixture app has production actions")
	}
	if app.Terminal == nil || app.Ticker != ticker || app.Collector == nil || app.Notes == nil || app.Debug == nil || app.Settings == nil || app.Details == nil {
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
