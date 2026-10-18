//go:build unix

package commandrunner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestDefaultLocalCompletionKillsDetachedDescendants(t *testing.T) {
	pidPath := commandTestPIDPath(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	commandResult(t, cancel, result)
	go func() {
		defer close(result)
		result <- DefaultLocal(ctx, "", "sh", []string{"-c", `sleep 3600 >/dev/null 2>&1 & echo $! > "$1"`, "sh", pidPath}, io.Discard, io.Discard)
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("DefaultLocal failed: %v", err)
		}
	case <-time.After(commandTestWait):
		t.Fatal("detached command did not finish")
	}
	assertCommandDescendantGone(t, pidPath)
}

// Children outlive every observation budget; only runner cleanup (or failure
// cleanup registered before launch) can end them, never natural fixture expiry.
const commandTestWait = 30 * time.Second

func commandTestPIDPath(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(root, "child.pid")
	t.Cleanup(func() {
		data, err := os.ReadFile(path) //nolint:gosec // G304: test-owned PID file.
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	return path
}

func waitCommandPID(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(commandTestWait)
	for {
		data, err := os.ReadFile(path) //nolint:gosec // G304: test-owned PID file.
		if err == nil && strings.HasSuffix(string(data), "\n") {
			return
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect child pid file: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("child command did not publish its PID")
		}
		// PID publication and process disappearance have no portable event.
		time.Sleep(10 * time.Millisecond)
	}
}

// A test-owned deadline is armed only after startup, so its short behavioral
// stimulus cannot be consumed by scheduling or verification identity probes.
type commandDeadlineContext struct{ context.Context }

func (c commandDeadlineContext) Err() error { return context.Cause(c.Context) }

func commandResult[T any](t *testing.T, cancel context.CancelFunc, result <-chan T) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		select {
		case <-result:
		case <-time.After(commandTestWait):
			t.Error("command result did not drain during cleanup")
		}
	})
}

func assertCommandDescendantGone(t *testing.T, pidPath string) {
	t.Helper()
	pidText, err := os.ReadFile(pidPath) //nolint:gosec // G304: path is a test-owned temporary file.
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidText)))
	if err != nil {
		t.Fatalf("parse child pid: %v", err)
	}
	deadline := time.Now().Add(commandTestWait)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			// Disarm fallback cleanup before this PID can be reused.
			if err := os.Remove(pidPath); err != nil {
				t.Fatal(err)
			}
			break
		}
		if err != nil {
			t.Fatalf("inspect detached child: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("detached child %d survived command completion", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDefaultLocalCompletionBoundsInheritedPipes(t *testing.T) {
	pidPath := commandTestPIDPath(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	commandResult(t, cancel, result)
	go func() {
		defer close(result)
		result <- DefaultLocal(ctx, "", "sh", []string{"-c", `sleep 3600 & echo $! > "$1"`, "sh", pidPath}, io.Discard, io.Discard)
	}()
	select {
	case err := <-result:
		if !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("incomplete output error = %v, want ErrWaitDelay", err)
		}
		assertCommandDescendantGone(t, pidPath)
	case <-time.After(commandTestWait):
		t.Fatal("command completion hung on inherited output pipes")
	}
}

func TestDefaultLocalCancellationKillsDescendants(t *testing.T) {
	pidPath := commandTestPIDPath(t, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan error, 1)
	commandResult(t, cancel, result)
	go func() {
		defer close(result)
		var stdout bytes.Buffer
		result <- DefaultLocal(ctx, "", "sh", []string{"-c", `sleep 3600 & echo $! > "$1"; wait`, "sh", pidPath}, &stdout, io.Discard)
	}()

	waitCommandPID(t, pidPath)

	cancel()
	select {
	case err := <-result:
		if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("DefaultLocal returned %v with context error %v, want cancellation failure", err, ctx.Err())
		}
	case <-time.After(commandTestWait):
		t.Fatal("DefaultLocal did not terminate after cancellation")
	}
	assertCommandDescendantGone(t, pidPath)
}

func TestDefaultLocalTimeoutKillsDescendants(t *testing.T) {
	pidPath := commandTestPIDPath(t, t.TempDir())
	base, expire := context.WithCancelCause(context.Background())
	ctx := commandDeadlineContext{base}
	cancel := func() { expire(context.Canceled) }
	result := make(chan error, 1)
	commandResult(t, cancel, result)
	go func() {
		defer close(result)
		result <- DefaultLocal(ctx, "", "sh", []string{"-c", `sleep 3600 & echo $! > "$1"; wait`, "sh", pidPath}, io.Discard, io.Discard)
	}()
	waitCommandPID(t, pidPath)
	timer := time.AfterFunc(200*time.Millisecond, func() { expire(context.DeadlineExceeded) })
	defer timer.Stop()
	select {
	case err := <-result:
		if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Fatalf("timeout failed: %v, %v", err, ctx.Err())
		}
	case <-time.After(commandTestWait):
		t.Fatal("DefaultLocal did not terminate after deadline")
	}
	assertCommandDescendantGone(t, pidPath)
}

func TestDefaultLocalVerificationCacheProcessLifecycle(t *testing.T) {
	setupIdentityTools(t)
	for _, mode := range []string{"cancellation", "timeout", "detached", "inherited-pipes"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			pidPath := commandTestPIDPath(t, root)
			ctx := WithVerificationCache(context.Background(), root)
			base, expire := context.WithCancelCause(ctx)
			ctx = commandDeadlineContext{base}
			cancel := func() { expire(context.Canceled) }
			defer cancel()
			script := `sleep 3600 & echo $! > "$1"; wait`
			switch mode {
			case "detached":
				script = `sleep 3600 >/dev/null 2>&1 & echo $! > "$1"`
			case "inherited-pipes":
				script = `sleep 3600 & echo $! > "$1"`
			}
			result := make(chan error, 1)
			commandResult(t, cancel, result)
			go func() {
				defer close(result)
				result <- DefaultLocal(ctx, root, "sh", []string{"-c", script, "sh", pidPath}, io.Discard, io.Discard)
			}()
			if mode == "cancellation" || mode == "timeout" {
				waitCommandPID(t, pidPath)
				if mode == "timeout" {
					timer := time.AfterFunc(200*time.Millisecond, func() { expire(context.DeadlineExceeded) })
					defer timer.Stop()
				} else {
					cancel()
				}
			}
			select {
			case err := <-result:
				switch mode {
				case "detached":
					if err != nil {
						t.Fatal(err)
					}
				case "inherited-pipes":
					if !errors.Is(err, exec.ErrWaitDelay) {
						t.Fatalf("want ErrWaitDelay, got %v", err)
					}
				default:
					want := context.Canceled
					if mode == "timeout" {
						want = context.DeadlineExceeded
					}
					if err == nil || !errors.Is(ctx.Err(), want) {
						t.Fatalf("cancellation failed: %v, %v", err, ctx.Err())
					}
				}
			case <-time.After(commandTestWait):
				t.Fatal("opt-in command did not finish promptly")
			}
			assertCommandDescendantGone(t, pidPath)
		})
	}
}

func TestVerificationIdentityDescendants(t *testing.T) {
	for _, mode := range []string{"timeout", "cancel", "pipes", "detached"} {
		t.Run(mode, func(t *testing.T) {
			root, bin := t.TempDir(), t.TempDir()
			pid := commandTestPIDPath(t, root)
			script := "sleep 3600 & echo $! > \"$PID_FILE\"; wait"
			if mode == "pipes" {
				script = "sleep 3600 & echo $! > \"$PID_FILE\"; echo go1.26.2"
			}
			if mode == "detached" {
				script = "sleep 3600 >/dev/null 2>&1 & echo $! > \"$PID_FILE\"; echo go1.26.2"
			}
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil { //nolint:gosec // G306: private executable test fixture.
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(WithVerificationCache(context.Background(), root))
			defer cancel()
			// Cancellation must follow child publication, not race the
			// production probe deadline. Timeout coverage retains that bound.
			timeout := 2 * commandTestWait
			if mode == "timeout" {
				timeout = time.Second // Behavioral probe timeout, including startup.
			}
			result := make(chan bool, 1)
			commandResult(t, cancel, result)
			go func() {
				defer close(result)
				_, ok := verificationIdentityWithTimeout(ctx, timeout, root, []string{"PATH=" + bin + ":/bin:/usr/bin", "PID_FILE=" + pid}, "go", "env", "GOVERSION")
				result <- ok
			}()
			if mode == "cancel" {
				waitCommandPID(t, pid)
				cancel()
			}
			select {
			case ok := <-result:
				if (mode == "cancel" || mode == "timeout" || mode == "pipes") && ok {
					t.Fatal("incomplete probe returned a usable identity")
				}
			case <-time.After(commandTestWait):
				t.Fatal("probe hung")
			}
			if mode == "cancel" {
				if _, err := verificationCacheEnvironmentAt(ctx, root, nil); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			}
			if mode == "timeout" {
				// The one-second probe bound includes process startup. If it
				// expires before publication, no child cleanup can be asserted.
				if _, err := os.Stat(pid); errors.Is(err, os.ErrNotExist) {
					return
				}
			}
			assertCommandDescendantGone(t, pid)
		})
	}
}

func TestVerificationCachePrivateAndInaccessibleStorage(t *testing.T) {
	setupCacheChild(t)
	root := t.TempDir()
	ctx := WithVerificationCache(context.Background(), root)
	if _, err := cacheChild(ctx, root); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{".tao", ".tao/cache", ".tao/cache/golangci-lint", ".tao/cache/golangci-lint/.gitignore", ".tao/cache/golangci-lint/" + testCacheNamespace} {
		info, err := os.Stat(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0700)
		if !info.IsDir() {
			want = 0600
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s permissions = %o, want %o", path, info.Mode().Perm(), want)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses permission checks")
	}
	cache := filepath.Join(root, ".tao", "cache", "golangci-lint", testCacheNamespace)
	if err := os.Chmod(cache, 0500); err != nil { //nolint:gosec // G302: owner-only directory traversal is required for this permission test.
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cache, 0700) }) //nolint:gosec // G302: restore owner-only directory access for test cleanup.
	marker := filepath.Join(root, "launched")
	t.Setenv("TAO_CACHE_TEST_MARKER", marker)
	if _, err := cacheChild(ctx, root); err == nil || !strings.Contains(err.Error(), "verification cache") {
		t.Fatalf("want inaccessible storage error, got %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("command launched with inaccessible storage: %v", err)
	}
}

func TestDefaultLocalStripsSliceCompletionOwnerToken(t *testing.T) {
	t.Setenv(SliceCompletionOwnerEnv, "leaked-owner")
	t.Setenv("TAO_RUNNER_KEEP", "kept")
	var stdout bytes.Buffer
	err := DefaultLocal(context.Background(), "", "sh", []string{"-c", `test -z "${TAO_SLICE_COMPLETION_OWNER+x}" && printf '%s' "$TAO_RUNNER_KEEP"`}, &stdout, io.Discard)
	if err != nil {
		t.Fatalf("spawned command still saw %s: %v", SliceCompletionOwnerEnv, err)
	}
	if stdout.String() != "kept" {
		t.Fatalf("unrelated environment was not passed through: %q", stdout.String())
	}
}
