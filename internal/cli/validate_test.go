package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestValidateSliceTimingGolden(t *testing.T) {
	for _, evidence := range []bool{false, true} {
		t.Run(map[bool]string{false: "without evidence", true: "matching evidence"}[evidence], func(t *testing.T) {
			var out bytes.Buffer
			detail := validatePlanDetail(t.TempDir(), []string{"go version"}, nil)
			detail.State.Status = plan.StatusInProgress
			current := detail.Slices.Slices[0].ID
			detail.State.Plan.CurrentSlice = &current
			detail.Slices.Slices[0].Status = plan.StatusInProgress
			if evidence {
				detail.Events = []plan.Event{{Type: plan.EventTypeSliceStarted, PlanID: detail.State.Plan.ID, SliceID: current, Timestamp: time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)}}
			}
			before, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			repo := fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}
			err = (newTestApp(App{Out: &out, Err: &out, Repository: func(string) Repository { return repo }})).Run(context.Background(), []string{"validate", "example"})
			if !errors.Is(err, errPlanValidationFailed) {
				t.Fatalf("expected validation failure, got %v; output:\n%s", err, out.String())
			}
			after, err := json.Marshal(detail)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("validation mutated detail")
			}
			assertGolden(t, "testdata/slice_timing_validation.golden", out.Bytes())
		})
	}
}

func TestValidateSliceTimingLeavesArtifactsUnchanged(t *testing.T) {
	for _, evidence := range []bool{false, true} {
		t.Run(map[bool]string{false: "without evidence", true: "matching evidence"}[evidence], func(t *testing.T) {
			fixture := newRunPlanFixture(t, plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
			if evidence {
				event, err := json.Marshal(plan.Event{Type: plan.EventTypeSliceStarted, PlanID: fixture.id, SliceID: "001-a", Timestamp: time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(fixture.dir, "events.jsonl"), append(event, '\n'), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := map[string]string{}
			for _, name := range []string{"state.json", "slices.json", "events.jsonl"} {
				before[name] = readText(t, filepath.Join(fixture.dir, name))
			}
			var out bytes.Buffer
			err := (newTestApp(App{Out: &out, Err: &out})).Run(context.Background(), []string{"--plans-dir", fixture.root, "validate", fixture.id})
			if !errors.Is(err, errPlanValidationFailed) || !strings.Contains(out.String(), "timing.started_at") {
				t.Fatalf("expected timing failure, got %v; output:\n%s", err, out.String())
			}
			for name, content := range before {
				if readText(t, filepath.Join(fixture.dir, name)) != content {
					t.Fatalf("validation changed %s", name)
				}
			}
		})
	}
}

func TestValidateApprovalContractGolden(t *testing.T) {
	var out bytes.Buffer
	detail := validatePlanDetail(t.TempDir(), []string{"go version"}, nil)
	slice := &detail.Slices.Slices[0]
	slice.Goal = "The user's mGBA check is supplied through this slice's approval."
	slice.Tasks = []string{"Use the supplied facts.", "Approval must state observer/emulator/version/date/outcome."}
	slice.Approval = &plan.Approval{Required: true, Approved: true}
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}
	err := (newTestApp(App{Out: &out, Err: &out, Repository: func(string) Repository { return repo }})).Run(context.Background(), []string{"validate", "example"})
	if !errors.Is(err, errPlanValidationFailed) {
		t.Fatalf("expected validation failure, got %v; output:\n%s", err, out.String())
	}
	for _, want := range []string{"error 001-a", "goal", "tasks[1]", "authorization-only", "required_inputs", "operator amendment", "--add-task"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in output:\n%s", want, out.String())
		}
	}
	assertGolden(t, "testdata/approval_contract_validation.golden", out.Bytes())
}

func TestValidateUsageAndDispatch(t *testing.T) {
	var out bytes.Buffer
	app := newTestApp(App{Out: &out, Err: &out})
	if err := app.validate(context.Background(), fakeRepository{}, nil); err == nil {
		t.Fatal("expected validate usage error")
	}

	repo := fakeRepository{details: map[string]*plan.PlanDetail{
		"example": validatePlanDetail(t.TempDir(), []string{"go version"}, nil),
	}}
	if err := (newTestApp(App{Out: &out, Err: &out, Repository: func(string) Repository { return repo }})).Run(context.Background(), []string{"validate", "example"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Validation: 20260427-1810-example") || !strings.Contains(out.String(), "No validation findings.") {
		t.Fatalf("unexpected validate output:\n%s", out.String())
	}
}

func TestValidatePrintsWarningsWithoutFailing(t *testing.T) {
	var out bytes.Buffer
	repoRoot := t.TempDir()
	detail := validatePlanDetail(repoRoot, []string{"go test missing_test.go"}, []string{"events.jsonl line 1: invalid"})
	err := newTestApp(App{Out: &out, Err: &out}).validate(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}, []string{"example"})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Artifact Warnings:", "events.jsonl line 1: invalid", "Verification Findings:", "warning 001-a", "missing_test.go"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
}

func TestValidateLegacyPlacementWarningsAreAdvisory(t *testing.T) {
	var out bytes.Buffer
	warnings := []string{
		"state.json workspace.strategy uses legacy worktree; prefer isolated",
		"slices.json slice 001-a execution_start.workspace_strategy uses legacy worktree; prefer isolated",
	}
	detail := validatePlanDetail(t.TempDir(), []string{"go version"}, warnings)
	detail.State.Workspace = &plan.Workspace{Strategy: plan.WorkspaceStrategyWorktree}
	err := (newTestApp(App{Out: &out, Err: &out})).validate(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}, []string{"example"})
	if err != nil {
		t.Fatal(err)
	}
	for _, warning := range warnings {
		if !strings.Contains(out.String(), warning) {
			t.Fatalf("missing warning %q: %s", warning, out.String())
		}
	}
}

func TestValidatePrintsAgentBudgetWarningsWithoutFailing(t *testing.T) {
	var out bytes.Buffer
	detail := validatePlanDetail(t.TempDir(), []string{"go version"}, nil)
	detail.Events = []plan.Event{{
		Type:      plan.EventTypeAgentMetrics,
		Timestamp: time.Date(2026, 4, 27, 18, 0, 0, 0, time.UTC),
		PlanID:    "20260427-1810-example",
		SliceID:   "001-a",
		Metrics: &plan.AgentMetrics{
			SessionID:    "session-1",
			Status:       plan.StatusCompleted,
			OutputTokens: 150001,
		},
	}}
	err := newTestApp(App{Out: &out, Err: &out}).validate(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}, []string{"example"})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Agent Metrics Budget Warnings:", "output_tokens", "observed 150001 > threshold 150000"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
}

func TestValidatePrintsGuardrailWarningsWithoutFailing(t *testing.T) {
	var out bytes.Buffer
	detail := validatePlanDetail(t.TempDir(), []string{"go test ./..."}, nil)
	detail.Slices.Slices[0].ExpectedFiles = []string{"internal/...", "internal/plan/validate.go"}
	detail.State.Plan.PendingSlices = append(detail.State.Plan.PendingSlices, "002-gate")
	detail.Slices.Slices = append(detail.Slices.Slices, plan.Slice{
		ID: "002-gate", Status: plan.StatusPending,
		Verification: plan.Verification{Commands: []string{"make verify"}},
	})
	repo := fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}
	err := (newTestApp(App{Out: &out, Err: &out, Repository: func(string) Repository { return repo }})).Run(context.Background(), []string{"validate", "example"})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Verification Findings:", "warning 001-a", "slice expected file", "verification command is broad", "slice 001-a changes Go files in internal/plan without a lint command while slice 002-gate declares the repository gate make verify"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
}

func TestValidateReturnsErrorForMissingRequiredInput(t *testing.T) {
	var out bytes.Buffer
	detail := validatePlanDetail(t.TempDir(), []string{"go test ."}, nil)
	detail.Slices.Slices[0].RequiredInputs = []plan.RequiredInput{{Path: "missing.txt", Kind: plan.RequiredInputFile, Reason: "test fixture"}}
	err := newTestApp(App{Out: &out, Err: &out}).validate(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}, []string{"example"})
	if !errors.Is(err, errPlanValidationFailed) {
		t.Fatalf("expected validation failure, got %v", err)
	}
	text := out.String()
	if !strings.Contains(text, "error 001-a") || !strings.Contains(text, "does not exist") {
		t.Fatalf("expected error finding in output:\n%s", text)
	}
}

func TestValidateResolvesPrefixesSlugsAndPaths(t *testing.T) {
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	app := newTestApp(App{Out: io.Discard, Err: io.Discard})
	for _, args := range [][]string{
		{"--plans-dir", fixture.root, "v", "20260430"},
		{"--plans-dir", fixture.root, "val", "run"},
		{"--plans-dir", fixture.root, "validate", fixture.dir},
	} {
		if err := app.Run(context.Background(), args); err != nil {
			t.Fatalf("validate args %v failed: %v", args, err)
		}
	}
}

func TestValidateSiblingPlansGolden(t *testing.T) {
	root, id := siblingCLIArtifacts(t)
	var out bytes.Buffer
	err := newTestApp(App{Out: &out, Err: &out}).Run(context.Background(), []string{"--plans-dir", root, "validate", id})
	if err != nil || !strings.Contains(out.String(), "Advisory snapshot") {
		t.Fatalf("expected successful advisory validation: %v\n%s", err, out.String())
	}
	assertGolden(t, "testdata/sibling_plans_validation.golden", out.Bytes())
}

func TestValidateSiblingCoverageAndErrors(t *testing.T) {
	for _, mode := range []string{"no overlap", "incomplete", "invalid"} {
		t.Run(mode, func(t *testing.T) {
			root, id := siblingCLIArtifacts(t)
			if mode != "invalid" {
				if err := os.RemoveAll(filepath.Join(root, "sibling")); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "incomplete" {
				if err := os.Mkdir(filepath.Join(root, "broken"), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "invalid" {
				path := filepath.Join(root, id, "slices.json")
				var file plan.SlicesFile
				if err := json.Unmarshal([]byte(readText(t, path)), &file); err != nil {
					t.Fatal(err)
				}
				file.Slices[0].RequiredInputs = []plan.RequiredInput{{Path: "missing.txt", Kind: plan.RequiredInputFile, Reason: "required fixture"}}
				data, err := json.Marshal(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			err := newTestApp(App{Out: &out, Err: &out}).Run(context.Background(), []string{"--plans-dir", root, "validate", id})
			if mode == "invalid" {
				if !errors.Is(err, errPlanValidationFailed) || !strings.Contains(out.String(), "BEGIN SIBLING DATA") {
					t.Fatalf("expected validation error with advisory context: %v\n%s", err, &out)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if mode == "incomplete" && !strings.Contains(out.String(), "Coverage gap") {
				t.Fatalf("missing gap: %s", &out)
			}
			if mode == "no overlap" && out.String() != "Validation: target\nNo validation findings.\n" {
				t.Fatalf("changed no-overlap output: %s", &out)
			}
		})
	}
}

// Real sibling artifacts live beside the explicit target, including a foreign repository.
func siblingCLIArtifacts(t *testing.T) (string, string) {
	t.Helper()
	root, repoRoot := t.TempDir(), t.TempDir()
	for _, id := range []string{"target", "sibling", "foreign"} {
		detail := validatePlanDetail(repoRoot, []string{"go version"}, nil)
		detail.State.Plan.ID = id
		detail.Slices.PlanID = id
		detail.State.Schema = "tao.plan.state.v1"
		detail.Slices.Schema = "tao.plan.slices.v1"
		detail.Slices.Slices[0].ExpectedFiles = []string{"shared.md"}
		detail.Slices.Slices[0].Goal = "Shared documentation"
		if id == "foreign" {
			detail.State.Repo.Root = t.TempDir()
		}
		dir := filepath.Join(root, id)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "planning-brief.md"), []byte("# Brief\n\n## User Goal\nDocumentation\n## Constraints\nNone\n## Non-goals\nNone\n## Expected Files/Packages\nshared.md\n## Validation Strategy\ngo version\n## Open Questions\nNone\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]any{"state.json": detail.State, "slices.json": detail.Slices} {
			data, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root, "target"
}

func validatePlanDetail(repoRoot string, commands []string, warnings []string) *plan.PlanDetail {
	return &plan.PlanDetail{
		Dir: filepath.Join(repoRoot, "plan"),
		State: plan.State{
			Status: plan.StatusPlanned,
			Repo:   plan.Repo{Name: "repo", Root: repoRoot, Branch: "feature"},
			Plan: plan.PlanState{
				ID:            "20260427-1810-example",
				Title:         "Example",
				PendingSlices: []string{"001-a"},
			},
		},
		Slices: plan.SlicesFile{PlanID: "20260427-1810-example", Slices: []plan.Slice{{
			ID:     "001-a",
			Title:  "A",
			Status: plan.StatusPending,
			Verification: plan.Verification{
				Commands: commands,
			},
		}}},
		Warnings: warnings,
	}
}
