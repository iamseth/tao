package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
)

// Mode controls automatic update behavior.
type Mode string

const (
	// ModeWarn checks for updates and reports them without installing.
	ModeWarn Mode = "warn"
	// ModeAuto permits automatic installation of available updates.
	ModeAuto Mode = "auto"
	// ModeOff disables automatic update checks.
	ModeOff Mode = "off"
)

// ParseMode parses an update mode. An empty value selects ModeWarn.
func ParseMode(value string) (Mode, error) {
	if value == "" {
		return ModeWarn, nil
	}

	switch Mode(value) {
	case ModeWarn, ModeAuto, ModeOff:
		return Mode(value), nil
	default:
		return "", fmt.Errorf("invalid update mode %q: want warn, auto, or off", value)
	}
}

// VersionComparison describes the relationship between a running Tao version
// and the latest stable release.
type VersionComparison string

const (
	// VersionDevelopment means the running build has the development version.
	VersionDevelopment VersionComparison = "development"
	// VersionCurrent means the running build matches the latest release.
	VersionCurrent VersionComparison = "current"
	// VersionUpdateAvailable means the latest release is newer.
	VersionUpdateAvailable VersionComparison = "update_available"
	// VersionAhead means the running build is newer and must not be downgraded.
	VersionAhead VersionComparison = "ahead"
)

// CompareVersions compares a running version with a latest release tag. Only
// stable vMAJOR.MINOR.PATCH versions are accepted. The development version is
// reported explicitly rather than treated as malformed.
func CompareVersions(current, latest string) (VersionComparison, error) {
	if current == "dev" {
		return VersionDevelopment, nil
	}

	currentVersion, err := parseStableVersion(current)
	if err != nil {
		return "", fmt.Errorf("invalid running version: %w", err)
	}
	latestVersion, err := parseStableVersion(latest)
	if err != nil {
		return "", fmt.Errorf("invalid latest version: %w", err)
	}

	comparison := currentVersion.compare(latestVersion)
	switch {
	case comparison < 0:
		return VersionUpdateAvailable, nil
	case comparison > 0:
		return VersionAhead, nil
	default:
		return VersionCurrent, nil
	}
}

type stableVersion struct {
	major uint64
	minor uint64
	patch uint64
}

func parseStableVersion(value string) (stableVersion, error) {
	if value == "" || !strings.HasPrefix(value, "v") {
		return stableVersion{}, fmt.Errorf("version %q must match vMAJOR.MINOR.PATCH", value)
	}

	parts := strings.Split(value[1:], ".")
	if len(parts) != 3 {
		return stableVersion{}, fmt.Errorf("version %q must match vMAJOR.MINOR.PATCH", value)
	}

	values := make([]uint64, len(parts))
	for index, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return stableVersion{}, fmt.Errorf("version %q must match vMAJOR.MINOR.PATCH", value)
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return stableVersion{}, fmt.Errorf("version %q must match vMAJOR.MINOR.PATCH", value)
			}
		}
		parsed, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return stableVersion{}, fmt.Errorf("version %q contains an out-of-range component", value)
		}
		values[index] = parsed
	}

	return stableVersion{major: values[0], minor: values[1], patch: values[2]}, nil
}

func (version stableVersion) compare(other stableVersion) int {
	for _, pair := range [][2]uint64{
		{version.major, other.major},
		{version.minor, other.minor},
		{version.patch, other.patch},
	} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}
