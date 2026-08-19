package cli

import (
	"bytes"
	"context"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/tui"
)

func TestUICommandRegistrationAndHelp(t *testing.T) {
	metadata := commandByName("ui")
	if metadata == nil {
		t.Fatal("ui command is not registered")
	}
	if metadata.minPrefix != "" {
		t.Fatalf("ui min prefix = %q, want no alias", metadata.minPrefix)
	}
	if metadata.execute == nil || metadata.registerFlags == nil {
		t.Fatal("ui command is missing executor or flags")
	}

	var out bytes.Buffer
	if err := renderCommandHelp(&out, metadata); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"keyboard-driven dashboard", "running work, planned or in-review work", "Completed plans are hidden initially", "m confirms a selected reviewed-plan merge", "M confirms a repository-scoped merge --all", "press Enter for the full read-only slice page", "q and Ctrl-C quit globally", "Esc twice within one second", "--interval", "--completed-window", "tao monitor --once", "Usage:\n  tao ui"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("ui help missing %q in %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "queued work") {
		t.Fatalf("ui help retains removed queue wording in %q", out.String())
	}
}

func TestUIFlagDefaults(t *testing.T) {
	fs := flag.NewFlagSet("ui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	registerUIFlags(fs)
	if got := flagDurationValue(fs, "interval"); got != 2*time.Second {
		t.Fatalf("default interval = %s, want 2s", got)
	}
	if got := flagDurationValue(fs, "completed-window"); got != 168*time.Hour {
		t.Fatalf("default completed window = %s, want 168h", got)
	}
}

func TestUIFlagValidation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "zero interval", args: []string{"--interval", "0"}, want: "--interval must be greater than zero"},
		{name: "negative interval", args: []string{"--interval=-1s"}, want: "--interval must be greater than zero"},
		{name: "negative completed window", args: []string{"--completed-window=-1h"}, want: "--completed-window must be zero or greater"},
		{name: "positional argument", args: []string{"extra"}, want: "usage: tao ui"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			app := App{Out: &output, Err: &output, MonitorIsTerminal: func(io.Writer) bool { return false }}
			err := app.ui(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ui(%v) error = %v, want containing %q", test.args, err, test.want)
			}
		})
	}
}

func TestUIRejectsNonTerminalOutputWithMonitorGuidance(t *testing.T) {
	var output bytes.Buffer
	collectorCalled := false
	tickerCalled := false
	app := App{
		Out:               &output,
		Err:               &output,
		MonitorCollector:  collectingMonitorFunc(func(context.Context) error { collectorCalled = true; return nil }),
		MonitorIsTerminal: func(io.Writer) bool { return false },
		MonitorTicker: func(time.Duration) MonitorTicker {
			tickerCalled = true
			return nil
		},
	}
	err := app.ui(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "tao monitor --once") {
		t.Fatalf("non-terminal ui error = %v, want monitor --once guidance", err)
	}
	if collectorCalled || tickerCalled {
		t.Fatalf("non-terminal ui initialized loop dependencies: collector=%t ticker=%t", collectorCalled, tickerCalled)
	}
}

func TestStartDetachedUICommandReapsChildAsynchronously(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	holdPath := filepath.Join(dir, "hold")
	pidPath := filepath.Join(dir, "pid")
	if err := os.WriteFile(holdPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	started := make(chan error, 1)
	go func() {
		started <- startDetachedUICommand(tui.CommandRequest{
			Executable: executable,
			Args:       []string{"-test.run=^TestUIDetachedUICommandHelperProcess$", "--", "tao-ui-detached-helper", holdPath, pidPath},
			CWD:        dir,
		})
	}()

	pid := waitForDetachedUIPID(t, pidPath)
	t.Cleanup(func() {
		_ = os.Remove(holdPath)
		if err := syscall.Kill(pid, 0); err == nil {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("detached command launch blocked on the child")
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("detached command exited before its hold was removed: %v", err)
	}

	if err := os.Remove(holdPath); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if err == syscall.ESRCH {
			break
		}
		if err != nil {
			t.Fatalf("inspect detached command: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("detached command was not reaped")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUIDetachedUICommandHelperProcess(t *testing.T) {
	separator := -1
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i
			break
		}
	}
	if separator < 0 || len(os.Args) <= separator+1 || os.Args[separator+1] != "tao-ui-detached-helper" {
		return
	}
	if len(os.Args) != separator+4 {
		t.Fatalf("helper arguments = %q", os.Args)
	}
	holdPath, pidPath := os.Args[separator+2], os.Args[separator+3]
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil { //nolint:gosec // G703: parent test supplies paths rooted in t.TempDir.
		t.Fatal(err)
	}
	for {
		_, err := os.Stat(holdPath) //nolint:gosec // G703: parent test supplies paths rooted in t.TempDir.
		if os.IsNotExist(err) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForDetachedUIPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		contents, err := os.ReadFile(path) //nolint:gosec // G304: path is a test-owned temporary file.
		if err == nil {
			pid, err := strconv.Atoi(string(contents))
			if err != nil {
				t.Fatalf("parse detached command PID: %v", err)
			}
			return pid
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("detached command did not publish its PID")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type collectingMonitorFunc func(context.Context) error

func (f collectingMonitorFunc) Collect(ctx context.Context) (monitor.Snapshot, error) {
	return monitor.Snapshot{}, f(ctx)
}
