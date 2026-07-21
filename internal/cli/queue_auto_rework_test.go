package cli

import (
	"io"
	"testing"
)

func TestQueueStartAutoReworkEnvAndArgs(t *testing.T) {
	t.Setenv(envAutoRework, "true")
	t.Setenv(envMaxReworkAttempts, "3")
	app := App{Out: io.Discard, Err: io.Discard}

	_, policy, err := parseQueueStartArgs(app, nil)
	if err != nil || !policy.Enabled || policy.MaxAttempts != 3 {
		t.Fatalf("environment policy = %+v, err = %v", policy, err)
	}
	_, policy, err = parseQueueStartArgs(app, []string{"--auto-rework=false", "--max-rework-attempts=2"})
	if err != nil || policy.Enabled || policy.MaxAttempts != 2 {
		t.Fatalf("flag policy = %+v, err = %v", policy, err)
	}
}

func TestQueueStartAutoReworkValidation(t *testing.T) {
	t.Setenv(envAutoRework, "")
	t.Setenv(envMaxReworkAttempts, "")
	app := App{Out: io.Discard, Err: io.Discard}
	if _, _, err := parseQueueStartArgs(app, []string{"--max-rework-attempts=-1"}); err == nil {
		t.Fatal("expected negative attempts error")
	}
	_, policy, err := parseQueueStartArgs(app, []string{"--auto-rework", "--max-rework-attempts=0"})
	if err != nil || policy.Enabled {
		t.Fatalf("zero attempts policy = %+v, err = %v", policy, err)
	}
}
