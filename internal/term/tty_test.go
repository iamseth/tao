package term

import (
	"os"
	"testing"
)

type terminalProbeStub struct {
	*os.File
	terminal bool
}

func (s terminalProbeStub) IsTerminal() bool { return s.terminal }

func TestIsTerminal(t *testing.T) {
	t.Parallel()

	file, err := os.CreateTemp(t.TempDir(), "regular")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	device, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = device.Close() })
	closed, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name  string
		value any
		want  bool
	}{
		{name: "interface true overrides regular file", value: terminalProbeStub{File: file, terminal: true}, want: true},
		{name: "interface false overrides device", value: terminalProbeStub{File: device}, want: false},
		{name: "non-file", value: struct{}{}, want: false},
		{name: "nil", value: nil, want: false},
		{name: "nil file", value: (*os.File)(nil), want: false},
		{name: "regular file", value: file, want: false},
		{name: "character device", value: device, want: true},
		{name: "stat error", value: closed, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsTerminal(test.value); got != test.want {
				t.Fatalf("IsTerminal() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestColorEnabled(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		isTerminal bool
		env        map[string]string
		want       bool
	}{
		{name: "terminal defaults", isTerminal: true, want: true},
		{name: "redirected defaults", want: false},
		{name: "NO_COLOR overrides force", isTerminal: true, env: map[string]string{"NO_COLOR": "1", "CLICOLOR_FORCE": "1"}, want: false},
		{name: "NO_COLOR zero still disables", isTerminal: true, env: map[string]string{"NO_COLOR": "0"}, want: false},
		{name: "dumb overrides force", isTerminal: true, env: map[string]string{"TERM": "dumb", "CLICOLOR_FORCE": "1"}, want: false},
		{name: "normalized dumb", isTerminal: true, env: map[string]string{"TERM": " DUMB "}, want: false},
		{name: "force redirected", env: map[string]string{"CLICOLOR_FORCE": "1"}, want: true},
		{name: "force overrides CLICOLOR", env: map[string]string{"CLICOLOR_FORCE": " yes ", "CLICOLOR": "0"}, want: true},
		{name: "zero force redirected", env: map[string]string{"CLICOLOR_FORCE": " 0 "}, want: false},
		{name: "zero force terminal", isTerminal: true, env: map[string]string{"CLICOLOR_FORCE": "0"}, want: true},
		{name: "blank force redirected", env: map[string]string{"CLICOLOR_FORCE": " "}, want: false},
		{name: "CLICOLOR disables terminal", isTerminal: true, env: map[string]string{"CLICOLOR": " 0 "}, want: false},
		{name: "zero force respects CLICOLOR", isTerminal: true, env: map[string]string{"CLICOLOR_FORCE": "0", "CLICOLOR": "0"}, want: false},
		{name: "CLICOLOR cannot force redirected", env: map[string]string{"CLICOLOR": "1"}, want: false},
		{name: "CLICOLOR enabled terminal", isTerminal: true, env: map[string]string{"CLICOLOR": "1", "TERM": "xterm-256color"}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			getenv := func(key string) string { return test.env[key] }
			if got := ColorEnabled(test.isTerminal, getenv); got != test.want {
				t.Fatalf("ColorEnabled() = %t, want %t", got, test.want)
			}
		})
	}
}
