//go:build darwin || linux

package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/term"
)

func planningPTYIOCTL(fd, request uintptr, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, request, uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
}

type planningPTYRegistry struct {
	NoteRegistry
	entry taodata.RepoInventoryEntry
}

func (r planningPTYRegistry) MetadataInventory() ([]taodata.RepoInventoryEntry, error) {
	return []taodata.RepoInventoryEntry{r.entry}, nil
}

// Signals and controlling-terminal ownership are deliberately isolated in this
// helper process. The fake agent is another copy of the test binary, never an
// installed agent, and only the executable seam is replaced after validation.
func TestUINotePlanningPTYHelper(t *testing.T) {
	mode := os.Getenv("TAO_PLANNING_PTY_HELPER")
	if mode == "" {
		return
	}
	if mode == "child" {
		attributes, err := planningPTYAttributes(os.Stdin.Fd())
		if err != nil || attributes.Lflag&syscall.ICANON == 0 || attributes.Lflag&syscall.ISIG == 0 {
			t.Fatalf("child did not receive cooked terminal: %v", err)
		}
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		defer signal.Stop(interrupts)
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case <-interrupts:
					fmt.Println("CHILD_INTERRUPT")
				case <-done:
					return
				}
			}
		}()
		fmt.Printf("CHILD_READY %d\n", os.Getpid())
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			switch scanner.Text() {
			case "hello":
				fmt.Println("CHILD_INPUT")
			case "raw":
				// Deliberately do not restore: cancellation or a crashed agent
				// cannot be trusted to clean up its own terminal state.
				if err := term.NewTerminal(os.Stdin).EnterRaw(); err != nil {
					t.Fatal(err)
				}
				fmt.Println("CHILD_RAW")
			case "abnormal":
				//nolint:gocritic // Model a crashed agent: skip all deferred cleanup.
				os.Exit(23)
			case "exit":
				return
			}
		}
		if err := scanner.Err(); err != nil {
			t.Fatal(err)
		}
		return
	}
	original, err := planningPTYAttributes(os.Stdin.Fd())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_DATA_HOME", t.TempDir())
	t.Setenv("TAO_AGENT", "pi")
	t.Setenv("NO_COLOR", "1")
	t.Setenv("HOME", t.TempDir())
	tools := t.TempDir()
	//nolint:gosec // Test-owned executable stub for passive doctor checks, never an agent.
	if err := os.WriteFile(filepath.Join(tools, "pi"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", tools)
	app, registered, item := planningFixture(t)
	registry := app.registry()
	app.Registry = func() NoteRegistry {
		return planningPTYRegistry{NoteRegistry: registry, entry: taodata.RepoInventoryEntry{Repo: registered, NotesDir: registry.NotesDir(registered)}}
	}
	app.In, app.Out, app.Err = os.Stdin, os.Stdout, os.Stderr
	app.MonitorCollector = collectingMonitorFunc(func(context.Context) error { return nil })
	app.MonitorIsTerminal = func(io.Writer) bool { return true }
	app.MonitorTicker = func(time.Duration) MonitorTicker {
		return &monitorTickerStub{ch: make(chan time.Time), stopped: make(chan struct{})}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	launcher := newUINotePlanningLauncher(app, os.Stdin, os.Stdout)
	launcher.run = func(cmd *exec.Cmd) error {
		if len(cmd.Args) != 2 || cmd.Args[1] != "/tao-plan note:"+item.ID {
			t.Fatalf("unexpected command: %v", cmd.Args)
		}
		cmd.Path = executable
		cmd.Args = []string{executable, "-test.run=^TestUINotePlanningPTYHelper$"}
		cmd.Env = append(cmd.Env, "TAO_PLANNING_PTY_HELPER=child")
		return cmd.Run()
	}
	app.UINotePlanningLauncher = launcher
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := syscall.SetNonblock(3, true); err != nil {
		t.Fatal(err)
	}
	control := os.NewFile(3, "cancel-control")
	defer func() { _ = control.Close() }()
	go func() {
		var b [1]byte
		if _, err := control.Read(b[:]); err == nil {
			cancel()
		}
	}()
	if err := app.ui(ctx, nil); err != nil {
		t.Fatal(err)
	}
	restored, err := planningPTYAttributes(os.Stdin.Fd())
	if err != nil || original != restored {
		t.Fatalf("terminal dirty: err=%v before=%+v after=%+v", err, original, restored)
	}
	fmt.Println("DASHBOARD_DONE")
}

func TestUINotePlanningPTY(t *testing.T) {
	for _, ending := range []string{"exit", "cancel", "sigterm", "raw-exit", "raw-cancel", "raw-sigterm", "raw-abnormal"} {
		t.Run(ending, func(t *testing.T) {
			master, slave, err := openPlanningPTY()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = master.Close(); _ = slave.Close() }()
			// The terminal size is otherwise zero on a new PTY.
			size := [4]uint16{30, 100, 0, 0}
			//nolint:gosec // Test-owned PTY window-size buffer.
			if err := planningPTYIOCTL(slave.Fd(), uintptr(syscall.TIOCSWINSZ), unsafe.Pointer(&size)); err != nil {
				t.Fatal(err)
			}
			control, writer, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = control.Close(); _ = writer.Close() }()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			//nolint:gosec // Current test executable, fixed helper arguments.
			cmd := exec.CommandContext(ctx, executable, "-test.run=^TestUINotePlanningPTYHelper$")
			cmd.Env = append(os.Environ(), "TAO_PLANNING_PTY_HELPER=dashboard")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			cmd.ExtraFiles = []*os.File{control}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }()
			readUntil := func(marker string) string {
				t.Helper()
				if err := master.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				b := make([]byte, 4096)
				for !strings.Contains(output.String(), marker) {
					n, err := master.Read(b)
					output.Write(b[:n])
					if err != nil {
						t.Fatalf("waiting for %q: %v\n%s", marker, err, output.String())
					}
					if output.Len() > 256*1024 {
						t.Fatal("unbounded helper output")
					}
				}
				return output.String()
			}
			send := func(keys string) {
				t.Helper()
				if err := master.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err := io.WriteString(master, keys); err != nil {
					t.Fatal(err)
				}
			}
			readUntil("tao │")
			send("\x1b[Z")
			readUntil("open note")
			send("p")
			ready := readUntil("CHILD_READY ")
			// Read the complete PID line even when PTY reads split the marker.
			tail := strings.SplitN(ready, "CHILD_READY ", 2)[1]
			if !strings.Contains(tail, "\n") {
				tail += readUntil("\n")
			}
			pid, err := strconv.Atoi(strings.Fields(tail)[0])
			if err != nil {
				t.Fatal(err)
			}
			send("hello\n")
			readUntil("CHILD_INPUT")
			send("\x03")
			readUntil("CHILD_INTERRUPT")
			if strings.HasPrefix(ending, "raw-") {
				send("raw\n")
				readUntil("CHILD_RAW")
				attributes, err := planningPTYAttributes(slave.Fd())
				if err != nil || attributes.Lflag&syscall.ICANON != 0 {
					t.Fatalf("child did not enter raw mode: %v", err)
				}
			}
			switch strings.TrimPrefix(ending, "raw-") {
			case "exit", "abnormal":
				send(strings.TrimPrefix(ending, "raw-") + "\n")
				readUntil("tao │")
				attributes, err := planningPTYAttributes(slave.Fd())
				if err != nil || attributes.Lflag&syscall.ICANON != 0 {
					t.Fatalf("dashboard did not resume raw mode: %v", err)
				}
				send("q")
			case "cancel":
				if _, err := writer.Write([]byte{1}); err != nil {
					t.Fatal(err)
				}
			case "sigterm":
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			}
			readUntil("DASHBOARD_DONE")
			go func() { _, _ = io.Copy(io.Discard, master) }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("helper did not settle")
			}
			if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
				t.Fatalf("child %d was not reaped: %v", pid, err)
			}
		})
	}
}
