package run

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestCoupledFilesSelectedPreflight(t *testing.T) {
	recordedRoot, executionRoot := t.TempDir(), t.TempDir()
	detail := runPathSessionDetail(t, recordedRoot, plan.StatusPlanned, []string{"001-a"}, nil, plan.StatusPending)
	detail.State.Repo.Root = recordedRoot
	detail.Slices.Slices[0].ExpectedFiles = []string{"internal/cli/example.go"}
	detail.Slices.Slices[0].Verification.Commands = []string{"go version"}
	target := filepath.Join(executionRoot, "internal/cli/testdata/zsh_completion.golden")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	var events []plan.Event
	runner := SelectedSliceRunner{out: &out, execution: testRunExecution(ExecutionConfig{}, RunDependencies{EventAppender: eventAppenderFunc(func(_ string, event plan.Event) error { events = append(events, event); return nil })})}
	quiet, err := runner.validateSelectedSlice(detail, "001-a", recordedRoot)
	if err != nil || quiet.HasErrors() {
		t.Fatalf("quiet: %+v, %v", quiet, err)
	}
	for _, finding := range quiet.Findings {
		if finding.Code == "slice_coupled_file_missing" {
			t.Fatal("used wrong root")
		}
	}
	out.Reset()
	result, err := runner.validateSelectedSlice(detail, "001-a", executionRoot)
	if err != nil || result.HasErrors() {
		t.Fatalf("preflight: %+v, %v", result, err)
	}
	shared := plan.ValidateSelectedSliceVerificationAtRoot(detail, executionRoot)
	found := false
	warnings := 0
	for _, finding := range shared.Findings {
		if finding.Severity == plan.VerificationFindingWarning {
			warnings++
		}
		if finding.Code == "slice_coupled_file_missing" {
			found = true
			if !strings.Contains(out.String(), finding.Message) {
				t.Fatalf("missing shared proposal: %s", out.String())
			}
		}
	}
	if !found {
		t.Fatal("missing proposal")
	}
	if err := runner.recordRunContext(detail, "001-a", "packet", result); err != nil {
		t.Fatal(err)
	}
	found = false
	for _, event := range events {
		if event.Type == plan.EventTypeRunContext {
			found = true
			if event.GuardrailWarnings != warnings || event.SliceID != "001-a" {
				t.Fatalf("context: %+v", event)
			}
		}
	}
	if !found {
		t.Fatal("missing diagnostic run_context")
	}
	after, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(target) //nolint:gosec // G304: test-controlled temporary target.
	if err != nil || string(data) != "unchanged" || !bytes.Equal(before, after) {
		t.Fatal("preflight mutated scope or files", err)
	}
}
