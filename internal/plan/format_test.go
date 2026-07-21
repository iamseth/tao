package plan

import (
	"os"
	"testing"
	"time"
)

func TestFormatTimeUsesLocalTimezone(t *testing.T) {
	originalTZ := os.Getenv("TZ")
	originalLocal := time.Local
	t.Cleanup(func() {
		time.Local = originalLocal
		if originalTZ == "" {
			_ = os.Unsetenv("TZ")
			return
		}
		_ = os.Setenv("TZ", originalTZ)
	})
	if err := os.Setenv("TZ", "America/Chicago"); err != nil {
		t.Fatal(err)
	}
	time.Local = time.FixedZone("CDT", -5*60*60)

	utc := time.Date(2026, 4, 27, 21, 32, 40, 0, time.UTC)
	if got := FormatTime(&utc); got != "2026-04-27 16:32:40 CDT" {
		t.Fatalf("expected local CDT output, got %q", got)
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		name string
		d    time.Duration
		want string
	}{
		{name: "zero", d: 0, want: "-"},
		{name: "seconds", d: 42 * time.Second, want: "42s"},
		{name: "minutes", d: 2*time.Minute + 3*time.Second, want: "2m03s"},
		{name: "hours", d: 3*time.Hour + 4*time.Minute + 5*time.Second, want: "3h04m"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatDuration(test.d); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestFormatHumanTime(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		t    *time.Time
		want string
	}{
		{name: "nil", t: nil, want: "-"},
		{name: "sub-minute minimum", t: new(now.Add(-30 * time.Second)), want: "1m"},
		{name: "minutes", t: new(now.Add(-42 * time.Minute)), want: "42m"},
		{name: "one hour boundary", t: new(now.Add(-time.Hour)), want: "1h"},
		{name: "hours", t: new(now.Add(-23*time.Hour - 59*time.Minute)), want: "23h"},
		{name: "twenty four hour boundary", t: new(now.Add(-24 * time.Hour)), want: "2026-05-24"},
		{name: "date-only output", t: new(now.Add(-48 * time.Hour)), want: "2026-05-23"},
		{name: "future timestamp", t: new(now.Add(5 * time.Minute)), want: "1m"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatHumanTime(test.t, now); got != test.want {
				t.Fatalf("expected %q, got %q", test.want, got)
			}
		})
	}
}

func TestFormatHumanTimeUsesLocalDate(t *testing.T) {
	originalTZ := os.Getenv("TZ")
	originalLocal := time.Local
	t.Cleanup(func() {
		time.Local = originalLocal
		if originalTZ == "" {
			_ = os.Unsetenv("TZ")
			return
		}
		_ = os.Setenv("TZ", originalTZ)
	})
	if err := os.Setenv("TZ", "America/Chicago"); err != nil {
		t.Fatal(err)
	}
	time.Local = time.FixedZone("CDT", -5*60*60)

	now := time.Date(2026, 4, 28, 21, 32, 40, 0, time.UTC)
	utc := time.Date(2026, 4, 27, 2, 30, 0, 0, time.UTC)
	if got := FormatHumanTime(&utc, now); got != "2026-04-26" {
		t.Fatalf("expected local date output, got %q", got)
	}
}
