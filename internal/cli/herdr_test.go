package cli

import (
	"errors"
	"testing"
)

func TestHerdrStatusReporterThreadsStatus(t *testing.T) {
	expectedErr := errors.New("boom")
	tracker := &recordingHerdrTracker{}
	reporter := herdrStatusReporter{tracker: tracker}

	err := reporter.Track("run herdr-status", func() error { return expectedErr })
	if !errors.Is(err, expectedErr) {
		t.Fatalf("Track error = %v, want %v", err, expectedErr)
	}
	if tracker.status != "run herdr-status" {
		t.Fatalf("tracker status = %q, want run herdr-status", tracker.status)
	}
	if !tracker.called {
		t.Fatal("tracker did not run function")
	}
}

type recordingHerdrTracker struct {
	status string
	called bool
}

func (r *recordingHerdrTracker) Track(status string, fn func() error) error {
	r.status = status
	r.called = true
	return fn()
}
