package merge

import (
	"context"
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
	"time"
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
	backing, err := os.OpenFile(backingPath, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G304: backingPath is a test-owned path rooted in t.TempDir.
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backing.Close() }()
	if _, err := backing.WriteString("parent-only rollback contents\n"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(backingPath); err != nil {
		t.Fatal(err)
	}

	policy := singleMergeFilesystemConfinement{
		protectedPaths: []string{protectedRoot}, integrationRoot: integrationRoot, allowEdits: true,
	}
	requireSingleMergeSandboxProbe(t, integrationRoot, func() (string, []string, error) {
		return singleMergeFilesystemConfinementCommand(policy, runtimeRoot, "/bin/true", nil)
	})

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

// Keep availability policy test-only and separate from the boundary assertions,
// which must fail regardless of whether local sandbox availability is optional.
func requireSingleMergeSandboxProbe(t *testing.T, dir string, command func() (string, []string, error)) {
	t.Helper()
	unavailable := func(format string, args ...any) {
		t.Helper()
		if os.Getenv("TAO_REQUIRE_CONFINEMENT_TESTS") == "1" {
			t.Fatalf(format, args...)
		}
		t.Skipf(format, args...)
	}
	name, args, err := command()
	if err != nil {
		unavailable("Linux provider confinement unavailable: %v", err)
	}
	probe := exec.Command(name, args...) //nolint:gosec // test-only probe of the generated sandbox command, or a fixed regression command.
	probe.Dir = dir
	if output, err := probe.CombinedOutput(); err != nil {
		unavailable("Linux provider confinement cannot start: %v: %s", err, output)
	}
}

func TestSingleMergeSandboxAvailability(t *testing.T) {
	const helper = "TAO_TEST_SINGLE_MERGE_SANDBOX_AVAILABILITY"
	if mode := os.Getenv(helper); mode != "" {
		requireSingleMergeSandboxProbe(t, t.TempDir(), func() (string, []string, error) {
			switch mode {
			case "construction":
				return "", nil, errors.New("synthetic command construction failure")
			case "startup":
				return "/bin/sh", []string{"-c", "printf 'synthetic startup diagnostic' >&2; exit 23"}, nil
			default:
				t.Fatalf("unknown availability helper mode %q", mode)
				return "", nil, nil
			}
		})
		t.Fatal("unavailable probe unexpectedly returned")
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range []struct {
		name       string
		diagnostic string
	}{
		{"construction", "Linux provider confinement unavailable: synthetic command construction failure"},
		{"startup", "Linux provider confinement cannot start: exit status 23: synthetic startup diagnostic"},
	} {
		for _, gate := range []struct {
			name     string
			value    string
			required bool
		}{
			{name: "unset"},
			{name: "empty"},
			{name: "zero", value: "0"},
			{name: "other", value: "true"},
			{name: "required", value: "1", required: true},
		} {
			t.Run(site.name+"/"+gate.name, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, executable, "-test.run=^TestSingleMergeSandboxAvailability$", "-test.v")
				// Explicitly remove inherited helper modes and the CI gate so optional
				// cases really exercise the default even in required-mode CI.
				for _, entry := range os.Environ() {
					key, _, _ := strings.Cut(entry, "=")
					if key != helper && key != singleMergePIDConfinementHelper && key != "TAO_REQUIRE_CONFINEMENT_TESTS" {
						command.Env = append(command.Env, entry)
					}
				}
				command.Env = append(command.Env, helper+"="+site.name)
				if gate.name != "unset" {
					command.Env = append(command.Env, "TAO_REQUIRE_CONFINEMENT_TESTS="+gate.value)
				}
				output, err := command.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("availability helper timed out: %v\n%s", ctx.Err(), output)
				}
				want, unwanted := "--- SKIP: TestSingleMergeSandboxAvailability", "--- FAIL: TestSingleMergeSandboxAvailability"
				if gate.required {
					want, unwanted = unwanted, want
					var exit *exec.ExitError
					if !errors.As(err, &exit) || exit.ExitCode() != 1 {
						t.Fatalf("required mode must exit 1, got %v\n%s", err, output)
					}
				} else if err != nil {
					t.Fatalf("optional mode must exit successfully: %v\n%s", err, output)
				}
				text := string(output)
				if !strings.Contains(text, want) || strings.Contains(text, unwanted) || !strings.Contains(text, site.diagnostic) {
					t.Fatalf("want %q with %q and no %q, got:\n%s", want, site.diagnostic, unwanted, text)
				}
			})
		}
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
