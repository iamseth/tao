package runtimeconfig

import "testing"

// Environment syntax, presence and named consumption errors are covered by
// env_snapshot_test.go; this test owns the cross-field policy only.
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
