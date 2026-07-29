package selfupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
)

func TestCacheSuccessfulCheckFreshnessBoundaries(t *testing.T) {
	t.Parallel()

	checkedAt := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		now         time.Time
		shouldCheck bool
		fresh       bool
	}{
		{name: "just checked", now: checkedAt, shouldCheck: false, fresh: true},
		{name: "inside 24 hours", now: checkedAt.Add(24*time.Hour - time.Nanosecond), shouldCheck: false, fresh: true},
		{name: "exactly 24 hours", now: checkedAt.Add(24 * time.Hour), shouldCheck: true, fresh: false},
		{name: "past 24 hours", now: checkedAt.Add(25 * time.Hour), shouldCheck: true, fresh: false},
		{name: "clock moved backward", now: checkedAt.Add(-time.Nanosecond), shouldCheck: true, fresh: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cache := testCache(t, test.now)
			state := CacheState{latestDiscovery: testRelease(), successfulCheckAt: checkedAt}
			if got := cache.ShouldCheck(state); got != test.shouldCheck {
				t.Errorf("ShouldCheck() = %t, want %t", got, test.shouldCheck)
			}
			_, fresh := cache.FreshRelease(state)
			if fresh != test.fresh {
				t.Errorf("FreshRelease() fresh = %t, want %t", fresh, test.fresh)
			}
		})
	}
}

func TestCacheFailedCheckFreshnessBoundaries(t *testing.T) {
	t.Parallel()

	failedAt := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		now         time.Time
		shouldCheck bool
	}{
		{name: "just failed", now: failedAt, shouldCheck: false},
		{name: "inside one hour", now: failedAt.Add(time.Hour - time.Nanosecond), shouldCheck: false},
		{name: "exactly one hour", now: failedAt.Add(time.Hour), shouldCheck: true},
		{name: "past one hour", now: failedAt.Add(2 * time.Hour), shouldCheck: true},
		{name: "clock moved backward", now: failedAt.Add(-time.Second), shouldCheck: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cache := testCache(t, test.now)
			state := CacheState{failedCheckAt: failedAt}
			if got := cache.ShouldCheck(state); got != test.shouldCheck {
				t.Errorf("ShouldCheck() = %t, want %t", got, test.shouldCheck)
			}
			if _, fresh := cache.FreshRelease(state); fresh {
				t.Error("FreshRelease() accepted a failed check")
			}
		})
	}
}

func TestCacheNoticeDeliveredAtMostOncePerSuccessfulCheck(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	cache := testCache(t, now)
	var state CacheState
	if err := cache.RecordSuccessfulCheck(&state, testRelease()); err != nil {
		t.Fatalf("RecordSuccessfulCheck() error = %v", err)
	}
	if !cache.ShouldNotice(state) {
		t.Fatal("ShouldNotice() = false after a successful check")
	}
	if !cache.RecordNotice(&state) {
		t.Fatal("RecordNotice() = false for eligible notice")
	}
	if cache.ShouldNotice(state) {
		t.Error("ShouldNotice() = true after notice delivery")
	}
	if cache.RecordNotice(&state) {
		t.Error("second RecordNotice() = true")
	}

	now = now.Add(24 * time.Hour)
	cache.Now = func() time.Time { return now }
	if err := cache.RecordSuccessfulCheck(&state, testRelease()); err != nil {
		t.Fatalf("second RecordSuccessfulCheck() error = %v", err)
	}
	if !cache.ShouldNotice(state) {
		t.Error("a new successful check did not start a new notice cycle")
	}

	cache.RecordFailedCheck(&state)
	if cache.ShouldNotice(state) {
		t.Error("failed latest check authorized a notice from retained discovery")
	}
}

func TestCacheAutomaticInstallRetryBoundaries(t *testing.T) {
	t.Parallel()

	checkedAt := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	state := CacheState{latestDiscovery: testRelease(), successfulCheckAt: checkedAt}
	cache := testCache(t, checkedAt)
	if !cache.ShouldRetryAutomaticInstall(state) {
		t.Fatal("fresh discovery without failure is not eligible")
	}
	if !cache.RecordAutomaticInstallFailure(&state) {
		t.Fatal("RecordAutomaticInstallFailure() = false")
	}

	tests := []struct {
		name  string
		now   time.Time
		retry bool
	}{
		{name: "just failed", now: checkedAt, retry: false},
		{name: "inside one hour", now: checkedAt.Add(time.Hour - time.Nanosecond), retry: false},
		{name: "exactly one hour", now: checkedAt.Add(time.Hour), retry: true},
		{name: "past one hour", now: checkedAt.Add(2 * time.Hour), retry: true},
		{name: "failure timestamp in future", now: checkedAt.Add(-time.Second), retry: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := state
			candidate.successfulCheckAt = test.now.Add(-time.Minute)
			if test.name == "failure timestamp in future" {
				candidate.successfulCheckAt = test.now.Add(-time.Minute)
				candidate.automaticInstallFailedAt = test.now.Add(time.Second)
			}
			candidateCache := testCache(t, test.now)
			if got := candidateCache.ShouldRetryAutomaticInstall(candidate); got != test.retry {
				t.Errorf("ShouldRetryAutomaticInstall() = %t, want %t", got, test.retry)
			}
		})
	}
}

func TestCacheRoundTripCreatesPrivateAtomicState(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 5, 0, 0, 123, time.FixedZone("test", 2*60*60))
	dataHome := filepath.Join(t.TempDir(), "missing", "tao")
	cache := Cache{DataHome: dataHome, Now: func() time.Time { return now }, GOOS: "linux", GOARCH: "amd64"}
	var state CacheState
	if err := cache.RecordSuccessfulCheck(&state, testRelease()); err != nil {
		t.Fatalf("RecordSuccessfulCheck() error = %v", err)
	}
	if !cache.RecordNotice(&state) {
		t.Fatal("RecordNotice() = false")
	}
	if !cache.RecordAutomaticInstallFailure(&state) {
		t.Fatal("RecordAutomaticInstallFailure() = false")
	}
	if err := cache.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	info, err := os.Stat(cache.Path())
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("cache permissions = %04o, want 0600", got)
	}
	loaded, err := cache.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.latestDiscovery != testRelease() {
		t.Errorf("latest discovery = %#v", loaded.latestDiscovery)
	}
	if !loaded.successfulCheckAt.Equal(now.UTC()) || !loaded.noticeDeliveredFor.Equal(now.UTC()) || !loaded.automaticInstallFailedAt.Equal(now.UTC()) {
		t.Errorf("loaded timestamps = %#v", loaded)
	}
	if cache.ShouldNotice(loaded) {
		t.Error("round trip lost notice suppression")
	}
	if cache.ShouldRetryAutomaticInstall(loaded) {
		t.Error("round trip lost automatic install retry suppression")
	}
}

func TestCacheMalformedUnknownPartialOversizedAndUntrustedContentFailsOpen(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	timestamp := now.Format(time.RFC3339Nano)
	validRelease := `"latest_discovery":{"tag":"v1.2.3","checksums":{"name":"checksums.txt","url":"https://example.com/checksums.txt"},"archive":{"name":"tao_1.2.3_linux_amd64.tar.gz","url":"https://example.com/archive"}}`
	tests := []struct {
		name    string
		content string
	}{
		{name: "malformed", content: `{`},
		{name: "unknown schema", content: `{"schema":"tao.self-update-cache.v99"}`},
		{name: "missing release", content: fmt.Sprintf(`{"schema":%q,"successful_check_at":%q}`, updateCacheSchema, timestamp)},
		{name: "missing successful time", content: fmt.Sprintf(`{"schema":%q,%s}`, updateCacheSchema, validRelease)},
		{name: "partial notice", content: fmt.Sprintf(`{"schema":%q,"notice_delivered_for":%q}`, updateCacheSchema, timestamp)},
		{name: "untrusted release URL", content: fmt.Sprintf(`{"schema":%q,"successful_check_at":%q,"latest_discovery":{"tag":"v1.2.3","checksums":{"name":"checksums.txt","url":"file:///tmp/checksums"},"archive":{"name":"tao_1.2.3_linux_amd64.tar.gz","url":"https://example.com/archive"}}}`, updateCacheSchema, timestamp)},
		{name: "wrong target archive", content: fmt.Sprintf(`{"schema":%q,"successful_check_at":%q,"latest_discovery":{"tag":"v1.2.3","checksums":{"name":"checksums.txt","url":"https://example.com/checksums"},"archive":{"name":"tao_1.2.3_darwin_amd64.tar.gz","url":"https://example.com/archive"}}}`, updateCacheSchema, timestamp)},
		{name: "oversized", content: strings.Repeat("x", maxUpdateCacheBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			cache := testCache(t, now)
			if err := os.WriteFile(cache.Path(), []byte(test.content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			state, err := cache.Load()
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !cache.ShouldCheck(state) {
				t.Error("invalid cache did not fail open to a new check")
			}
			if _, fresh := cache.FreshRelease(state); fresh {
				t.Error("invalid cache authorized cached release metadata")
			}
		})
	}
}

func TestCacheStaleContentFailsOpenAtExactBoundaries(t *testing.T) {
	t.Parallel()

	checkedAt := time.Date(2026, 7, 28, 5, 0, 0, 0, time.UTC)
	cache := testCache(t, checkedAt)
	state := CacheState{}
	if err := cache.RecordSuccessfulCheck(&state, testRelease()); err != nil {
		t.Fatalf("RecordSuccessfulCheck() error = %v", err)
	}
	if err := cache.Save(state); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	cache.Now = func() time.Time { return checkedAt.Add(24 * time.Hour) }
	loaded, err := cache.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cache.ShouldCheck(loaded) {
		t.Error("24-hour-old successful cache suppressed a check")
	}
	if cache.ShouldNotice(loaded) || cache.ShouldRetryAutomaticInstall(loaded) {
		t.Error("stale cache authorized a notice or installation")
	}

	cache.Now = func() time.Time { return checkedAt }
	cache.RecordFailedCheck(&loaded)
	if err := cache.Save(loaded); err != nil {
		t.Fatalf("Save(failure) error = %v", err)
	}
	cache.Now = func() time.Time { return checkedAt.Add(time.Hour) }
	loaded, err = cache.Load()
	if err != nil {
		t.Fatalf("Load(failure) error = %v", err)
	}
	if !cache.ShouldCheck(loaded) {
		t.Error("one-hour-old failed cache suppressed a check")
	}
}

func TestCacheMissingStateAndPersistenceFailuresAreDistinguishable(t *testing.T) {
	t.Parallel()

	cache := testCache(t, time.Now())
	state, err := cache.Load()
	if err != nil {
		t.Fatalf("Load(missing) error = %v", err)
	}
	if !cache.ShouldCheck(state) {
		t.Error("missing cache did not permit a check")
	}

	writeFailure := errors.New("disk unavailable")
	cache.writeFile = func(string, []byte, atomicfile.Options) error { return writeFailure }
	err = cache.Save(CacheState{failedCheckAt: cache.now()})
	if err == nil || !IsPersistenceError(err) || !errors.Is(err, writeFailure) {
		t.Fatalf("Save() error = %v, want wrapped persistence error", err)
	}

	invalid := CacheState{successfulCheckAt: cache.now()}
	err = cache.Save(invalid)
	if err == nil || IsPersistenceError(err) {
		t.Fatalf("Save(invalid) error = %v, want non-persistence validation error", err)
	}
}

func TestCachePermissionFailuresWhereEnforced(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory permissions are not enforced on Windows")
	}

	dataHome := t.TempDir()
	if err := os.Chmod(dataHome, 0o500); err != nil { //nolint:gosec // Directory execute permission is required for the permission-failure test.
		t.Skipf("cannot set test directory permissions: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dataHome, 0o700) }) //nolint:gosec // Restore private directory access for TempDir cleanup.
	cache := Cache{DataHome: dataHome, Now: time.Now, GOOS: "linux", GOARCH: "amd64"}
	err := cache.Save(CacheState{failedCheckAt: time.Now().UTC()})
	if err == nil {
		_ = os.Remove(cache.Path())
		t.Skip("filesystem allows writes to read-only test directory")
	}
	if !IsPersistenceError(err) {
		t.Fatalf("Save() error = %v, want PersistenceError", err)
	}
}

func TestCacheConcurrentAtomicReadersAndWriters(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 5, 0, 0, 0, time.UTC)
	cache := testCache(t, now)
	seed := CacheState{}
	if err := cache.RecordSuccessfulCheck(&seed, testRelease()); err != nil {
		t.Fatalf("RecordSuccessfulCheck() error = %v", err)
	}
	if err := cache.Save(seed); err != nil {
		t.Fatalf("seed Save() error = %v", err)
	}

	const goroutines = 8
	const iterations = 75
	start := make(chan struct{})
	errorsChannel := make(chan error, goroutines)
	var wait sync.WaitGroup
	for worker := range goroutines {
		wait.Go(func() {
			<-start
			if worker%2 == 0 {
				for range iterations {
					state := seed
					if err := cache.Save(state); err != nil {
						errorsChannel <- err
						return
					}
				}
				return
			}
			for range iterations {
				state, err := cache.Load()
				if err != nil {
					errorsChannel <- err
					return
				}
				if release, fresh := cache.FreshRelease(state); !fresh || release != testRelease() {
					errorsChannel <- fmt.Errorf("reader observed incomplete state: fresh=%t release=%#v", fresh, release)
					return
				}
			}
		})
	}
	close(start)
	wait.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		t.Error(err)
	}
}

func testCache(t *testing.T, now time.Time) Cache {
	t.Helper()
	return Cache{
		DataHome: t.TempDir(),
		Now:      func() time.Time { return now },
		GOOS:     "linux",
		GOARCH:   "amd64",
	}
}

func testRelease() Release {
	return Release{
		Tag:       "v1.2.3",
		Checksums: Asset{Name: "checksums.txt", URL: "https://example.com/checksums.txt"},
		Archive:   Asset{Name: "tao_1.2.3_linux_amd64.tar.gz", URL: "https://example.com/archive"},
	}
}
