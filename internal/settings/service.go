// Package settings owns validated, lossless scoped settings persistence. The
// captured environment is supplied at construction; reads never consult it again.
package settings

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
	"github.com/iamseth/tao/internal/configtypes"
	"github.com/iamseth/tao/internal/filelock"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

type Target struct {
	Global       bool
	RepositoryID string
}

type View struct {
	Stored      configtypes.SettingsValues
	Effective   runtimeconfig.EnvSnapshot
	Diagnostics []string
	// LoadError reports structural/storage failures, distinct from field admission.
	LoadError error
}

// Service contains no process environment readers. write is an atomic-write
// seam for fault injection; production construction always uses atomicfile.
type Service struct {
	dataHome    string
	environment runtimeconfig.EnvSnapshot
	write       func(string, []byte, atomicfile.Options) error
}

func NewService(dataHome string, environment runtimeconfig.EnvSnapshot) *Service {
	// Freeze relative roots against the invocation's working directory, including
	// roots that do not exist yet. Keep an empty or unresolvable root invalid.
	if dataHome != "" && !filepath.IsAbs(dataHome) {
		if cwd, err := os.Getwd(); err == nil {
			if canonical, err := filepath.EvalSymlinks(cwd); err == nil {
				cwd = canonical
			}
			dataHome = filepath.Join(cwd, dataHome)
		}
	}
	if canonical, err := filepath.EvalSymlinks(dataHome); err == nil {
		dataHome = canonical
	}
	return &Service{dataHome: dataHome, environment: environment, write: atomicfile.Write}
}

// RepositoryRegistered reports whether a repository document exists for the
// identifier, readable or not. Catalog listings omit unreadable documents, so
// callers that must fail closed on a corrupt registration check this first.
func (s *Service) RepositoryRegistered(id string) bool {
	if strings.TrimSpace(id) == "" {
		return false
	}
	path := filepath.Join(s.dataHome, "repos", id, "repo.json")
	if err := safePath(path); err != nil {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// HasRegistrations reports whether any repository document exists under the
// data home, readable or not, so callers can decide whether discovering a
// linked worktree's control checkout is worth a git lookup.
func (s *Service) HasRegistrations() bool {
	entries, err := os.ReadDir(filepath.Join(s.dataHome, "repos"))
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(s.dataHome, "repos", entry.Name(), "repo.json")); err == nil {
				return true
			}
		}
	}
	return false
}

func (s *Service) location(target Target) (string, string, error) {
	if s.dataHome == "" || !filepath.IsAbs(s.dataHome) {
		return "", "", fmt.Errorf("absolute data home is required")
	}
	if target.Global {
		if target.RepositoryID != "" {
			return "", "", fmt.Errorf("choose global or repository scope, not both")
		}
		return filepath.Join(s.dataHome, "config.json"), "global", nil
	}
	id := target.RepositoryID
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, "/\\") || strings.TrimSpace(id) != id {
		return "", "", fmt.Errorf("invalid repository ID %q", id)
	}
	return filepath.Join(s.dataHome, "repos", id, "repo.json"), "repo", nil
}

func recovery(path string, err error) error {
	return fmt.Errorf("%s: %w; repair the file manually or restore a backup; it was not overwritten", path, err)
}

// Read returns the usable stored values and effective snapshot even when field
// diagnostics are errors. Callers must not discard the error for execution.
func (s *Service) Read(ctx context.Context, target Target) (View, error) {
	return s.read(ctx, target, nil)
}

// ReadWithGlobal reads a repository against a previously captured global view.
// It never reopens the global file or recaptures environment state.
func (s *Service) ReadWithGlobal(ctx context.Context, target Target, global View) (View, error) {
	return s.read(ctx, target, &global)
}

func (s *Service) read(ctx context.Context, target Target, captured *View) (View, error) {
	view := View{Effective: runtimeconfig.ResolveSettings(nil, nil, s.environment)}
	fail := func(err error) (View, error) {
		view.LoadError = errors.Join(view.LoadError, err)
		view.Diagnostics = append(view.Diagnostics, err.Error())
		return view, err
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	path, scope, err := s.location(target)
	if err != nil {
		return fail(err)
	}
	globalPath := filepath.Join(s.dataHome, "config.json")
	var global document
	var globalErr error
	if captured == nil {
		global, globalErr = loadDocument(globalPath, "global", "")
	} else {
		global.values, globalErr = captured.Stored.Clone(), captured.LoadError
	}
	var repo document
	var repoErr error
	if scope == "repo" {
		repo, repoErr = loadDocument(path, scope, target.RepositoryID)
		view.Stored = repo.values.Clone()
	} else {
		view.Stored = global.values.Clone()
	}
	var errs []error
	if globalErr != nil {
		errs = append(errs, recovery(globalPath, globalErr))
	}
	if repoErr != nil {
		errs = append(errs, recovery(path, repoErr))
	}
	view.LoadError = errors.Join(errs...)
	view.Effective = runtimeconfig.ResolveSettings(global.values, repo.values, s.environment)
	for _, status := range view.Effective.SettingsStatus() {
		if status.Warning != "" {
			errs = append(errs, fmt.Errorf("%s: %s", status.Key, status.Warning))
		}
	}
	for _, err := range errs {
		view.Diagnostics = append(view.Diagnostics, err.Error())
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	return view, errors.Join(errs...)
}

// Update validates only the changed fields, then applies the complete change set
// to a fresh locked document. Cross-layer budget admission belongs to consumers.
func (s *Service) Update(ctx context.Context, target Target, changes map[string]*string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, scope, err := s.location(target)
	if err != nil {
		return err
	}
	parsed := configtypes.SettingsValues{}
	keys := make([]string, 0, len(changes))
	for key := range changes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := changes[key]
		if value == nil {
			continue
		}
		if strings.TrimSpace(*value) == "null" && !strings.HasSuffix(key, ".stop") {
			return fmt.Errorf("%s: null is only valid for STOP caps", key)
		}
		raw, err := runtimeconfig.ParseSetting(key, *value)
		if err != nil {
			return err
		}
		if err := runtimeconfig.ValidateSettings(configtypes.SettingsValues{key: raw}, scope); err != nil {
			return err
		}
		parsed[key] = raw
	}
	// Repository updates may never allocate an unregistered repository.
	if scope == "repo" {
		if _, err := loadDocument(path, scope, target.RepositoryID); err != nil {
			return recovery(path, err)
		}
	}
	if err := safePath(path); err != nil {
		return recovery(path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	lockPath := path + ".lock"
	if err := safePath(lockPath); err != nil {
		return err
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) //nolint:gosec // validated data-home path
	if err != nil {
		return err
	}
	defer func() { _ = lock.Close() }()
	if err := filelock.LockPoll(ctx, lock, 10*time.Millisecond); err != nil {
		return err
	}
	defer func() { _ = filelock.Unlock(lock) }()
	d, err := loadDocument(path, scope, target.RepositoryID)
	if err != nil {
		return recovery(path, err)
	}
	for _, key := range keys {
		if changes[key] == nil {
			// Unknown saved fields can be removed for forward-version repair, but
			// misspelled absent keys are rejected rather than reported as successful.
			if _, exists := d.values[key]; !exists {
				allowed := false
				for _, def := range runtimeconfig.SettingDefinitions() {
					if def.Key == key && slices.Contains(def.Scopes, scope) {
						allowed = true
						break
					}
				}
				if !allowed {
					return fmt.Errorf("unknown or disallowed %s setting %q", scope, key)
				}
			}
			delete(d.values, key)
		} else {
			d.values[key] = parsed[key]
		}
	}
	data, err := d.encode(scope)
	if err != nil {
		return recovery(path, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return s.write(path, append(data, '\n'), atomicfile.Options{Perm: 0600})
}
