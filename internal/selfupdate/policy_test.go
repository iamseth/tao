package selfupdate

import (
	"strings"
	"testing"
)

func TestParseMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  Mode
	}{
		{name: "empty defaults to warn", want: ModeWarn},
		{name: "warn", value: "warn", want: ModeWarn},
		{name: "auto", value: "auto", want: ModeAuto},
		{name: "off", value: "off", want: ModeOff},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseMode(test.value)
			if err != nil {
				t.Fatalf("ParseMode(%q) error = %v", test.value, err)
			}
			if got != test.want {
				t.Errorf("ParseMode(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestParseModeRejectsInvalidValue(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"sometimes", "WARN", " warn "} {
		_, err := ParseMode(value)
		if err == nil {
			t.Errorf("ParseMode(%q) error = nil, want invalid mode error", value)
			continue
		}
		for _, text := range []string{value, "warn", "auto", "off"} {
			if !strings.Contains(err.Error(), text) {
				t.Errorf("ParseMode(%q) error = %q, want it to contain %q", value, err, text)
			}
		}
	}
}

func TestCompareVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current string
		latest  string
		want    VersionComparison
	}{
		{name: "development build", current: "dev", latest: "v1.2.3", want: VersionDevelopment},
		{name: "equal", current: "v1.2.3", latest: "v1.2.3", want: VersionCurrent},
		{name: "new major", current: "v1.9.9", latest: "v2.0.0", want: VersionUpdateAvailable},
		{name: "new minor", current: "v1.2.9", latest: "v1.3.0", want: VersionUpdateAvailable},
		{name: "new patch", current: "v1.2.3", latest: "v1.2.4", want: VersionUpdateAvailable},
		{name: "running version newer", current: "v3.0.0", latest: "v2.99.99", want: VersionAhead},
		{name: "numeric not lexical", current: "v1.9.0", latest: "v1.10.0", want: VersionUpdateAvailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := CompareVersions(test.current, test.latest)
			if err != nil {
				t.Fatalf("CompareVersions(%q, %q) error = %v", test.current, test.latest, err)
			}
			if got != test.want {
				t.Errorf("CompareVersions(%q, %q) = %q, want %q", test.current, test.latest, got, test.want)
			}
		})
	}
}

func TestCompareVersionsRejectsMalformedVersions(t *testing.T) {
	t.Parallel()

	malformed := []string{
		"", "1.2.3", "v1", "v1.2", "v1.2.3.4", "v01.2.3", "v1.02.3",
		"v1.2.03", "v1.2.x", "v1.2.3-rc.1", "v1.2.3+meta", " v1.2.3",
		"v18446744073709551616.0.0",
	}
	for _, value := range malformed {
		t.Run("current_"+value, func(t *testing.T) {
			t.Parallel()
			if _, err := CompareVersions(value, "v2.0.0"); err == nil {
				t.Errorf("CompareVersions(%q, valid) error = nil", value)
			}
		})
		t.Run("latest_"+value, func(t *testing.T) {
			t.Parallel()
			if _, err := CompareVersions("v1.0.0", value); err == nil {
				t.Errorf("CompareVersions(valid, %q) error = nil", value)
			}
		})
	}
}
