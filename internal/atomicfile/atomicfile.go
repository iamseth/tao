// Package atomicfile owns atomic durable file replacement. It writes through a
// temporary file in the destination directory, syncs the file before rename or
// link, and syncs the destination directory after installation when supported.
package atomicfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Options configures how Write creates and installs a file.
type Options struct {
	// Perm sets the destination permissions. When zero, Write preserves an
	// existing file's permission bits and uses 0600 for a new file.
	Perm os.FileMode
	// Exclusive installs the file with a hard link so an existing destination
	// causes the write to fail.
	Exclusive bool
	// Link and Rename override the installation operations. Nil uses os.Link
	// and os.Rename, respectively.
	Link   func(oldpath, newpath string) error
	Rename func(oldpath, newpath string) error
}

// RemoveOptions configures how Remove unlinks a file. Remove uses os.Remove
// and the platform directory sync when the corresponding function is nil.
type RemoveOptions struct {
	RemoveFile func(path string) error
	SyncDir    func(path string) error
}

// PostRemoveSyncError reports that Remove successfully unlinked the file but
// could not prove the removal durable by syncing its parent directory.
type PostRemoveSyncError struct {
	err error
}

func (e *PostRemoveSyncError) Error() string {
	return fmt.Sprintf("sync destination directory: %v", e.err)
}

func (e *PostRemoveSyncError) Unwrap() error {
	return e.err
}

// IsPostRemoveSyncError reports whether err occurred after Remove successfully
// unlinked its target.
func IsPostRemoveSyncError(err error) bool {
	var syncErr *PostRemoveSyncError
	return errors.As(err, &syncErr)
}

// SyncDir syncs a directory so newly installed or removed entries are durable.
// Platforms that do not support directory sync treat the operation as complete.
func SyncDir(path string) error {
	return syncDir(path, openDir)
}

// Remove unlinks path and syncs its parent directory so the removal is durable.
func Remove(path string, opts RemoveOptions) error {
	removeFile := opts.RemoveFile
	if removeFile == nil {
		removeFile = os.Remove
	}
	if err := removeFile(path); err != nil {
		return fmt.Errorf("remove file: %w", err)
	}
	syncDirectory := opts.SyncDir
	if syncDirectory == nil {
		syncDirectory = SyncDir
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return &PostRemoveSyncError{err: err}
	}
	return nil
}

// Write atomically writes data to path according to opts.
func Write(path string, data []byte, opts Options) error {
	perm := opts.Perm
	if perm == 0 {
		perm = 0o600
		info, err := os.Stat(path)
		if err == nil {
			perm = info.Mode().Perm()
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("stat destination: %w", err)
		}
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*") // #nosec G304 -- the caller selects the destination path.
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temporary file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temporary file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary file: %w", err)
	}

	if opts.Exclusive {
		link := opts.Link
		if link == nil {
			link = os.Link
		}
		if err := link(tmpPath, path); err != nil {
			return fmt.Errorf("install file exclusively: %w", err)
		}
		if err := os.Remove(tmpPath); err != nil {
			return fmt.Errorf("remove temporary file: %w", err)
		}
	} else {
		rename := opts.Rename
		if rename == nil {
			rename = os.Rename
		}
		if err := rename(tmpPath, path); err != nil {
			return fmt.Errorf("replace file: %w", err)
		}
	}

	if err := SyncDir(dir); err != nil {
		return fmt.Errorf("sync destination directory: %w", err)
	}
	return nil
}

type dirSyncFile interface {
	Sync() error
	Close() error
}

func openDir(path string) (dirSyncFile, error) {
	return os.Open(path) // #nosec G304 -- the caller selects the destination directory.
}

func syncDir(dir string, open func(string) (dirSyncFile, error)) error {
	file, err := open(dir)
	if err != nil {
		if isUnsupportedDirSyncError(err) {
			return nil
		}
		return err
	}
	defer func() { _ = file.Close() }()
	if err := file.Sync(); err != nil && !isUnsupportedDirSyncError(err) {
		return err
	}
	return nil
}

func isUnsupportedDirSyncError(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS)
}
