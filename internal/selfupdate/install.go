package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/iamseth/tao/internal/atomicfile"
)

const (
	maxChecksumsBytes         = 1 << 20
	maxCompressedArchiveBytes = 32 << 20
	maxExpandedArchiveBytes   = 128 << 20
	maxBinaryBytes            = 96 << 20
	defaultDownloadTimeout    = 30 * time.Second
	lockRetryInterval         = 25 * time.Millisecond
)

// Installer securely downloads and atomically installs a validated Tao release.
// Its path, platform, and filesystem hooks are injectable so tests never touch
// the running test executable.
type Installer struct {
	HTTPClient     *http.Client
	RequestTimeout time.Duration
	Executable     func() (string, error)
	EvalSymlinks   func(string) (string, error)
	GOOS           func() string
	GOARCH         func() string
	Chmod          func(string, os.FileMode) error
	Rename         func(string, string) error
	Link           func(string, string) error
	Remove         func(string) error
	SyncDir        func(string) error
	AcquireLock    func(context.Context, string) (func() error, error)

	maxArchiveBytes int64
}

// InstallResult describes an installer attempt. ConcurrentUpdate is true when
// another updater replaced the executable while this attempt waited for the
// cross-process lock.
type InstallResult struct {
	Path             string
	Installed        bool
	ConcurrentUpdate bool
}

// HomebrewError reports a package-manager-owned executable that must not be
// replaced in place.
type HomebrewError struct {
	Path string
}

func (err *HomebrewError) Error() string {
	return fmt.Sprintf("Tao executable %q is managed by Homebrew; run 'brew upgrade tao'", err.Path)
}

// IsHomebrewError reports whether an installation was refused because the
// executable belongs to Homebrew.
func IsHomebrewError(err error) bool {
	var homebrewErr *HomebrewError
	return errors.As(err, &homebrewErr)
}

// IsHomebrewManagedPath recognizes resolved Homebrew Cellar paths for Tao.
func IsHomebrewManagedPath(path string) bool {
	parts := strings.FieldsFunc(filepath.Clean(path), func(character rune) bool {
		return character == filepath.Separator
	})
	for index := 0; index+1 < len(parts); index++ {
		if parts[index] == "Cellar" && parts[index+1] == "tao" {
			return true
		}
	}
	return false
}

// Install verifies release checksums and archive structure before replacing the
// resolved running executable. The original executable remains unchanged on
// every failure before the atomic rename.
func (installer *Installer) Install(ctx context.Context, release Release) (InstallResult, error) {
	if installer == nil {
		return InstallResult{}, errors.New("install Tao update: nil installer")
	}
	goos, goarch := installer.platform()
	if err := validateTarget(goos, goarch); err != nil {
		return InstallResult{}, err
	}
	if err := validateInstallRelease(release, goos, goarch); err != nil {
		return InstallResult{}, err
	}

	before, err := installer.executableSnapshot()
	if err != nil {
		return InstallResult{}, err
	}
	result := InstallResult{Path: before.path}

	unlock, err := installer.lock(ctx, before.path+".update.lock")
	if err != nil {
		return result, fmt.Errorf("install Tao update: acquire update lock: %w", err)
	}
	defer func() { _ = unlock() }()

	after, err := installer.executableSnapshot()
	if err != nil {
		return result, err
	}
	if after.path != before.path || after.digest != before.digest {
		result.Path = after.path
		result.ConcurrentUpdate = true
		return result, nil
	}

	checksums, err := installer.downloadBytes(ctx, release.Checksums.URL, maxChecksumsBytes, "checksums")
	if err != nil {
		return result, err
	}
	expectedChecksum, err := parseArchiveChecksum(checksums, release.Archive.Name)
	if err != nil {
		return result, err
	}

	directory := filepath.Dir(after.path)
	archivePath, archiveChecksum, err := installer.downloadArchive(ctx, release.Archive.URL, directory)
	if err != nil {
		return result, err
	}
	defer func() { _ = os.Remove(archivePath) }()
	if archiveChecksum != expectedChecksum {
		return result, fmt.Errorf("install Tao update: checksum mismatch for %q", release.Archive.Name)
	}

	binaryPath, err := extractTaoBinary(archivePath, directory)
	if err != nil {
		return result, err
	}
	defer func() { _ = os.Remove(binaryPath) }()
	if err := installer.chmod(binaryPath, after.mode.Perm()); err != nil {
		return result, fmt.Errorf("install Tao update: set executable permissions: %w", err)
	}
	binary, err := os.Open(binaryPath) // #nosec G304 -- path is a private temporary file created above.
	if err != nil {
		return result, fmt.Errorf("install Tao update: reopen extracted executable: %w", err)
	}
	if err := binary.Sync(); err != nil {
		_ = binary.Close()
		return result, fmt.Errorf("install Tao update: sync extracted executable: %w", err)
	}
	if err := binary.Close(); err != nil {
		return result, fmt.Errorf("install Tao update: close extracted executable: %w", err)
	}

	if err := installer.replace(after.path, binaryPath); err != nil {
		return result, err
	}
	result.Installed = true
	return result, nil
}

type executableState struct {
	path   string
	mode   os.FileMode
	digest [sha256.Size]byte
}

func (installer *Installer) executableSnapshot() (executableState, error) {
	executable := installer.Executable
	if executable == nil {
		executable = os.Executable
	}
	path, err := executable()
	if err != nil {
		return executableState{}, fmt.Errorf("install Tao update: resolve running executable: %w", err)
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return executableState{}, fmt.Errorf("install Tao update: resolve executable path: %w", err)
		}
	}
	evalSymlinks := installer.EvalSymlinks
	if evalSymlinks == nil {
		evalSymlinks = filepath.EvalSymlinks
	}
	path, err = evalSymlinks(path)
	if err != nil {
		return executableState{}, fmt.Errorf("install Tao update: resolve executable symlinks: %w", err)
	}
	path = filepath.Clean(path)
	if IsHomebrewManagedPath(path) {
		return executableState{}, &HomebrewError{Path: path}
	}
	info, err := os.Stat(path)
	if err != nil {
		return executableState{}, fmt.Errorf("install Tao update: inspect executable: %w", err)
	}
	if !info.Mode().IsRegular() {
		return executableState{}, fmt.Errorf("install Tao update: executable %q is not a regular file", path)
	}
	if info.Mode().Perm()&0o111 == 0 {
		return executableState{}, fmt.Errorf("install Tao update: executable %q has no execute permission", path)
	}
	file, err := os.Open(path) // #nosec G304 -- path is the resolved running executable.
	if err != nil {
		return executableState{}, fmt.Errorf("install Tao update: read executable: %w", err)
	}
	digest, readErr := io.ReadAll(io.LimitReader(file, maxBinaryBytes+1))
	closeErr := file.Close()
	if readErr != nil {
		return executableState{}, fmt.Errorf("install Tao update: read executable: %w", readErr)
	}
	if closeErr != nil {
		return executableState{}, fmt.Errorf("install Tao update: close executable: %w", closeErr)
	}
	if len(digest) > maxBinaryBytes {
		return executableState{}, fmt.Errorf("install Tao update: executable exceeds %d bytes", maxBinaryBytes)
	}
	return executableState{path: path, mode: info.Mode(), digest: sha256.Sum256(digest)}, nil
}

func (installer *Installer) platform() (string, string) {
	goos := runtime.GOOS
	if installer.GOOS != nil {
		goos = installer.GOOS()
	}
	goarch := runtime.GOARCH
	if installer.GOARCH != nil {
		goarch = installer.GOARCH()
	}
	return goos, goarch
}

func validateInstallRelease(release Release, goos, goarch string) error {
	validated, err := selectRelease(releaseMetadata{
		TagName: release.Tag,
		Assets: []assetMetadata{
			{Name: release.Checksums.Name, BrowserDownloadURL: release.Checksums.URL},
			{Name: release.Archive.Name, BrowserDownloadURL: release.Archive.URL},
		},
	}, goos, goarch)
	if err != nil {
		return fmt.Errorf("install Tao update: validate release: %w", err)
	}
	if validated != release {
		return errors.New("install Tao update: release assets changed during validation")
	}
	return nil
}

func (installer *Installer) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	timeout := installer.RequestTimeout
	if timeout == 0 {
		timeout = defaultDownloadTimeout
	}
	if timeout < 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func (installer *Installer) response(ctx context.Context, rawURL, label string) (*http.Response, context.CancelFunc, error) {
	requestContext, cancel := installer.requestContext(ctx)
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, rawURL, nil)
	if err != nil {
		cancel()
		return nil, func() {}, fmt.Errorf("install Tao update: create %s request: %w", label, err)
	}
	request.Header.Set("User-Agent", "tao-self-update")
	client := installer.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		cancel()
		return nil, func() {}, fmt.Errorf("install Tao update: download %s: %w", label, err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		_ = response.Body.Close()
		cancel()
		return nil, func() {}, fmt.Errorf("install Tao update: download %s: server returned %s", label, response.Status)
	}
	return response, cancel, nil
}

func (installer *Installer) downloadBytes(ctx context.Context, rawURL string, limit int64, label string) ([]byte, error) {
	response, cancel, err := installer.response(ctx, rawURL, label)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer func() { _ = response.Body.Close() }()
	content, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("install Tao update: read %s: %w", label, err)
	}
	if int64(len(content)) > limit {
		return nil, fmt.Errorf("install Tao update: %s exceeds %d bytes", label, limit)
	}
	return content, nil
}

func (installer *Installer) downloadArchive(ctx context.Context, rawURL, directory string) (string, [sha256.Size]byte, error) {
	response, cancel, err := installer.response(ctx, rawURL, "release archive")
	if err != nil {
		return "", [sha256.Size]byte{}, err
	}
	defer cancel()
	defer func() { _ = response.Body.Close() }()

	file, err := os.CreateTemp(directory, ".tao-archive-*")
	if err != nil {
		return "", [sha256.Size]byte{}, fmt.Errorf("install Tao update: create archive temporary file: %w", err)
	}
	path := file.Name()
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(path)
		}
	}()

	hash := sha256.New()
	limit := installer.archiveLimit()
	limited := &io.LimitedReader{R: response.Body, N: limit + 1}
	written, err := io.Copy(io.MultiWriter(file, hash), limited)
	if err != nil {
		return "", [sha256.Size]byte{}, fmt.Errorf("install Tao update: read release archive: %w", err)
	}
	if written > limit {
		return "", [sha256.Size]byte{}, fmt.Errorf("install Tao update: release archive exceeds %d bytes", limit)
	}
	if err := file.Close(); err != nil {
		return "", [sha256.Size]byte{}, fmt.Errorf("install Tao update: close archive temporary file: %w", err)
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	keep = true
	return path, digest, nil
}

func parseArchiveChecksum(content []byte, archiveName string) ([sha256.Size]byte, error) {
	var selected [sha256.Size]byte
	seen := make(map[string]struct{})
	found := false
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		return selected, errors.New("install Tao update: checksums file is empty")
	}
	for number, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 || fields[1] == "" {
			return selected, fmt.Errorf("install Tao update: malformed checksum entry on line %d", number+1)
		}
		if _, duplicate := seen[fields[1]]; duplicate {
			return selected, fmt.Errorf("install Tao update: duplicate checksum entry for %q", fields[1])
		}
		seen[fields[1]] = struct{}{}
		digest, err := hex.DecodeString(fields[0])
		if err != nil || len(digest) != sha256.Size {
			return selected, fmt.Errorf("install Tao update: malformed checksum entry on line %d", number+1)
		}
		if fields[1] == archiveName {
			copy(selected[:], digest)
			found = true
		}
	}
	if !found {
		return selected, fmt.Errorf("install Tao update: checksum for %q is missing", archiveName)
	}
	return selected, nil
}

func extractTaoBinary(archivePath, directory string) (string, error) {
	archive, err := os.Open(archivePath) // #nosec G304 -- path is the private downloaded archive.
	if err != nil {
		return "", fmt.Errorf("install Tao update: open release archive: %w", err)
	}
	defer func() { _ = archive.Close() }()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return "", fmt.Errorf("install Tao update: open gzip archive: %w", err)
	}
	defer func() { _ = compressed.Close() }()
	limited := &io.LimitedReader{R: compressed, N: maxExpandedArchiveBytes + 1}
	reader := tar.NewReader(limited)

	var binary *os.File
	var binaryPath string
	seen := make(map[string]struct{})
	cleanup := true
	defer func() {
		if binary != nil {
			_ = binary.Close()
		}
		if cleanup && binaryPath != "" {
			_ = os.Remove(binaryPath)
		}
	}()

	for {
		header, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return "", fmt.Errorf("install Tao update: inspect release archive: %w", nextErr)
		}
		name := filepath.ToSlash(header.Name)
		if name == "" || strings.HasPrefix(name, "/") || filepath.IsAbs(header.Name) || name != filepath.ToSlash(filepath.Clean(header.Name)) || name == ".." || strings.HasPrefix(name, "../") {
			return "", fmt.Errorf("install Tao update: unsafe archive path %q", header.Name)
		}
		if _, duplicate := seen[name]; duplicate {
			return "", fmt.Errorf("install Tao update: duplicate archive entry %q", name)
		}
		seen[name] = struct{}{}

		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			return "", fmt.Errorf("install Tao update: archive entry %q is not a regular file or directory", name)
		}
		if name != "tao" {
			if header.FileInfo().Mode().Perm()&0o111 != 0 {
				return "", fmt.Errorf("install Tao update: unexpected executable archive entry %q", name)
			}
			continue
		}
		if binary != nil {
			return "", errors.New("install Tao update: release archive contains multiple tao executables")
		}
		if header.Size < 0 || header.Size > maxBinaryBytes {
			return "", fmt.Errorf("install Tao update: tao executable exceeds %d bytes", maxBinaryBytes)
		}
		if header.FileInfo().Mode().Perm()&0o111 == 0 {
			return "", errors.New("install Tao update: archived tao file is not executable")
		}
		binary, err = os.CreateTemp(directory, ".tao-update-*")
		if err != nil {
			return "", fmt.Errorf("install Tao update: create executable temporary file: %w", err)
		}
		binaryPath = binary.Name()
		written, copyErr := io.Copy(binary, reader) // #nosec G110 -- both the tar entry and expanded stream are bounded above.
		if copyErr != nil {
			return "", fmt.Errorf("install Tao update: extract tao executable: %w", copyErr)
		}
		if written != header.Size {
			return "", errors.New("install Tao update: truncated tao executable")
		}
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return "", fmt.Errorf("install Tao update: finish reading release archive: %w", err)
	}
	if limited.N == 0 {
		return "", fmt.Errorf("install Tao update: expanded archive exceeds %d bytes", maxExpandedArchiveBytes)
	}
	if binary == nil {
		return "", errors.New("install Tao update: release archive does not contain tao")
	}
	if err := binary.Close(); err != nil {
		return "", fmt.Errorf("install Tao update: close extracted executable: %w", err)
	}
	binary = nil
	cleanup = false
	return binaryPath, nil
}

func (installer *Installer) archiveLimit() int64 {
	if installer.maxArchiveBytes > 0 {
		return installer.maxArchiveBytes
	}
	return maxCompressedArchiveBytes
}

func (installer *Installer) lock(ctx context.Context, path string) (func() error, error) {
	if installer.AcquireLock != nil {
		return installer.AcquireLock(ctx, path)
	}
	return acquireFileLock(ctx, path)
}

func acquireFileLock(ctx context.Context, path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- callers choose private update lock paths.
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() error {
				unlockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				closeErr := file.Close()
				return errors.Join(unlockErr, closeErr)
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		case <-time.After(lockRetryInterval):
		}
	}
}

func (installer *Installer) chmod(path string, mode os.FileMode) error {
	if installer.Chmod != nil {
		return installer.Chmod(path, mode)
	}
	return os.Chmod(path, mode)
}

func (installer *Installer) replace(destination, temporary string) error {
	directory := filepath.Dir(destination)
	backup, err := os.CreateTemp(directory, ".tao-backup-*")
	if err != nil {
		return fmt.Errorf("install Tao update: create rollback path: %w", err)
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return fmt.Errorf("install Tao update: close rollback path: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return fmt.Errorf("install Tao update: prepare rollback path: %w", err)
	}
	defer func() { _ = os.Remove(backupPath) }()

	link := installer.Link
	if link == nil {
		link = os.Link
	}
	if err := link(destination, backupPath); err != nil {
		return fmt.Errorf("install Tao update: preserve executable for rollback: %w", err)
	}
	syncDirectory := installer.SyncDir
	if syncDirectory == nil {
		syncDirectory = atomicfile.SyncDir
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("install Tao update: sync rollback executable: %w", err)
	}
	rename := installer.Rename
	if rename == nil {
		rename = os.Rename
	}
	if err := rename(temporary, destination); err != nil {
		return fmt.Errorf("install Tao update: replace executable: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		rollbackErr := rename(backupPath, destination)
		if rollbackErr == nil {
			rollbackErr = syncDirectory(directory)
		}
		return fmt.Errorf("install Tao update: sync installed executable: %w", errors.Join(err, rollbackErr))
	}
	remove := installer.Remove
	if remove == nil {
		remove = os.Remove
	}
	if err := remove(backupPath); err != nil {
		return fmt.Errorf("install Tao update: remove rollback executable after installation: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("install Tao update: sync rollback cleanup: %w", err)
	}
	return nil
}
