package configtypes

import "testing"

func TestNormalizeRecordedExecutionMode(t *testing.T) {
	for _, tt := range []struct {
		value   string
		want    ExecutionMode
		invalid bool
	}{
		{"", "", false},
		{"isolated", ExecutionModeIsolated, false},
		{"worktree", ExecutionModeIsolated, false},
		{"current", ExecutionModeCurrent, false},
		{"sandbox", "", true},
		{" isolated ", "", true},
		{"Current", "", true},
	} {
		t.Run(tt.value, func(t *testing.T) {
			got, err := NormalizeRecordedExecutionMode(tt.value)
			if (err != nil) != tt.invalid || got != tt.want {
				t.Fatalf("NormalizeRecordedExecutionMode(%q) = %q, %v; want %q, invalid=%v", tt.value, got, err, tt.want, tt.invalid)
			}
		})
	}
}

func TestExecutionModeString(t *testing.T) {
	for _, value := range []string{"", "isolated", "current", "unknown"} {
		if got := ExecutionMode(value).String(); got != value {
			t.Fatalf("String() = %q, want %q", got, value)
		}
	}
}
