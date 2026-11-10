package insights

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/testenv"
)

func TestGitDefectMissingPromisedBlobDoesNotFetch(t *testing.T) {
	testenv.SkipIfShort(t)
	root := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...) // #nosec G204 -- test-owned local fixture arguments.
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %q: %s (%v)", args, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "main")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	testenv.DisableGitMaintenance(t, root)
	for i, text := range []string{"broken\n", "repaired\n"} {
		if err := os.WriteFile(filepath.Join(root, "file"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		git("add", "--", "file")
		message := "feat: implementation"
		if i == 1 {
			message = "fix: repair"
		}
		git("commit", "-m", message)
	}
	blob := git("rev-parse", "HEAD^:file")
	plans := []DefectPlan{{ID: "20260101-000000-target", CurrentMerged: true, MergeSHA: git("rev-parse", "HEAD^"), CreatedAt: time.Unix(1, 0), MergeAt: time.Unix(1, 0)}}
	// Simulate a partial clone whose parent-side blob is only promised. The
	// remote is deliberately local and absent so even a regression cannot use
	// the network; trace2 detects an attempted fetch regardless of its success.
	git("config", "remote.origin.url", filepath.Join(t.TempDir(), "absent"))
	git("config", "remote.origin.promisor", "true")
	git("config", "remote.origin.partialclonefilter", "blob:none")
	collector := NewGitDefectCollector("repo", root, t.TempDir(), nil)
	report, err := collector.CollectDefects(context.Background(), plans, time.Now())
	if err != nil || !report.Coverage.Scan.Complete {
		t.Fatalf("complete fixture: %+v (%v)", report.Coverage, err)
	}
	if err := os.Remove(filepath.Join(root, ".git", "objects", blob[:2], blob[2:])); err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "trace.jsonl")
	t.Setenv("GIT_TRACE2_EVENT", trace)
	// An inherited opt-in to lazy fetching must not override collector policy.
	t.Setenv("GIT_NO_LAZY_FETCH", "0")
	report, err = collector.CollectDefects(context.Background(), plans, time.Now())
	if err != nil || report.Availability != DefectIncomplete || report.Coverage.Scan.Complete || !report.Coverage.Scan.MissingHistory {
		t.Fatalf("missing promised blob: %+v (%v)", report, err)
	}
	data, err := os.ReadFile(trace) // #nosec G304 -- test-owned trace path.
	if err != nil || len(data) == 0 {
		t.Fatalf("missing Git trace: %v", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Event == "child_start" && defectContains(event.Argv, "fetch") {
			t.Fatalf("collector attempted lazy fetch: %s", line)
		}
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "objects", blob[:2], blob[2:])); !os.IsNotExist(err) {
		t.Fatalf("missing object was restored: %v", err)
	}
}
