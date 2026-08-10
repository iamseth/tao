package plan

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const runLockFileName = ".run.lock"

// RunLock is the read-only process information recorded in a plan's .run.lock.
type RunLock struct {
	PID          int
	ProcessAlive bool
}

// ReadRunLock reads a plan's run lock and probes whether its recorded process
// is alive. A missing lock is returned as an os.ErrNotExist error.
func ReadRunLock(planDir string) (RunLock, error) {
	return readRunLock(planDir, runLockProcessAlive)
}

func readRunLock(planDir string, processAlive func(int) bool) (RunLock, error) {
	if strings.TrimSpace(planDir) == "" {
		return RunLock{}, errors.New("read plan run lock: plan dir is empty")
	}
	path, err := filepath.Abs(planDir)
	if err != nil {
		return RunLock{}, fmt.Errorf("resolve plan run lock path for %q: %w", planDir, err)
	}
	content, err := os.ReadFile(filepath.Join(path, runLockFileName)) // #nosec G304 -- plan lock path is inside the resolved Tao plan data directory.
	if err != nil {
		return RunLock{}, err
	}
	pid, err := parseRunLockPID(content)
	if err != nil {
		return RunLock{}, err
	}
	return RunLock{PID: pid, ProcessAlive: processAlive(pid)}, nil
}

func parseRunLockPID(content []byte) (int, error) {
	for line := range strings.SplitSeq(string(content), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || key != "pid" {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || pid <= 0 {
			return 0, fmt.Errorf("parse plan run lock pid %q", strings.TrimSpace(value))
		}
		return pid, nil
	}
	return 0, errors.New("parse plan run lock: pid is missing")
}

func runLockProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	defer func() { _ = process.Release() }()
	if err := process.Signal(syscall.Signal(0)); err != nil {
		if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
			return false
		}
		return true
	}
	return true
}
