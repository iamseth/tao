package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runqueue"
)

func TestStatusShowsRuntimeEnvAndPlanRollup(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_COMMIT_POLICY", "slice")
	t.Setenv("TAO_PULL_REQUEST", "1")
	summaries := []plan.PlanSummary{{ID: "plan-a", Status: plan.StatusCompleted, Complete: true, Reviewed: true, ReviewVerdict: "approve"}}
	var out bytes.Buffer
	app := App{Out: &out, Repository: func(string) Repository { return fakeRepository{summaries: summaries} }}
	if err := app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Runtime defaults:", "TAO_COMMIT_POLICY", "slice", "TAO_PULL_REQUEST", "true", "Plans:", "total      1", "verdicts   approve=1"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in status output, got %q", want, out.String())
		}
	}
	for _, notWant := range []string{"Serve:", "Queue:"} {
		if strings.Contains(out.String(), notWant) {
			t.Fatalf("status output unexpectedly contains %q: %q", notWant, out.String())
		}
	}
}

func TestStatusJSONContainsOnlyLocalStatus(t *testing.T) {
	clearTaoEnv(t)
	var out bytes.Buffer
	app := App{Out: &out, Repository: func(string) Repository {
		return fakeRepository{summaries: []plan.PlanSummary{{Status: plan.StatusInProgress}}}
	}}
	if err := app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &raw); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if len(raw) != 2 || raw["runtime_env"] == nil || raw["plans"] == nil {
		t.Fatalf("unexpected status JSON fields: %s", out.String())
	}
	var payload statusPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.RuntimeEnv) != len(taoEnvKeys()) || payload.Plans.Total != 1 || payload.Plans.Statuses.InProgress != 1 {
		t.Fatalf("unexpected status payload: %+v", payload)
	}
}

func TestStatusWithNoPlanRepositoryShowsEmptyRollup(t *testing.T) {
	clearTaoEnv(t)
	var out bytes.Buffer
	app := App{Out: &out, Repository: func(string) Repository { return nil }}
	if err := app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Runtime defaults:", "Plans:", "total      0", "verdicts   -"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("expected %q in status output, got %q", want, out.String())
		}
	}
}

func TestStatusJSONWithPlanListErrorIsValidAndEmpty(t *testing.T) {
	clearTaoEnv(t)
	var out bytes.Buffer
	app := App{Out: &out, Repository: func(string) Repository { return fakeRepository{err: errors.New("plans unavailable")} }}
	if err := app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var payload statusPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out.String())
	}
	if payload.Plans.Total != 0 || len(payload.RuntimeEnv) != len(taoEnvKeys()) {
		t.Fatalf("unexpected status payload: %+v", payload)
	}
}

func TestQueueStatusSummaryLineCountsReviewedSucceeded(t *testing.T) {
	clearTaoEnv(t)
	configureQueueDataHome(t)
	queuedAt := time.Date(2026, 6, 28, 3, 0, 0, 0, time.UTC)
	store := queueStoreForTest(t)
	if err := store.SaveSnapshot(runqueue.QueueSnapshot{Entries: []runqueue.QueueEntry{
		{PlanID: "plan-a", Status: runqueue.QueueStatusSucceeded, QueuedAt: queuedAt},
		{PlanID: "plan-b", Status: runqueue.QueueStatusSucceeded, QueuedAt: queuedAt},
		{PlanID: "plan-c", Status: runqueue.QueueStatusFailed, QueuedAt: queuedAt, Error: "boom"},
		{PlanID: "plan-d", Status: runqueue.QueueStatusRunning, QueuedAt: queuedAt},
		{PlanID: "plan-e", Status: runqueue.QueueStatusPending, QueuedAt: queuedAt},
	}}); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	app := App{Out: &out, Repository: func(string) Repository {
		return fakeRepository{summaries: []plan.PlanSummary{{ID: "plan-a", Reviewed: true}, {ID: "plan-b"}}}
	}}
	if err := app.Run(context.Background(), []string{"queue", "status"}); err != nil {
		t.Fatal(err)
	}
	assertContains(t, out.String(), "Summary: 5 visible (1 running, 1 queued, 1 failed, 2 succeeded (1 reviewed))")
}
