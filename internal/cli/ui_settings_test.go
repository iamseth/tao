package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

func TestUISettingsServiceCollectsAndUpdatesRepositoryDefaults(t *testing.T) {
	for _, name := range runtimeconfig.RuntimeEnvKeys() {
		t.Setenv(name, "")
	}
	t.Setenv(runtimeconfig.EnvPullRequest, "true")
	registry := taodata.Registry{DataHome: t.TempDir()}
	explicitFalse := false
	for _, repository := range []taodata.Repo{
		{Schema: taodata.RepoSchema, ID: "repo-b", Name: "beta", Root: "/beta", UpdatedAt: "old"},
		{Schema: taodata.RepoSchema, ID: "repo-a", Name: "alpha", Root: "/alpha", UpdatedAt: "old", RunDefaults: &taodata.RepoRunDefaults{PullRequest: &explicitFalse}},
	} {
		if err := registry.WriteRepo(repository); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	app := App{
		Now: func() time.Time { return now },
		RepoHealthCheck: func(context.Context, taodata.Repo) taodata.RepoHealth {
			return taodata.RepoHealth{Status: taodata.RepoHealthOK, Message: "ok"}
		},
	}
	service := uiSettingsService{app: app, registry: registry, userHomeDir: func() (string, error) { return "/test/home", nil }}
	snapshot, err := service.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.InheritedPullRequest || snapshot.DisplayHome != "/test/home" || len(snapshot.Repositories) != 2 || snapshot.Repositories[0].ID != "repo-a" {
		t.Fatalf("settings snapshot baseline=%t home=%q repositories=%+v", snapshot.InheritedPullRequest, snapshot.DisplayHome, snapshot.Repositories)
	}
	if value := snapshot.Repositories[0].PullRequest; value == nil || *value {
		t.Fatalf("explicit repository setting = %v, want false", value)
	}
	if snapshot.Repositories[1].PullRequest != nil {
		t.Fatalf("inherited repository setting = %v, want nil", snapshot.Repositories[1].PullRequest)
	}

	if err := service.SetPullRequestDefault(context.Background(), "repo-a", nil); err != nil {
		t.Fatal(err)
	}
	stored, err := registry.ReadRepo("repo-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stored.PullRequestDefault(); ok || stored.UpdatedAt != now.Format(time.RFC3339) {
		t.Fatalf("stored unset repository = %+v", stored)
	}
}

func TestUISettingsServiceLeavesDisplayHomeEmptyWhenLookupFails(t *testing.T) {
	for _, name := range runtimeconfig.RuntimeEnvKeys() {
		t.Setenv(name, "")
	}
	service := uiSettingsService{
		app:         App{},
		registry:    taodata.Registry{DataHome: t.TempDir()},
		userHomeDir: func() (string, error) { return "", errors.New("home unavailable") },
	}
	snapshot, err := service.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.DisplayHome != "" {
		t.Fatalf("display home = %q, want empty fallback context", snapshot.DisplayHome)
	}
}
