package process

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

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
