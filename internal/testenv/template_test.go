package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRepoTemplateCopiesIndependentlyAndBuildsOnce(t *testing.T) {
	builds := 0
	template := NewRepoTemplate(func(dir string) error {
		builds++
		if err := os.Mkdir(filepath.Join(dir, "nested"), 0o750); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, "nested", "file"), []byte("base\n"), 0o600)
	})
	t.Cleanup(func() {
		if err := template.Cleanup(); err != nil {
			t.Error(err)
		}
	})
	first, second := filepath.Join(t.TempDir(), "first"), filepath.Join(t.TempDir(), "second")
	for _, dest := range []string{first, second} {
		if copied, err := template.CopyTo(dest); err != nil || !copied {
			t.Fatalf("copy = %v, %v", copied, err)
		}
	}
	if builds != 1 {
		t.Fatalf("builds = %d", builds)
	}
	if err := os.WriteFile(filepath.Join(first, "nested", "file"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(second, "nested", "file")) // #nosec G304 -- test-owned path.
	if err != nil || string(data) != "base\n" {
		t.Fatalf("copies share content: %q, %v", data, err)
	}
	info, err := os.Stat(filepath.Join(second, "nested"))
	if err != nil || info.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %v, %v", info, err)
	}
	root := template.root
	if err := template.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("template remains: %v", err)
	}
}

func TestRepoTemplateRefusesChangedGitEnvironment(t *testing.T) {
	template := NewRepoTemplate(func(string) error { t.Fatal("template built under a changed environment"); return nil })
	t.Setenv("GIT_CONFIG_COUNT", "1")
	if copied, err := template.CopyTo(filepath.Join(t.TempDir(), "dest")); copied || err != nil {
		t.Fatalf("copy = %v, %v", copied, err)
	}
}

func TestCopyTreeRejectsSymlinks(t *testing.T) {
	source := t.TempDir()
	if err := os.Symlink("elsewhere", filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	if err := CopyTree(source, filepath.Join(t.TempDir(), "dest")); err == nil {
		t.Fatal("copied a symlink")
	}
}
