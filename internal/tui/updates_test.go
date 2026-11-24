package tui

import (
	"context"
	"testing"
	"time"
)

func receiveLoopUpdate(t *testing.T, s *loopState) bool {
	t.Helper()
	select {
	case update := <-s.ensureUpdates():
		return update(s)
	case <-time.After(time.Second):
		t.Fatal("missing loop update")
		return false
	}
}

func TestLoopUpdatesInitializationAndOrder(t *testing.T) {
	s := loopState{}
	updates := s.ensureUpdates()
	if updates == nil || cap(updates) == 0 || s.ensureUpdates() != updates {
		t.Fatal("queue must be bounded and stable")
	}
	for i := range cap(updates) {
		if !publishLoopUpdate(context.Background(), updates, func(s *loopState) bool {
			if s.selected != i {
				t.Fatalf("application order: got %d want %d", s.selected, i)
			}
			s.selected++
			return i%2 == 0
		}) {
			t.Fatal("publication failed")
		}
	}
	if s.selected != 0 {
		t.Fatal("publisher mutated state")
	}
	for i := range cap(updates) {
		if got := receiveLoopUpdate(t, &s); got != (i%2 == 0) {
			t.Fatal("redraw decision changed")
		}
	}
}

func TestLoopUpdatesSaturationCancellation(t *testing.T) {
	s := loopState{}
	updates := s.ensureUpdates()
	for range cap(updates) {
		updates <- func(*loopState) bool { return false }
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, done := make(chan struct{}), make(chan bool, 1)
	go func() {
		close(started)
		done <- publishLoopUpdate(ctx, updates, func(*loopState) bool { t.Error("canceled update applied"); return true })
	}()
	<-started
	select {
	case <-done:
		t.Fatal("publisher escaped full queue")
	default:
	}
	cancel()
	select {
	case published := <-done:
		if published {
			t.Fatal("published into full queue")
		}
	case <-time.After(time.Second):
		t.Fatal("publisher ignored cancellation")
	}
	for range cap(updates) {
		receiveLoopUpdate(t, &s)
	}
	if !publishLoopUpdate(context.Background(), updates, func(*loopState) bool { return true }) {
		t.Fatal("queue unusable after cancellation")
	}
	if !receiveLoopUpdate(t, &s) {
		t.Fatal("lost redraw")
	}
}
