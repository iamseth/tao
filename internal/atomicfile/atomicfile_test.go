package atomicfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestWritePreservesExistingPermissionsAndReplacesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o640); err != nil { //nolint:gosec // The test verifies preservation of group-readable permissions.
		t.Fatal(err)
	}

	want := []byte("new\x00content\n")
	if err := Write(path, want, Options{}); err != nil {
		t.Fatal(err)
	}

	assertFile(t, path, want, 0o640)
	assertNoTempFiles(t, dir)
}

func TestWritePermissionsForNewFile(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		perm os.FileMode
	}{
		{name: "default", perm: 0o600},
		{name: "explicit", opts: Options{Perm: 0o640}, perm: 0o640},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "artifact")
			want := []byte("exact content")

			if err := Write(path, want, tt.opts); err != nil {
				t.Fatal(err)
			}

			assertFile(t, path, want, tt.perm)
			assertNoTempFiles(t, dir)
		})
	}
}

func TestWriteExclusiveExistingFileFailsWithoutReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.json")
	original := []byte("original")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	err := Write(path, []byte("replacement"), Options{Exclusive: true})
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("expected os.ErrExist, got %v", err)
	}

	assertFile(t, path, original, 0o600)
	assertNoTempFiles(t, dir)
}

func TestWriteExclusiveSuccessRemovesTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "note.json")
	want := []byte("new note")

	if err := Write(path, want, Options{Exclusive: true}); err != nil {
		t.Fatal(err)
	}

	assertFile(t, path, want, 0o600)
	assertNoTempFiles(t, dir)
}

func TestRemoveUnlinksFileDurably(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.json")
	if err := os.WriteFile(path, []byte("intent"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := Remove(path, RemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed file stat error = %v, want os.ErrNotExist", err)
	}
}

func TestRemoveFailureLeavesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.json")
	if err := os.WriteFile(path, []byte("intent"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected remove failure")
	called := false

	err := Remove(path, RemoveOptions{RemoveFile: func(got string) error {
		called = true
		if got != path {
			t.Fatalf("remove path = %q, want %q", got, path)
		}
		return wantErr
	}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("remove error = %v, want %v", err, wantErr)
	}
	if IsPostRemoveSyncError(err) {
		t.Fatalf("remove failure was classified as post-unlink: %v", err)
	}
	if !called {
		t.Fatal("remove hook was not called")
	}
	assertFile(t, path, []byte("intent"), 0o600)
}

func TestRemoveClassifiesDirectorySyncFailureAfterUnlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "journal.json")
	if err := os.WriteFile(path, []byte("intent"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("injected directory sync failure")
	called := false

	err := Remove(path, RemoveOptions{SyncDir: func(got string) error {
		called = true
		if got != dir {
			t.Fatalf("sync path = %q, want %q", got, dir)
		}
		return wantErr
	}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("remove error = %v, want %v", err, wantErr)
	}
	if !IsPostRemoveSyncError(err) {
		t.Fatalf("remove error was not classified as post-unlink: %v", err)
	}
	if !called {
		t.Fatal("directory sync hook was not called")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("removed file stat error = %v, want os.ErrNotExist", statErr)
	}
}

func TestWriteInstallHookErrorsPropagateAndCleanUp(t *testing.T) {
	tests := []struct {
		name      string
		exclusive bool
		options   func(error, *bool) Options
	}{
		{
			name:      "link",
			exclusive: true,
			options: func(wantErr error, called *bool) Options {
				return Options{Exclusive: true, Link: func(oldpath, newpath string) error {
					*called = true
					assertHookPaths(t, oldpath, newpath)
					return wantErr
				}}
			},
		},
		{
			name: "rename",
			options: func(wantErr error, called *bool) Options {
				return Options{Rename: func(oldpath, newpath string) error {
					*called = true
					assertHookPaths(t, oldpath, newpath)
					return wantErr
				}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "record.json")
			wantErr := errors.New("injected install failure")
			called := false
			opts := tt.options(wantErr, &called)
			opts.Exclusive = tt.exclusive

			err := Write(path, []byte("content"), opts)
			if !errors.Is(err, wantErr) {
				t.Fatalf("expected injected error, got %v", err)
			}
			if !called {
				t.Fatalf("expected %s hook to be called", tt.name)
			}
			if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("destination unexpectedly installed: %v", err)
			}
			assertNoTempFiles(t, dir)
		})
	}
}

func TestSyncDirErrorHandling(t *testing.T) {
	ordinaryOpenErr := errors.New("open failed")
	ordinarySyncErr := errors.New("sync failed")
	tests := []struct {
		name     string
		openErr  error
		syncErr  error
		wantErr  error
		wantSync bool
	}{
		{name: "open error", openErr: ordinaryOpenErr, wantErr: ordinaryOpenErr},
		{name: "sync error", syncErr: ordinarySyncErr, wantErr: ordinarySyncErr, wantSync: true},
		{name: "open EINVAL unsupported", openErr: syscall.EINVAL},
		{name: "open ENOTSUP unsupported", openErr: syscall.ENOTSUP},
		{name: "open ENOSYS unsupported", openErr: syscall.ENOSYS},
		{name: "sync EINVAL unsupported", syncErr: syscall.EINVAL, wantSync: true},
		{name: "sync ENOTSUP unsupported", syncErr: syscall.ENOTSUP, wantSync: true},
		{name: "sync ENOSYS unsupported", syncErr: syscall.ENOSYS, wantSync: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &stubDirSyncFile{syncErr: tt.syncErr}
			opened := false
			err := syncDir("destination", func(path string) (dirSyncFile, error) {
				opened = true
				if path != "destination" {
					t.Fatalf("opened path = %q, want destination", path)
				}
				if tt.openErr != nil {
					return nil, tt.openErr
				}
				return file, nil
			})

			if tt.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if !opened {
				t.Fatal("directory open function was not called")
			}
			if file.synced != tt.wantSync {
				t.Fatalf("synced = %t, want %t", file.synced, tt.wantSync)
			}
			if tt.openErr == nil && !file.closed {
				t.Fatal("opened directory was not closed")
			}
		})
	}
}

type stubDirSyncFile struct {
	syncErr error
	synced  bool
	closed  bool
}

func (f *stubDirSyncFile) Sync() error {
	f.synced = true
	return f.syncErr
}

func (f *stubDirSyncFile) Close() error {
	f.closed = true
	return nil
}

func assertHookPaths(t *testing.T, oldpath, newpath string) {
	t.Helper()
	if filepath.Dir(oldpath) != filepath.Dir(newpath) {
		t.Fatalf("temporary file is not in destination directory: %q, %q", oldpath, newpath)
	}
	wantPrefix := "." + filepath.Base(newpath) + ".tmp-"
	if !strings.HasPrefix(filepath.Base(oldpath), wantPrefix) {
		t.Fatalf("temporary file %q does not have prefix %q", oldpath, wantPrefix)
	}
}

func assertFile(t *testing.T, path string, want []byte, wantPerm os.FileMode) {
	t.Helper()
	got, err := os.ReadFile(path) // #nosec G304 -- tests construct path in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("content mismatch: got %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != wantPerm {
		t.Fatalf("permissions = %04o, want %04o", info.Mode().Perm(), wantPerm)
	}
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file remains: %s", entry.Name())
		}
	}
}
