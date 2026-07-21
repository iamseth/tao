package plan

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func TestNoPlanReviewReachInsOutsidePlanPackage(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot, err := reachInGuardRepoRoot(filename)
	if err != nil {
		t.Fatal(err)
	}
	internalRoot := filepath.Join(repoRoot, "internal")
	forbidden := regexp.MustCompile(`\.Plan\.Review\b`)

	var offenders []string
	if err := filepath.WalkDir(internalRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == internalRoot {
				return nil
			}
			rel, err := filepath.Rel(internalRoot, path)
			if err != nil {
				return err
			}
			if rel == "plan" || rel == "plantest" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		matches, err := planReviewReachIns(path, repoRoot, forbidden)
		if err != nil {
			return err
		}
		offenders = append(offenders, matches...)
		return nil
	}); err != nil {
		t.Fatalf("scan internal/ for raw .Plan.Review reach-ins: %v", err)
	}

	if len(offenders) > 0 {
		t.Fatalf("raw .Plan.Review reach-ins outside internal/plan are forbidden; use plan.CurrentReview / plan.PersistedReview / plan.SetPersistedReview instead:\n%s", strings.Join(offenders, "\n"))
	}
}

func TestRunDoesNotUseRawPlanLifecycleMutators(t *testing.T) {
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot, err := reachInGuardRepoRoot(filename)
	if err != nil {
		t.Fatal(err)
	}
	runRoot := filepath.Join(repoRoot, "internal", "run")
	forbidden := regexp.MustCompile(`\bplan\.Mark[A-Z][A-Za-z0-9_]*\b`)

	var offenders []string
	if err := filepath.WalkDir(runRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		matches, err := planReviewReachIns(path, repoRoot, forbidden)
		if err != nil {
			return err
		}
		offenders = append(offenders, matches...)
		return nil
	}); err != nil {
		t.Fatalf("scan internal/run for raw plan lifecycle mutators: %v", err)
	}

	if len(offenders) > 0 {
		t.Fatalf("raw plan lifecycle mutations in production run code are forbidden; use a complete PlanRecord operation instead:\n%s", strings.Join(offenders, "\n"))
	}
}

func reachInGuardRepoRoot(filename string) (string, error) {
	if filepath.IsAbs(filename) {
		return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", "..")), nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory for relative caller path %q: %w", filename, err)
	}
	repoRoot, err := findReachInGuardRepoRoot(cwd)
	if err != nil {
		return "", fmt.Errorf("runtime.Caller returned relative path %q: %w", filename, err)
	}
	return repoRoot, nil
}

func findReachInGuardRepoRoot(start string) (string, error) {
	dir := filepath.Clean(start)
	for {
		if reachInGuardRootExists(dir) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate repo root from %s", start)
		}
		dir = parent
	}
}

func reachInGuardRootExists(dir string) bool {
	guardInfo, guardErr := os.Stat(filepath.Join(dir, "internal", "plan", "reachin_guard_test.go"))
	if guardErr != nil || guardInfo.IsDir() {
		return false
	}
	goModInfo, goModErr := os.Stat(filepath.Join(dir, "go.mod"))
	return goModErr == nil && !goModInfo.IsDir()
}

func planReviewReachIns(path, repoRoot string, forbidden *regexp.Regexp) ([]string, error) {
	// #nosec G304 -- path is produced by filepath.WalkDir under the test-discovered repo root.
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		rel = path
	}

	var offenders []string
	for lineNumber, line := range strings.Split(string(contents), "\n") {
		if forbidden.MatchString(line) {
			offenders = append(offenders, fmt.Sprintf("%s:%d", filepath.ToSlash(rel), lineNumber+1))
		}
	}
	return offenders, nil
}
