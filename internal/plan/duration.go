package plan

import "time"

func SliceDuration(slice Slice, now time.Time) time.Duration {
	if slice.Timing.DurationSeconds != nil {
		return time.Duration(*slice.Timing.DurationSeconds) * time.Second
	}
	if slice.Timing.StartedAt == nil {
		return 0
	}
	end := now
	if slice.Timing.CompletedAt != nil {
		end = *slice.Timing.CompletedAt
	}
	return end.Sub(*slice.Timing.StartedAt).Round(time.Second)
}
