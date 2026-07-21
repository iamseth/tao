package identity

import (
	"errors"
	"os/user"
	"testing"
)

func TestApproverPrefersUserName(t *testing.T) {
	got := approver(
		func() (*user.User, error) { return &user.User{Name: "Ada Lovelace", Username: "ada"}, nil },
		func(string) string { return "env-value" },
	)
	if got != "Ada Lovelace" {
		t.Fatalf("expected display name, got %q", got)
	}
}

func TestApproverFallsBackToUsername(t *testing.T) {
	got := approver(
		func() (*user.User, error) { return &user.User{Name: "  ", Username: "ada"}, nil },
		func(string) string { return "env-value" },
	)
	if got != "ada" {
		t.Fatalf("expected login name, got %q", got)
	}
}

func TestApproverFallsBackToEnvInPrecedenceOrder(t *testing.T) {
	env := map[string]string{"USER": "shell-user", "TAO_APPROVED_BY": "approver-env"}
	got := approver(
		func() (*user.User, error) { return nil, errors.New("no user") },
		func(key string) string { return env[key] },
	)
	if got != "approver-env" {
		t.Fatalf("expected TAO_APPROVED_BY to win, got %q", got)
	}
}

func TestApproverReturnsEmptyWhenNothingResolves(t *testing.T) {
	got := approver(
		func() (*user.User, error) { return nil, errors.New("no user") },
		func(string) string { return "" },
	)
	if got != "" {
		t.Fatalf("expected empty approver, got %q", got)
	}
}
