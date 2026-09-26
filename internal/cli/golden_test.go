package cli

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite golden fixtures under testdata instead of comparing")

func goldenFixturePath(rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("golden fixture path %q must be relative to testdata", rel)
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == ".." {
			return "", fmt.Errorf("golden fixture path %q must not contain '..' elements", rel)
		}
	}
	path := filepath.Clean(rel)
	if !strings.HasPrefix(path, "testdata"+string(filepath.Separator)) {
		return "", fmt.Errorf("golden fixture path %q must name a file under testdata", rel)
	}
	return path, nil
}

func checkGolden(rel string, got []byte, update bool) error {
	path, err := goldenFixturePath(rel)
	if err != nil {
		return err
	}
	if update {
		//nolint:nolintlint // Keep the confinement justification even when gosec accepts mode 0600.
		return os.WriteFile(path, got, 0o600) //nolint:gosec // G306/G304: path is confined to checked-in testdata by goldenFixturePath.
	}
	want, err := os.ReadFile(path) //nolint:gosec // G306/G304: path is confined to checked-in testdata by goldenFixturePath.
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("output differs from %s; run make update-golden:\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
	return nil
}

func assertGolden(t *testing.T, rel string, got []byte) {
	t.Helper()
	if err := checkGolden(rel, got, *updateGolden); err != nil {
		t.Fatal(err)
	}
	if *updateGolden {
		t.Logf("rewrote golden fixture %s", rel)
	}
}

func TestGoldenHelper(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Mkdir("testdata", 0o700); err != nil {
		t.Fatal(err)
	}

	t.Run("update writes exact bytes", func(t *testing.T) {
		want := []byte("fixture\n\x00exact bytes\n")
		if err := checkGolden("testdata/sample.golden", want, true); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile("testdata/sample.golden")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("fixture = %q, want %q", got, want)
		}
	})

	t.Run("compare matching bytes", func(t *testing.T) {
		if err := os.WriteFile("testdata/matching.golden", []byte("matching\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := checkGolden("testdata/matching.golden", []byte("matching\n"), false); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("compare mismatch gives regeneration guidance", func(t *testing.T) {
		if err := os.WriteFile("testdata/mismatch.golden", []byte("expected\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := checkGolden("testdata/mismatch.golden", []byte("actual\n"), false)
		if err == nil {
			t.Fatal("expected mismatch error")
		}
		for _, want := range []string{"testdata/mismatch.golden", "run make update-golden", "actual\n", "expected\n"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error = %q, want %q", err, want)
			}
		}
		got, err := os.ReadFile("testdata/mismatch.golden")
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "expected\n" {
			t.Fatalf("compare changed fixture to %q", got)
		}
	})

	t.Run("reject unsafe paths", func(t *testing.T) {
		absolute, err := filepath.Abs("testdata/sample.golden")
		if err != nil {
			t.Fatal(err)
		}
		for _, rel := range []string{absolute, "../testdata/sample.golden", "testdata/../testdata/sample.golden", "outside/sample.golden", "testdata-other/sample.golden", "testdata", "testdata/.", ""} {
			if _, err := goldenFixturePath(rel); err == nil || !strings.Contains(err.Error(), rel) {
				t.Errorf("goldenFixturePath(%q) error = %v, want rejection naming path", rel, err)
			}
		}
	})

	t.Run("clean valid path", func(t *testing.T) {
		got, err := goldenFixturePath("./testdata/insights/./sample.golden")
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join("testdata", "insights", "sample.golden"); got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
	})

	t.Run("update does not create directories", func(t *testing.T) {
		if err := checkGolden("testdata/missing/sample.golden", []byte("sample"), true); err == nil || !os.IsNotExist(err) {
			t.Fatalf("update error = %v, want missing directory error", err)
		}
	})
}
