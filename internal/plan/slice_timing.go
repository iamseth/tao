package plan

import (
	"fmt"
	"time"
)

// ResolveSliceStartedAt reads timing metadata without granting lifecycle or Git
// authority. A present timestamp is preserved; missing timing requires exact,
// nonzero, unambiguous durable slice_started evidence.
func ResolveSliceStartedAt(detail *PlanDetail, sliceID string) (startedAt time.Time, recovered bool, err error) {
	if detail == nil {
		return time.Time{}, false, fmt.Errorf("plan detail is nil")
	}
	slice := findSlice(detail, sliceID)
	if slice == nil {
		return time.Time{}, false, classify(ErrNotFound, "slice %s not found", sliceID)
	}
	if slice.Timing.StartedAt != nil {
		return *slice.Timing.StartedAt, false, nil
	}
	for _, event := range detail.Events {
		if event.Type != EventTypeSliceStarted || event.PlanID != detail.State.Plan.ID || event.SliceID != sliceID {
			continue
		}
		if event.Timestamp.IsZero() {
			return time.Time{}, false, fmt.Errorf("slice %s has no started_at: zero slice_started timestamp", sliceID)
		}
		if !startedAt.IsZero() && !startedAt.Equal(event.Timestamp) {
			return time.Time{}, false, fmt.Errorf("slice %s has no started_at: conflicting slice_started timestamps", sliceID)
		}
		startedAt = event.Timestamp
	}
	if startedAt.IsZero() {
		return time.Time{}, false, fmt.Errorf("slice %s has no started_at: no matching slice_started evidence", sliceID)
	}
	return startedAt, true, nil
}
