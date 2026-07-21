package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestLogPrintsAgentRunLog(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	want := "\x1b[32magent output\x1b[0m\n"
	if err := os.WriteFile(plan.LogPath(fixture.dir), []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	err := App{Out: &out, Err: &out}.Run(context.Background(), []string{"--plans-dir", fixture.root, "log", "20260430-1200"})
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != want {
		t.Fatalf("unexpected log output %q", got)
	}
}

func TestLogFollowsAppendedOutput(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	logPath := plan.LogPath(fixture.dir)
	if err := os.WriteFile(logPath, []byte("initial\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	appended := "\x1b[31mappended\x1b[0m\n"
	out := newNotifyingBuffer(appended)
	var errOut bytes.Buffer
	app := App{Out: out, Err: &errOut}
	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Run(ctx, []string{"--plans-dir", fixture.root, "log", fixture.id, "-f"})
	}()

	time.Sleep(50 * time.Millisecond)
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: test-controlled log path
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(logFile, appended); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	if err := logFile.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-out.done:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatalf("expected appended output, got %q", out.String())
	}
	cancel()
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context cancellation, got %v", err)
	}
	if got, want := out.String(), "initial\n"+appended; !strings.Contains(got, want) {
		t.Fatalf("unexpected followed output %q", got)
	}
}

func TestLogUsageRepoAndMissingLogErrors(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.log(context.Background(), fakeRepository{}, nil); err == nil {
		t.Fatal("expected log usage error")
	}
	err := app.log(context.Background(), fakeRepository{err: errors.New("nope")}, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected repo error, got %v", err)
	}

	root := t.TempDir()
	planID := "20260430-1200-run-plan"
	writeRunPlan(t, root, planID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	err = app.log(context.Background(), plan.NewFileRepository(root), []string{planID})
	if err == nil || !strings.Contains(err.Error(), "agent log not found") {
		t.Fatalf("expected missing log error, got %v", err)
	}
}
