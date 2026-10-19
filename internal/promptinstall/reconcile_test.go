package promptinstall

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	agentpkg "github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/prompts"
)

func reconciliationAgents(t *testing.T) []agentpkg.Descriptor {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(root, "pi"))
	t.Setenv("TAO_CLAUDE_COMMANDS_DIR", filepath.Join(root, "claude"))
	source := filepath.Join(root, "source")
	if err := os.MkdirAll(source, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "package.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TAO_PI_EXTENSION_DIR", source)
	return agentpkg.All()
}

func TestReconcileUndiscoverableExtensionSource(t *testing.T) {
	agents := reconciliationAgents(t)
	original := findPiExtensionSourceFrom
	findPiExtensionSourceFrom = func(string) (string, bool) { return "", false }
	t.Cleanup(func() { findPiExtensionSourceFrom = original })
	t.Setenv("TAO_PI_EXTENSION_DIR", "")

	results, err := ReconcileDiscovered(agents)
	if err != nil {
		t.Fatalf("unavailable source must be a quiet skip: %v", err)
	}
	var skipped, installed int
	for _, result := range results {
		switch result.Status {
		case "skipped":
			skipped++
			if _, err := os.Lstat(result.Path); !os.IsNotExist(err) {
				t.Fatalf("skipped target was created: %v", err)
			}
		case "installed":
			installed++
			if _, err := os.ReadFile(result.Path); err != nil {
				t.Fatal(err)
			}
		}
	}
	if skipped == 0 || installed == 0 || len(results) != len(agents)*len(prompts.Definitions()) {
		t.Fatalf("incomplete maintenance: %+v", results)
	}
	if _, err := CheckDiscovered(agents); err == nil {
		t.Fatal("explicit check must still report missing source")
	}
	if _, err := InstallDiscovered(agents, false); err == nil {
		t.Fatal("explicit install must still report missing source")
	}
}

func TestReconcileInvalidExplicitExtensionSource(t *testing.T) {
	agents := reconciliationAgents(t)
	t.Setenv("TAO_PI_EXTENSION_DIR", filepath.Join(t.TempDir(), "missing"))
	if _, err := ReconcileDiscovered(agents); err == nil {
		t.Fatal("invalid explicit source must remain an error, not fall back to discovery")
	}
}

func TestReconcileMixedAndRepeat(t *testing.T) {
	agents := reconciliationAgents(t)
	for _, a := range agents {
		dir, err := a.PromptDir()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for name, text := range map[string]string{"tao-plan": "tao-managed: old", "tao-slice": "user content"} {
			if err := os.WriteFile(Path(dir, name), []byte(text), 0o640); err != nil { //nolint:gosec // Exercise preservation of existing group-readable permissions.
				t.Fatal(err)
			}
		}
	}
	results, err := ReconcileDiscovered(agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(agents)*len(prompts.Definitions()) {
		t.Fatalf("results: %d", len(results))
	}
	stamp := time.Unix(123456789, 0)
	for _, r := range results {
		if r.Name == "tao-slice" && r.Status != "unmanaged" {
			t.Fatalf("unmanaged: %+v", r)
		}
		info, err := os.Lstat(r.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if err := os.Chtimes(r.Path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if r.Name == "tao-plan" && info.Mode().Perm() != 0o640 {
			t.Fatalf("permissions: %v", info.Mode())
		}
	}
	again, err := ReconcileDiscovered(agents)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range again {
		info, err := os.Lstat(r.Path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.ModTime().Equal(stamp) {
			t.Fatalf("rewritten: %+v", r)
		}
		content, err := os.ReadFile(r.Path)
		if err != nil {
			t.Fatal(err)
		}
		if r.Name == "tao-slice" && string(content) != "user content" {
			t.Fatal("unmanaged changed")
		}
	}
	if results, err := ReconcileDiscovered(nil); err != nil || len(results) != 0 {
		t.Fatalf("empty: %v %v", results, err)
	}
}

func TestReconcileContinuesFailures(t *testing.T) {
	agents := reconciliationAgents(t)
	a := agents[0]
	dir, _ := a.PromptDir()
	if err := os.MkdirAll(Path(dir, "tao-plan"), 0o750); err != nil {
		t.Fatal(err)
	}
	a.RenderInlinePrompt = nil
	bad := a
	bad.PromptDir = func() (string, error) { return "", errors.New("directory failure") }
	results, err := ReconcileDiscovered(append([]agentpkg.Descriptor{bad, a}, agents[1:]...))
	if err == nil || !strings.Contains(err.Error(), "directory failure") || !strings.Contains(err.Error(), "RenderInlinePrompt") || !strings.Contains(err.Error(), "tao-plan") {
		t.Fatalf("errors: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no progress")
	}
	last := agents[len(agents)-1]
	target, _ := last.PromptDir()
	if _, err := os.Stat(Path(target, "tao-plan")); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileExtensionConflictsAndSourceFailure(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "source"} {
		t.Run(kind, func(t *testing.T) {
			agents := reconciliationAgents(t)
			path, err := piExtensionTarget()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(path, 0o750); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			case "source":
				// A non-directory source produces a deterministic lookup error without repository fallback.
				file := filepath.Join(t.TempDir(), "file")
				if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("TAO_PI_EXTENSION_DIR", file)
			}
			results, err := ReconcileDiscovered(agents)
			if kind == "source" && err == nil {
				t.Fatal("missing source error")
			}
			if kind != "source" && err != nil {
				t.Fatal(err)
			}
			if len(results) == 0 {
				t.Fatal("no markdown progress")
			}
			if kind == "directory" {
				info, err := os.Stat(path)
				if err != nil || !info.IsDir() {
					t.Fatal("directory replaced")
				}
			}
			if kind == "symlink" {
				dest, err := os.Readlink(path)
				if err != nil || dest == os.Getenv("TAO_PI_EXTENSION_DIR") {
					t.Fatal("link replaced")
				}
			}
		})
	}
}

func TestReconcileConcurrent(t *testing.T) {
	agents := reconciliationAgents(t)
	for _, stale := range []bool{false, true} {
		if stale {
			for _, a := range agents {
				dir, _ := a.PromptDir()
				if err := os.WriteFile(Path(dir, "tao-plan"), []byte("tao-managed: old"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
		}
		var wg sync.WaitGroup
		for range 12 {
			wg.Go(func() {
				if _, err := ReconcileDiscovered(agents); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
	}
	results, err := CheckDiscovered(agents)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range results {
		if r.Status != "current" {
			t.Fatalf("incomplete: %+v", r)
		}
	}
	for _, a := range agents {
		dir, _ := a.PromptDir()
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".tao-prompt-") {
				t.Fatalf("temporary file leaked: %s", e.Name())
			}
		}
	}
}
