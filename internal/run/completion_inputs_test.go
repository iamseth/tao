package run

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/plan"
)

func writeCompletionInputFiles(t *testing.T, notes string, results string) SliceCompletionInputFiles {
	t.Helper()
	dir := t.TempDir()
	files := SliceCompletionInputFiles{
		NotesFile:               filepath.Join(dir, "notes.md"),
		VerificationResultsFile: filepath.Join(dir, "results.json"),
	}
	if err := os.WriteFile(files.NotesFile, []byte(notes), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(files.VerificationResultsFile, []byte(results), 0o600); err != nil {
		t.Fatal(err)
	}
	return files
}

func TestLoadSliceCompletionInputsBoundsAndNormalizes(t *testing.T) {
	longDetails := strings.Repeat("x", maxCompletionVerificationDetailsRunes+10)
	resultBytes, err := json.Marshal([]plan.VerificationRun{{Command: " go test ./internal/run ", CWD: "/repo", Result: " passed ", Details: longDetails}})
	if err != nil {
		t.Fatal(err)
	}
	files := writeCompletionInputFiles(t, "  finished the slice  ", string(resultBytes))

	inputs, err := LoadSliceCompletionInputs(files)
	if err != nil {
		t.Fatal(err)
	}
	if inputs.Notes != "finished the slice" {
		t.Fatalf("notes = %q, want trimmed", inputs.Notes)
	}
	got := inputs.VerificationResults[0]
	if got.Command != "go test ./internal/run" || got.Result != "passed" {
		t.Fatalf("expected trimmed command/result, got %#v", got)
	}
	if length := len([]rune(got.Details)); length != maxCompletionVerificationDetailsRunes {
		t.Fatalf("details length = %d, want %d", length, maxCompletionVerificationDetailsRunes)
	}
	if inputs.CommitProposal != nil {
		t.Fatalf("expected no proposal without a proposal file, got %#v", inputs.CommitProposal)
	}
}

func TestLoadSliceCompletionInputsRejectsInvalidEvidence(t *testing.T) {
	tests := []struct {
		name    string
		notes   string
		results string
		want    string
	}{
		{
			name:    "oversized notes",
			notes:   strings.Repeat("n", int(maxCompletionNotesBytes)+1),
			results: `[]`,
			want:    "notes file exceeds",
		},
		{
			name:    "oversized results",
			notes:   "ok",
			results: strings.Repeat("[", int(maxCompletionVerificationResultsBytes)+1),
			want:    "verification results file exceeds",
		},
		{
			name:    "malformed results JSON",
			notes:   "ok",
			results: `{"command":`,
			want:    "read verification results JSON",
		},
		{
			name:    "missing fields",
			notes:   "ok",
			results: `[{"command":"go test ./internal/run","cwd":"/repo"}]`,
			want:    "must include command, cwd, and result",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			files := writeCompletionInputFiles(t, tc.notes, tc.results)
			if _, err := LoadSliceCompletionInputs(files); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestLoadSliceCompletionInputsRejectsExcessiveResultCount(t *testing.T) {
	results := make([]plan.VerificationRun, maxCompletionVerificationResults+1)
	for i := range results {
		results[i] = plan.VerificationRun{Command: "true", CWD: "/repo", Result: "passed"}
	}
	resultBytes, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	files := writeCompletionInputFiles(t, "ok", string(resultBytes))
	if _, err := LoadSliceCompletionInputs(files); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected result count limit error, got %v", err)
	}
}

func TestLoadSliceCompletionInputsNormalizesRelativeCWDs(t *testing.T) {
	resultBytes, err := json.Marshal([]plan.VerificationRun{
		{Command: "go test ./...", CWD: "sub", Result: "passed"},
		{Command: "go build ./...", CWD: "/abs", Result: "passed"},
	})
	if err != nil {
		t.Fatal(err)
	}
	files := writeCompletionInputFiles(t, "ok", string(resultBytes))

	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o750); err != nil {
		t.Fatal(err)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(base); err != nil {
		t.Fatal(err)
	}

	inputs, err := LoadSliceCompletionInputs(files)
	if err != nil {
		t.Fatal(err)
	}
	got := inputs.VerificationResults[0].CWD
	want, err := filepath.EvalSymlinks(filepath.Join(base, "sub"))
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("relative cwd = %q, want %q", got, want)
	}
	if inputs.VerificationResults[1].CWD != "/abs" {
		t.Fatalf("absolute cwd changed: %q", inputs.VerificationResults[1].CWD)
	}
}

func TestSliceCompletionInputFilesRemoveIsBestEffort(t *testing.T) {
	files := writeCompletionInputFiles(t, "ok", `[]`)
	files.CommitProposalFile = filepath.Join(t.TempDir(), "missing.json")
	files.Remove()
	if _, err := os.Stat(files.NotesFile); !os.IsNotExist(err) {
		t.Fatalf("notes file still present: %v", err)
	}
	if _, err := os.Stat(files.VerificationResultsFile); !os.IsNotExist(err) {
		t.Fatalf("results file still present: %v", err)
	}
}

func TestLoadSliceCompletionInputsDecodesProposal(t *testing.T) {
	files := writeCompletionInputFiles(t, "ok", `[]`)
	files.CommitProposalFile = filepath.Join(t.TempDir(), "proposal.json")
	proposal := `{"type":"feat","scope":"run","summary":"load completion inputs","what":"Load bounded inputs.","why":"Keep the CLI thin."}`
	if err := os.WriteFile(files.CommitProposalFile, []byte(proposal), 0o600); err != nil {
		t.Fatal(err)
	}
	inputs, err := LoadSliceCompletionInputs(files)
	if err != nil {
		t.Fatal(err)
	}
	if inputs.CommitProposal == nil || inputs.CommitProposal.Summary != "load completion inputs" {
		t.Fatalf("decoded proposal = %#v", inputs.CommitProposal)
	}
	// Sanity-check the bound aliases stay wired to the shared agent-input caps.
	if maxCompletionNotesBytes != agentinput.MaxFileBytes || maxCompletionVerificationDetailsRunes != agentinput.MaxTextRunes {
		t.Fatal("completion bounds diverged from shared agent-input caps")
	}
}
