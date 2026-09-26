package steal

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func localGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...) // #nosec G204 -- fixed local Git commands and test-owned arguments.
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFixtureFile(t *testing.T, root, name string, content []byte) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil { // #nosec G301 -- test fixture directories.
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0755); err != nil { // #nosec G306 -- executable fixture mode exercises hardening.
		t.Fatal(err)
	}
}

// All Git operations in these fixtures are local; no remote transport is used.
func snapshotFixture(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	parent := t.TempDir()
	repo := filepath.Join(parent, "source")
	if err := os.Mkdir(repo, 0755); err != nil { // #nosec G301 -- test fixture repository.
		t.Fatal(err)
	}
	localGit(t, repo, "init", "--initial-branch=main")
	writeFixtureFile(t, repo, "VERSION", []byte("1.2.3\n"))
	writeFixtureFile(t, repo, "binary.dat", []byte("text\x00bytes"))
	writeFixtureFile(t, repo, "node_modules/pkg/index.js", []byte("do not execute"))
	writeFixtureFile(t, repo, "nested/vendor/data", []byte("generated"))
	if err := os.Symlink("VERSION", filepath.Join(repo, "link")); err != nil {
		t.Fatal(err)
	}
	localGit(t, repo, "add", ".")
	localGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.com", "-c", "commit.gpgsign=false", "commit", "-m", "fixture")
	clone := filepath.Join(parent, "snapshot")
	localGit(t, parent, "clone", "--quiet", "--no-hardlinks", "--", repo, clone)
	return clone
}

func TestHardenSnapshot(t *testing.T) {
	root := snapshotFixture(t)
	objects, err := ObjectStoreBytes(root)
	if err != nil || objects <= 0 {
		t.Fatalf("object store = %d, %v", objects, err)
	}
	omitted, size, err := HardenSnapshot(root, MaxCloneBytes)
	if err != nil {
		t.Fatal(err)
	}
	want := []Omission{{"binary.dat", "binary"}, {"link", "symlink"}, {"nested/vendor", "generated"}, {"node_modules", "generated"}}
	if !reflect.DeepEqual(omitted, want) {
		t.Fatalf("omitted = %+v", omitted)
	}
	if _, err := os.Lstat(filepath.Join(root, "link")); !os.IsNotExist(err) {
		t.Fatalf("symlink remains: %v", err)
	}
	var total int64
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if info.Mode().Perm() != 0755 {
				t.Errorf("directory %s: %v", path, info.Mode())
			}
		} else {
			if info.Mode().Perm() != 0444 {
				t.Errorf("file %s: %v", path, info.Mode())
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			if !strings.HasPrefix(rel, ".git"+string(filepath.Separator)) {
				total += info.Size()
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if size != total || size == 0 {
		t.Fatalf("size = %d, want %d", size, total)
	}
	if _, _, err := HardenSnapshot(root, 1); err == nil || !strings.Contains(err.Error(), "1") {
		t.Fatalf("cap error = %v", err)
	}
	if got := DeclaredVersion(root); got != "1.2.3" {
		t.Fatal(got)
	}
}

func TestSnapshotDoesNotFollowSymlinks(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	writeFixtureFile(t, outside, "untouched", []byte("keep"))
	if err := os.Symlink(outside, filepath.Join(root, "directory-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	if _, err := ObjectStoreBytes(root); err == nil {
		t.Fatal("accepted symlink object store")
	}
	omitted, size, err := HardenSnapshot(root, 100)
	if err != nil || size != 0 || len(omitted) != 2 {
		t.Fatalf("%+v, %d, %v", omitted, size, err)
	}
	info, err := os.Stat(filepath.Join(outside, "untouched"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0755 {
		t.Fatal("followed symlink")
	}
}

func TestDeclaredVersion(t *testing.T) {
	for _, tt := range []struct{ name, content, want string }{
		{"VERSION", "v2.3.4-rc.1\n", "v2.3.4-rc.1"},
		{"package.json", `{"name":"example","version":"2.3.4"}`, "2.3.4"},
		{"Cargo.toml", "[package]\nname = \"example\"\nversion = \"2.3.4\"\n", "2.3.4"},
		{"pyproject.toml", "[project]\nversion = '2.3.4'\n", "2.3.4"},
		{"go.mod", "module example.com/example\n\ngo 1.26.2\n", "1.26.2"},
		{"VERSION", "not a version", ""},
		{"package.json", `{"dependencies":{"version":"9.9.9"}}`, ""},
		{"VERSION", strings.Repeat(" ", 4096) + "9.9.9", ""},
		{"Cargo.toml", "[dependencies]\nversion = \"9.9.9\"", ""},
	} {
		t.Run(tt.name+tt.want, func(t *testing.T) {
			root := t.TempDir()
			writeFixtureFile(t, root, tt.name, []byte(tt.content))
			if got := DeclaredVersion(root); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
	root := t.TempDir()
	if got := DeclaredVersion(root); got != "" {
		t.Fatal(got)
	}
	writeFixtureFile(t, root, "VERSION", []byte("1.0.0"))
	writeFixtureFile(t, root, "package.json", []byte(`{"version":"2.0.0"}`))
	if got := DeclaredVersion(root); got != "1.0.0" {
		t.Fatal(got)
	}
}

func TestGeneratedDirectoriesRemainHardenedAndCounted(t *testing.T) {
	for _, name := range []string{"node_modules", "vendor", "dist", "build", "target", ".venv", "__pycache__"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFixtureFile(t, root, name+"/file", []byte("kept"))
			writeFixtureFile(t, root, "nested/"+name+"/file", []byte("kept"))
			omitted, size, err := HardenSnapshot(root, 8)
			if err != nil || size != 8 || len(omitted) != 2 {
				t.Fatalf("%+v, %d, %v", omitted, size, err)
			}
			for _, path := range []string{name, "nested/" + name} {
				if !slices.Contains(omitted, Omission{path, "generated"}) {
					t.Fatalf("missing omission for %s", path)
				}
				info, err := os.Stat(filepath.Join(root, path, "file"))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0444 {
					t.Fatalf("file mode = %v", info.Mode())
				}
			}
			if _, _, err := HardenSnapshot(root, 7); err == nil {
				t.Fatal("generated files did not count toward cap")
			}
		})
	}
}

func TestHardenSnapshotRemovesNonRegularFiles(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0600); err != nil {
		t.Fatal(err)
	}
	omitted, size, err := HardenSnapshot(root, MaxCloneBytes)
	if err != nil || size != 0 || !reflect.DeepEqual(omitted, []Omission{{"pipe", "non-regular"}}) {
		t.Fatalf("%+v, %d, %v", omitted, size, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "pipe")); !os.IsNotExist(err) {
		t.Fatalf("pipe remains: %v", err)
	}
}

func TestBinaryProbeIsBounded(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "text", append(bytes.Repeat([]byte("x"), 8192), 0))
	omitted, _, err := HardenSnapshot(root, MaxCloneBytes)
	if err != nil || len(omitted) != 0 {
		t.Fatalf("%+v, %v", omitted, err)
	}
}
