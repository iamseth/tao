package cli

import (
	"context"
	"errors"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/settings"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/tui"
)

type uiSettingsService struct {
	app         App
	registry    NoteRegistry
	userHomeDir func() (string, error)
}

func (s uiSettingsService) Collect(ctx context.Context) (tui.SettingsSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return tui.SettingsSnapshot{}, err
	}
	snapshot := tui.SettingsSnapshot{CollectedAt: s.app.now()}
	userHomeDir := s.userHomeDir
	if userHomeDir == nil {
		userHomeDir = os.UserHomeDir
	}
	if home, err := userHomeDir(); err == nil {
		snapshot.DisplayHome = home
	}
	service := s.app.settingsService()
	global, globalErr := service.Read(ctx, settings.Target{Global: true})
	if global.LoadError != nil {
		snapshot.CollectionError = globalErr.Error()
	}
	env := global.Effective
	snapshot.Values = uiSettingsValues(env, false, false)
	for _, row := range s.app.settingsPaths() {
		snapshot.Paths = append(snapshot.Paths, tui.SettingsRuntimeDefault{Name: row.Name, Value: row.Value, Source: row.Source, Warning: row.Warning})
	}
	if baseline := env.Defaults().PullRequest; baseline != nil {
		snapshot.InheritedPullRequest = *baseline
	}
	snapshot.InheritedPullRequestInvalid = env.Require(runtimeconfig.EnvPullRequest) != nil
	for _, row := range env.Status() {
		snapshot.RuntimeDefaults = append(snapshot.RuntimeDefaults, tui.SettingsRuntimeDefault{Name: row.Name, Value: row.Value, Source: row.Source, Warning: row.Warning})
	}
	repositories, findings, err := s.repositories()
	if err != nil {
		return snapshot, err
	}
	sort.SliceStable(repositories, func(i, j int) bool {
		left := strings.ToLower(repositories[i].Name) + "\x00" + repositories[i].ID
		right := strings.ToLower(repositories[j].Name) + "\x00" + repositories[j].ID
		return left < right
	})
	for _, repository := range repositories {
		health := taodata.RepoHealthChecker{}.Check(ctx, repository)
		if s.app.RepoHealthCheck != nil {
			health = s.app.RepoHealthCheck(ctx, repository)
		}
		setting := tui.RepositorySetting{
			ID: repository.ID, Name: repository.Name, Root: repository.Root,
			Health: health.Status, Finding: health.Message,
		}
		view, _ := service.ReadWithGlobal(ctx, settings.Target{RepositoryID: repository.ID}, global)
		setting.Values = uiSettingsValues(view.Effective, true, s.writable() && view.LoadError == nil)
		setting.Finding = strings.Join(append([]string{setting.Finding, findings[repository.ID]}, view.Diagnostics...), "; ")
		if value, ok := repository.PullRequestDefault(); ok {
			setting.PullRequest = &value
		}
		snapshot.Repositories = append(snapshot.Repositories, setting)
	}
	return snapshot, nil
}

func (s uiSettingsService) SetPullRequestDefault(ctx context.Context, repositoryID string, value *bool) error {
	var text *string
	if value != nil {
		v := strconv.FormatBool(*value)
		text = &v
	}
	return s.SetSetting(ctx, repositoryID, "pull_request", text)
}

func (s uiSettingsService) writable() bool {
	_, ok := s.registry.(interface{ WriteRepo(taodata.Repo) error })
	return ok
}

func (s uiSettingsService) SetSetting(ctx context.Context, repositoryID, key string, value *string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.writable() {
		return errors.New("repository settings are read-only")
	}
	return s.app.settingsService().Update(ctx, settings.Target{RepositoryID: repositoryID}, map[string]*string{key: value})
}

func uiSettingsValues(env runtimeconfig.EnvSnapshot, repository, editable bool) []tui.SettingsValue {
	defs := make(map[string]runtimeconfig.SettingDefinition)
	for _, d := range runtimeconfig.SettingDefinitions() {
		defs[d.Key] = d
	}
	var values []tui.SettingsValue
	for _, row := range env.SettingsStatus() {
		d := defs[row.Key]
		stored := row.GlobalValue
		if repository {
			stored = row.RepositoryValue
		}
		values = append(values, tui.SettingsValue{Key: row.Key, Value: row.Value, Stored: stored, Source: row.Source, Warning: row.Warning, Kind: d.Kind, Choices: slices.Clone(d.Choices), Editable: editable && slices.Contains(d.Scopes, "repo")})
	}
	return values
}

func (s uiSettingsService) repositories() ([]taodata.Repo, map[string]string, error) {
	findings := make(map[string]string)
	if inventory, ok := s.registry.(interface {
		MetadataInventory() ([]taodata.RepoInventoryEntry, error)
	}); ok {
		entries, err := inventory.MetadataInventory()
		var repos []taodata.Repo
		for _, entry := range entries {
			repos = append(repos, entry.Repo)
			if entry.MetadataError != nil {
				findings[entry.Repo.ID] = entry.MetadataError.Error()
			}
		}
		return repos, findings, err
	}
	repos, err := s.registry.ListRepos()
	return repos, findings, err
}

func newUISettingsService(a App) tui.SettingsService {
	a.SettingsService = a.settingsService()
	return uiSettingsService{app: a, registry: a.registry()}
}
