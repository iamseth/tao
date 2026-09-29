package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

func TestStatusMixedInvalidConfigurationKeepsCompleteRows(t *testing.T) {
	clearTaoEnv(t)
	values := map[string]string{
		runtimeconfig.EnvAgent: "bad-agent", runtimeconfig.EnvUpdate: "bad-update",
		runtimeconfig.EnvBudgetPlanCost: "bad-budget", runtimeconfig.EnvTheme: "bad-theme",
		runtimeconfig.EnvRunHeader: "bad-header", runtimeconfig.EnvPullRequest: "bad-bool",
		runtimeconfig.EnvModel: "bad model",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	registered := taodata.Repo{RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(true), Models: &taodata.RepoModelDefaults{Model: "repo-model"}}}
	var out bytes.Buffer
	app := App{Out: &out, Registry: func() NoteRegistry { return &fakeNoteRegistry{current: registered} }, Repository: func(string) Repository { return nil }}
	if err := app.Run(context.Background(), []string{"status", "--json"}); err != nil {
		t.Fatal(err)
	}
	var payload statusPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	want := snapshotWith(values).Status()
	if len(payload.RuntimeEnv) != len(want) {
		t.Fatalf("got %d rows, want %d", len(payload.RuntimeEnv), len(want))
	}
	for i, row := range payload.RuntimeEnv {
		if row.Name != want[i].Name || row.Warning != want[i].Warning {
			t.Errorf("lost order/diagnostic: %+v, want %+v", row, want[i])
		}
		source := want[i].Source
		if row.Name == runtimeconfig.EnvPullRequest || row.Name == runtimeconfig.EnvModel {
			source = "repository"
		}
		if row.Source != source {
			t.Errorf("source = %q, want %q for %s", row.Source, source, row.Name)
		}
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, line := range strings.Split(out.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasPrefix(fields[0], "TAO_") {
			names = append(names, fields[0])
		}
	}
	if len(names) != len(want) {
		t.Fatalf("got %d text rows, want %d", len(names), len(want))
	}
	for i, row := range want {
		if names[i] != row.Name {
			t.Fatalf("text row %d = %s, want %s", i, names[i], row.Name)
		}
	}
	for _, text := range []string{"rejected", "using default", "repo-model", "bad-bool", "bad-budget"} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing %q: %s", text, out.String())
		}
	}
}

func TestStatusInvalidThemeWarnsWithoutFailing(t *testing.T) {
	clearTaoEnv(t)
	t.Setenv("TAO_THEME", "nope")
	var out bytes.Buffer
	app := App{Out: &out, Repository: func(string) Repository { return fakeRepository{} }}
	if err := app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TAO_THEME", "tokyonight", "nope", "using default"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("status missing %q: %s", want, out.String())
		}
	}
}

func TestStatusRepositoryModelDefaults(t *testing.T) {
	modelKeys := []string{runtimeconfig.EnvModel, runtimeconfig.EnvRunModel, runtimeconfig.EnvReviewModel, runtimeconfig.EnvMergeReviewModel, runtimeconfig.EnvResolverModel, runtimeconfig.EnvReworkEscalationModel}
	for _, mode := range []string{"repository", "default", "env"} {
		t.Run(mode, func(t *testing.T) {
			clearTaoEnv(t)
			var registered taodata.Repo
			if mode == "repository" {
				if err := json.Unmarshal([]byte(`{"id":"repo-a","run_defaults":{"models":{"model":"base","run_model":"run","review_model":"review","merge_review_model":"merge","resolver_model":"resolver","rework_escalation_model":"escalation"}}}`), &registered); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "default" {
				for _, key := range modelKeys {
					t.Setenv(key, "environment")
				}
				t.Setenv(runtimeconfig.EnvReworkEscalationFromAttempt, "3")
			}
			registry := &fakeNoteRegistry{current: registered, repos: []taodata.Repo{registered}}
			var out bytes.Buffer
			app := App{Out: &out, Registry: func() NoteRegistry { return registry }, Repository: func(string) Repository { return fakeRepository{} }}
			if err := app.Run(context.Background(), []string{"status", "--json"}); err != nil {
				t.Fatal(err)
			}
			var payload statusPayload
			if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			out.Reset()
			if err := app.Run(context.Background(), []string{"status"}); err != nil {
				t.Fatal(err)
			}
			values := []string{"base", "run", "review", "merge", "resolver", "escalation"}
			keys := append(append([]string(nil), modelKeys...), runtimeconfig.EnvReworkEscalationFromAttempt)
			for i, key := range keys {
				want, source := "", mode
				switch key {
				case runtimeconfig.EnvReworkEscalationFromAttempt:
					want, source = "4", "default"
					if mode != "default" {
						want, source = "3", "env"
					}
				default:
					switch mode {
					case "repository":
						want = values[i]
					case "env":
						want = "environment"
					}
				}
				found := false
				for _, row := range payload.RuntimeEnv {
					if row.Name == key {
						found = true
						if row.Value != want || row.Source != source {
							t.Errorf("%s JSON row = %+v, want value %q source %s", key, row, want, source)
						}
					}
				}
				if !found {
					t.Errorf("missing JSON row %s", key)
				}
				if want == "" {
					want = "-"
				}
				found = false
				for _, line := range strings.Split(out.String(), "\n") {
					fields := strings.Fields(line)
					if len(fields) > 0 && fields[0] == key {
						found = true
						if strings.Join(fields, " ") != key+" "+want+" "+source {
							t.Errorf("status line = %q, want %s %s %s", line, key, want, source)
						}
					}
				}
				if !found {
					t.Errorf("missing text row %s", key)
				}
			}
		})
	}
}

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
	for _, want := range []string{"Runtime defaults:", "TAO_COMMIT_POLICY", "slice", "TAO_PULL_REQUEST", "true", "TAO_PLANNER_ROUTING", "TAO_THEME", "Plans:", "total      1", "verdicts   approve=1"} {
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

func TestStatusCountsAbandonmentSeparatelyFromCompletion(t *testing.T) {
	clearTaoEnv(t)
	summaries := []plan.PlanSummary{
		{ID: "abandoned", Status: plan.StatusAbandoned},
		{ID: "completed", Status: plan.StatusCompleted, Complete: true, Reviewed: true},
	}
	var out bytes.Buffer
	app := App{Out: &out, Repository: func(string) Repository { return fakeRepository{summaries: summaries} }}
	if err := app.Run(context.Background(), []string{"status"}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "1 completed, 1 abandoned") || !strings.Contains(text, "done       1 complete, 1 reviewed") || !strings.Contains(text, "abandoned  1") {
		t.Fatalf("status did not separate abandonment:\n%s", text)
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
