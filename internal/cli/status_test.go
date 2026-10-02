package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

func persistStatusRepository(t *testing.T, repo taodata.Repo) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("TAO_DATA_HOME", home)
	repo.Schema = taodata.RepoSchema
	repo.Root = home
	repo.Name = "test-repo"
	dir := filepath.Join(home, "repos", repo.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(repo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "repo.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

// Legacy adapters remain callable for the compatibility release, but are not
// used to compose effective presentation.
func TestLegacyRepositoryStatusAdapter(t *testing.T) {
	repo := taodata.Repo{RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(false), Models: &taodata.RepoModelDefaults{Base: "legacy"}}}
	app := App{Registry: func() NoteRegistry { return &fakeNoteRegistry{current: repo} }}
	patch, err := app.currentRepositoryRunOptions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	rows := applyRepositoryRunDefaultsToStatus(runtimeconfig.LoadEnv(nil).Status(), patch)
	for _, row := range rows {
		switch row.Name {
		case runtimeconfig.EnvModel:
			if row.Value != "legacy" || row.Source != "repository" {
				t.Fatal(row)
			}
		case runtimeconfig.EnvPullRequest:
			if row.Value != "false" || row.Source != "repository" {
				t.Fatal(row)
			}
		}
	}
}

func TestStatusCommandApplicability(t *testing.T) {
	var out bytes.Buffer
	rows := snapshotWith(nil).Status()
	if err := (App{Out: &out}).writeStatus(statusPayload{RuntimeEnv: rows}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Precedence: built-in → global → repository → environment → flags") {
		t.Fatal("missing precedence guidance")
	}
	for _, row := range rows {
		commands := runtimeconfig.ApplicableCommands(row.Name)
		if len(commands) == 0 {
			continue
		}
		start := strings.Index(out.String(), row.Name+" ")
		if start < 0 {
			t.Fatalf("missing row %s", row.Name)
		}
		lines := strings.Split(out.String()[start:], "\n")
		want := "    applies to: " + strings.Join(commands, ", ")
		if len(lines) < 2 || lines[1] != want {
			t.Errorf("%s: want next line %q", row.Name, want)
		}
	}
}

func TestStatusMixedInvalidConfigurationKeepsCompleteRows(t *testing.T) {
	clearTaoEnv(t)
	values := map[string]string{
		runtimeconfig.EnvSessionWarnPercent: "100",
		runtimeconfig.EnvAgent:              "bad-agent", runtimeconfig.EnvUpdate: "bad-update",
		runtimeconfig.EnvBudgetPlanCostWarn: "bad-budget", runtimeconfig.EnvTheme: "bad-theme",
		runtimeconfig.EnvRunHeader: "bad-header", runtimeconfig.EnvPullRequest: "bad-bool",
		runtimeconfig.EnvModel: "bad model", runtimeconfig.EnvMaxSliceCostDeprecated: "10",
	}
	for key, value := range values {
		t.Setenv(key, value)
	}
	registered := taodata.Repo{ID: "repo-a", RunDefaults: &taodata.RepoRunDefaults{PullRequest: new(true), Models: &taodata.RepoModelDefaults{Base: "repo-model"}}}
	var out bytes.Buffer
	persistStatusRepository(t, registered)
	app := App{Out: &out, Registry: func() NoteRegistry { return &fakeNoteRegistry{current: registered, repos: []taodata.Repo{registered}} }, Repository: func(string) Repository { return nil }}
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
	for _, text := range []string{"rejected", "using default", "repo-model", "bad-bool", "bad-budget", "deprecated; use " + runtimeconfig.EnvBudgetSliceCostStop} {
		if !strings.Contains(out.String(), text) {
			t.Errorf("missing %q: %s", text, out.String())
		}
	}
	// A set alias appears with its warning; unset aliases are absent from both outputs.
	if !slices.Contains(names, runtimeconfig.EnvMaxSliceCostDeprecated) || slices.Contains(names, runtimeconfig.EnvMaxSliceOutputTokensDeprecated) || slices.Contains(names, runtimeconfig.EnvBudgetPlanCostDeprecated) {
		t.Fatalf("alias rows in text output: %v", names)
	}
	if strings.Contains(out.String(), runtimeconfig.EnvMaxSliceOutputTokensDeprecated) || strings.Contains(out.String(), runtimeconfig.EnvBudgetPlanCostDeprecated+" ") {
		t.Fatalf("unset alias named in text output: %s", out.String())
	}
	var aliasSeen bool
	for _, row := range payload.RuntimeEnv {
		switch row.Name {
		case runtimeconfig.EnvMaxSliceCostDeprecated:
			aliasSeen = true
			if row.Value != "10" || row.Source != "env" || row.Warning != "deprecated; use "+runtimeconfig.EnvBudgetSliceCostStop {
				t.Errorf("alias JSON row: %+v", row)
			}
		case runtimeconfig.EnvBudgetSliceCostStop:
			if row.Value != "10" || row.Source != "env" {
				t.Errorf("canonical JSON row: %+v", row)
			}
		case runtimeconfig.EnvMaxSliceOutputTokensDeprecated, runtimeconfig.EnvBudgetPlanCostDeprecated:
			t.Errorf("unset alias in JSON output: %+v", row)
		}
	}
	if !aliasSeen {
		t.Fatal("set alias missing from JSON output")
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
			if mode == "repository" {
				registered = registered.WithReviewAgentDefault("claude")
			}
			if mode == "env" {
				t.Setenv(runtimeconfig.EnvReviewAgent, "pi")
				for _, key := range modelKeys {
					t.Setenv(key, "environment")
				}
				t.Setenv(runtimeconfig.EnvReworkEscalationFromAttempt, "3")
			}
			if registered.ID != "" {
				persistStatusRepository(t, registered)
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
			keys := append(append([]string(nil), modelKeys...), runtimeconfig.EnvReworkEscalationFromAttempt, runtimeconfig.EnvReviewAgent)
			for i, key := range keys {
				want, source := "", mode
				switch key {
				case runtimeconfig.EnvReviewAgent:
					if mode == "repository" {
						want = "claude"
					}
					if mode == "env" {
						want = "pi"
					}
				case runtimeconfig.EnvReworkEscalationFromAttempt:
					want, source = "4", "default"
					if mode == "env" {
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
	if len(raw) != 4 || raw["runtime_env"] == nil || raw["plans"] == nil || raw["settings"] == nil || raw["paths"] == nil {
		t.Fatalf("unexpected status JSON fields: %s", out.String())
	}
	var payload statusPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.RuntimeEnv) != len(runtimeconfig.LoadEnv(nil).Status()) || payload.Plans.Total != 1 || payload.Plans.Statuses.InProgress != 1 {
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
	if payload.Plans.Total != 0 || len(payload.RuntimeEnv) != len(runtimeconfig.LoadEnv(nil).Status()) {
		t.Fatalf("unexpected status payload: %+v", payload)
	}
}

func TestStatusRepositoryReworkDefaults(t *testing.T) {
	clearTaoEnv(t)
	registered := taodata.Repo{ID: "repo-a", RunDefaults: &taodata.RepoRunDefaults{MaxReworkAttempts: new(0), ReworkEscalationFromAttempt: new(2)}}
	persistStatusRepository(t, registered)
	snapshot := snapshotWith(map[string]string{runtimeconfig.EnvMaxReworkAttempts: "9"})
	var out bytes.Buffer
	app := App{Out: &out, RuntimeEnv: snapshot, Registry: func() NoteRegistry { return &fakeNoteRegistry{current: registered, repos: []taodata.Repo{registered}} }}
	if err := app.status(context.Background(), nil, []string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var payload statusPayload
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	wants := map[string][2]string{runtimeconfig.EnvMaxReworkAttempts: {"9", "env"}, runtimeconfig.EnvReworkEscalationFromAttempt: {"2", "repository"}}
	for name, want := range wants {
		found := false
		for _, row := range payload.RuntimeEnv {
			if row.Name == name {
				found = true
				if row.Value != want[0] || row.Source != want[1] {
					t.Fatalf("row: %+v", row)
				}
			}
		}
		if !found {
			t.Fatal("missing", name)
		}
	}
	out.Reset()
	if err := app.status(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(out.String(), "\n") {
		if strings.Contains(line, runtimeconfig.EnvReworkEscalationFromAttempt) && (!strings.Contains(line, "repository") || !strings.Contains(line, "2")) {
			t.Fatal(line)
		}
	}
	if !strings.Contains(out.String(), "saved global=- repository=0") {
		t.Fatalf("masked saved value not shown: %s", out.String())
	}
}
