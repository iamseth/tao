package build

import (
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

const unknownCommit = "unknown"
const unknownBuildAge = "unknown"
const devVersion = "dev"

// version holds the semantic version of the build. Release builds override it
// via -ldflags "-X github.com/iamseth/tao/internal/build.version=v1.2.3". Local
// source builds leave it empty and fall back to a development placeholder.
var version string

// Version returns the semantic version embedded in the running binary, falling
// back to a development placeholder for local builds without an injected value.
func Version() string {
	return NormalizeVersion(version)
}

// NormalizeVersion trims an injected version string and substitutes the
// development placeholder when no usable version is present.
func NormalizeVersion(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return devVersion
	}
	return value
}

// Commit returns the short Git commit embedded in the running binary.
func Commit() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownCommit
	}
	return CommitFromSettings(info.Settings)
}

// BuildAge returns a human-readable age for the running binary.
func BuildAge() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownBuildAge
	}
	return BuildAgeFromSettings(info.Settings, time.Now())
}

func CommitFromSettings(settings []debug.BuildSetting) string {
	for _, setting := range settings {
		if setting.Key == "vcs.revision" {
			return ShortCommit(setting.Value)
		}
	}
	return unknownCommit
}

func BuildAgeFromSettings(settings []debug.BuildSetting, now time.Time) string {
	buildTime, ok := BuildTimeFromSettings(settings)
	if !ok {
		return unknownBuildAge
	}
	return FormatBuildAge(buildTime, now)
}

func BuildTimeFromSettings(settings []debug.BuildSetting) (time.Time, bool) {
	for _, setting := range settings {
		if setting.Key != "vcs.time" {
			continue
		}
		buildTime, err := time.Parse(time.RFC3339, strings.TrimSpace(setting.Value))
		if err != nil {
			return time.Time{}, false
		}
		return buildTime, true
	}
	return time.Time{}, false
}

func FormatBuildAge(buildTime, now time.Time) string {
	if buildTime.IsZero() || now.Before(buildTime) {
		return unknownBuildAge
	}

	age := now.Sub(buildTime)
	minute := time.Minute
	hour := time.Hour
	day := 24 * hour
	week := 7 * day
	month := 30 * day
	year := 365 * day

	switch {
	case age < minute:
		return "less than 1 minute old"
	case age < hour:
		return plural(int(age/minute), "minute") + " old"
	case age < day:
		return plural(int(age/hour), "hour") + " old"
	case age < week:
		return plural(int(age/day), "day") + " old"
	case age < month:
		return plural(int(age/week), "week") + " old"
	case age < year:
		return plural(int(age/month), "month") + " old"
	default:
		return plural(int(age/year), "year") + " old"
	}
}

func ShortCommit(revision string) string {
	revision = strings.TrimSpace(revision)
	if len(revision) < 7 {
		return unknownCommit
	}
	return revision[:7]
}

func plural(count int, unit string) string {
	if count == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(count) + " " + unit + "s"
}
