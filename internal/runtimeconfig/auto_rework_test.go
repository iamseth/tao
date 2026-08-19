package runtimeconfig

import (
	"strings"
	"testing"
)

func TestParseAutoReworkEnvConsumerMatrix(t *testing.T) {
	tests := []struct {
		name           string
		enabledValue   string
		attemptsValue  string
		wantCLIEnabled bool
		wantAttempts   int
		wantError      string
	}{
		{name: "unset", wantCLIEnabled: true, wantAttempts: DefaultMaxReworkAttempts},
		{name: "valid", enabledValue: "false", attemptsValue: "7", wantAttempts: 7},
		{name: "whitespace padded", enabledValue: " true ", attemptsValue: " 7 ", wantCLIEnabled: true, wantAttempts: 7},
		{name: "negative", enabledValue: "true", attemptsValue: "-1", wantError: EnvMaxReworkAttempts + ": must be a non-negative integer"},
		{name: "non-numeric", enabledValue: "true", attemptsValue: "many", wantError: EnvMaxReworkAttempts + ": must be a non-negative integer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cliEnabled, cliAttempts, cliErr := ParseAutoReworkEnv(true, DefaultMaxReworkAttempts, tt.enabledValue, tt.attemptsValue)
			mergeEnabled, mergeAttempts, mergeErr := ParseAutoReworkEnv(false, DefaultMaxReworkAttempts, "", tt.attemptsValue)
			if tt.wantError != "" {
				if cliErr == nil || cliErr.Error() != tt.wantError {
					t.Fatalf("cli error = %v, want %q", cliErr, tt.wantError)
				}
				if mergeErr == nil || mergeErr.Error() != cliErr.Error() {
					t.Fatalf("merge error = %v, want cli error %q", mergeErr, cliErr)
				}
				return
			}
			if cliErr != nil || mergeErr != nil {
				t.Fatalf("cli error = %v, merge error = %v", cliErr, mergeErr)
			}
			if cliEnabled != tt.wantCLIEnabled || cliAttempts != tt.wantAttempts {
				t.Fatalf("cli defaults = (%t, %d), want (%t, %d)", cliEnabled, cliAttempts, tt.wantCLIEnabled, tt.wantAttempts)
			}
			if mergeEnabled || mergeAttempts != tt.wantAttempts {
				t.Fatalf("merge defaults = (%t, %d), want (false, %d)", mergeEnabled, mergeAttempts, tt.wantAttempts)
			}
		})
	}
}

func TestParseAutoReworkEnvUsesStandardBoolParsing(t *testing.T) {
	for _, value := range []string{"true", " 1 ", "FALSE", "t"} {
		if _, _, err := ParseAutoReworkEnv(false, DefaultMaxReworkAttempts, value, ""); err != nil {
			t.Fatalf("value %q: %v", value, err)
		}
	}
	_, _, err := ParseAutoReworkEnv(false, DefaultMaxReworkAttempts, "yes", "")
	if err == nil || !strings.Contains(err.Error(), EnvAutoRework) {
		t.Fatalf("expected named boolean error, got %v", err)
	}
}

func TestResolveAutoReworkPolicy(t *testing.T) {
	policy, err := ResolveAutoReworkPolicy(true, DefaultMaxReworkAttempts, true)
	if err != nil || !policy.Enabled || policy.MaxAttempts != 5 {
		t.Fatalf("policy = %+v, err = %v", policy, err)
	}
	policy, err = ResolveAutoReworkPolicy(true, 0, false)
	if err != nil || policy.Enabled || policy.MaxAttempts != 0 {
		t.Fatalf("zero policy = %+v, err = %v", policy, err)
	}
	if _, err := ResolveAutoReworkPolicy(true, 1, false); err == nil {
		t.Fatal("expected automatic review requirement")
	}
	if _, err := ResolveAutoReworkPolicy(false, -1, true); err == nil {
		t.Fatal("expected negative attempts error")
	}
	if err := ValidateAutoReworkPolicy(AutoReworkPolicy{Enabled: true, MaxAttempts: 3}, false); err == nil {
		t.Fatal("expected persisted policy to require review on its run request")
	}
}
