package plan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestArtifactWriteErrorBranches(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(filePath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeState(filePath, State{}); err == nil || !strings.Contains(err.Error(), "write state.json") {
		t.Fatalf("expected WriteState error, got %v", err)
	}
	if err := writeSlices(filePath, SlicesFile{}); err == nil || !strings.Contains(err.Error(), "write slices.json") {
		t.Fatalf("expected WriteSlices error, got %v", err)
	}
	if err := AppendEvent(filePath, Event{Type: "x"}); err == nil {
		t.Fatal("expected AppendEvent open error")
	}
	if err := atomicWriteFile(filepath.Join(filePath, "child"), []byte("x")); err == nil {
		t.Fatal("expected atomicWriteFile error for file parent")
	}
}

func TestUnsupportedDirSyncErrors(t *testing.T) {
	for _, err := range []error{syscall.EINVAL, syscall.ENOTSUP, syscall.ENOSYS} {
		if !isUnsupportedDirSyncError(err) {
			t.Fatalf("expected %v to be unsupported", err)
		}
	}
	if isUnsupportedDirSyncError(errors.New("other")) {
		t.Fatal("ordinary errors should not be classified as unsupported dir sync")
	}
}
