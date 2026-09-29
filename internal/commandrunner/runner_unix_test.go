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
		if _, err := os.Stat(pidPath); err == nil {
			break
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
