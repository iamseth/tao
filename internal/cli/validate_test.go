package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestValidateUsageAndDispatch(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.validate(context.Background(), fakeRepository{}, nil); err == nil {
		t.Fatal("expected validate usage error")
	}

	repo := fakeRepository{details: map[string]*plan.PlanDetail{
		"example": validatePlanDetail(t.TempDir(), []string{"go version"}, nil),
	}}
	if err := (App{Out: &out, Err: &out, Repository: func(string) Repository { return repo }}).Run(context.Background(), []string{"validate", "example"}); err != nil {
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
	err := App{Out: &out, Err: &out}.validate(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}, []string{"example"})
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
	err := App{Out: &out, Err: &out}.validate(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}, []string{"example"})
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
	detail.Slices.Slices[0].ExpectedFiles = []string{"internal/..."}
	err := App{Out: &out, Err: &out}.validate(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}, []string{"example"})
	if err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"Verification Findings:", "warning 001-a", "slice expected file", "verification command is broad"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected %q in output:\n%s", want, text)
		}
	}
}

func TestValidateReturnsErrorForMissingRequiredInput(t *testing.T) {
	var out bytes.Buffer
	detail := validatePlanDetail(t.TempDir(), []string{"go test ."}, nil)
	detail.Slices.Slices[0].RequiredInputs = []plan.RequiredInput{{Path: "missing.txt", Kind: plan.RequiredInputFile, Reason: "test fixture"}}
	err := App{Out: &out, Err: &out}.validate(context.Background(), fakeRepository{details: map[string]*plan.PlanDetail{"example": detail}}, []string{"example"})
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
	app := App{Out: io.Discard, Err: io.Discard}
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

func validatePlanDetail(repoRoot string, commands []string, warnings []string) *plan.PlanDetail {
	return &plan.PlanDetail{
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
