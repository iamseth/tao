package plan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRunLockParsesPIDAndProbesLiveness(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, runLockFileName), []byte("pid=4242\ncreated_at=2026-08-10T15:00:00Z\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var probed int
	lock, err := readRunLock(dir, func(pid int) bool {
		probed = pid
		return false
	})
	if err != nil {
		t.Fatal(err)
	}
	if lock.PID != 4242 || lock.ProcessAlive || probed != 4242 {
		t.Fatalf("run lock = %+v, probed pid = %d", lock, probed)
	}
}

func TestReadRunLockRejectsMissingOrInvalidPID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "missing", content: "created_at=2026-08-10T15:00:00Z\n", want: "pid is missing"},
		{name: "invalid", content: "pid=not-a-pid\n", want: `pid "not-a-pid"`},
		{name: "nonpositive", content: "pid=0\n", want: `pid "0"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, runLockFileName), []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := readRunLock(dir, func(int) bool { return true })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("readRunLock() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestReadRunLockPreservesMissingFileError(t *testing.T) {
	_, err := ReadRunLock(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadRunLock() error = %v, want os.ErrNotExist", err)
	}
}
