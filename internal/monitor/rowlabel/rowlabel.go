package rowlabel

import (
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
)

// MaxSliceIDCells caps the display width of a slice ID in a phase label.
const MaxSliceIDCells = 20

// StatusRole is a renderer-independent semantic role for a status label.
type StatusRole uint8

const (
	StatusRoleNeutral StatusRole = iota
	StatusRoleSuccess
	StatusRoleActive
	StatusRoleReview
	StatusRoleWarn
)

// StatusRoleFor classifies a status for display, not lifecycle decisions.
func StatusRoleFor(status string) StatusRole {
	switch status {
	case plan.StatusCompleted, plan.StatusReviewed:
		return StatusRoleSuccess
	case plan.StatusInProgress:
		return StatusRoleActive
	case plan.StatusInReview:
		return StatusRoleReview
	case plan.StatusBlocked, plan.StatusPlanned, plan.StatusPending, plan.StatusChangesRequested, plan.StatusVerificationFailed:
		return StatusRoleWarn
	default:
		return StatusRoleNeutral
	}
}

// PlanLabel prefers the readable plan slug, then the ID, then the title.
func PlanLabel(row monitor.Row) string {
	id := strings.TrimSpace(row.PlanID)
	if slug, ok := plan.PlanSlug(id); ok {
		return slug
	}
	if id != "" {
		return id
	}
	return DisplayValue(row.PlanTitle)
}

// PhaseLabel projects the phase, slice ID, or possibly stalled run marker.
func PhaseLabel(row monitor.Row) string {
	if row.Status == plan.StatusAbandoned {
		return "-"
	}
	if IsStalled(row) {
		return fmt.Sprintf("stalled? (%s old)", DurationLabel(row.HeartbeatAge))
	}
	phase := strings.TrimSpace(string(row.Phase))
	sliceID := strings.TrimSpace(row.SliceID)
	if sliceID != "" && (phase == "" || phase == "running_slice") {
		return cells.Truncate(sliceID, MaxSliceIDCells)
	}
	return DisplayValue(phase)
}

// DurationLabel formats a nonnegative duration in whole seconds, minutes, or hours.
func DurationLabel(duration time.Duration) string {
	duration = max(duration, 0)
	switch {
	case duration < time.Minute:
		return fmt.Sprintf("%ds", duration/time.Second)
	case duration < time.Hour:
		return fmt.Sprintf("%dm", duration/time.Minute)
	default:
		return fmt.Sprintf("%dh", duration/time.Hour)
	}
}

// SlicesLabel combines original and rework slice progress.
func SlicesLabel(row monitor.Row) string {
	completed := row.OriginalCompletedCount + row.ReworkCompletedCount
	value := fmt.Sprintf("%d/%d", completed, row.OriginalTotalCount)
	if row.ReworkTotalCount > 0 {
		value += fmt.Sprintf("+%d", row.ReworkTotalCount)
	}
	return value
}

// DisplayValue substitutes a dash for blank text, preserving nonblank values.
func DisplayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// IsStalled reports whether a stale run still has a lock with a live process.
// It is a display predicate, not lifecycle or recovery authority.
func IsStalled(row monitor.Row) bool {
	return row.Liveness == monitor.LivenessStale && row.RunLockPresent && row.RunLockProcessAlive
}
