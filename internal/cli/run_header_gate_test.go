package cli

import (
	"flag"
	"io"
	"testing"
)

func TestRunHeaderEnabled(t *testing.T) {
	tests := []struct {
		name             string
		noRunHeader      bool
		stdoutIsTerminal bool
		term             string
		rows             int
		columns          int
		want             bool
	}{
		{name: "enabled", stdoutIsTerminal: true, term: "xterm-256color", rows: 12, columns: 60, want: true},
		{name: "disabled by flag", noRunHeader: true, stdoutIsTerminal: true, term: "xterm-256color", rows: 12, columns: 60},
		{name: "disabled for non-terminal output", term: "xterm-256color", rows: 12, columns: 60},
		{name: "disabled for dumb terminal", stdoutIsTerminal: true, term: "dumb", rows: 12, columns: 60},
		{name: "disabled for too few rows", stdoutIsTerminal: true, term: "xterm-256color", rows: 11, columns: 60},
		{name: "disabled for too few columns", stdoutIsTerminal: true, term: "xterm-256color", rows: 12, columns: 59},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := runHeaderEnabled(test.noRunHeader, test.stdoutIsTerminal, test.term, test.rows, test.columns)
			if got != test.want {
				t.Fatalf("runHeaderEnabled() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRunHeaderEnabledByDefaultWhenEnvUnset(t *testing.T) {
	unsetEnvForTest(t, envRunHeader)

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerRunFlags(fs)

	if flagBoolValue(fs, "no-run-header") {
		t.Fatal("unset TAO_RUN_HEADER disabled the run header")
	}
}

func TestRunHeaderEnvZeroDisablesDefault(t *testing.T) {
	t.Setenv(envRunHeader, "0")

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerRunFlags(fs)

	if !flagBoolValue(fs, "no-run-header") {
		t.Fatal("TAO_RUN_HEADER=0 did not disable the run header")
	}
}
