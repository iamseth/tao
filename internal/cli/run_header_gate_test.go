package cli

import (
	"flag"
	"io"
	"testing"

	"github.com/iamseth/tao/internal/runtimeconfig"
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
	unsetEnvForTest(t, runtimeconfig.EnvRunHeader)

	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerRunFlags(fs)

	if flagBoolValue(fs, "no-run-header") {
		t.Fatal("unset TAO_RUN_HEADER disabled the run header")
	}
}

func TestRunHeaderSnapshotDefaults(t *testing.T) {
	for _, value := range []string{"0", "false", "off", "invalid"} {
		t.Run(value, func(t *testing.T) {
			snapshot := runtimeconfig.LoadEnv(func(key string) (string, bool) {
				return value, key == runtimeconfig.EnvRunHeader
			})
			app := App{Err: io.Discard, RuntimeEnv: &snapshot}
			fs, _, err := app.parseArgsFor(&runCommand, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := flagBoolValue(fs, "no-run-header"), value != "invalid"; got != want {
				t.Fatalf("header %q: disabled = %v, want %v", value, got, want)
			}
			if flagWasProvided(fs, "no-run-header") {
				t.Fatal("environment default counted as an explicit flag")
			}
			fs, _, err = app.parseArgsFor(&runCommand, []string{"--no-run-header=false"})
			if err != nil || flagBoolValue(fs, "no-run-header") || !flagWasProvided(fs, "no-run-header") {
				t.Fatalf("explicit false did not override default: %v", err)
			}
		})
	}
}
