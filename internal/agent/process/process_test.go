package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSessionLifetimeObservesExitWithoutWait(t *testing.T) {
	dir := t.TempDir()
	ended := make(chan struct{})
	ctx := WithSessionLifetime(context.Background(), nil, func() { close(ended) })
	// Runtime readers may wait for EOF before calling Wait. A descendant holding
	// the pipes must not keep the dead provider's nested completion alive.
	proc, err := DefaultProcessStarter(ctx, dir, "sh", []string{"-c", `(while [ ! -f release ]; do sleep 0.02; done) & exit 0`})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.WriteFile(filepath.Join(dir, "release"), nil, 0o600); err != nil {
			t.Error(err)
		}
		_ = proc.Kill()
		_ = waitForProcess(t, proc, 3*time.Second)
	})
	select {
	case <-ended:
	case <-time.After(3 * time.Second):
		t.Fatal("provider exit observation depends on caller Wait or inherited-pipe EOF")
	}
}

func TestSessionLifetimePropagationAndExitBeforePipeDrain(t *testing.T) {
	t.Setenv("TAO_PROCESS_GENERIC_ENV", "old")
	readerDone := make(chan struct{})
	observed := make(chan bool, 1)
	env := []string{"TAO_PROCESS_GENERIC_ENV=invocation"}
	ctx := WithSessionLifetime(context.Background(), env, func() {
		select {
		case <-readerDone:
			observed <- false
		default:
			observed <- true
		}
	})
	env[0] = "TAO_PROCESS_GENERIC_ENV=mutated"
	// The descendant retains stdout after its parent exits. Observing process
	// death must precede bounded pipe draining, not depend on EOF from descendants.
	proc, err := DefaultProcessStarter(ctx, "", "sh", []string{"-c", `printf '%s\n' "$TAO_PROCESS_GENERIC_ENV"; sleep 1 & exit 0`})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Kill() }()
	output := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(proc.Stdout())
		close(readerDone)
		output <- string(data)
	}()
	if err := waitForProcess(t, proc, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	if !<-observed {
		t.Fatal("lifetime ended only after inherited pipes drained")
	}
	if got := <-output; got != "invocation\n" {
		t.Fatalf("private environment = %q", got)
	}
	if ctx.Err() != nil {
		t.Fatal("provider exit changed runtime context semantics")
	}
}

func TestDefaultProcessStarterErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := DefaultProcessStarter(ctx, "", "cat", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if _, err := DefaultProcessStarter(context.Background(), "", "definitely-not-a-tao-test-command", nil); err == nil {
		t.Fatal("expected missing command error")
	}
}

func TestDefaultProcessStarterCanRunAndWait(t *testing.T) {
	proc, err := DefaultProcessStarter(context.Background(), "", "cat", nil)
	if err != nil {
		t.Fatal(err)
	}
	if proc.Stdin() == nil || proc.Stdout() == nil || proc.Stderr() == nil {
		t.Fatal("expected process streams")
	}
	if err := proc.Stdin().Close(); err != nil {
		t.Fatal(err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultProcessStarterConcurrentWaitDrainsOutput(t *testing.T) {
	t.Setenv("TAO_PROCESS_OUTPUT_HELPER", "1")
	proc, err := DefaultProcessStarter(context.Background(), "", os.Args[0], []string{"-test.run=^TestProcessOutputHelper$", "--"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = proc.Kill() }()
	_ = proc.Stdin().Close()
	type output struct {
		data []byte
		err  error
	}
	stdout, stderr := make(chan output, 1), make(chan output, 1)
	for reader, done := range map[io.Reader]chan output{proc.Stdout(): stdout, proc.Stderr(): stderr} {
		go func() {
			data, err := io.ReadAll(reader)
			done <- output{data, err}
		}()
	}
	if err := waitForProcess(t, proc, 3*time.Second); err != nil {
		t.Fatal(err)
	}
	for label, done := range map[string]chan output{"stdout": stdout, "stderr": stderr} {
		select {
		case got := <-done:
			want := strings.Repeat(label+"\n", 64*1024)
			if got.err != nil || string(got.data) != want {
				t.Fatalf("%s truncated: got %d bytes, want %d, error %v", label, len(got.data), len(want), got.err)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s reader did not finish after Wait", label)
		}
	}
}

func TestProcessOutputHelper(t *testing.T) {
	if os.Getenv("TAO_PROCESS_OUTPUT_HELPER") != "1" {
		return
	}
	for label, writer := range map[string]*os.File{"stdout": os.Stdout, "stderr": os.Stderr} {
		if _, err := io.WriteString(writer, strings.Repeat(label+"\n", 64*1024)); err != nil {
			os.Exit(1)
		}
	}
	os.Exit(0)
}

func TestDefaultProcessStarterTerminatesOnContextCancel(t *testing.T) {
	t.Setenv("TAO_PROCESS_TEST_HELPER", "1")
	ctx, cancel := context.WithCancel(context.Background())
	proc, err := DefaultProcessStarter(ctx, "", os.Args[0], []string{"-test.run=TestProcessHelper", "--"})
	if err != nil {
		t.Fatal(err)
	}

	cancel()
	waitErr := waitForProcess(t, proc, 2*time.Second)
	if waitErr == nil {
		t.Fatal("expected cancelled process to exit with an error")
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("expected double kill to be harmless, got %v", err)
	}
}

func TestDefaultProcessStarterStripsHerdrEnv(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr.sock")
	t.Setenv("HERDR_PANE_ID", "pane-1")
	t.Setenv("TAO_PROCESS_GENERIC_ENV", "visible")

	snapshot := runEnvHelper(t)
	if snapshot.HerdrEnvPresent || snapshot.HerdrSocketPathPresent || snapshot.HerdrPaneIDPresent {
		t.Fatalf("expected Herdr env to be stripped, got %#v", snapshot)
	}
	if !snapshot.GenericPresent || snapshot.GenericValue != "visible" {
		t.Fatalf("expected generic env to remain visible, got %#v", snapshot)
	}
}

func TestDefaultProcessStarterKeepsGenericEnv(t *testing.T) {
	t.Setenv("TAO_PROCESS_GENERIC_ENV", "visible")

	snapshot := runEnvHelper(t)
	if !snapshot.GenericPresent || snapshot.GenericValue != "visible" {
		t.Fatalf("expected generic env to remain visible, got %#v", snapshot)
	}
}

func TestDefaultProcessStarterSetsPWDForCWDWhileStrippingHerdrEnv(t *testing.T) {
	t.Setenv("HERDR_ENV", "1")
	t.Setenv("HERDR_SOCKET_PATH", "/tmp/herdr.sock")
	t.Setenv("HERDR_PANE_ID", "pane-1")
	cwd := t.TempDir()

	snapshot := runEnvHelperInCWD(t, cwd)
	if snapshot.PWDValue != cwd {
		t.Fatalf("expected child PWD %q, got %#v", cwd, snapshot)
	}
	if snapshot.HerdrEnvPresent || snapshot.HerdrSocketPathPresent || snapshot.HerdrPaneIDPresent {
		t.Fatalf("expected Herdr env to be stripped, got %#v", snapshot)
	}
}

func TestProcessHelper(t *testing.T) {
	if os.Getenv("TAO_PROCESS_TEST_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestProcessEnvHelper(t *testing.T) {
	if os.Getenv("TAO_PROCESS_ENV_HELPER") != "1" {
		return
	}

	genericValue, genericPresent := os.LookupEnv("TAO_PROCESS_GENERIC_ENV")
	_, herdrEnvPresent := os.LookupEnv("HERDR_ENV")
	_, herdrSocketPathPresent := os.LookupEnv("HERDR_SOCKET_PATH")
	_, herdrPaneIDPresent := os.LookupEnv("HERDR_PANE_ID")
	snapshot := processEnvSnapshot{
		HerdrEnvPresent:        herdrEnvPresent,
		HerdrSocketPathPresent: herdrSocketPathPresent,
		HerdrPaneIDPresent:     herdrPaneIDPresent,
		GenericPresent:         genericPresent,
		GenericValue:           genericValue,
		PWDValue:               os.Getenv("PWD"),
	}
	if err := json.NewEncoder(os.Stdout).Encode(snapshot); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func runEnvHelper(t *testing.T) processEnvSnapshot {
	t.Helper()
	return runEnvHelperInCWD(t, "")
}

func runEnvHelperInCWD(t *testing.T, cwd string) processEnvSnapshot {
	t.Helper()
	t.Setenv("TAO_PROCESS_ENV_HELPER", "1")

	proc, err := DefaultProcessStarter(context.Background(), cwd, os.Args[0], []string{"-test.run=^TestProcessEnvHelper$", "--"})
	if err != nil {
		t.Fatal(err)
	}

	var snapshot processEnvSnapshot
	if err := json.NewDecoder(proc.Stdout()).Decode(&snapshot); err != nil {
		_ = proc.Kill()
		t.Fatalf("decode helper environment: %v", err)
	}
	if err := proc.Wait(); err != nil {
		t.Fatalf("env helper failed: %v", err)
	}
	return snapshot
}

type processEnvSnapshot struct {
	HerdrEnvPresent        bool
	HerdrSocketPathPresent bool
	HerdrPaneIDPresent     bool
	GenericPresent         bool
	GenericValue           string
	PWDValue               string
}

func waitForProcess(t *testing.T, proc Process, timeout time.Duration) error {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		done <- proc.Wait()
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		_ = proc.Kill()
		t.Fatalf("process did not exit within %s", timeout)
		return nil
	}
}

func TestExecProcessAccessorsAndKillNilProcess(t *testing.T) {
	stdin := testWriteCloser{}
	stdout := strings.NewReader("out")
	stderr := strings.NewReader("err")
	proc := &execProcess{cmd: &exec.Cmd{}, stdin: stdin, stdout: stdout, stderr: stderr}
	if proc.Stdin() != stdin || proc.Stdout() != stdout || proc.Stderr() != stderr {
		t.Fatal("unexpected process streams")
	}
	if err := proc.Kill(); err != nil {
		t.Fatalf("expected nil kill error for nil process, got %v", err)
	}
}

type testWriteCloser struct{}

func (testWriteCloser) Write(p []byte) (int, error) { return len(p), nil }
func (testWriteCloser) Close() error                { return nil }
