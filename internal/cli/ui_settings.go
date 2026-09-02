package cli

import (
	"context"
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/runtimeconfig"
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
	rows, err := runtimeconfig.RuntimeEnvStatus()
	if err != nil {
		snapshot.CollectionError = "runtime defaults: " + err.Error()
	} else {
		for _, row := range rows {
			snapshot.RuntimeDefaults = append(snapshot.RuntimeDefaults, tui.SettingsRuntimeDefault{Name: row.Name, Value: row.Value, Source: row.Source, Warning: row.Warning})
			if row.Name == runtimeconfig.EnvPullRequest {
				snapshot.InheritedPullRequest, _ = strconv.ParseBool(row.Value)
			}
		}
	}
	repositories, err := s.registry.ListRepos()
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
		if value, ok := repository.PullRequestDefault(); ok {
			setting.PullRequest = &value
		}
		snapshot.Repositories = append(snapshot.Repositories, setting)
	}
	return snapshot, nil
}

func (s uiSettingsService) SetPullRequestDefault(ctx context.Context, repositoryID string, value *bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	repository, err := s.registry.ReadRepo(repositoryID)
	if err != nil {
		return err
	}
	writer, ok := s.registry.(interface{ WriteRepo(taodata.Repo) error })
	if !ok {
		return errors.New("repository settings are read-only")
	}
	repository = repository.WithPullRequestDefault(value)
	repository.UpdatedAt = s.app.now().UTC().Format("2006-01-02T15:04:05Z07:00")
	return writer.WriteRepo(repository)
}

func newUISettingsService(a App) tui.SettingsService {
	return uiSettingsService{app: a, registry: a.registry()}
}
