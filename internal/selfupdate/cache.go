package selfupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
)

const (
	updateCacheSchema          = "tao.self-update-cache.v1"
	updateCacheFilename        = "self-update.json"
	maxUpdateCacheBytes        = 64 << 10
	successfulCheckFreshness   = 24 * time.Hour
	failedCheckFreshness       = time.Hour
	automaticInstallRetryDelay = time.Hour
	updateCacheLockFilename    = "self-update.lock"
)

// Cache persists bounded self-update check state outside repository and plan
// data. DataHome, Now, GOOS, and GOARCH are injectable for hermetic tests;
// empty runtime values use production defaults.
type Cache struct {
	DataHome string
	Now      func() time.Time
	GOOS     string
	GOARCH   string

	acquireLock func(context.Context, string) (func() error, error)
	writeFile   func(string, []byte, atomicfile.Options) error
}

// CacheState is an opaque, validated snapshot of self-update check state.
// Callers should use Cache policy methods rather than timestamps as authority
// to notify or automatically install a release.
type CacheState struct {
	latestDiscovery              Release
	successfulCheckAt            time.Time
	failedCheckAt                time.Time
	noticeDeliveredFor           time.Time
	automaticInstallFailedAt     time.Time
	automaticInstallCompletedFor time.Time
}

// PersistenceError identifies a cache filesystem failure. Interactive update
// commands may return it, while best-effort startup checks can safely ignore it.
type PersistenceError struct {
	Operation string
	Path      string
	Err       error
}

func (err *PersistenceError) Error() string {
	return fmt.Sprintf("%s self-update cache %q: %v", err.Operation, err.Path, err.Err)
}

func (err *PersistenceError) Unwrap() error {
	return err.Err
}

// IsPersistenceError reports whether err is a cache filesystem failure.
func IsPersistenceError(err error) bool {
	var persistenceErr *PersistenceError
	return errors.As(err, &persistenceErr)
}

// NewCache returns a cache rooted directly in Tao's data home.
func NewCache(dataHome string) *Cache {
	return &Cache{DataHome: dataHome, Now: time.Now}
}

// Path returns the cache file location under Tao's data home.
func (cache Cache) Path() string {
	return filepath.Join(cache.DataHome, updateCacheFilename)
}

func (cache Cache) lock(ctx context.Context) (func() error, error) {
	path := filepath.Join(cache.DataHome, updateCacheLockFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, cache.persistenceError("create directory for", path, err)
	}
	if cache.acquireLock != nil {
		return cache.acquireLock(ctx, path)
	}
	unlock, err := acquireFileLock(ctx, path)
	if err != nil {
		return nil, cache.persistenceError("lock", path, err)
	}
	return unlock, nil
}

// Load reads a bounded cache snapshot. Missing, malformed, unknown-schema, and
// semantically partial content is treated as an empty cache so a later check
// can safely replace it. Filesystem failures remain distinguishable.
func (cache Cache) Load() (CacheState, error) {
	path := cache.Path()
	file, err := os.Open(path) // #nosec G304 -- the caller supplies Tao's data home.
	if errors.Is(err, os.ErrNotExist) {
		return CacheState{}, nil
	}
	if err != nil {
		return CacheState{}, cache.persistenceError("read", path, err)
	}
	defer func() { _ = file.Close() }()

	content, err := io.ReadAll(io.LimitReader(file, maxUpdateCacheBytes+1))
	if err != nil {
		return CacheState{}, cache.persistenceError("read", path, err)
	}
	if len(content) > maxUpdateCacheBytes {
		return CacheState{}, nil
	}

	var document cacheDocument
	if err := json.Unmarshal(content, &document); err != nil || document.Schema != updateCacheSchema {
		return CacheState{}, nil
	}
	state, valid := cache.decode(document)
	if !valid {
		return CacheState{}, nil
	}
	return state, nil
}

// Save atomically writes a validated cache snapshot with private permissions.
func (cache Cache) Save(state CacheState) error {
	path := cache.Path()
	document, err := cache.encode(state)
	if err != nil {
		return err
	}
	content, err := json.Marshal(document)
	if err != nil {
		return fmt.Errorf("encode self-update cache: %w", err)
	}
	content = append(content, '\n')

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return cache.persistenceError("create directory for", path, err)
	}
	writeFile := cache.writeFile
	if writeFile == nil {
		writeFile = atomicfile.Write
	}
	if err := writeFile(path, content, atomicfile.Options{Perm: 0o600}); err != nil {
		return cache.persistenceError("write", path, err)
	}
	return nil
}

// ShouldCheck reports whether startup may perform another release check. Exact
// freshness boundaries are due, and future timestamps fail open rather than
// suppressing checks indefinitely.
func (cache Cache) ShouldCheck(state CacheState) bool {
	now := cache.now()
	if !state.failedCheckAt.IsZero() {
		return intervalElapsed(now, state.failedCheckAt, failedCheckFreshness)
	}
	if !state.successfulCheckAt.IsZero() {
		return intervalElapsed(now, state.successfulCheckAt, successfulCheckFreshness)
	}
	return true
}

// FreshRelease returns the validated latest discovery only while the latest
// check succeeded and remains inside the successful-check freshness window.
func (cache Cache) FreshRelease(state CacheState) (Release, bool) {
	if state.successfulCheckAt.IsZero() || !state.failedCheckAt.IsZero() {
		return Release{}, false
	}
	if !insideInterval(cache.now(), state.successfulCheckAt, successfulCheckFreshness) {
		return Release{}, false
	}
	return state.latestDiscovery, true
}

// ShouldNotice reports whether the fresh successful check has not yet produced
// a notice. A notice can therefore be delivered at most once per check.
func (cache Cache) ShouldNotice(state CacheState) bool {
	if _, ok := cache.FreshRelease(state); !ok {
		return false
	}
	return !state.noticeDeliveredFor.Equal(state.successfulCheckAt)
}

// ShouldRetryAutomaticInstall reports whether a fresh validated discovery is
// eligible for an automatic install attempt. Failed attempts suppress retries
// for one hour; future failure timestamps fail open.
func (cache Cache) ShouldRetryAutomaticInstall(state CacheState) bool {
	if _, ok := cache.FreshRelease(state); !ok {
		return false
	}
	if state.automaticInstallCompletedFor.Equal(state.successfulCheckAt) {
		return false
	}
	if state.automaticInstallFailedAt.IsZero() {
		return true
	}
	return intervalElapsed(cache.now(), state.automaticInstallFailedAt, automaticInstallRetryDelay)
}

// RecordSuccessfulCheck replaces the latest discovery and starts a new notice
// delivery cycle. The release must satisfy the same target-specific validation
// as live discovery.
func (cache Cache) RecordSuccessfulCheck(state *CacheState, release Release) error {
	if state == nil {
		return errors.New("record successful self-update check: nil cache state")
	}
	validated, err := cache.validateRelease(release)
	if err != nil {
		return fmt.Errorf("record successful self-update check: %w", err)
	}
	state.latestDiscovery = validated
	state.successfulCheckAt = cache.now()
	state.failedCheckAt = time.Time{}
	state.noticeDeliveredFor = time.Time{}
	state.automaticInstallFailedAt = time.Time{}
	state.automaticInstallCompletedFor = time.Time{}
	return nil
}

// RecordFailedCheck records a failed discovery attempt while retaining the last
// validated discovery only as diagnostic cache state.
func (cache Cache) RecordFailedCheck(state *CacheState) {
	if state == nil {
		return
	}
	state.failedCheckAt = cache.now()
}

// RecordNotice marks the current successful check as having delivered its
// notice. It returns false when no fresh notice is eligible.
func (cache Cache) RecordNotice(state *CacheState) bool {
	if state == nil || !cache.ShouldNotice(*state) {
		return false
	}
	state.noticeDeliveredFor = state.successfulCheckAt
	return true
}

// RecordAutomaticInstallFailure records the retry boundary for a failed
// automatic installation. It returns false without a fresh validated release.
func (cache Cache) RecordAutomaticInstallFailure(state *CacheState) bool {
	if state == nil {
		return false
	}
	if _, ok := cache.FreshRelease(*state); !ok {
		return false
	}
	state.automaticInstallFailedAt = cache.now()
	return true
}

// RecordAutomaticInstallSuccess suppresses further automatic installation of
// the release selected by the current successful check.
func (cache Cache) RecordAutomaticInstallSuccess(state *CacheState) bool {
	if state == nil {
		return false
	}
	if _, ok := cache.FreshRelease(*state); !ok {
		return false
	}
	state.automaticInstallFailedAt = time.Time{}
	state.automaticInstallCompletedFor = state.successfulCheckAt
	return true
}

func (cache Cache) now() time.Time {
	if cache.Now == nil {
		return time.Now().UTC()
	}
	return cache.Now().UTC()
}

func (cache Cache) target() (string, string) {
	goos := cache.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	goarch := cache.GOARCH
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	return goos, goarch
}

func (cache Cache) validateRelease(release Release) (Release, error) {
	goos, goarch := cache.target()
	metadata := releaseMetadata{
		TagName: release.Tag,
		Assets: []assetMetadata{
			{Name: release.Checksums.Name, BrowserDownloadURL: release.Checksums.URL},
			{Name: release.Archive.Name, BrowserDownloadURL: release.Archive.URL},
		},
	}
	validated, err := selectRelease(metadata, goos, goarch)
	if err != nil {
		return Release{}, fmt.Errorf("validate cached release: %w", err)
	}
	return validated, nil
}

func (cache Cache) encode(state CacheState) (cacheDocument, error) {
	if state.successfulCheckAt.IsZero() && state.failedCheckAt.IsZero() {
		if !state.noticeDeliveredFor.IsZero() || !state.automaticInstallFailedAt.IsZero() || !state.automaticInstallCompletedFor.IsZero() || state.latestDiscovery != (Release{}) {
			return cacheDocument{}, errors.New("encode self-update cache: partial state has no check timestamp")
		}
		return cacheDocument{Schema: updateCacheSchema}, nil
	}

	document := cacheDocument{Schema: updateCacheSchema}
	if !state.successfulCheckAt.IsZero() {
		validated, err := cache.validateRelease(state.latestDiscovery)
		if err != nil {
			return cacheDocument{}, fmt.Errorf("encode self-update cache: %w", err)
		}
		document.LatestDiscovery = cachedReleaseFrom(validated)
		document.SuccessfulCheckAt = timePointer(state.successfulCheckAt)
	}
	if !state.failedCheckAt.IsZero() {
		document.FailedCheckAt = timePointer(state.failedCheckAt)
	}
	if !state.noticeDeliveredFor.IsZero() {
		if state.successfulCheckAt.IsZero() || !state.noticeDeliveredFor.Equal(state.successfulCheckAt) {
			return cacheDocument{}, errors.New("encode self-update cache: notice does not identify the successful check")
		}
		document.NoticeDeliveredFor = timePointer(state.noticeDeliveredFor)
	}
	if !state.automaticInstallFailedAt.IsZero() {
		if state.successfulCheckAt.IsZero() {
			return cacheDocument{}, errors.New("encode self-update cache: automatic install failure has no validated discovery")
		}
		document.AutomaticInstallFailedAt = timePointer(state.automaticInstallFailedAt)
	}
	if !state.automaticInstallCompletedFor.IsZero() {
		if state.successfulCheckAt.IsZero() || !state.automaticInstallCompletedFor.Equal(state.successfulCheckAt) {
			return cacheDocument{}, errors.New("encode self-update cache: automatic install completion does not identify the successful check")
		}
		document.AutomaticInstallCompletedFor = timePointer(state.automaticInstallCompletedFor)
	}
	return document, nil
}

func (cache Cache) decode(document cacheDocument) (CacheState, bool) {
	state := CacheState{}
	if document.SuccessfulCheckAt != nil || document.LatestDiscovery != nil {
		if document.SuccessfulCheckAt == nil || document.SuccessfulCheckAt.IsZero() || document.LatestDiscovery == nil {
			return CacheState{}, false
		}
		validated, err := cache.validateRelease(document.LatestDiscovery.release())
		if err != nil {
			return CacheState{}, false
		}
		state.successfulCheckAt = document.SuccessfulCheckAt.UTC()
		state.latestDiscovery = validated
	}
	if document.FailedCheckAt != nil {
		if document.FailedCheckAt.IsZero() {
			return CacheState{}, false
		}
		state.failedCheckAt = document.FailedCheckAt.UTC()
	}
	if document.NoticeDeliveredFor != nil {
		if state.successfulCheckAt.IsZero() || !document.NoticeDeliveredFor.Equal(state.successfulCheckAt) {
			return CacheState{}, false
		}
		state.noticeDeliveredFor = document.NoticeDeliveredFor.UTC()
	}
	if document.AutomaticInstallFailedAt != nil {
		if document.AutomaticInstallFailedAt.IsZero() || state.successfulCheckAt.IsZero() {
			return CacheState{}, false
		}
		state.automaticInstallFailedAt = document.AutomaticInstallFailedAt.UTC()
	}
	if document.AutomaticInstallCompletedFor != nil {
		if state.successfulCheckAt.IsZero() || !document.AutomaticInstallCompletedFor.Equal(state.successfulCheckAt) {
			return CacheState{}, false
		}
		state.automaticInstallCompletedFor = document.AutomaticInstallCompletedFor.UTC()
	}
	return state, true
}

func (cache Cache) persistenceError(operation, path string, err error) error {
	return &PersistenceError{Operation: operation, Path: path, Err: err}
}

func intervalElapsed(now, recorded time.Time, interval time.Duration) bool {
	return now.Before(recorded) || !now.Before(recorded.Add(interval))
}

func insideInterval(now, recorded time.Time, interval time.Duration) bool {
	return !now.Before(recorded) && now.Before(recorded.Add(interval))
}

type cacheDocument struct {
	Schema                       string         `json:"schema"`
	LatestDiscovery              *cachedRelease `json:"latest_discovery,omitempty"`
	SuccessfulCheckAt            *time.Time     `json:"successful_check_at,omitempty"`
	FailedCheckAt                *time.Time     `json:"failed_check_at,omitempty"`
	NoticeDeliveredFor           *time.Time     `json:"notice_delivered_for,omitempty"`
	AutomaticInstallFailedAt     *time.Time     `json:"automatic_install_failed_at,omitempty"`
	AutomaticInstallCompletedFor *time.Time     `json:"automatic_install_completed_for,omitempty"`
}

type cachedRelease struct {
	Tag       string      `json:"tag"`
	Checksums cachedAsset `json:"checksums"`
	Archive   cachedAsset `json:"archive"`
}

type cachedAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

func cachedReleaseFrom(release Release) *cachedRelease {
	return &cachedRelease{
		Tag:       release.Tag,
		Checksums: cachedAsset{Name: release.Checksums.Name, URL: release.Checksums.URL},
		Archive:   cachedAsset{Name: release.Archive.Name, URL: release.Archive.URL},
	}
}

func (release cachedRelease) release() Release {
	return Release{
		Tag:       release.Tag,
		Checksums: Asset{Name: release.Checksums.Name, URL: release.Checksums.URL},
		Archive:   Asset{Name: release.Archive.Name, URL: release.Archive.URL},
	}
}

func timePointer(value time.Time) *time.Time {
	value = value.UTC()
	return &value
}
