package verification

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAnalyzerCachesPackageDiscovery(t *testing.T) {
	repo := t.TempDir()
	serviceDir := filepath.Join(repo, "services", "api")
	packagePath := filepath.Join(serviceDir, "package.json")
	writeFile(t, packagePath, `{"name":"@repo/api"}`)

	analyzer := NewAnalyzer(repo)
	analysis := analyzer.Analyze("pnpm --filter @repo/api test")
	if analysis.WorkingDir != serviceDir {
		t.Fatalf("expected package directory lookup, got %q", analysis.WorkingDir)
	}
	if err := os.Remove(packagePath); err != nil {
		t.Fatal(err)
	}

	analysis = analyzer.Analyze("pnpm --filter @repo/api test")
	if analysis.WorkingDir != serviceDir {
		t.Fatalf("expected cached package directory lookup, got %q", analysis.WorkingDir)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil { //nolint:gosec // G301: test fixture directory permissions
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil { //nolint:gosec // G306: test fixture file permissions
		t.Fatal(err)
	}
}
