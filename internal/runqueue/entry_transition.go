package runqueue

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// EntryOutcome identifies how one synchronous attempt to drive a queue entry
// ended. It is deliberately separate from QueueStatus: waiting and the two
// running/requeue outcomes describe scheduler actions rather than durable
// terminal states.
type EntryOutcome string

const (
	EntryOutcomeSucceeded         EntryOutcome = "succeeded"
	EntryOutcomeFailed            EntryOutcome = "failed"
	EntryOutcomeSkipped           EntryOutcome = "skipped"
	EntryOutcomeWaiting           EntryOutcome = "waiting"
	EntryOutcomeReady             EntryOutcome = "ready"
	EntryOutcomeRequeuedAfterStop EntryOutcome = "requeued_after_stop"
	EntryOutcomeRetainedRunning   EntryOutcome = "retained_running"
)

// EntryResult is the result of driving one queue entry. Err is required for a
// failed result. Reason supplies the durable reason for skipped and waiting
// results; for skipped results Err is used when Reason is empty.
type EntryResult struct {
	Outcome EntryOutcome
	Err     error
	Reason  string
}

// EntryTransition is a value-only request to replace one durable queue entry.
// Before identifies the exact queue generation and expected status; After is
// the replacement to persist and publish.
type EntryTransition struct {
	Before QueueEntry
	After  QueueEntry
	Result EntryResult
}

// EntryDriverHost is the narrow queue-owner seam used by EntryDriver. A host
// must persist After before publishing it to in-memory queue/status views. It
// owns any synchronization needed for that ordering and must not invoke driver
// or other external callbacks while its locks are held.
type EntryDriverHost interface {
	TransitionEntry(context.Context, EntryTransition) error
}

func entryTransitionForResult(entry QueueEntry, result EntryResult, now time.Time) (EntryTransition, bool, error) {
	if entry.PlanID == "" {
		return EntryTransition{}, false, errors.New("entry result requires plan id")
	}

	after := entry
	clearResultFields := func() {
		after.FinishedAt = nil
		after.Error = ""
		after.SkipReason = ""
		after.WaitReason = ""
	}
	finish := func() {
		finished := now
		after.FinishedAt = &finished
		after.RecoveryPending = false
		after.WaitReason = ""
		after.Error = ""
		after.SkipReason = ""
	}

	switch result.Outcome {
	case EntryOutcomeSucceeded:
		if entry.Status != QueueStatusRunning {
			return EntryTransition{}, false, outcomeStatusError(result.Outcome, entry.Status)
		}
		finish()
		after.Status = QueueStatusSucceeded
	case EntryOutcomeFailed:
		if entry.Status != QueueStatusPending && entry.Status != QueueStatusRunning {
			return EntryTransition{}, false, outcomeStatusError(result.Outcome, entry.Status)
		}
		if result.Err == nil {
			return EntryTransition{}, false, errors.New("failed entry result requires an error")
		}
		finish()
		after.Status = QueueStatusFailed
		after.Error = result.Err.Error()
	case EntryOutcomeSkipped:
		if entry.Status != QueueStatusPending && entry.Status != QueueStatusRunning {
			return EntryTransition{}, false, outcomeStatusError(result.Outcome, entry.Status)
		}
		finish()
		after.Status = QueueStatusSkipped
		after.SkipReason = result.Reason
		if after.SkipReason == "" && result.Err != nil {
			after.SkipReason = result.Err.Error()
		}
	case EntryOutcomeWaiting:
		if entry.Status != QueueStatusPending && entry.Status != QueueStatusRunning {
			return EntryTransition{}, false, outcomeStatusError(result.Outcome, entry.Status)
		}
		clearResultFields()
		after.Status = QueueStatusPending
		after.StartedAt = nil
		after.WaitReason = result.Reason
	case EntryOutcomeReady:
		if entry.Status != QueueStatusPending && entry.Status != QueueStatusRunning {
			return EntryTransition{}, false, outcomeStatusError(result.Outcome, entry.Status)
		}
		return EntryTransition{}, false, nil
	case EntryOutcomeRequeuedAfterStop:
		if entry.Status != QueueStatusRunning {
			return EntryTransition{}, false, outcomeStatusError(result.Outcome, entry.Status)
		}
		clearResultFields()
		after.Status = QueueStatusPending
		after.StartedAt = nil
	case EntryOutcomeRetainedRunning:
		if entry.Status != QueueStatusRunning {
			return EntryTransition{}, false, outcomeStatusError(result.Outcome, entry.Status)
		}
		return EntryTransition{}, false, nil
	default:
		return EntryTransition{}, false, fmt.Errorf("unknown entry outcome %q", result.Outcome)
	}

	return EntryTransition{Before: entry, After: after, Result: result}, true, nil
}

func outcomeStatusError(outcome EntryOutcome, status QueueStatus) error {
	return fmt.Errorf("entry outcome %s cannot be applied from status %s", outcome, status)
}
