package merge

import (
	"fmt"
	"io"
)

// FormatBatchTransitionLine projects a durable transition as one progress line,
// without a trailing newline.
func FormatBatchTransitionLine(transition BatchTransition) string {
	state := transition.State
	deferred := 0
	for _, candidate := range state.Candidates {
		if candidate.Deferred != nil {
			deferred++
		}
	}
	ejected := 0
	if state.Ejection != nil && state.Ejection.Status == "completed" {
		ejected = 1
	}
	line := fmt.Sprintf("%s merge-batch %s #%d %s -> %s candidates=%d deferred=%d ejected=%d",
		transition.At, state.ID, transition.Sequence, transition.From, transition.To, len(state.Candidates), deferred, ejected)
	if state.LandedSHA != "" {
		line += " landed=" + state.LandedSHA
	}
	return line
}

// BatchProgressStore adds best-effort display to the batch owner's durable
// mutations. The caller retains responsibility for batch ownership exclusion.
type BatchProgressStore struct {
	*BatchStore
	out io.Writer
}

func NewBatchProgressStore(store *BatchStore, out io.Writer) *BatchProgressStore {
	return &BatchProgressStore{BatchStore: store, out: out}
}

func (s *BatchProgressStore) Initialize(state BatchState, at string) (BatchState, error) {
	stored, err := s.BatchStore.Initialize(state, at)
	if err == nil && s.out != nil {
		_, _ = fmt.Fprintln(s.out, FormatBatchTransitionLine(BatchTransition{
			At: at, Sequence: stored.LogSequence, To: stored.Status, State: stored,
		}))
	}
	return stored, err
}

func (s *BatchProgressStore) Transition(next BatchState, at string) (BatchState, error) {
	var current BatchState
	if s.out != nil {
		// This read is presentation-only; the underlying mutation remains the
		// authority for state, sequencing, validation, and errors.
		current, _ = s.Load(next.ID)
	}
	stored, err := s.BatchStore.Transition(next, at)
	if err == nil && s.out != nil {
		_, _ = fmt.Fprintln(s.out, FormatBatchTransitionLine(BatchTransition{
			At: at, Sequence: stored.LogSequence, From: current.Status, To: stored.Status, State: stored,
		}))
	}
	return stored, err
}
