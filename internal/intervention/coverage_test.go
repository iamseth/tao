package intervention

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoverage(t *testing.T) {
	dir := t.TempDir()
	if ok, err := ReadCoverage(dir, "plan"); ok || err != nil {
		t.Fatalf("absent: %v %v", ok, err)
	}
	if err := WriteCoverage(dir, "plan"); err != nil {
		t.Fatal(err)
	}
	if ok, err := ReadCoverage(dir, "plan"); !ok || err != nil {
		t.Fatalf("valid: %v %v", ok, err)
	}
	path := filepath.Join(dir, "intervention-coverage.json")
	before, err := os.ReadFile(path) // #nosec G304 -- test-owned temporary marker.
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteCoverage(dir, "other"); err == nil {
		t.Fatal("replaced marker")
	}
	after, _ := os.ReadFile(path) // #nosec G304 -- test-owned temporary marker.
	if string(before) != string(after) {
		t.Fatal("marker changed")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	if ok, _ := ReadCoverage(dir, "other"); ok {
		t.Fatal("wrong identity accepted")
	}
}

func TestCoverageUnreadable(t *testing.T) {
	dir := t.TempDir()
	if err := WriteCoverage(dir, "plan"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "intervention-coverage.json")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0600) })
	if f, err := os.Open(path); err == nil { // #nosec G304 -- test-owned temporary marker permission probe.
		_ = f.Close()
		t.Skip("process can read permission-denied files")
	}
	if ok, err := ReadCoverage(dir, "plan"); ok || err == nil {
		t.Fatal("unreadable marker accepted")
	}
}

func TestCoverageInvalid(t *testing.T) {
	for _, data := range []string{"", "{", `{}`, `{"schema":"tao.intervention.coverage.v1","version":2,"plan_id":"plan"}`, `{"schema":"other","version":1,"plan_id":"plan"}`, `{"schema":"tao.intervention.coverage.v1","version":1,"plan_id":"plan"} {}`, strings.Repeat("x", 8192)} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "intervention-coverage.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if ok, err := ReadCoverage(dir, "plan"); ok || err == nil {
			t.Fatalf("invalid accepted: %v %v", ok, err)
		}
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing")
	if err := WriteCoverage(missing, "plan"); err == nil {
		t.Fatal("created directory")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("directory exists")
	}
	for _, id := range []string{"", strings.Repeat("x", 4096)} {
		if err := WriteCoverage(dir, id); err == nil {
			t.Fatal("invalid ID accepted")
		}
	}
	path := filepath.Join(dir, "intervention-coverage.json")
	if err := os.Symlink("missing", path); err != nil {
		t.Fatal(err)
	}
	if ok, err := ReadCoverage(dir, "plan"); ok || err == nil {
		t.Fatal("symlink accepted")
	}
	if err := WriteCoverage(dir, "plan"); err == nil {
		t.Fatal("replaced symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if ok, err := ReadCoverage(dir, "plan"); ok || err == nil {
		t.Fatal("directory accepted")
	}
	if err := WriteCoverage(dir, "plan"); err == nil {
		t.Fatal("replaced directory")
	}
}
