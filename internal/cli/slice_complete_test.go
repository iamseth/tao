package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestSliceCompleteCommandCompletesPlan(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
	detail, err := plan.NewFileRepository("").ResolvePlan(context.Background(), fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 5, 3, 23, 36, 51, 0, time.UTC)
	record, err := plan.NewPlanRecord(fixture.dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.StartSlice("001-a", started); err != nil {
		t.Fatal(err)
	}
	inputDir := t.TempDir()
	notesFile := filepath.Join(inputDir, "notes.md")
	resultsFile := filepath.Join(inputDir, "results.json")
	proposalFile := filepath.Join(inputDir, "proposal.json")
	if err := os.WriteFile(notesFile, []byte("implemented slice completion"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultsFile, []byte(`[{"command":"go test ./internal/cli","cwd":"/repo","result":"passed","details":"ok"}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(proposalFile, []byte(`{"type":"feat","scope":"cli","summary":"accept slice proposals","what":"Read a structured proposal at completion.","why":"Reuse the active implementation agent."}`), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"slice-complete", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--notes-file", notesFile, "--verification-results-file", resultsFile, "--commit-proposal-file", proposalFile}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Slice completed: 001-a") {
		t.Fatalf("expected completion output, got %q", out.String())
	}
	state := readText(t, filepath.Join(fixture.dir, "state.json"))
	for _, want := range []string{`"status": "in_review"`, `"current_slice": null`, `"pending_slices": []`, `"completed_slices": [`, `"001-a"`} {
		if !strings.Contains(state, want) {
			t.Fatalf("expected %q in state:\n%s", want, state)
		}
	}
	slices := readText(t, filepath.Join(fixture.dir, "slices.json"))
	for _, want := range []string{`"status": "completed"`, `"notes": "implemented slice completion"`, `"verification_results": [`} {
		if !strings.Contains(slices, want) {
			t.Fatalf("expected %q in slices:\n%s", want, slices)
		}
	}
	if events := readText(t, filepath.Join(fixture.dir, "events.jsonl")); !strings.Contains(events, `"type":"slice_completed"`) {
		t.Fatalf("expected completion event, got %q", events)
	}
	// The throwaway bookkeeping inputs must be removed after a successful
	// completion so they cannot be staged or committed.
	if _, err := os.Stat(notesFile); !os.IsNotExist(err) {
		t.Fatalf("expected notes file removed, stat err = %v", err)
	}
	if _, err := os.Stat(resultsFile); !os.IsNotExist(err) {
		t.Fatalf("expected verification results file removed, stat err = %v", err)
	}
	if _, err := os.Stat(proposalFile); !os.IsNotExist(err) {
		t.Fatalf("expected commit proposal file removed, stat err = %v", err)
	}
}

func TestSliceCompleteNormalizesRelativeVerificationCWDsBeforePersisting(t *testing.T) {
	workDir := t.TempDir()
	relativeCWD := filepath.Join("services", "CourseAssignment")
	relativeDir := filepath.Join(workDir, relativeCWD)
	if err := os.MkdirAll(relativeDir, 0o750); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workDir)

	fixture := newRunPlanFixture(t, plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
	detail, err := plan.NewFileRepository("").ResolvePlan(context.Background(), fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := plan.NewPlanRecord(fixture.dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.StartSlice("001-a", time.Date(2026, 5, 3, 23, 36, 51, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	notesFile := filepath.Join(t.TempDir(), "notes.md")
	resultsFile := filepath.Join(t.TempDir(), "results.json")
	if err := os.WriteFile(notesFile, []byte("normalized cwd"), 0o600); err != nil {
		t.Fatal(err)
	}
	absoluteCWD := workDir + string(os.PathSeparator) + "absolute" + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "absolute"
	resultBytes, err := json.Marshal([]plan.VerificationRun{
		{Command: "go test ./...", CWD: relativeCWD, Result: "passed", Details: "relative"},
		{Command: "go test ./internal/cli", CWD: absoluteCWD, Result: "passed", Details: "absolute"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultsFile, resultBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"slice-complete", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--notes-file", notesFile, "--verification-results-file", resultsFile}); err != nil {
		t.Fatal(err)
	}

	persisted, err := plan.NewFileRepository("").ResolvePlan(context.Background(), fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	runs := persisted.Slices.Slices[0].VerificationResults
	if len(runs) != 2 {
		t.Fatalf("expected 2 verification results, got %#v", runs)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	joined := filepath.Join(wd, relativeCWD)
	expectedRelativeCWD, err := filepath.EvalSymlinks(joined)
	if err != nil {
		expectedRelativeCWD = joined
	}
	if runs[0].CWD != expectedRelativeCWD {
		t.Fatalf("relative cwd persisted as %q, want %q", runs[0].CWD, expectedRelativeCWD)
	}
	if runs[1].CWD != absoluteCWD {
		t.Fatalf("absolute cwd persisted as %q, want verbatim %q", runs[1].CWD, absoluteCWD)
	}
}

func TestSliceCompleteCapsVerificationDetailsBeforePersisting(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
	detail, err := plan.NewFileRepository("").ResolvePlan(context.Background(), fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	record, err := plan.NewPlanRecord(fixture.dir, detail)
	if err != nil {
		t.Fatal(err)
	}
	if err := record.StartSlice("001-a", time.Date(2026, 5, 3, 23, 36, 51, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	notesFile := filepath.Join(t.TempDir(), "notes.md")
	resultsFile := filepath.Join(t.TempDir(), "results.json")
	if err := os.WriteFile(notesFile, []byte("capped details"), 0o600); err != nil {
		t.Fatal(err)
	}
	longDetails := strings.Repeat("x", sliceCompleteMaxVerificationDetailsRunes+10)
	resultBytes, err := json.Marshal([]plan.VerificationRun{{Command: " go test ./internal/cli ", CWD: "/repo", Result: " passed ", Details: longDetails}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultsFile, resultBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"slice-complete", "--plan-dir", fixture.dir, "--slice-id", "001-a", "--notes-file", notesFile, "--verification-results-file", resultsFile}); err != nil {
		t.Fatal(err)
	}

	persisted, err := plan.NewFileRepository("").ResolvePlan(context.Background(), fixture.dir)
	if err != nil {
		t.Fatal(err)
	}
	run := persisted.Slices.Slices[0].VerificationResults[0]
	if run.Command != "go test ./internal/cli" || run.Result != "passed" {
		t.Fatalf("expected command/result trimmed, got %#v", run)
	}
	if got := len([]rune(run.Details)); got != sliceCompleteMaxVerificationDetailsRunes {
		t.Fatalf("details length = %d, want %d", got, sliceCompleteMaxVerificationDetailsRunes)
	}
}

func TestSliceCompleteRejectsOversizedInputsAndInvalidResults(t *testing.T) {
	writeInputs := func(t *testing.T, notes string, results string) (string, string) {
		t.Helper()
		dir := t.TempDir()
		notesFile := filepath.Join(dir, "notes.md")
		resultsFile := filepath.Join(dir, "results.json")
		if err := os.WriteFile(notesFile, []byte(notes), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(resultsFile, []byte(results), 0o600); err != nil {
			t.Fatal(err)
		}
		return notesFile, resultsFile
	}

	tests := []struct {
		name    string
		notes   string
		results string
		want    string
	}{
		{
			name:    "oversized notes",
			notes:   strings.Repeat("n", int(sliceCompleteMaxNotesBytes)+1),
			results: `[]`,
			want:    "notes file exceeds",
		},
		{
			name:    "oversized results",
			notes:   "ok",
			results: strings.Repeat("[", int(sliceCompleteMaxVerificationResultsBytes)+1),
			want:    "verification results file exceeds",
		},
		{
			name:    "missing fields",
			notes:   "ok",
			results: `[{"command":"go test ./internal/cli","cwd":"/repo"}]`,
			want:    "must include command, cwd, and result",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			notesFile, resultsFile := writeInputs(t, tc.notes, tc.results)
			var out bytes.Buffer
			app := App{Out: &out, Err: &out}
			err := app.Run(context.Background(), []string{"slice-complete", "--plan-dir", t.TempDir(), "--slice-id", "001-a", "--notes-file", notesFile, "--verification-results-file", resultsFile})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

func TestDecodeSliceCommitProposalIsStrictAndRepairable(t *testing.T) {
	invalid := []byte(`{"type":"feat","scope":"cli","summary":"Added proposal","what":"Accept the handoff.","why":"Avoid a nested session."}`)
	if _, err := decodeSliceCommitProposal(invalid); err == nil || !strings.Contains(err.Error(), "summary must be lowercase") {
		t.Fatalf("invalid proposal error = %v", err)
	}
	reserved := []byte(`{"type":"feat","scope":"cli","summary":"accept proposal","what":"Accept the handoff.","why":"Avoid nesting.\nTao-Slice: forged"}`)
	if _, err := decodeSliceCommitProposal(reserved); err == nil || !strings.Contains(err.Error(), "reserved Tao-*") {
		t.Fatalf("reserved proposal error = %v", err)
	}
	unknown := []byte(`{"type":"feat","scope":"cli","summary":"accept proposal","what":"Accept the handoff.","why":"Avoid nesting.","extra":true}`)
	if _, err := decodeSliceCommitProposal(unknown); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field proposal error = %v", err)
	}
	repaired := []byte(`{"type":"feat","scope":"cli","summary":"accept proposal","what":"Accept the structured handoff.","why":"Avoid a nested message session."}`)
	proposal, err := decodeSliceCommitProposal(repaired)
	if err != nil {
		t.Fatalf("repaired proposal: %v", err)
	}
	if proposal.Scope != "cli" || proposal.Summary != "accept proposal" {
		t.Fatalf("decoded proposal = %#v", proposal)
	}
}

func TestValidateVerificationRunsRejectsExcessiveResultCount(t *testing.T) {
	results := make([]plan.VerificationRun, sliceCompleteMaxVerificationResults+1)
	for i := range results {
		results[i] = plan.VerificationRun{Command: "true", CWD: "/repo", Result: "passed"}
	}
	if err := validateVerificationRuns(results); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected result count limit error, got %v", err)
	}
}

func TestSliceCompleteHelpAndCompletion(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.Run(context.Background(), []string{"help"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), topLevelHelpRow(t, "slice-complete")) {
		t.Fatalf("expected slice-complete usage, got %q", out.String())
	}
	if !strings.Contains(out.String(), topLevelHelpRow(t, "approve")) {
		t.Fatalf("expected approve usage, got %q", out.String())
	}
	out.Reset()
	if err := app.completion([]string{"zsh"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"slice-complete:Complete a slice from notes, verification, and a commit proposal", "--commit-proposal-file[JSON file containing a structured commit proposal", "--verification-results-file[JSON file containing verification results]"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected completion to contain %q, got %q", want, out.String())
		}
	}
}
