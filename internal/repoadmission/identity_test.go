package repoadmission

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/iamseth/tao/internal/testenv"
)

func TestIdentity(t *testing.T) {
	testenv.SkipIfShort(t)
	root := newRepo(t)
	git(t, root, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	linked := filepath.Join(t.TempDir(), "linked")
	git(t, root, "worktree", "add", "--detach", linked)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, root)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := identity(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{relative, alias, linked} {
		actual, err := identity(context.Background(), path)
		if err != nil || actual != expected {
			t.Fatalf("%s: %s %v, want %s", path, actual, err, expected)
		}
		err = With(context.Background(), Request{Root: root, Mode: Run}, func(context.Context) error {
			if err := With(context.Background(), Request{Root: path, Mode: Merge}, func(context.Context) error { return nil }); err == nil {
				t.Fatal("identity alias bypassed admission")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"", t.TempDir(), filepath.Join(root, "missing")} {
		if _, err := identity(context.Background(), path); err == nil {
			t.Fatalf("accepted missing identity %q", path)
		}
	}
}

func TestIdentityRejectsInvalidOutput(t *testing.T) {
	original := identityGit
	t.Cleanup(func() { identityGit = original })
	for _, output := range []string{"", "relative\n", "/missing/tao/identity\n"} {
		identityGit = func(context.Context, string) ([]byte, error) { return []byte(output), nil }
		if _, err := identity(context.Background(), "root"); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
}
