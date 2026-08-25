package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestListScenariosAndViews(t *testing.T) {
	var scenarios bytes.Buffer
	if code := run(context.Background(), []string{"--list-scenarios"}, &bytes.Buffer{}, &scenarios, &bytes.Buffer{}); code != 0 {
		t.Fatalf("scenario list exit = %d", code)
	}
	for _, name := range []string{"mixed\t", "empty\t", "stress\t"} {
		if !strings.Contains(scenarios.String(), name) {
			t.Fatalf("scenario list missing %q:\n%s", name, scenarios.String())
		}
	}

	var views bytes.Buffer
	if code := run(context.Background(), []string{"--list-views"}, &bytes.Buffer{}, &views, &bytes.Buffer{}); code != 0 {
		t.Fatalf("view list exit = %d", code)
	}
	if got, want := views.String(), "plans\nnotes\nplan-detail\nnote-detail\nslice-detail\n"; got != want {
		t.Fatalf("view list = %q, want %q", got, want)
	}
}

func TestPlainOutputIsDeterministicAndUsesSelectedFixture(t *testing.T) {
	args := []string{"--plain", "--scenario", "empty", "--view", "plans", "--size", "48x8"}
	var first, second bytes.Buffer
	if err := execute(context.Background(), args, &bytes.Buffer{}, &first, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if err := execute(context.Background(), args, &bytes.Buffer{}, &second, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("identical plain preview invocations produced different bytes")
	}
	if !strings.Contains(first.String(), "No plans.") {
		t.Fatalf("empty fixture output missing empty state:\n%s", first.String())
	}
	if strings.Contains(first.String(), "\x1b[H") || strings.Contains(first.String(), "\x1b[2J") {
		t.Fatalf("plain output contains screen controls: %q", first.String())
	}
	if lines := strings.Count(strings.TrimSuffix(first.String(), "\n"), "\n") + 1; lines > 8 {
		t.Fatalf("plain output has %d lines, want at most 8", lines)
	}
}

func TestPlainColorCanBeForced(t *testing.T) {
	var output bytes.Buffer
	err := execute(context.Background(), []string{"--plain", "--color", "--size", "120x30"}, &bytes.Buffer{}, &output, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "\x1b[") {
		t.Fatal("--color plain output contains no ANSI styling")
	}
}

func TestInvalidFlagsAndDimensionsHaveDeterministicFailureExit(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown scenario", args: []string{"--plain", "--scenario", "missing"}, want: "--list-scenarios"},
		{name: "unknown view", args: []string{"--plain", "--view", "missing"}, want: "--list-views"},
		{name: "missing separator", args: []string{"--plain", "--size", "80"}, want: "WIDTHxHEIGHT"},
		{name: "nonpositive width", args: []string{"--plain", "--size", "0x20"}, want: "positive integers"},
		{name: "extra separator", args: []string{"--plain", "--size", "80x20x2"}, want: "WIDTHxHEIGHT"},
		{name: "plain-only view", args: []string{"--view", "notes"}, want: "require --plain"},
		{name: "plain-only size", args: []string{"--size", "80x20"}, want: "require --plain"},
		{name: "plain-only color", args: []string{"--color"}, want: "require --plain"},
		{name: "listing combination", args: []string{"--list-views", "--plain"}, want: "cannot be combined"},
		{name: "positional", args: []string{"extra"}, want: "accepts flags only"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var errOutput bytes.Buffer
			if code := run(context.Background(), test.args, &bytes.Buffer{}, &bytes.Buffer{}, &errOutput); code != 1 {
				t.Fatalf("exit = %d, want 1", code)
			}
			if !strings.Contains(errOutput.String(), test.want) {
				t.Fatalf("error output missing %q: %s", test.want, errOutput.String())
			}
		})
	}
}

func TestNonTerminalInteractivePathIsActionableAndHelpExitsZero(t *testing.T) {
	var errOutput bytes.Buffer
	if code := run(context.Background(), nil, &bytes.Buffer{}, &bytes.Buffer{}, &errOutput); code != 1 {
		t.Fatalf("non-terminal interactive exit = %d, want 1", code)
	}
	if !strings.Contains(errOutput.String(), "use --plain") {
		t.Fatalf("non-terminal error is not actionable: %s", errOutput.String())
	}

	errOutput.Reset()
	if code := run(context.Background(), []string{"--help"}, &bytes.Buffer{}, &bytes.Buffer{}, &errOutput); code != 0 {
		t.Fatalf("help exit = %d, want 0", code)
	}
	for _, flagName := range []string{"-list-scenarios", "-list-views", "-scenario", "-plain", "-view", "-size", "-color"} {
		if !strings.Contains(errOutput.String(), flagName) {
			t.Fatalf("help missing %s:\n%s", flagName, errOutput.String())
		}
	}
}
