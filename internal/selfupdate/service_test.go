package selfupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestServiceUpdateIsUncachedAndInstallsOnlyNewerRelease(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		current       string
		wantCompare   VersionComparison
		wantInstalled bool
		wantError     string
	}{
		{name: "newer", current: "v1.0.0", wantCompare: VersionUpdateAvailable, wantInstalled: true},
		{name: "current", current: "v1.2.3", wantCompare: VersionCurrent},
		{name: "ahead", current: "v2.0.0", wantCompare: VersionAhead},
		{name: "development", current: "dev", wantError: "development builds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			discoverer := &fakeReleaseSource{release: testRelease()}
			installer := &fakeReleaseInstaller{result: InstallResult{Path: "/tmp/tao", Installed: true}}
			service := Service{
				Discoverer:     discoverer,
				Installer:      installer,
				CurrentVersion: func() string { return test.current },
			}
			result, err := service.Update(context.Background())
			if test.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("Update() error = %v, want %q", err, test.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("Update() error = %v", err)
			}
			if result.Comparison != test.wantCompare || result.Installed != test.wantInstalled {
				t.Errorf("Update() result = %+v", result)
			}
			if got := installer.callCount(); got != boolInt(test.wantInstalled) {
				t.Errorf("installer calls = %d", got)
			}
			if discoverer.callCount() != 1 {
				t.Errorf("discoverer calls = %d, want 1", discoverer.callCount())
			}
		})
	}
}

func TestServiceUpdateRejectsDevelopmentBuildBeforeDiscovery(t *testing.T) {
	t.Parallel()

	discoveryFailure := errors.New("discovery failed")
	discoverer := &fakeReleaseSource{err: discoveryFailure}
	service := Service{
		Discoverer:     discoverer,
		Installer:      &fakeReleaseInstaller{},
		CurrentVersion: func() string { return "dev" },
	}

	_, err := service.Update(context.Background())
	if err == nil || !strings.Contains(err.Error(), "development builds cannot self-update") {
		t.Fatalf("Update() error = %v, want development-build error", err)
	}
	if errors.Is(err, discoveryFailure) {
		t.Fatalf("Update() error = %v, want discovery failure to be bypassed", err)
	}
	if got := discoverer.callCount(); got != 0 {
		t.Errorf("discoverer calls = %d, want 0", got)
	}
}

func TestServiceUpdateReturnsDiscoveryAndInstallFailures(t *testing.T) {
	t.Parallel()

	discoveryFailure := errors.New("discovery failed")
	service := Service{
		Discoverer:     &fakeReleaseSource{err: discoveryFailure},
		Installer:      &fakeReleaseInstaller{},
		CurrentVersion: func() string { return "v1.0.0" },
	}
	if _, err := service.Update(context.Background()); !errors.Is(err, discoveryFailure) {
		t.Fatalf("Update() discovery error = %v", err)
	}

	installFailure := errors.New("install failed")
	service.Discoverer = &fakeReleaseSource{release: testRelease()}
	service.Installer = &fakeReleaseInstaller{err: installFailure}
	if _, err := service.Update(context.Background()); !errors.Is(err, installFailure) {
		t.Fatalf("Update() install error = %v", err)
	}
}

func TestServiceStartupWarnUsesCacheAndNoticesOnce(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	cache := serviceTestCache(t, &now)
	discoverer := &fakeReleaseSource{release: testRelease()}
	installer := &fakeReleaseInstaller{}
	service := Service{
		Discoverer:     discoverer,
		Installer:      installer,
		Cache:          cache,
		CurrentVersion: func() string { return "v1.0.0" },
	}

	first := service.Startup(context.Background(), ModeWarn)
	if first.Failure != nil || !first.Checked || first.Cached || !strings.Contains(first.Notice, "run 'tao update'") {
		t.Fatalf("first Startup() = %+v", first)
	}
	second := service.Startup(context.Background(), ModeWarn)
	if second.Failure != nil || second.Checked || !second.Cached || second.Notice != "" {
		t.Fatalf("second Startup() = %+v", second)
	}
	if discoverer.callCount() != 1 || installer.callCount() != 0 {
		t.Errorf("calls: discover=%d install=%d", discoverer.callCount(), installer.callCount())
	}

	loaded, err := cache.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cache.ShouldNotice(loaded) {
		t.Error("notice delivery was not persisted")
	}
}

func TestServiceStartupAutoInstallsOnceAndReportsNextInvocation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	cache := serviceTestCache(t, &now)
	installer := &fakeReleaseInstaller{result: InstallResult{Installed: true, Path: "/tmp/tao"}}
	service := Service{
		Discoverer:     &fakeReleaseSource{release: testRelease()},
		Installer:      installer,
		Cache:          cache,
		CurrentVersion: func() string { return "v1.0.0" },
	}

	outcome := service.Startup(context.Background(), ModeAuto)
	if outcome.Failure != nil || !outcome.Installed || outcome.Path != "/tmp/tao" || !strings.Contains(outcome.Notice, "next invocation") {
		t.Fatalf("Startup() = %+v", outcome)
	}
	if installer.callCount() != 1 {
		t.Errorf("installer calls = %d, want 1", installer.callCount())
	}
	second := service.Startup(context.Background(), ModeAuto)
	if second.Failure != nil || !second.Cached || second.Installed || second.Notice != "" {
		t.Fatalf("second Startup() = %+v", second)
	}
	if installer.callCount() != 1 {
		t.Errorf("installer calls after cached startup = %d, want 1", installer.callCount())
	}
	loaded, err := cache.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cache.ShouldNotice(loaded) {
		t.Error("successful automatic install notice was not persisted")
	}
	if cache.ShouldRetryAutomaticInstall(loaded) {
		t.Error("successful automatic install was not persisted")
	}
}

func TestServiceConcurrentStartupWarnDeliversOneNotice(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	cache := serviceTestCache(t, &now)
	releaseLock, attempts := holdStartupPolicyLock(t, cache, 2)
	defer releaseLock()
	discoverer := &fakeReleaseSource{release: testRelease()}
	service := Service{
		Discoverer:     discoverer,
		Installer:      &fakeReleaseInstaller{},
		Cache:          cache,
		CurrentVersion: func() string { return "v1.0.0" },
	}

	outcomes := concurrentStartups(service, ModeWarn, 2)
	waitForLockAttempts(t, attempts, 2)
	releaseLock()

	var notices int
	for range 2 {
		outcome := <-outcomes
		if outcome.Failure != nil {
			t.Fatalf("Startup() failure = %v", outcome.Failure)
		}
		if outcome.Notice != "" {
			notices++
		}
	}
	if notices != 1 || discoverer.callCount() != 1 {
		t.Errorf("notices=%d discovery calls=%d, want 1 each", notices, discoverer.callCount())
	}
}

func TestServiceConcurrentStartupAutoSuppressesFailedInstallRetry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	cache := serviceTestCache(t, &now)
	releaseLock, attempts := holdStartupPolicyLock(t, cache, 2)
	defer releaseLock()
	installFailure := errors.New("install unavailable")
	discoverer := &fakeReleaseSource{release: testRelease()}
	installer := &fakeReleaseInstaller{err: installFailure}
	service := Service{
		Discoverer:     discoverer,
		Installer:      installer,
		Cache:          cache,
		CurrentVersion: func() string { return "v1.0.0" },
	}

	outcomes := concurrentStartups(service, ModeAuto, 2)
	waitForLockAttempts(t, attempts, 2)
	releaseLock()

	var failures int
	for range 2 {
		outcome := <-outcomes
		if errors.Is(outcome.AutomaticInstallFailure, installFailure) {
			failures++
		} else if outcome.Failure != nil {
			t.Fatalf("Startup() unexpected failure = %v", outcome.Failure)
		}
	}
	if failures != 1 || discoverer.callCount() != 1 || installer.callCount() != 1 {
		t.Errorf("failures=%d discovery calls=%d installer calls=%d, want 1 each", failures, discoverer.callCount(), installer.callCount())
	}
}

func TestServiceStartupAutoReportsInstallAfterWarnNotice(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	cache := serviceTestCache(t, &now)
	installer := &fakeReleaseInstaller{result: InstallResult{Installed: true, Path: "/tmp/tao"}}
	service := Service{
		Discoverer:     &fakeReleaseSource{release: testRelease()},
		Installer:      installer,
		Cache:          cache,
		CurrentVersion: func() string { return "v1.0.0" },
	}

	warn := service.Startup(context.Background(), ModeWarn)
	if warn.Failure != nil || !strings.Contains(warn.Notice, "run 'tao update'") {
		t.Fatalf("warn Startup() = %+v", warn)
	}
	auto := service.Startup(context.Background(), ModeAuto)
	if auto.Failure != nil || !auto.Cached || !auto.Installed || !strings.Contains(auto.Notice, "next invocation") {
		t.Fatalf("auto Startup() = %+v", auto)
	}
	if installer.callCount() != 1 {
		t.Errorf("installer calls = %d, want 1", installer.callCount())
	}
}

func TestServiceStartupInstallContextHasFiniteDefaultAndPreservesCallerDeadline(t *testing.T) {
	t.Parallel()

	service := Service{}
	started := time.Now()
	installContext, cancelInstall := service.startupInstallContext(context.Background())
	defer cancelInstall()
	deadline, ok := installContext.Deadline()
	if !ok {
		t.Fatal("startup install context has no deadline")
	}
	if deadline.Before(started) || deadline.After(started.Add(defaultStartupInstallTimeout+time.Second)) {
		t.Errorf("startup install deadline = %v, want finite default near %v", deadline, defaultStartupInstallTimeout)
	}

	callerContext, cancelCaller := context.WithTimeout(context.Background(), time.Second)
	defer cancelCaller()
	callerDeadline, _ := callerContext.Deadline()
	installContext, cancelInstall = service.startupInstallContext(callerContext)
	defer cancelInstall()
	installDeadline, ok := installContext.Deadline()
	if !ok || !installDeadline.Equal(callerDeadline) {
		t.Errorf("startup install deadline = %v, want earlier caller deadline %v", installDeadline, callerDeadline)
	}
}

func TestServiceStartupLockContentionExpiresNonFatally(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	destination := testExecutable(t, "old", 0o755)
	installer := testInstaller(nil, destination)
	unlock, err := installer.lock(context.Background(), destination+".update.lock")
	if err != nil {
		t.Fatalf("lock() error = %v", err)
	}
	defer func() { _ = unlock() }()

	service := Service{
		Discoverer:            &fakeReleaseSource{release: testRelease()},
		Installer:             installer,
		Cache:                 serviceTestCache(t, &now),
		CurrentVersion:        func() string { return "v1.0.0" },
		StartupInstallTimeout: 50 * time.Millisecond,
	}
	outcome := service.Startup(context.Background(), ModeAuto)
	if !errors.Is(outcome.Failure, context.DeadlineExceeded) || !errors.Is(outcome.AutomaticInstallFailure, context.DeadlineExceeded) {
		t.Fatalf("Startup() = %+v, want non-fatal lock deadline", outcome)
	}
	if outcome.Installed || outcome.Notice != "" {
		t.Fatalf("Startup() = %+v, want no installation or notice", outcome)
	}
	assertFileContent(t, destination, "old")
}

func TestServiceStartupFailuresAreNonFatalAndThrottled(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	cache := serviceTestCache(t, &now)
	installFailure := errors.New("install unavailable")
	installer := &fakeReleaseInstaller{err: installFailure}
	service := Service{
		Discoverer:     &fakeReleaseSource{release: testRelease()},
		Installer:      installer,
		Cache:          cache,
		CurrentVersion: func() string { return "v1.0.0" },
	}

	first := service.Startup(context.Background(), ModeAuto)
	if !errors.Is(first.Failure, installFailure) || !errors.Is(first.AutomaticInstallFailure, installFailure) || first.Notice != "" || first.Installed {
		t.Fatalf("first Startup() = %+v", first)
	}
	second := service.Startup(context.Background(), ModeAuto)
	if second.Failure != nil || second.Notice != "" || installer.callCount() != 1 {
		t.Fatalf("throttled Startup() = %+v, calls=%d", second, installer.callCount())
	}

	now = now.Add(time.Hour)
	third := service.Startup(context.Background(), ModeAuto)
	if !errors.Is(third.Failure, installFailure) || installer.callCount() != 2 {
		t.Fatalf("retry Startup() = %+v, calls=%d", third, installer.callCount())
	}
}

func TestServiceStartupDiscoveryFailureCachesRetryAndModeOffDoesNothing(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	cache := serviceTestCache(t, &now)
	discoveryFailure := errors.New("offline")
	discoverer := &fakeReleaseSource{err: discoveryFailure}
	service := Service{
		Discoverer:     discoverer,
		Installer:      &fakeReleaseInstaller{},
		Cache:          cache,
		CurrentVersion: func() string { return "v1.0.0" },
	}

	first := service.Startup(context.Background(), ModeWarn)
	if !errors.Is(first.Failure, discoveryFailure) || !first.Checked {
		t.Fatalf("first Startup() = %+v", first)
	}
	second := service.Startup(context.Background(), ModeWarn)
	if second.Failure != nil || second.Checked || discoverer.callCount() != 1 {
		t.Fatalf("cached failure Startup() = %+v, calls=%d", second, discoverer.callCount())
	}
	off := service.Startup(context.Background(), ModeOff)
	if off != (StartupOutcome{}) || discoverer.callCount() != 1 {
		t.Fatalf("off Startup() = %+v, calls=%d", off, discoverer.callCount())
	}
}

func TestServiceStartupHomebrewFailureCarriesRecommendation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 29, 6, 0, 0, 0, time.UTC)
	service := Service{
		Discoverer: &fakeReleaseSource{release: testRelease()},
		Installer: &fakeReleaseInstaller{err: &HomebrewError{
			Path: "/opt/homebrew/Cellar/tao/1.2.3/bin/tao",
		}},
		Cache:          serviceTestCache(t, &now),
		CurrentVersion: func() string { return "v1.0.0" },
	}
	outcome := service.Startup(context.Background(), ModeAuto)
	if !IsHomebrewError(outcome.Failure) || !IsHomebrewError(outcome.AutomaticInstallFailure) || !strings.Contains(outcome.Notice, "brew upgrade tao") {
		t.Fatalf("Startup() = %+v", outcome)
	}
}

func serviceTestCache(t *testing.T, now *time.Time) *Cache {
	t.Helper()
	return &Cache{
		DataHome: filepath.Join(t.TempDir(), "data"),
		Now:      func() time.Time { return *now },
		GOOS:     "linux",
		GOARCH:   "amd64",
	}
}

func holdStartupPolicyLock(t *testing.T, cache *Cache, expectedAttempts int) (func(), <-chan struct{}) {
	t.Helper()
	if err := os.MkdirAll(cache.DataHome, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	path := filepath.Join(cache.DataHome, updateCacheLockFilename)
	unlock, err := acquireFileLock(context.Background(), path)
	if err != nil {
		t.Fatalf("acquireFileLock() error = %v", err)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			if err := unlock(); err != nil {
				t.Errorf("unlock() error = %v", err)
			}
		})
	}
	attempts := make(chan struct{}, expectedAttempts)
	cache.acquireLock = func(ctx context.Context, lockPath string) (func() error, error) {
		attempts <- struct{}{}
		return acquireFileLock(ctx, lockPath)
	}
	return release, attempts
}

func concurrentStartups(service Service, mode Mode, count int) <-chan StartupOutcome {
	start := make(chan struct{})
	outcomes := make(chan StartupOutcome, count)
	for range count {
		go func() {
			<-start
			outcomes <- service.Startup(context.Background(), mode)
		}()
	}
	close(start)
	return outcomes
}

func waitForLockAttempts(t *testing.T, attempts <-chan struct{}, count int) {
	t.Helper()
	for range count {
		select {
		case <-attempts:
		case <-time.After(time.Second):
			t.Fatal("startup did not attempt the policy lock")
		}
	}
}

type fakeReleaseSource struct {
	mu      sync.Mutex
	release Release
	err     error
	calls   int
}

func (source *fakeReleaseSource) Latest(context.Context) (Release, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	return source.release, source.err
}

func (source *fakeReleaseSource) callCount() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

type fakeReleaseInstaller struct {
	mu     sync.Mutex
	result InstallResult
	err    error
	calls  int
}

func (installer *fakeReleaseInstaller) Install(context.Context, Release) (InstallResult, error) {
	installer.mu.Lock()
	defer installer.mu.Unlock()
	installer.calls++
	return installer.result, installer.err
}

func (installer *fakeReleaseInstaller) callCount() int {
	installer.mu.Lock()
	defer installer.mu.Unlock()
	return installer.calls
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
