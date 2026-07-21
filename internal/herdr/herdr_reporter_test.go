package herdr

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReporterDisabledWithoutHerdrEnv(t *testing.T) {
	clearHerdrEnv(t)

	reporter := New()
	if reporter.Enabled() {
		t.Fatal("expected reporter to be disabled without Herdr environment")
	}

	called := false
	if err := reporter.Track("run disabled", func() error {
		called = true
		return nil
	}); err != nil {
		t.Fatalf("Track returned unexpected error: %v", err)
	}
	if !called {
		t.Fatal("Track did not run function for disabled reporter")
	}

	expectedErr := errors.New("boom")
	if err := reporter.Track("run disabled", func() error { return expectedErr }); !errors.Is(err, expectedErr) {
		t.Fatalf("Track error = %v, want %v", err, expectedErr)
	}

	defer func() {
		if recovered := recover(); recovered != "panic-value" {
			t.Fatalf("recovered panic = %v, want panic-value", recovered)
		}
	}()
	_ = reporter.Track("run disabled", func() error { panic("panic-value") })
}

func TestReporterSwallowsSocketFailures(t *testing.T) {
	reporter := newReporterForSocket(t, filepath.Join(t.TempDir(), "missing.sock"))
	if !reporter.Enabled() {
		t.Fatal("expected reporter to be enabled with Herdr environment")
	}

	reporter.Working("run missing-socket")
	reporter.Idle()
	reporter.Blocked()

	expectedErr := errors.New("socket unavailable")
	if err := reporter.Track("run missing-socket", func() error { return expectedErr }); !errors.Is(err, expectedErr) {
		t.Fatalf("Track error = %v, want %v", err, expectedErr)
	}

	defer func() {
		if recovered := recover(); recovered != "still-panics" {
			t.Fatalf("recovered panic = %v, want still-panics", recovered)
		}
	}()
	_ = reporter.Track("run missing-socket", func() error { panic("still-panics") })
}

func TestTrackReportsSuccessAndErrorSettling(t *testing.T) {
	recorder := newReportRecorder(t)
	reporter := newReporterForSocket(t, recorder.path)

	if err := reporter.Track("run herdr-status", func() error { return nil }); err != nil {
		t.Fatalf("Track returned unexpected error: %v", err)
	}
	assertReport(t, recorder.next(t), "working", "run herdr-status")
	assertReport(t, recorder.next(t), "idle", "")

	expectedErr := errors.New("run failed")
	if err := reporter.Track("run herdr-status", func() error { return expectedErr }); !errors.Is(err, expectedErr) {
		t.Fatalf("Track error = %v, want %v", err, expectedErr)
	}
	assertReport(t, recorder.next(t), "working", "run herdr-status")
	assertReport(t, recorder.next(t), "blocked", "")
}

func TestTrackReReportsActiveRunAndDefersBlocked(t *testing.T) {
	recorder := newReportRecorder(t)
	reporter := newReporterForSocket(t, recorder.path)
	expectedErr := errors.New("first failed")

	releaseFirst := make(chan struct{})
	firstStarted, firstDone := startTrackedRun(reporter, "run first", releaseFirst, expectedErr)
	waitForTrackStart(t, firstStarted)
	assertReport(t, recorder.next(t), "working", "run first")

	releaseSecond := make(chan struct{})
	secondStarted, secondDone := startTrackedRun(reporter, "run second", releaseSecond, nil)
	waitForTrackStart(t, secondStarted)
	assertReport(t, recorder.next(t), "working", "run second")

	close(releaseFirst)
	if err := waitForTrackDone(t, firstDone); !errors.Is(err, expectedErr) {
		t.Fatalf("first Track error = %v, want %v", err, expectedErr)
	}
	assertReport(t, recorder.next(t), "working", "run second")

	close(releaseSecond)
	if err := waitForTrackDone(t, secondDone); err != nil {
		t.Fatalf("second Track returned unexpected error: %v", err)
	}
	assertReport(t, recorder.next(t), "blocked", "")
}

func TestWorkingReportIncludesCustomStatusPayload(t *testing.T) {
	recorder := newReportRecorder(t)
	reporter := newReporterForSocket(t, recorder.path)

	reporter.Working("run herdr-status")

	var payload map[string]any
	if err := json.Unmarshal(recorder.nextLine(t), &payload); err != nil {
		t.Fatalf("unmarshal report payload: %v", err)
	}
	params, ok := payload["params"].(map[string]any)
	if !ok {
		t.Fatalf("payload params missing or invalid: %#v", payload["params"])
	}
	if _, ok := params["status"]; ok {
		t.Fatalf("payload includes legacy status field: %#v", params["status"])
	}
	if got, ok := params["custom_status"].(string); !ok || got != "run herdr-status" {
		t.Fatalf("payload custom_status = %#v, want %q", params["custom_status"], "run herdr-status")
	}
}

func TestTrackReportsBlockedBeforePanic(t *testing.T) {
	recorder := newReportRecorder(t)
	reporter := newReporterForSocket(t, recorder.path)

	defer func() {
		if recovered := recover(); recovered != "panic-value" {
			t.Fatalf("recovered panic = %v, want panic-value", recovered)
		}
		assertReport(t, recorder.next(t), "working", "run panic")
		assertReport(t, recorder.next(t), "blocked", "")
	}()
	_ = reporter.Track("run panic", func() error { panic("panic-value") })
}

func startTrackedRun(reporter *Reporter, status string, release <-chan struct{}, result error) (<-chan struct{}, <-chan error) {
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- reporter.Track(status, func() error {
			close(started)
			<-release
			return result
		})
	}()
	return started, done
}

func waitForTrackStart(t *testing.T, started <-chan struct{}) {
	t.Helper()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tracked run to start")
	}
}

func waitForTrackDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for tracked run to finish")
		return nil
	}
}

func clearHerdrEnv(t *testing.T) {
	t.Helper()
	t.Setenv(envHerdr, "")
	t.Setenv(envSocketPath, "")
	t.Setenv(envPaneID, "")
}

func newReporterForSocket(t *testing.T, socketPath string) *Reporter {
	t.Helper()
	t.Setenv(envHerdr, "1")
	t.Setenv(envSocketPath, socketPath)
	t.Setenv(envPaneID, "pane-1")
	return New()
}

type reportRecorder struct {
	path  string
	lines chan []byte
}

func newReportRecorder(t *testing.T) *reportRecorder {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "herdr.sock")
	// Darwin limits Unix socket paths to 104 bytes, while testing.TempDir
	// includes the full (and often long) test name.
	if len(path) >= 100 {
		var err error
		dir, err = os.MkdirTemp("/tmp", "tao-herdr-")
		if err != nil {
			t.Fatalf("create short socket directory: %v", err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		path = filepath.Join(dir, "herdr.sock")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen on unix socket: %v", err)
	}
	recorder := &reportRecorder{path: path, lines: make(chan []byte, 16)}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			line, err := bufio.NewReader(conn).ReadBytes('\n')
			_ = conn.Close()
			if err == nil {
				recorder.lines <- line
			}
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("timed out waiting for recorder to stop")
		}
	})

	return recorder
}

func (r *reportRecorder) next(t *testing.T) reportRequest {
	t.Helper()
	line := r.nextLine(t)
	var request reportRequest
	if err := json.Unmarshal(line, &request); err != nil {
		t.Fatalf("unmarshal report %q: %v", string(line), err)
	}
	return request
}

func (r *reportRecorder) nextLine(t *testing.T) []byte {
	t.Helper()
	select {
	case line := <-r.lines:
		return line
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Herdr report")
		return nil
	}
}

func assertReport(t *testing.T, request reportRequest, state string, status string) {
	t.Helper()
	if request.ID == "" {
		t.Fatal("request ID is empty")
	}
	if request.Method != "pane.report_agent" {
		t.Fatalf("method = %q, want pane.report_agent", request.Method)
	}
	if request.Params.PaneID != "pane-1" {
		t.Fatalf("pane_id = %q, want pane-1", request.Params.PaneID)
	}
	if request.Params.Source != herdrSource {
		t.Fatalf("source = %q, want %q", request.Params.Source, herdrSource)
	}
	if request.Params.Agent != herdrAgent {
		t.Fatalf("agent = %q, want %q", request.Params.Agent, herdrAgent)
	}
	if request.Params.State != state {
		t.Fatalf("state = %q, want %q", request.Params.State, state)
	}
	if request.Params.CustomStatus != status {
		t.Fatalf("custom_status = %q, want %q", request.Params.CustomStatus, status)
	}
}
