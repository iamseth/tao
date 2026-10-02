package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
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
	app.SettingsService = settings.NewService(registry.DataHome, *app.RuntimeEnv)
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
	if _, ok := stored.PullRequestDefault(); ok {
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
			app.SettingsService = settings.NewService(registry.DataHome, *app.RuntimeEnv)
			service := uiSettingsService{app: app, registry: registry}
			for range 2 {
				snapshot, err := service.Collect(context.Background())
				if err != nil || snapshot.InheritedPullRequest != (value == " YES ") || snapshot.InheritedPullRequestInvalid != (value == "invalid") {
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

func TestUISettingsInvalidFieldsRemainVisibleAndRefresh(t *testing.T) {
	ctx := context.Background()
	registry := taodata.Registry{DataHome: t.TempDir()}
	if err := registry.WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: "repo", Name: "broken", Root: "/missing"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry.DataHome, "repos", "repo", "repo.json"), []byte(`{"schema":"tao.repo.v1","id":"repo","name":"broken","root":"/missing","run_defaults":{"agent":"invalid","max_slices":3}}`), 0600); err != nil {
		t.Fatal(err)
	}
	service := uiSettingsService{registry: registry, app: App{SettingsService: settings.NewService(registry.DataHome, *snapshotWith(map[string]string{runtimeconfig.EnvAgent: "pi"}))}}
	snapshot, err := service.Collect(ctx)
	if err != nil || len(snapshot.Repositories) != 1 {
		t.Fatalf("inventory: %+v %v", snapshot, err)
	}
	repo := snapshot.Repositories[0]
	if repo.ID != "repo" || repo.Root != "/missing" || repo.Name != "broken" || repo.Finding == "" {
		t.Fatalf("lost identity/diagnostics: %+v", repo)
	}
	for _, row := range repo.Values {
		if row.Key == "agent" && (row.Stored != "invalid" || row.Warning == "" || row.Value != "pi") {
			t.Fatalf("invalid masked row: %+v", row)
		}
	}
	value := "claude"
	if err := service.SetSetting(ctx, "repo", "agent", &value); err != nil {
		t.Fatal(err)
	}
	t.Setenv(runtimeconfig.EnvAgent, "claude")
	snapshot, err = service.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range snapshot.Repositories[0].Values {
		if row.Key == "agent" && (row.Stored != "claude" || row.Warning != "" || row.Value != "pi") {
			t.Fatalf("refresh recaptured environment or lost save: %+v", row)
		}
	}
}

func TestUISettingsEditorUnsetDisabledAndInvalid(t *testing.T) {
	ctx := context.Background()
	registry := taodata.Registry{DataHome: t.TempDir()}
	if err := registry.WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: "repo", Name: "example", Root: "/missing"}); err != nil {
		t.Fatal(err)
	}
	service := uiSettingsService{registry: registry, app: App{SettingsService: settings.NewService(registry.DataHome, *snapshotWith(map[string]string{runtimeconfig.EnvModel: "frozen"}))}}
	for _, test := range []struct {
		key    string
		value  *string
		valid  bool
		stored string
	}{
		{"models.model", new("saved"), true, "saved"},
		{"models.model", new(""), false, "saved"},
		{"models.model", nil, true, ""},
		{"budget.slice.cost.stop", new("0"), true, "0"},
		{"budget.slice.cost.stop", new("null"), true, "null"},
		{"budget.slice.cost.stop", nil, true, ""},
	} {
		err := service.SetSetting(ctx, "repo", test.key, test.value)
		if (err == nil) != test.valid {
			t.Fatalf("%s: %v", test.key, err)
		}
		snapshot, err := service.Collect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range snapshot.Repositories[0].Values {
			if row.Key == test.key {
				if row.Stored != test.stored {
					t.Fatalf("stored %s=%q want %q", test.key, row.Stored, test.stored)
				}
				if test.key == "models.model" && row.Value != "frozen" {
					t.Fatal("environment changed")
				}
			}
		}
	}
}

func TestUISettingsSharedWriterAndMaskedProjection(t *testing.T) {
	ctx := context.Background()
	registry := taodata.Registry{DataHome: t.TempDir()}
	if err := registry.WriteRepo(taodata.Repo{Schema: taodata.RepoSchema, ID: "repo", Name: "example", Root: "/missing"}); err != nil {
		t.Fatal(err)
	}
	service := uiSettingsService{registry: registry, app: App{SettingsService: settings.NewService(registry.DataHome, *snapshotWith(map[string]string{runtimeconfig.EnvModel: "environment"}))}}
	model := "saved"
	if err := service.SetSetting(ctx, "repo", "models.model", &model); err != nil {
		t.Fatal(err)
	}
	if err := service.SetPullRequestDefault(ctx, "repo", new(true)); err != nil {
		t.Fatal(err)
	}
	model = "saved-again"
	if err := service.SetSetting(ctx, "repo", "models.model", &model); err != nil {
		t.Fatal(err)
	}
	snapshot, err := service.Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Paths) == 0 || len(snapshot.Values) != len(runtimeconfig.SettingDefinitions()) {
		t.Fatal("missing global settings or paths")
	}
	rows := snapshot.Repositories[0].Values
	if len(rows) != len(runtimeconfig.SettingDefinitions()) {
		t.Fatalf("missing schema rows: %d", len(rows))
	}
	for _, row := range rows {
		if row.Key == "models.model" && (row.Value != "environment" || row.Stored != "saved-again" || row.Source != "env" || !row.Editable) {
			t.Fatalf("masked model: %+v", row)
		}
		if row.Key == "pull_request" && row.Stored != "true" {
			t.Fatalf("PR lost: %+v", row)
		}
		if row.Key == "theme" && row.Editable {
			t.Fatal("global theme editable")
		}
	}
}

// Repository values are projected under the repository, never onto the global
// rows, and an environment export masks a saved repository value.
func TestUISettingsRepositoryReworkProjection(t *testing.T) {
	ctx := context.Background()
	registry := taodata.Registry{DataHome: t.TempDir()}
	repo := (taodata.Repo{Schema: taodata.RepoSchema, ID: "repo", Name: "example", Root: "/missing"}).WithReworkDefaults(new(0), new(1))
	if err := registry.WriteRepo(repo); err != nil {
		t.Fatal(err)
	}
	service := uiSettingsService{registry: registry, app: App{SettingsService: settings.NewService(registry.DataHome, *snapshotWith(map[string]string{runtimeconfig.EnvMaxReworkAttempts: "8"}))}}
	snapshot, err := service.Collect(ctx)
	if err != nil || len(snapshot.Repositories) != 1 {
		t.Fatalf("inventory: %+v %v", snapshot, err)
	}
	wants := map[string][3]string{
		"max_rework_attempts":            {"8", "env", "0"},
		"rework_escalation_from_attempt": {"1", "repository", "1"},
	}
	seen := 0
	for _, row := range snapshot.Repositories[0].Values {
		want, ok := wants[row.Key]
		if !ok {
			continue
		}
		seen++
		if row.Value != want[0] || row.Source != want[1] || row.Stored != want[2] {
			t.Fatalf("%s: %+v", row.Key, row)
		}
	}
	if seen != len(wants) {
		t.Fatalf("missing numeric rework rows: %d", seen)
	}
	for _, row := range snapshot.RuntimeDefaults {
		if row.Source == "repository" {
			t.Fatalf("global row carries a repository value: %+v", row)
		}
	}
}
