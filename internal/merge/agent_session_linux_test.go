package merge

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const singleMergePIDConfinementHelper = "TAO_TEST_SINGLE_MERGE_PID_CONFINEMENT_HELPER"

func TestSingleMergeProcessSandboxHidesTaoParent(t *testing.T) {
	if os.Getenv(singleMergePIDConfinementHelper) == "1" {
		assertTaoParentIsOutsidePIDNamespace(t)
		return
	}

	parentSignals := make(chan os.Signal, 1)
	signal.Notify(parentSignals, syscall.SIGUSR1)
	defer signal.Stop(parentSignals)

	root := t.TempDir()
	integrationRoot := filepath.Join(root, "integration")
	protectedRoot := filepath.Join(root, "protected")
	runtimeRoot := filepath.Join(root, "runtime")
	for _, path := range []string{
		integrationRoot,
		protectedRoot,
		runtimeRoot,
		filepath.Join(runtimeRoot, "cache"),
		filepath.Join(runtimeRoot, "state"),
	} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	// Model Tao's unlinked rollback backing: its contents have no filesystem
	// name and are reachable only through the parent process's open descriptor.
	backingPath := filepath.Join(root, "rollback-backing")
	backing, err := os.OpenFile(backingPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer backing.Close()
	if _, err := backing.WriteString("parent-only rollback contents\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(backingPath); err != nil {
		t.Fatal(err)
	}

	policy := singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}
	probeName, probeArgs, err := singleMergeFilesystemConfinementCommand(policy, runtimeRoot, "/bin/true", nil)
	if err != nil {
		t.Skipf("Linux provider confinement unavailable: %v", err)
	}
	probe := exec.Command(probeName, probeArgs...) //nolint:gosec // fixed test executable probes Tao's generated sandbox command.
	probe.Dir = integrationRoot
	if output, err := probe.CombinedOutput(); err != nil {
		t.Skipf("Linux provider confinement cannot start: %v: %s", err, output)
	}

	t.Setenv(singleMergePIDConfinementHelper, "1")
	t.Setenv("TAO_TEST_HOST_PARENT_PID", strconv.Itoa(os.Getpid()))
	t.Setenv("TAO_TEST_HOST_PARENT_FD", strconv.FormatUint(uint64(backing.Fd()), 10))
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name, args, err := singleMergeFilesystemConfinementCommand(policy, runtimeRoot, executable, []string{
		"-test.run=^TestSingleMergeProcessSandboxHidesTaoParent$",
	})
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(name, args...) //nolint:gosec // the current test binary probes Tao's generated sandbox command.
	command.Dir = integrationRoot
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("private PID confinement failed: %v:\n%s", err, output)
	}
}

func assertTaoParentIsOutsidePIDNamespace(t *testing.T) {
	t.Helper()
	parentPID, err := strconv.Atoi(os.Getenv("TAO_TEST_HOST_PARENT_PID"))
	if err != nil || parentPID < 1 {
		t.Fatalf("invalid host parent PID: %q", os.Getenv("TAO_TEST_HOST_PARENT_PID"))
	}
	parentFD, err := strconv.Atoi(os.Getenv("TAO_TEST_HOST_PARENT_FD"))
	if err != nil || parentFD < 0 {
		t.Fatalf("invalid host parent FD: %q", os.Getenv("TAO_TEST_HOST_PARENT_FD"))
	}

	procRoot := filepath.Join("/proc", strconv.Itoa(parentPID))
	var failures []string
	if _, err := os.Stat(procRoot); !errors.Is(err, os.ErrNotExist) {
		failures = append(failures, fmt.Sprintf("discovered Tao parent at %s: %v", procRoot, err))
	}
	for _, path := range []string{
		filepath.Join(procRoot, "mem"),
		filepath.Join(procRoot, "fd", strconv.Itoa(parentFD)),
	} {
		file, err := os.Open(path) //nolint:gosec // hostile-process probe must attempt these parent /proc paths.
		if err == nil {
			_ = file.Close()
			failures = append(failures, "opened Tao parent path "+path)
		}
	}
	if err := syscall.Kill(parentPID, syscall.SIGUSR1); !errors.Is(err, syscall.ESRCH) {
		failures = append(failures, fmt.Sprintf("signal reached visible Tao parent PID: %v", err))
	}
	if err := syscall.PtraceAttach(parentPID); err == nil {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(parentPID, &status, 0, nil)
		_ = syscall.PtraceDetach(parentPID)
		failures = append(failures, "attached to Tao parent with ptrace")
	} else if !errors.Is(err, syscall.ESRCH) {
		failures = append(failures, fmt.Sprintf("Tao parent remained ptrace-addressable: %v", err))
	}
	if len(failures) > 0 {
		t.Fatal(strings.Join(failures, "; "))
	}
}
