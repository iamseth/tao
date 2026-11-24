package tui

import "context"

// loopUpdate runs only on the event loop. True requests one central frame write;
// false requests none. Workers capture results, not mutable loop state.
type loopUpdate func(*loopState) bool

func (a App) applyLoopUpdate(s *loopState, update loopUpdate) error {
	if update(s) {
		return a.writeFrame(*s)
	}
	return nil
}

// ensureUpdates is called only by the loop goroutine, before starting workers.
func (s *loopState) ensureUpdates() chan loopUpdate {
	if s.updates == nil {
		// Allow a small burst across loaders while bounding queued work and memory.
		s.updates = make(chan loopUpdate, 16)
	}
	return s.updates
}

// publishLoopUpdate is for workers only: publishing synchronously from the loop
// could deadlock a full queue. Producers capture the queue before starting and
// never close it; cancellation releases publishers when the loop exits.
func publishLoopUpdate(ctx context.Context, updates chan<- loopUpdate, update loopUpdate) bool {
	select {
	case updates <- update:
		return true
	case <-ctx.Done():
		return false
	}
}
