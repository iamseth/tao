package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestInstallerInstallsVerifiedArchiveAndPreservesPermissions(t *testing.T) {
	t.Parallel()

	archive := testArchive(t, testTarEntry{name: "tao", mode: 0o755, content: "new tao"})
	server, release := testReleaseServer(t, archive, nil)
	defer server.Close()
	destination := testExecutable(t, "old tao", 0o751)
	installer := testInstaller(server.Client(), destination)

	result, err := installer.Install(context.Background(), release)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if !result.Installed || result.Path != destination || result.ConcurrentUpdate {
		t.Fatalf("Install() result = %+v", result)
	}
	content, err := os.ReadFile(destination) //nolint:gosec // Destination is created in this test's temporary directory.
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "new tao" {
		t.Errorf("installed content = %q", content)
	}
	info, err := os.Stat(destination)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o751 {
		t.Errorf("installed permissions = %04o, want 0751", info.Mode().Perm())
	}
}

func TestParseArchiveChecksumRejectsMalformedDuplicateMissingAndMismatch(t *testing.T) {
	t.Parallel()

	archiveName := "tao_1.2.3_linux_amd64.tar.gz"
	valid := strings.Repeat("a", sha256.Size*2)
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "empty", content: "", want: "empty"},
		{name: "malformed fields", content: valid, want: "malformed"},
		{name: "malformed digest", content: strings.Repeat("z", sha256.Size*2) + "  " + archiveName, want: "malformed"},
		{name: "duplicate", content: valid + "  " + archiveName + "\n" + valid + "  " + archiveName, want: "duplicate"},
		{name: "missing", content: valid + "  other.tar.gz", want: "missing"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := parseArchiveChecksum([]byte(test.content), archiveName)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parseArchiveChecksum() error = %v, want %q", err, test.want)
			}
		})
	}

	archive := testArchive(t, testTarEntry{name: "tao", mode: 0o755, content: "new"})
	destination := testExecutable(t, "old", 0o755)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/checksums.txt":
			_, _ = fmt.Fprintf(writer, "%s  tao_1.2.3_linux_amd64.tar.gz\n", strings.Repeat("0", sha256.Size*2))
		case "/archive":
			_, _ = writer.Write(archive)
		}
	}))
	defer server.Close()
	release := testHTTPRelease(server.URL)
	_, err := testInstaller(server.Client(), destination).Install(context.Background(), release)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Install() error = %v, want checksum mismatch", err)
	}
	assertFileContent(t, destination, "old")
}

func TestExtractTaoBinaryRejectsHostileArchives(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entries []testTarEntry
		want    string
	}{
		{name: "traversal", entries: []testTarEntry{{name: "../tao", mode: 0o755, content: "bad"}}, want: "unsafe archive path"},
		{name: "absolute", entries: []testTarEntry{{name: "/tao", mode: 0o755, content: "bad"}}, want: "unsafe archive path"},
		{name: "symlink", entries: []testTarEntry{{name: "tao", mode: 0o755, typeflag: tar.TypeSymlink, linkname: "/bin/sh"}}, want: "not a regular file"},
		{name: "device", entries: []testTarEntry{{name: "tao", mode: 0o755, typeflag: tar.TypeChar}}, want: "not a regular file"},
		{name: "duplicate", entries: []testTarEntry{{name: "tao", mode: 0o755, content: "one"}, {name: "tao", mode: 0o755, content: "two"}}, want: "duplicate"},
		{name: "unexpected executable", entries: []testTarEntry{{name: "helper", mode: 0o755, content: "bad"}, {name: "tao", mode: 0o755, content: "good"}}, want: "unexpected executable"},
		{name: "missing", entries: []testTarEntry{{name: "README", mode: 0o644, content: "text"}}, want: "does not contain tao"},
		{name: "non executable tao", entries: []testTarEntry{{name: "tao", mode: 0o644, content: "bad"}}, want: "not executable"},
		{name: "oversized header", entries: []testTarEntry{{name: "tao", mode: 0o755, size: maxBinaryBytes + 1}}, want: "exceeds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			archivePath := filepath.Join(directory, "archive.tar.gz")
			if err := os.WriteFile(archivePath, testArchive(t, test.entries...), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			_, err := extractTaoBinary(archivePath, directory)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("extractTaoBinary() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestInstallerBoundsChecksumsAndHonorsCancellation(t *testing.T) {
	t.Parallel()

	destination := testExecutable(t, "old", 0o755)
	oversizedServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, strings.Repeat("x", maxChecksumsBytes+1))
	}))
	defer oversizedServer.Close()
	release := testHTTPRelease(oversizedServer.URL)
	installer := testInstaller(oversizedServer.Client(), destination)
	_, err := installer.Install(context.Background(), release)
	if err == nil || !strings.Contains(err.Error(), "checksums exceeds") {
		t.Fatalf("Install() oversized checksums error = %v", err)
	}
	assertFileContent(t, destination, "old")

	archive := []byte(strings.Repeat("a", 65))
	archiveServer, archiveRelease := testReleaseServer(t, archive, nil)
	defer archiveServer.Close()
	installer = testInstaller(archiveServer.Client(), destination)
	installer.maxArchiveBytes = 64
	_, err = installer.Install(context.Background(), archiveRelease)
	if err == nil || !strings.Contains(err.Error(), "release archive exceeds 64 bytes") {
		t.Fatalf("Install() oversized archive error = %v", err)
	}
	assertFileContent(t, destination, "old")

	cancelServer := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer cancelServer.Close()
	release = testHTTPRelease(cancelServer.URL)
	installer = testInstaller(cancelServer.Client(), destination)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = installer.Install(ctx, release)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Install() cancellation error = %v", err)
	}
	assertFileContent(t, destination, "old")
}

func TestInstallerPreservesOriginalOnPermissionRenameAndSyncFailures(t *testing.T) {
	t.Parallel()

	archive := testArchive(t, testTarEntry{name: "tao", mode: 0o755, content: "new"})
	server, release := testReleaseServer(t, archive, nil)
	defer server.Close()
	injected := errors.New("injected failure")
	tests := []struct {
		name   string
		mutate func(*Installer)
	}{
		{name: "chmod", mutate: func(installer *Installer) { installer.Chmod = func(string, os.FileMode) error { return injected } }},
		{name: "rename", mutate: func(installer *Installer) { installer.Rename = func(string, string) error { return injected } }},
		{name: "link", mutate: func(installer *Installer) { installer.Link = func(string, string) error { return injected } }},
		{name: "post rename sync rollback", mutate: func(installer *Installer) {
			var calls atomic.Int32
			installer.SyncDir = func(string) error {
				if calls.Add(1) == 2 {
					return injected
				}
				return nil
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			destination := testExecutable(t, "old", 0o755)
			installer := testInstaller(server.Client(), destination)
			test.mutate(installer)
			_, err := installer.Install(context.Background(), release)
			if err == nil {
				t.Fatal("Install() error = nil")
			}
			assertFileContent(t, destination, "old")
		})
	}
}

func TestInstallerRefusesHomebrewBeforeMutation(t *testing.T) {
	t.Parallel()

	var lockCalled bool
	installer := &Installer{
		Executable:   func() (string, error) { return "/opt/homebrew/bin/tao", nil },
		EvalSymlinks: func(string) (string, error) { return "/opt/homebrew/Cellar/tao/1.2.3/bin/tao", nil },
		GOOS:         func() string { return "darwin" },
		GOARCH:       func() string { return "arm64" },
		AcquireLock: func(context.Context, string) (func() error, error) {
			lockCalled = true
			return func() error { return nil }, nil
		},
	}
	release := Release{
		Tag:       "v1.2.3",
		Checksums: Asset{Name: "checksums.txt", URL: "https://example.com/checksums.txt"},
		Archive:   Asset{Name: "tao_1.2.3_darwin_arm64.tar.gz", URL: "https://example.com/archive"},
	}
	_, err := installer.Install(context.Background(), release)
	if !IsHomebrewError(err) || !strings.Contains(err.Error(), "brew upgrade tao") {
		t.Fatalf("Install() error = %v", err)
	}
	if lockCalled {
		t.Error("Homebrew refusal acquired the update lock")
	}
	if !IsHomebrewManagedPath("/usr/local/Cellar/tao/1.2.3/bin/tao") || IsHomebrewManagedPath("/tmp/Cellar/other/bin/tao") {
		t.Error("IsHomebrewManagedPath() did not match exact Cellar/tao components")
	}
}

func TestInstallerLockContentionIsCancellable(t *testing.T) {
	t.Parallel()

	destination := testExecutable(t, "old", 0o755)
	installer := testInstaller(http.DefaultClient, destination)
	unlock, err := installer.lock(context.Background(), destination+".update.lock")
	if err != nil {
		t.Fatalf("lock() error = %v", err)
	}
	defer func() { _ = unlock() }()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = installer.Install(ctx, testHTTPRelease("https://example.com"))
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Install() error = %v, want deadline", err)
	}
	assertFileContent(t, destination, "old")
}

func TestInstallerConcurrentCallsInstallOnce(t *testing.T) {
	t.Parallel()

	archive := testArchive(t, testTarEntry{name: "tao", mode: 0o755, content: "new"})
	var archiveRequests atomic.Int32
	server, release := testReleaseServer(t, archive, func(checksum string) string {
		return checksum
	})
	originalHandler := server.Config.Handler
	server.Config.Handler = http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/archive" {
			archiveRequests.Add(1)
		}
		originalHandler.ServeHTTP(writer, request)
	})
	defer server.Close()
	destination := testExecutable(t, "old", 0o755)
	installer := testInstaller(server.Client(), destination)

	unlock, err := installer.lock(context.Background(), destination+".update.lock")
	if err != nil {
		t.Fatalf("lock() error = %v", err)
	}
	results := make(chan InstallResult, 2)
	errorsChannel := make(chan error, 2)
	var wait sync.WaitGroup
	for range 2 {
		wait.Go(func() {
			result, installErr := installer.Install(context.Background(), release)
			results <- result
			errorsChannel <- installErr
		})
	}
	time.Sleep(75 * time.Millisecond)
	if err := unlock(); err != nil {
		t.Fatalf("unlock() error = %v", err)
	}
	wait.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Errorf("Install() error = %v", err)
		}
	}
	var installed, concurrent int
	for result := range results {
		if result.Installed {
			installed++
		}
		if result.ConcurrentUpdate {
			concurrent++
		}
	}
	if installed != 1 || concurrent != 1 || archiveRequests.Load() != 1 {
		t.Errorf("installed=%d concurrent=%d archive requests=%d", installed, concurrent, archiveRequests.Load())
	}
	assertFileContent(t, destination, "new")
}

type testTarEntry struct {
	name     string
	mode     int64
	typeflag byte
	linkname string
	content  string
	size     int64
}

func testArchive(t *testing.T, entries ...testTarEntry) []byte {
	t.Helper()
	var buffer strings.Builder
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	for _, entry := range entries {
		typeflag := entry.typeflag
		if typeflag == 0 {
			typeflag = tar.TypeReg
		}
		size := int64(len(entry.content))
		if entry.size != 0 {
			size = entry.size
		}
		header := &tar.Header{Name: entry.name, Mode: entry.mode, Typeflag: typeflag, Linkname: entry.linkname, Size: size}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
		if entry.content != "" {
			if _, err := io.WriteString(archive, entry.content); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
		}
	}
	if err := archive.Close(); err != nil && !strings.Contains(err.Error(), "missed writing") {
		t.Fatalf("tar Close() error = %v", err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatalf("gzip Close() error = %v", err)
	}
	return []byte(buffer.String())
}

func testReleaseServer(t *testing.T, archive []byte, alterChecksum func(string) string) (*httptest.Server, Release) {
	t.Helper()
	archiveName := "tao_1.2.3_linux_amd64.tar.gz"
	digest := sha256.Sum256(archive)
	checksum := fmt.Sprintf("%x  %s\n", digest, archiveName)
	if alterChecksum != nil {
		checksum = alterChecksum(checksum)
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/checksums.txt":
			_, _ = io.WriteString(writer, checksum)
		case "/archive":
			_, _ = writer.Write(archive)
		default:
			http.NotFound(writer, request)
		}
	}))
	release := testHTTPRelease(server.URL)
	return server, release
}

func testHTTPRelease(baseURL string) Release {
	return Release{
		Tag:       "v1.2.3",
		Checksums: Asset{Name: "checksums.txt", URL: baseURL + "/checksums.txt"},
		Archive:   Asset{Name: "tao_1.2.3_linux_amd64.tar.gz", URL: baseURL + "/archive"},
	}
}

func testExecutable(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tao")
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func testInstaller(client *http.Client, destination string) *Installer {
	return &Installer{
		HTTPClient:   client,
		Executable:   func() (string, error) { return destination, nil },
		EvalSymlinks: func(path string) (string, error) { return path, nil },
		GOOS:         func() string { return "linux" },
		GOARCH:       func() string { return "amd64" },
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path) //nolint:gosec // Path is created in the caller's test temporary directory.
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if string(content) != want {
		t.Errorf("file content = %q, want %q", content, want)
	}
}
