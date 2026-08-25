package tuipreview

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/tui"
)

const interactiveRefreshInterval = time.Second

type wallTicker struct {
	*time.Ticker
}

func (t wallTicker) C() <-chan time.Time {
	return t.Ticker.C
}

// NewInteractiveApp wires one fixture scenario to the production event loop.
// Callers provide terminal boundaries so tests can inspect the wiring without
// reproducing production PTY lifecycle coverage.
func NewInteractiveApp(scenario Scenario, input io.Reader, output io.Writer, terminal tui.Terminal, ticker tui.Ticker) tui.App {
	return tui.App{
		Input:     input,
		Output:    output,
		Terminal:  terminal,
		Ticker:    ticker,
		Collector: scenario.NewSnapshotCollector(),
		Notes:     scenario.NewNoteSnapshotCollector(),
		Details:   scenario.NewDetailRepository(),
		Actions:   nil,
		Now:       func() time.Time { return scenario.Now },
	}
}

// RunInteractive runs a scenario with real terminal input, output, sizing, and
// resize events. tui.App owns raw mode, detail followers, and restoration.
func RunInteractive(ctx context.Context, scenario Scenario, input, output *os.File) error {
	if input == nil {
		return errors.New("interactive preview requires terminal input")
	}
	if output == nil {
		return errors.New("interactive preview requires terminal output")
	}
	terminal := term.NewTerminal(input)
	if _, err := terminal.Size(); err != nil {
		return errors.New("interactive preview requires terminal input; use --plain outside a terminal")
	}
	if _, err := term.NewOutputTerminal(output).Size(); err != nil {
		return errors.New("interactive preview requires terminal output; use --plain outside a terminal")
	}
	ticker := wallTicker{Ticker: time.NewTicker(interactiveRefreshInterval)}
	app := NewInteractiveApp(scenario, input, output, terminal, ticker)
	return app.Run(ctx)
}
