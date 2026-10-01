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
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	if err := DefaultLocal(context.Background(), "", "sh", []string{"-c", `sleep 30 >/dev/null 2>&1 & echo $! > "$1"`, "sh", pidPath}, io.Discard, io.Discard); err != nil {
		t.Fatalf("DefaultLocal failed: %v", err)
	}
	assertCommandDescendantGone(t, pidPath)
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
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
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
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		result <- DefaultLocal(ctx, "", "sh", []string{"-c", `sleep 30 & echo $! > "$1"`, "sh", pidPath}, io.Discard, io.Discard)
	}()
	select {
	case err := <-result:
		if !errors.Is(err, exec.ErrWaitDelay) {
			t.Fatalf("incomplete output error = %v, want ErrWaitDelay", err)
		}
		assertCommandDescendantGone(t, pidPath)
	case <-time.After(2 * time.Second):
		cancel()
		<-result
		t.Fatal("command completion hung on inherited output pipes")
	}
}

func TestDefaultLocalCancellationKillsDescendants(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result := make(chan error, 1)
	go func() {
		var stdout bytes.Buffer
		result <- DefaultLocal(ctx, "", "sh", []string{"-c", `sleep 30 & echo $! > "$1"; wait`, "sh", pidPath}, &stdout, io.Discard)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		// File creation precedes the shell's PID write; cancel only after publication.
		if data, err := os.ReadFile(pidPath); err == nil { //nolint:gosec // G304: test-owned temporary PID file.
			if strings.HasSuffix(string(data), "\n") {
				break
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("inspect child pid file: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("child command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}

	cancel()
	select {
	case err := <-result:
		if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("DefaultLocal returned %v with context error %v, want cancellation failure", err, ctx.Err())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DefaultLocal did not terminate after cancellation")
	}
	assertCommandDescendantGone(t, pidPath)
}

func TestDefaultLocalTimeoutKillsDescendants(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := DefaultLocal(ctx, "", "sh", []string{"-c", `sleep 30 & echo $! > "$1"; wait`, "sh", pidPath}, io.Discard, io.Discard)
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
		t.Fatalf("timeout failed: %v, %v", err, ctx.Err())
	}
	assertCommandDescendantGone(t, pidPath)
}

func TestDefaultLocalVerificationCacheProcessLifecycle(t *testing.T) {
	for _, mode := range []string{"cancellation", "timeout", "detached", "inherited-pipes"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			pidPath := filepath.Join(root, "child.pid")
			ctx := WithVerificationCache(context.Background(), root)
			var cancel context.CancelFunc
			if mode == "timeout" {
				ctx, cancel = context.WithTimeout(ctx, 200*time.Millisecond)
			} else {
				ctx, cancel = context.WithCancel(ctx)
			}
			defer cancel()
			script := `sleep 30 & echo $! > "$1"; wait`
			switch mode {
			case "detached":
				script = `sleep 30 >/dev/null 2>&1 & echo $! > "$1"`
			case "inherited-pipes":
				script = `sleep 30 & echo $! > "$1"`
			}
			result := make(chan error, 1)
			go func() {
				result <- DefaultLocal(ctx, root, "sh", []string{"-c", script, "sh", pidPath}, io.Discard, io.Discard)
			}()
			if mode == "cancellation" {
				deadline := time.Now().Add(5 * time.Second)
				for {
					if data, err := os.ReadFile(pidPath); err == nil { //nolint:gosec // G304: test-owned temporary PID file.
						if strings.HasSuffix(string(data), "\n") {
							break
						}
					}
					if time.Now().After(deadline) {
						t.Fatal("child command did not start")
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
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
					if err == nil || ctx.Err() == nil {
						t.Fatalf("cancellation failed: %v, %v", err, ctx.Err())
					}
				}
			case <-time.After(2 * time.Second):
				cancel()
				<-result
				t.Fatal("opt-in command did not finish promptly")
			}
			assertCommandDescendantGone(t, pidPath)
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
	for _, path := range []string{".tao", ".tao/cache", ".tao/cache/golangci-lint", ".tao/cache/golangci-lint/.gitignore"} {
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
	cache := filepath.Join(root, ".tao", "cache", "golangci-lint")
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
