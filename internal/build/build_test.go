package build

import (
	"runtime/debug"
	"testing"
	"time"
)

func TestRuntimeBuildInfoAccessorsReturnValues(t *testing.T) {
	if got := Commit(); got == "" {
		t.Fatal("Commit returned empty string")
	}
	if got := BuildAge(); got == "" {
		t.Fatal("BuildAge returned empty string")
	}
}

func TestVersionUsesInjectedReleaseValue(t *testing.T) {
	original := version
	version = "v1.2.3"
	t.Cleanup(func() { version = original })

	if got := Version(); got != "v1.2.3" {
		t.Fatalf("Version() = %q, want %q", got, "v1.2.3")
	}
}

func TestVersionFallsBackToDevForLocalBuilds(t *testing.T) {
	original := version
	t.Cleanup(func() { version = original })

	for _, value := range []string{"", "   "} {
		version = value
		if got := Version(); got != "dev" {
			t.Fatalf("Version() with version %q = %q, want %q", value, got, "dev")
		}
	}
}

func TestNormalizeVersion(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "v0.1.0", want: "v0.1.0"},
		{value: "  v0.1.0  ", want: "v0.1.0"},
		{value: "", want: "dev"},
		{value: "   ", want: "dev"},
	}
	for _, test := range tests {
		if got := NormalizeVersion(test.value); got != test.want {
			t.Fatalf("NormalizeVersion(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestShortCommitFormatsRevision(t *testing.T) {
	if got := ShortCommit("abcdef1234567890"); got != "abcdef1" {
		t.Fatalf("ShortCommit() = %q, want %q", got, "abcdef1")
	}
}

func TestShortCommitFallsBackForMissingRevision(t *testing.T) {
	for _, revision := range []string{"", "   ", "abc123"} {
		if got := ShortCommit(revision); got != "unknown" {
			t.Fatalf("ShortCommit(%q) = %q, want unknown", revision, got)
		}
	}
}

func TestCommitFromSettingsUsesVCSRevision(t *testing.T) {
	settings := []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef"}}
	if got := CommitFromSettings(settings); got != "0123456" {
		t.Fatalf("CommitFromSettings() = %q, want %q", got, "0123456")
	}
}

func TestCommitFromSettingsFallsBackWithoutRevision(t *testing.T) {
	settings := []debug.BuildSetting{{Key: "vcs.modified", Value: "true"}}
	if got := CommitFromSettings(settings); got != "unknown" {
		t.Fatalf("CommitFromSettings() = %q, want unknown", got)
	}
}

func TestBuildTimeFromSettingsUsesVCSTime(t *testing.T) {
	settings := []debug.BuildSetting{{Key: "vcs.time", Value: "2026-05-28T10:30:00Z"}}
	got, ok := BuildTimeFromSettings(settings)
	if !ok {
		t.Fatal("BuildTimeFromSettings() ok = false, want true")
	}
	want := time.Date(2026, 5, 28, 10, 30, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("BuildTimeFromSettings() = %s, want %s", got, want)
	}
}

func TestBuildTimeFromSettingsFallsBackForMissingOrInvalidTime(t *testing.T) {
	for _, settings := range [][]debug.BuildSetting{
		{{Key: "vcs.modified", Value: "true"}},
		{{Key: "vcs.time", Value: "not-a-time"}},
		{{Key: "vcs.time", Value: "   "}},
	} {
		if got, ok := BuildTimeFromSettings(settings); ok || !got.IsZero() {
			t.Fatalf("BuildTimeFromSettings(%v) = %s, %v; want zero, false", settings, got, ok)
		}
	}
}

func TestFormatBuildAge(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		buildTime time.Time
		want      string
	}{
		{name: "seconds", buildTime: now.Add(-30 * time.Second), want: "less than 1 minute old"},
		{name: "minutes", buildTime: now.Add(-42 * time.Minute), want: "42 minutes old"},
		{name: "singular hour", buildTime: now.Add(-time.Hour), want: "1 hour old"},
		{name: "days", buildTime: now.Add(-3 * 24 * time.Hour), want: "3 days old"},
		{name: "weeks", buildTime: now.Add(-14 * 24 * time.Hour), want: "2 weeks old"},
		{name: "months", buildTime: now.Add(-90 * 24 * time.Hour), want: "3 months old"},
		{name: "years", buildTime: now.Add(-2 * 365 * 24 * time.Hour), want: "2 years old"},
		{name: "future", buildTime: now.Add(time.Minute), want: "unknown"},
		{name: "zero", buildTime: time.Time{}, want: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := FormatBuildAge(test.buildTime, now); got != test.want {
				t.Fatalf("FormatBuildAge() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBuildAgeFromSettingsFallsBackWithoutValidBuildTime(t *testing.T) {
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	settings := []debug.BuildSetting{{Key: "vcs.time", Value: "nope"}}
	if got := BuildAgeFromSettings(settings, now); got != "unknown" {
		t.Fatalf("BuildAgeFromSettings() = %q, want unknown", got)
	}
}
