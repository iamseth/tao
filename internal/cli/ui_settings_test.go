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
	clearTaoEnv(t)
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
		RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvPullRequest: "true"}),
		Now:        func() time.Time { return now },
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

func TestUISettingsReviewAgentProjection(t *testing.T) {
	for _, value := range []string{"", "pi", "claude", "invalid"} {
		service := uiSettingsService{app: App{RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvReviewAgent: value})}, registry: &fakeNoteRegistry{}}
		snapshot, err := service.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range snapshot.RuntimeDefaults {
			if row.Name != runtimeconfig.EnvReviewAgent {
				continue
			}
			found = true
			if value == "invalid" {
				if row.Source != "invalid" || row.Warning == "" {
					t.Fatalf("invalid row: %+v", row)
				}
			} else if row.Value != value {
				t.Fatalf("row: %+v, want %q", row, value)
			}
		}
		if !found {
			t.Fatal("missing review agent setting")
		}
	}
}

func TestUISettingsDiagnosticBaselineDoesNotChangeRepositorySettings(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv(runtimeconfig.EnvPullRequest, "true")
	for _, value := range []string{" YES ", "off", "invalid", ""} {
		t.Run(value, func(t *testing.T) {
			registry := taodata.Registry{DataHome: t.TempDir()}
			repository := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-a", Root: "/alpha", UpdatedAt: "old", RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(false)}}
			if err := registry.WriteRepo(repository); err != nil {
				t.Fatal(err)
			}
			app := App{RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvPullRequest: value, runtimeconfig.EnvUpdate: "invalid"}), RepoHealthCheck: func(context.Context, taodata.Repo) taodata.RepoHealth { return taodata.RepoHealth{Status: "ok"} }}
			service := uiSettingsService{app: app, registry: registry}
			for range 2 {
				snapshot, err := service.Collect(context.Background())
				if err != nil || snapshot.CollectionError != "" || snapshot.InheritedPullRequest != (value == " YES ") || snapshot.InheritedPullRequestInvalid != (value == "invalid") {
					t.Fatalf("incorrect typed diagnostic baseline: %+v, %v", snapshot, err)
				}
			}
			stored, err := registry.ReadRepo(repository.ID)
			if err != nil || stored.UpdatedAt != "old" {
				t.Fatalf("diagnostics mutated stored repository: %+v, %v", stored, err)
			}
			if pr, set := stored.PullRequestDefault(); !set || pr {
				t.Fatalf("diagnostics changed explicit false: %+v", stored)
			}
		})
	}
}

func TestUISettingsServiceLeavesDisplayHomeEmptyWhenLookupFails(t *testing.T) {
	clearTaoEnv(t)
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

func TestUISettingsRepositoryReworkProjection(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "explicit zero"}[explicit], func(t *testing.T) {
			repo := taodata.Repo{ID: "repo-a"}
			if explicit {
				repo = repo.WithReworkDefaults(new(0), new(1))
			}
			registry := &fakeNoteRegistry{current: repo, repos: []taodata.Repo{repo}}
			service := uiSettingsService{app: App{RuntimeEnv: snapshotWith(map[string]string{runtimeconfig.EnvMaxReworkAttempts: "8"})}, registry: registry}
			snapshot, err := service.Collect(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			wants := map[string]string{runtimeconfig.EnvMaxReworkAttempts: "8", runtimeconfig.EnvReworkEscalationFromAttempt: "4"}
			if explicit {
				wants[runtimeconfig.EnvMaxReworkAttempts] = "0"
				wants[runtimeconfig.EnvReworkEscalationFromAttempt] = "1"
			}
			for name, want := range wants {
				found := false
				for _, row := range snapshot.RuntimeDefaults {
					if row.Name == name {
						found = true
						source := "default"
						if name == runtimeconfig.EnvMaxReworkAttempts {
							source = "env"
						}
						if explicit {
							source = "repository"
						}
						if row.Value != want || row.Source != source {
							t.Fatalf("row: %+v", row)
						}
					}
				}
				if !found {
					t.Fatal("missing", name)
				}
			}
		})
	}
}
