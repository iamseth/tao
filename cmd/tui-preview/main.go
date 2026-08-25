// tui-preview is a developer-only fixture runner for Tao's production TUI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/iamseth/tao/internal/tuipreview"
)

const (
	defaultScenario = tuipreview.ScenarioMixed
	defaultView     = tuipreview.ViewPlans
	defaultSize     = "100x30"
)

type commandOptions struct {
	listScenarios bool
	listViews     bool
	scenario      string
	plain         bool
	view          string
	size          string
	color         bool
	shortcuts     bool
	search        string
}

func main() {
	os.Exit(mainExitCode())
}

func mainExitCode() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	return run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}

func run(ctx context.Context, args []string, input io.Reader, output, errOutput io.Writer) int {
	if err := execute(ctx, args, input, output, errOutput); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		_, _ = fmt.Fprintf(errOutput, "tui-preview: %v\n", err)
		return 1
	}
	return 0
}

func execute(ctx context.Context, args []string, input io.Reader, output, errOutput io.Writer) error {
	options := commandOptions{}
	fs := flag.NewFlagSet("tui-preview", flag.ContinueOnError)
	fs.SetOutput(errOutput)
	fs.BoolVar(&options.listScenarios, "list-scenarios", false, "list fixture scenarios and exit")
	fs.BoolVar(&options.listViews, "list-views", false, "list plain-output views and exit")
	fs.StringVar(&options.scenario, "scenario", defaultScenario, "fixture scenario name")
	fs.BoolVar(&options.plain, "plain", false, "render one frame without taking over a terminal")
	fs.StringVar(&options.view, "view", string(defaultView), "plain-output view")
	fs.StringVar(&options.size, "size", defaultSize, "plain-output dimensions as WIDTHxHEIGHT")
	fs.BoolVar(&options.color, "color", false, "force ANSI color in plain output")
	fs.BoolVar(&options.shortcuts, "shortcuts", false, "show the shortcut popover in plain output")
	fs.StringVar(&options.search, "search", "", "filter plans or notes in plain output")
	fs.Usage = func() {
		_, _ = fmt.Fprintln(errOutput, "Usage: tui-preview [flags]")
		_, _ = fmt.Fprintln(errOutput, "Run an interactive fixture by default, or use --plain for one-shot output.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected arguments %q; this command accepts flags only", strings.Join(fs.Args(), " "))
	}

	set := make(map[string]bool)
	fs.Visit(func(value *flag.Flag) { set[value.Name] = true })
	if options.listScenarios || options.listViews {
		if options.plain || set["scenario"] || set["view"] || set["size"] || options.color || options.shortcuts || set["search"] {
			return errors.New("listing flags cannot be combined with --plain, --scenario, --view, --size, --color, --shortcuts, or --search")
		}
		if options.listScenarios {
			if err := writeScenarios(output); err != nil {
				return fmt.Errorf("write scenarios: %w", err)
			}
		}
		if options.listViews {
			if err := writeViews(output); err != nil {
				return fmt.Errorf("write views: %w", err)
			}
		}
		return nil
	}

	scenario, ok := tuipreview.Lookup(options.scenario)
	if !ok {
		return fmt.Errorf("unknown scenario %q; use --list-scenarios to see available fixtures", options.scenario)
	}
	if !options.plain {
		if set["view"] || set["size"] || options.color || options.shortcuts || set["search"] {
			return errors.New("--view, --size, --color, --shortcuts, and --search require --plain")
		}
		inputFile, inputOK := input.(*os.File)
		outputFile, outputOK := output.(*os.File)
		if !inputOK || !outputOK {
			return errors.New("interactive preview requires terminal stdin and stdout; use --plain for non-terminal output")
		}
		if err := tuipreview.RunInteractive(ctx, scenario, inputFile, outputFile); err != nil {
			return fmt.Errorf("run interactive preview (use --plain outside a terminal): %w", err)
		}
		return nil
	}

	view, ok := tuipreview.LookupView(options.view)
	if !ok {
		return fmt.Errorf("unknown view %q; use --list-views to see available views", options.view)
	}
	width, height, err := parseSize(options.size)
	if err != nil {
		return err
	}
	frame, err := tuipreview.Render(scenario, tuipreview.RenderOptions{
		View: view, Width: width, Height: height, Color: options.color, Plain: true, ShowShortcuts: options.shortcuts, SearchQuery: options.search,
	})
	if err != nil {
		return fmt.Errorf("render %s scenario %s: %w", view, scenario.Name, err)
	}
	if _, err := io.WriteString(output, frame); err != nil {
		return fmt.Errorf("write preview: %w", err)
	}
	return nil
}

func parseSize(value string) (int, int, error) {
	widthText, heightText, ok := strings.Cut(value, "x")
	if !ok || widthText == "" || heightText == "" || strings.Contains(heightText, "x") {
		return 0, 0, fmt.Errorf("invalid --size %q; expected WIDTHxHEIGHT with positive integers", value)
	}
	width, widthErr := strconv.Atoi(widthText)
	height, heightErr := strconv.Atoi(heightText)
	if widthErr != nil || heightErr != nil || width <= 0 || height <= 0 {
		return 0, 0, fmt.Errorf("invalid --size %q; expected WIDTHxHEIGHT with positive integers", value)
	}
	return width, height, nil
}

func writeScenarios(output io.Writer) error {
	for _, scenario := range tuipreview.Scenarios() {
		if _, err := fmt.Fprintf(output, "%s\t%s\n", scenario.Name, scenario.Description); err != nil {
			return err
		}
	}
	return nil
}

func writeViews(output io.Writer) error {
	for _, view := range tuipreview.Views() {
		if _, err := fmt.Fprintln(output, view); err != nil {
			return err
		}
	}
	return nil
}
