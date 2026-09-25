package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func writeMetricsPlan(t *testing.T, repoRoot string, planID string) string { //nolint:unparam // repoRoot kept for test readability and future cases
	t.Helper()
	planDir := filepath.Join(t.TempDir(), planID)
	if err := os.MkdirAll(planDir, 0o750); err != nil {
		t.Fatal(err)
	}
	state := `{"schema":"tao.plan.state.v1","status":"in_progress","created_at":"2026-05-01T00:00:00Z","updated_at":"2026-05-01T00:00:00Z","repo":{"name":"tao","root":"` + repoRoot + `","branch":"feature"},"plan":{"id":"` + planID + `","title":"Plan","current_slice":null,"completed_slices":[],"pending_slices":["001-a"],"timing":{"started_at":null,"completed_at":null,"last_activity_at":null}},"global_invariants":[],"open_questions":[]}`
	if err := os.WriteFile(filepath.Join(planDir, "state.json"), []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(planDir, "events.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return planDir
}

func readMetricsText(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path) //nolint:gosec // test reads a path from a t.TempDir-derived location
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}

func readAgentMetricEvents(t *testing.T, planDir string) []plan.Event {
	t.Helper()
	var events []plan.Event
	for line := range strings.SplitSeq(strings.TrimSpace(readMetricsText(t, filepath.Join(planDir, "events.jsonl"))), "\n") {
		if line == "" {
			continue
		}
		var event plan.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type == plan.EventTypeAgentMetrics {
			events = append(events, event)
		}
	}
	return events
}
