package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/note"
)

type planningLaunchFunc func(context.Context, note.CatalogNote) error

func (f planningLaunchFunc) Launch(ctx context.Context, item note.CatalogNote) error {
	return f(ctx, item)
}

type planningSignalFake struct {
	mu            sync.Mutex
	subscriptions map[chan<- os.Signal][]os.Signal
	stops         int
}

func (f *planningSignalFake) notify(ch chan<- os.Signal, signals ...os.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.subscriptions[ch] = signals
}
func (f *planningSignalFake) stop(ch chan<- os.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subscriptions, ch)
	f.stops++
}
func (f *planningSignalFake) send(sig os.Signal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for ch, signals := range f.subscriptions {
		for _, s := range signals {
			if s == sig {
				ch <- sig
			}
		}
	}
}

func TestUIPlanningSignalScopeCleanup(t *testing.T) {
	for _, outcome := range []string{"success", "start-error", "panic", "term", "parent-cancel"} {
		t.Run(outcome, func(t *testing.T) {
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &planningSignalFake{subscriptions: map[chan<- os.Signal][]os.Signal{}}
			scope := newUIPlanningSignalsWith(parent, fake.notify, fake.stop)
			defer scope.Close()
			expected := errors.New("start failure")
			for round := 0; round < 3; round++ {
				launcher := scopedNotePlanningLauncher{signals: scope, launcher: planningLaunchFunc(func(ctx context.Context, _ note.CatalogNote) error {
					fake.send(os.Interrupt)
					if ctx.Err() != nil {
						t.Fatal("foreground SIGINT cancelled parent")
					}
					switch outcome {
					case "start-error":
						return expected
					case "panic":
						panic(expected)
					case "term":
						fake.send(syscall.SIGTERM)
						awaitPlanningCancellation(t, ctx)
					case "parent-cancel":
						cancel()
						awaitPlanningCancellation(t, ctx)
					}
					return nil
				})}
				func() {
					defer func() {
						if got := recover(); got != nil {
							panicErr, ok := got.(error)
							if outcome != "panic" || !ok || !errors.Is(panicErr, expected) {
								t.Fatalf("panic = %v", got)
							}
						}
					}()
					err := launcher.Launch(scope.Context(), note.CatalogNote{})
					if outcome == "panic" {
						t.Fatal("panic disappeared")
					}
					if outcome == "start-error" && !errors.Is(err, expected) {
						t.Fatalf("error = %v", err)
					}
				}()
				if outcome == "term" || outcome == "parent-cancel" {
					break
				}
				if scope.Context().Err() != nil {
					t.Fatal("queued SIGINT cancelled resumed dashboard")
				}
			}
			if outcome != "term" && outcome != "parent-cancel" {
				fake.send(os.Interrupt)
			}
			awaitPlanningCancellation(t, scope.Context())
			scope.Close()
			fake.mu.Lock()
			defer fake.mu.Unlock()
			if len(fake.subscriptions) != 0 || fake.stops < 2 {
				t.Fatalf("subscriptions leaked: %+v", fake)
			}
		})
	}
}

func awaitPlanningCancellation(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not arrive")
	}
}

// Only isolated helpers receive signals. Each controller owns a separate process
// group, so the SIGINT broadcast models a terminal without touching go test.
func TestUIPlanningSignalsIsolatedProcesses(t *testing.T) {
	for _, scenario := range []string{"interrupt", "native-interrupt", "repeat", "start-failure", "exit-error", "term", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestUIPlanningSignalHelper$") //nolint:gosec // G204: only the current test binary, with fixed helper arguments.
			cmd.Env = append(os.Environ(), "TAO_UI_PLANNING_HELPER="+scenario)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			// Kill the isolated group on timeout too, not only its controller.
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper %s: %v\n%s", scenario, err, output)
			}
			if !strings.Contains(string(output), "planning-helper-ok") {
				t.Fatalf("helper did not finish: %s", output)
			}
		})
	}
}

func TestUIPlanningSignalHelper(t *testing.T) {
	scenario := os.Getenv("TAO_UI_PLANNING_HELPER")
	if scenario == "" {
		return
	}
	if scenario == "child" {
		planningSignalChild(t)
		return
	}
	if scenario == "native-child" {
		fmt.Println("ready")
		bufio.NewScanner(os.Stdin).Scan()
		return
	}
	// Safety assertion before any group signal.
	group, err := syscall.Getpgid(0)
	if err != nil || group != os.Getpid() {
		t.Fatal("controller must own an isolated process group")
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	scope := newUIPlanningSignals(parent)
	defer scope.Close()
	rounds := 1
	if scenario == "repeat" {
		rounds = 3
	}
	for round := 0; round < rounds; round++ {
		runPlanningSignalChild(t, scope, cancel, scenario)
		if scenario == "term" || scenario == "cancel" {
			break
		}
		if scope.Context().Err() != nil {
			t.Fatal("planning interruption cancelled dashboard")
		}
	}
	if scenario != "term" && scenario != "cancel" {
		if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
	}
	awaitPlanningCancellation(t, scope.Context())
	scope.Close()
	// After teardown, a fresh subscription must receive SIGINT. This also catches
	// an accidental global Ignore disposition left behind by scope cleanup.
	restored := make(chan os.Signal, 1)
	signal.Notify(restored, os.Interrupt)
	defer signal.Stop(restored)
	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-restored:
	case <-time.After(5 * time.Second):
		t.Fatal("signal subscription not restored")
	}
	fmt.Println("planning-helper-ok")
}

func planningSignalChild(t *testing.T) {
	t.Helper()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	input := make(chan string, 1)
	go func() { scanner := bufio.NewScanner(os.Stdin); scanner.Scan(); input <- scanner.Text() }()
	fmt.Println("ready")
	for {
		select {
		case <-interrupts:
			fmt.Println("interrupt")
		case command := <-input:
			if command == "fail" {
				t.Fatal("requested helper exit failure")
			}
			return
		}
	}
}

func runPlanningSignalChild(t *testing.T, scope *uiPlanningSignals, cancel context.CancelFunc, scenario string) {
	t.Helper()
	input, feed, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	defer func() { _ = feed.Close() }()
	output, emit, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = output.Close() }()
	defer func() { _ = emit.Close() }()
	app, _, item := planningFixture(t)
	app.In, app.Out, app.Err = input, emit, os.Stderr
	t.Setenv("TAO_AGENT", "pi")
	adapter := newUINotePlanningLauncher(app, input, emit)
	commands := make(chan *exec.Cmd, 1)
	adapter.run = func(cmd *exec.Cmd) error {
		// Replace only executable lookup/argv, retaining the production command's
		// context cancellation, cwd, environment and attached terminal streams.
		cmd.Path = os.Args[0]
		cmd.Args = []string{os.Args[0], "-test.run=^TestUIPlanningSignalHelper$"}
		cmd.Err = nil
		childMode := "child"
		if scenario == "native-interrupt" {
			childMode = "native-child"
		}
		cmd.Env = append(cmd.Env, "TAO_UI_PLANNING_HELPER="+childMode)
		if scenario == "start-failure" {
			cmd.Path = "/nonexistent/tao-planning-test-agent"
		}
		commands <- cmd
		return cmd.Run()
	}
	app.UINotePlanningLauncher = adapter
	launcher := app.uiNotePlanningLauncher(input, scope)
	finished := make(chan error, 1)
	go func() { finished <- launcher.Launch(scope.Context(), item) }()
	cmd := <-commands
	if scenario == "start-failure" {
		select {
		case err := <-finished:
			if err == nil {
				t.Fatal("missing startup error")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("startup did not finish")
		}
		return
	}
	lines := make(chan string, 4)
	go func() {
		scanner := bufio.NewScanner(output)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	expect := func(want string) {
		t.Helper()
		select {
		case got := <-lines:
			if got != want {
				t.Fatalf("child said %q, want %q", got, want)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("waiting for child %s", want)
		}
	}
	expect("ready")
	switch scenario {
	case "term":
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		awaitPlanningCancellation(t, scope.Context())
	case "cancel":
		cancel()
		awaitPlanningCancellation(t, scope.Context())
	case "native-interrupt":
		if err := syscall.Kill(-os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
	default:
		if err := syscall.Kill(-os.Getpid(), syscall.SIGINT); err != nil {
			t.Fatal(err)
		}
		expect("interrupt")
		if scope.Context().Err() != nil {
			t.Fatal("parent cancelled by foreground SIGINT")
		}
		command := "exit"
		if scenario == "exit-error" {
			command = "fail"
		}
		if _, err := fmt.Fprintln(feed, command); err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-finished:
		wantError := scenario == "term" || scenario == "cancel" || scenario == "exit-error" || scenario == "native-interrupt"
		if (err != nil) != wantError {
			t.Fatalf("child result = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("child not waited/reaped")
	}
	if cmd.ProcessState == nil {
		t.Fatal("missing waited process state")
	}
	if err := syscall.Kill(cmd.Process.Pid, 0); err != syscall.ESRCH {
		t.Fatalf("child still exists after Launch: %v", err)
	}
}

func TestUIPlanningLauncherComposition(t *testing.T) {
	app, _, item := planningFixture(t)
	scope := newUIPlanningSignals(context.Background())
	defer scope.Close()
	composed := app.uiNotePlanningLauncher(app.In, scope).(scopedNotePlanningLauncher)
	adapter, ok := composed.launcher.(*uiNotePlanningLauncher)
	if !ok || adapter.input != app.In || adapter.output != app.Out || composed.signals != scope {
		t.Fatal("default adapter not composed")
	}
	calls := 0
	app.UINotePlanningLauncher = planningLaunchFunc(func(ctx context.Context, got note.CatalogNote) error {
		calls++
		if got.ID != item.ID || ctx.Err() != nil {
			t.Fatal("injected launcher received wrong selection/context")
		}
		return nil
	})
	launcher := app.uiNotePlanningLauncher(app.In, scope)
	if err := launcher.Launch(scope.Context(), item); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("injected launcher not composed")
	}
}
