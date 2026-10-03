package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWithDetachedCheckout(t *testing.T) {
	root := t.TempDir()
	runGitCommand(t, root, "init")
	runGitCommand(t, root, "config", "user.name", "Test")
	runGitCommand(t, root, "config", "user.email", "test@example.com")
	path := filepath.Join(root, "tree")
	if err := os.WriteFile(path, []byte("base"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, root, "add", ".")
	runGitCommand(t, root, "commit", "-m", "base")
	base := gitOutput(t, root, "rev-parse", "HEAD")
	if err := os.WriteFile(path, []byte("head"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, root, "commit", "-am", "head")
	client := NewClient(root, nil)
	for _, fail := range []bool{false, true} {
		var dir string
		sentinel := errors.New("callback failure")
		err := client.WithDetachedCheckout(context.Background(), base, func(tmp string) error {
			dir = tmp
			data, err := os.ReadFile(filepath.Join(tmp, "tree")) //nolint:gosec // Test-owned temporary checkout.
			if err != nil || string(data) != "base" {
				t.Fatalf("tree=%q err=%v", data, err)
			}
			if fail {
				return sentinel
			}
			return nil
		})
		if fail && !errors.Is(err, sentinel) || !fail && err != nil {
			t.Fatal(err)
		}
		if dir == "" {
			t.Fatal("callback not called")
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("checkout remains: %v", err)
		}
	}
	if err := client.WithDetachedCheckout(context.Background(), "unknown-sha", func(string) error { t.Fatal("unexpected callback"); return nil }); err == nil {
		t.Fatal("expected resolution error")
	}
}
