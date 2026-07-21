package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
)

func TestDeleteUsageAndMissingForce(t *testing.T) {
	var out bytes.Buffer
	app := App{Out: &out, Err: &out}
	if err := app.delete(context.Background(), fakeRepository{}, nil); err == nil || !strings.Contains(err.Error(), "usage: tao delete") {
		t.Fatalf("expected delete usage error, got %v", err)
	}
	err := app.delete(context.Background(), fakeRepository{}, []string{"20260430-1200-delete"})
	if err == nil || !strings.Contains(err.Error(), "--force is required") {
		t.Fatalf("expected missing force error, got %v", err)
	}
}

func TestDeleteDeletesCompletedPlan(t *testing.T) {
	var out bytes.Buffer
	root := t.TempDir()
	planID := "20260430-1200-delete"
	planDir := writeRunPlan(t, root, planID, plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	app := App{Out: &out, Err: &out}

	if err := app.Run(context.Background(), []string{"--plans-dir", root, "delete", planID, "--force"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(planDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected plan directory to be deleted, stat error %v", err)
	}
	for _, want := range []string{"deleted plan " + planID, planDir} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected delete output to contain %q, got %q", want, out.String())
		}
	}
}

func TestDeleteForceDeletesActivePlan(t *testing.T) {
	var out bytes.Buffer
	root := t.TempDir()
	planID := "20260430-1200-delete"
	planDir := writeRunPlan(t, root, planID, plan.StatusInProgress, []string{"001-a"}, nil, "001-a", plan.StatusInProgress)
	app := App{Out: &out, Err: &out}

	if err := app.Run(context.Background(), []string{"--plans-dir", root, "de", planID, "--force"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(planDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected active plan directory to be deleted, stat error %v", err)
	}
	if !strings.Contains(out.String(), "deleted plan "+planID) {
		t.Fatalf("expected delete output to contain plan id, got %q", out.String())
	}
}
