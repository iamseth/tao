package plan

import (
	"fmt"
	"time"
)

func FormatDuration(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	d = d.Round(time.Second)
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	minutes := int(d / time.Minute)
	d -= time.Duration(minutes) * time.Minute
	seconds := int(d / time.Second)

	if hours > 0 {
		return fmt.Sprintf("%dh%02dm", hours, minutes)
	}
	if minutes > 0 {
		return fmt.Sprintf("%dm%02ds", minutes, seconds)
	}
	return fmt.Sprintf("%ds", seconds)
}

func FormatTime(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05 MST")
}

func FormatHumanTime(t *time.Time, now time.Time) string {
	if t == nil {
		return "-"
	}
	age := max(now.Sub(*t), 0)
	if age < time.Hour {
		minutes := max(int(age/time.Minute), 1)
		return fmt.Sprintf("%dm", minutes)
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh", int(age/time.Hour))
	}
	return t.Local().Format("2006-01-02")
}
