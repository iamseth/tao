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
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
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
	for _, want := range []string{"keyboard-driven dashboard", "Notes, Plans, Settings, and Debug tabs", "Plans is the initial tab", "Tab or the right arrow", "Shift+Tab or the left arrow", "gg jumps to the top of the visible list", "G jumps to the bottom", "Repository focus is shared by Plans and Notes", "immediate work under NOW, planned work under NEXT, and terminal plans under DONE", "immediate operational actions such as MONITOR, APPROVE, or MERGE", "Operational urgency always takes precedence", "disposition, valid sequence relationships, categorical priority, and recent activity", "missing, duplicate, or cyclic relationships only warn", "legacy plans remain visible as unranked", "RUN AGE is elapsed time for an observed invocation", "NEXT is a derived advisory label", "Press Enter to inspect the selected plan's full decision and lifecycle context", "e expands or collapses its scope file list", "DONE is always displayed with up to 15 completed or abandoned plans", "m confirms a selected reviewed-plan merge", "M confirms a repository-scoped merge --all", "press Enter for the full read-only slice page", "repository-owned open notes", "grouped by ascending tier", "every non-tier tag", "separate created and updated ages", "full read-only detail", "per-repository pull-request defaults", "explicit true, explicit false, and inherited", "doctor problems", "resolved runtime defaults from tao status", "Plan actions do not act on Notes, Settings, or Debug", "q and Ctrl-C quit globally", "Esc twice within one second", "--interval", "--completed-window", "tao monitor --once", "Usage:\n  tao ui"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("ui help missing %q in %q", want, out.String())
		}
	}
	if strings.Contains(out.String(), "queued work") {
		t.Fatalf("ui help retains removed queue wording in %q", out.String())
	}
}

func TestUIDetailInspectorComposesStalenessWithCommandRunner(t *testing.T) {
	var calls int
	runner := func(_ context.Context, cwd, name string, args []string, stdout, _ io.Writer) error {
		calls++
		if cwd != "" || name != "git" {
			t.Fatalf("inspection command cwd=%q name=%q", cwd, name)
		}
		if len(args) >= 2 && args[0] == "-C" {
			if args[1] != "/repo" {
				t.Fatalf("inspection Git root = %q", args[1])
			}
			args = args[2:]
		}
		if strings.Join(args, " ") == "rev-parse HEAD" {
			_, _ = io.WriteString(stdout, "aaaaaaaaaaaa1111\n")
		}
		return nil
	}
	result, err := newUIDetailInspector(runner).Inspect(context.Background(), &plan.PlanDetail{State: plan.State{
		Repo: plan.Repo{Root: "/repo", BaseCommit: "aaaaaaaaaaaa1111"},
		Plan: plan.PlanState{ID: "plan-a"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(result.Findings) != 0 {
		t.Fatalf("inspection calls=%d result=%+v, want one Git call and no findings", calls, result)
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
	registryCalled := false
	tickerCalled := false
	app := App{
		Out:               &output,
		Err:               &output,
		MonitorCollector:  collectingMonitorFunc(func(context.Context) error { collectorCalled = true; return nil }),
		MonitorIsTerminal: func(io.Writer) bool { return false },
		Registry: func() NoteRegistry {
			registryCalled = true
			return &fakeNoteRegistry{}
		},
		MonitorTicker: func(time.Duration) MonitorTicker {
			tickerCalled = true
			return nil
		},
	}
	err := app.ui(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "tao monitor --once") {
		t.Fatalf("non-terminal ui error = %v, want monitor --once guidance", err)
	}
	if collectorCalled || registryCalled || tickerCalled {
		t.Fatalf("non-terminal ui initialized loop dependencies: collector=%t registry=%t ticker=%t", collectorCalled, registryCalled, tickerCalled)
	}
}

func TestUIDependencyErrorsIdentifyUnavailableInventory(t *testing.T) {
	for _, test := range []struct {
		name             string
		monitorCollector MonitorSnapshotCollector
		want             string
	}{
		{name: "plans", want: "monitor repository inventory is unavailable"},
		{name: "notes", monitorCollector: collectingMonitorFunc(func(context.Context) error { return nil }), want: "note repository inventory is unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			app := App{
				In: strings.NewReader("q"), Out: &output, Err: &output,
				MonitorCollector:  test.monitorCollector,
				MonitorIsTerminal: func(io.Writer) bool { return true },
				UITerminal:        &uiTerminalStub{resizes: make(chan struct{})},
				Registry:          func() NoteRegistry { return &fakeNoteRegistry{} },
			}
			err := app.ui(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ui dependency error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestUIComposesRepositoryNotesCollector(t *testing.T) {
	notesDir := t.TempDir()
	entry := taodata.RepoInventoryEntry{
		Repo:     taodata.Repo{ID: "repo-a", Name: "alpha", Root: "/repos/alpha"},
		NotesDir: notesDir,
	}
	repository := note.NewRepository(notesDir, note.RepoReference{ID: entry.Repo.ID, Root: entry.Repo.Root})
	repository.Now = func() time.Time { return time.Date(2026, 8, 19, 16, 0, 0, 0, time.UTC) }
	repository.IDSuffix = func() string { return "ui01" }
	_, err := repository.Create(context.Background(), "CLI-composed open note", []string{"tui"})
	if err != nil {
		t.Fatal(err)
	}

	var requestedDir string
	var requestedRef note.RepoReference
	var output bytes.Buffer
	ticker := &monitorTickerStub{ch: make(chan time.Time), stopped: make(chan struct{})}
	app := App{
		In: strings.NewReader("\x1b[Zq"), Out: &output, Err: &output,
		MonitorCollector:  collectingMonitorFunc(func(context.Context) error { return nil }),
		MonitorIsTerminal: func(io.Writer) bool { return true },
		MonitorTicker:     func(time.Duration) MonitorTicker { return ticker },
		UITerminal:        &uiTerminalStub{size: term.Size{Width: 120, Height: 30}, resizes: make(chan struct{})},
		Registry:          func() NoteRegistry { return monitorRegistryStub{entries: []taodata.RepoInventoryEntry{entry}} },
		NoteRepository: func(dir string, ref note.RepoReference) NoteRepository {
			requestedDir, requestedRef = dir, ref
			return repository
		},
	}
	if err := app.ui(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if requestedDir != notesDir || requestedRef.ID != entry.Repo.ID || requestedRef.Root != entry.Repo.Root {
		t.Fatalf("note repository request = dir %q ref %+v, want %q and %+v", requestedDir, requestedRef, notesDir, note.RepoReference{ID: entry.Repo.ID, Root: entry.Repo.Root})
	}
	for _, want := range []string{"tao │▸notes  plans  settings  debug", "all repos", "1 open note", "CLI-composed open note"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("ui output missing %q in %q", want, output.String())
		}
	}
}

type uiTerminalStub struct {
	size    term.Size
	resizes chan struct{}
}

func (*uiTerminalStub) EnterRaw() error { return nil }
func (*uiTerminalStub) Restore() error  { return nil }
func (t *uiTerminalStub) Size() (term.Size, error) {
	return t.size, nil
}
func (t *uiTerminalStub) ResizeEvents(context.Context) <-chan struct{} { return t.resizes }

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
