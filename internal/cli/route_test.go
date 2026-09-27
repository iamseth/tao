package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plannerroute"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

const testRouteID = "20260926-120000-0123456789abcdef0123456789abcdef"

func createTestRoute(t *testing.T, app App, repo taodata.Repo, id string) plannerroute.Record {
	t.Helper()
	arm := plannerroute.Arm{Runtime: runtimeconfig.AgentPi, Provider: plannerroute.Inherited, Model: plannerroute.Inherited, ReasoningEffort: plannerroute.Inherited, PromptVersion: "test-prompt", PermissionMode: "default"}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	record := plannerroute.Record{
		Schema: plannerroute.Schema, ID: id, RepoID: repo.ID, CreatedAt: now,
		Context:    plannerroute.Context{FeatureSchema: plannerroute.FeatureSchema, RepoID: repo.ID, RepoName: repo.Name, UnitKind: "note", UnitID: "note-1", BaselineRuntime: runtimeconfig.AgentPi},
		Assignment: plannerroute.Assignment{PolicyVersion: "test-policy", Mode: plannerroute.ModeShadow, Selected: arm, Eligible: []plannerroute.WeightedArm{{Arm: arm, Probability: 1}}},
		Entries:    []plannerroute.Entry{{Kind: plannerroute.EntryAssigned, At: now}},
	}
	if err := plannerroute.NewStore(app.registry().PlannerRoutesDir(repo)).Create(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	return record
}

func TestRouteShow(t *testing.T) {
	repo := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo"}
	app, out, _ := noteTestApp(t, nil, repo)
	ctx := context.Background()
	if err := app.Run(ctx, []string{"route", "show", testRouteID}); !errors.Is(err, plannerroute.ErrNotFound) {
		t.Fatalf("missing route error = %v", err)
	}
	record := createTestRoute(t, app, repo, testRouteID)
	if err := app.Run(ctx, []string{"route", "show", testRouteID}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{testRouteID, "Context:", "note-1", "Assignment:", "test-policy", "shadow", record.Assignment.Selected.Key(), "Entries:", "assigned"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("show missing %q: %s", want, out.String())
		}
	}
	out.Reset()
	if err := app.Run(ctx, []string{"route", "show", testRouteID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var got plannerroute.Record
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, record) {
		t.Fatalf("show JSON = %s, error = %v", out, err)
	}
}

func TestRouteListJSONAndWarnings(t *testing.T) {
	repo := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo"}
	other := taodata.Repo{ID: "other-456", Name: "other", Root: "/other"}
	app, out, errOut := noteTestApp(t, nil, repo, other)
	ctx := context.Background()
	if err := app.Run(ctx, []string{"route", "list", "--json"}); err != nil {
		t.Fatal(err)
	}
	var empty map[string]json.RawMessage
	if err := json.Unmarshal(out.Bytes(), &empty); err != nil || string(empty["schema"]) != `"tao.route.list.v1"` || string(empty["routes"]) != "[]" || string(empty["warnings"]) != "[]" || len(empty) != 3 {
		t.Fatalf("empty list JSON = %s, error = %v", out, err)
	}
	if _, err := os.Stat(app.registry().PlannerRoutesDir(repo)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read created ledger directory: %v", err)
	}
	record := createTestRoute(t, app, other, testRouteID)
	badID := "20260926-120001-0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(filepath.Join(app.registry().PlannerRoutesDir(other), badID+".json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, selector := range []string{"other", "other-4"} {
		out.Reset()
		if err := app.Run(ctx, []string{"route", "--repo", selector, "list", "--json"}); err != nil {
			t.Fatal(err)
		}
		var got struct {
			Schema   string                `json:"schema"`
			Routes   []plannerroute.Record `json:"routes"`
			Warnings []string              `json:"warnings"`
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got.Schema != "tao.route.list.v1" || !reflect.DeepEqual(got.Routes, []plannerroute.Record{record}) || len(got.Warnings) != 1 || !strings.Contains(got.Warnings[0], badID) {
			t.Fatalf("list JSON = %s, error = %v", out, err)
		}
	}
	out.Reset()
	if err := app.Run(ctx, []string{"route", "list", "--repo", other.ID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), testRouteID+"  shadow  "+record.Assignment.Selected.Key()+"  none") || !strings.Contains(errOut.String(), badID) {
		t.Fatalf("list = %s, warnings = %s", out, errOut)
	}
}

func TestRouteLink(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repo := taodata.Repo{ID: "tao-123", Name: "tao", Root: root}
	app, out, registry := noteArchiveTestApp(t, repo)
	createTestRoute(t, app, repo, testRouteID)
	planID := "20260926-1200-linked-plan"
	planDir := writeRunPlan(t, registry.PlansDir(repo), planID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	before := map[string]string{}
	for _, name := range []string{"state.json", "slices.json", "events.jsonl"} {
		before[name] = readText(t, filepath.Join(planDir, name))
	}
	ctx := context.Background()
	store := plannerroute.NewStore(registry.PlannerRoutesDir(repo))
	for range 2 {
		out.Reset()
		if err := app.Run(ctx, []string{"route", "link", testRouteID, "linked-plan"}); err != nil {
			t.Fatal(err)
		}
		if out.String() != "Linked route "+testRouteID+" to plan "+planID+"\n" {
			t.Fatalf("link output = %q", out.String())
		}
	}
	record, err := store.Load(ctx, testRouteID)
	if err != nil {
		t.Fatal(err)
	}
	links := 0
	for _, entry := range record.Entries {
		if entry.Kind == plannerroute.EntryLinked {
			links++
			if entry.Link.PlanID != planID || entry.Link.PlanDir != planDir {
				t.Fatalf("link = %+v", entry.Link)
			}
		}
	}
	if links != 1 {
		t.Fatalf("linked entries = %d", links)
	}
	out.Reset()
	if err := app.Run(ctx, []string{"route", "link", testRouteID, planID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var got plannerroute.Record
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !reflect.DeepEqual(got, record) {
		t.Fatalf("link JSON = %s, error = %v", out, err)
	}
	for name, content := range before {
		if readText(t, filepath.Join(planDir, name)) != content {
			t.Errorf("link changed plan artifact %s", name)
		}
	}
	otherPlanID := "20260926-1201-other-plan"
	writeRunPlan(t, registry.PlansDir(repo), otherPlanID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	_, wantErr := store.Link(ctx, testRouteID, otherPlanID, "unused")
	if err := app.Run(ctx, []string{"route", "link", testRouteID, otherPlanID}); !errors.Is(err, plannerroute.ErrAlreadyLinked) || err.Error() != wantErr.Error() {
		t.Fatalf("already linked error = %v, want %v", err, wantErr)
	}
	otherRouteID := "20260926-120001-0123456789abcdef0123456789abcdef"
	createTestRoute(t, app, repo, otherRouteID)
	_, wantErr = store.Link(ctx, otherRouteID, planID, planDir)
	if err := app.Run(ctx, []string{"route", "link", otherRouteID, planID}); !errors.Is(err, plannerroute.ErrPlanAlreadyRouted) || err.Error() != wantErr.Error() {
		t.Fatalf("plan routed error = %v, want %v", err, wantErr)
	}
}

func TestRouteLinkRejectsCrossRepositoryPlans(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for _, outside := range []bool{false, true} {
		t.Run(map[bool]string{false: "foreign recorded root", true: "foreign plan directory"}[outside], func(t *testing.T) {
			repo := taodata.Repo{ID: "tao-123", Name: "tao", Root: root}
			if !outside {
				repo.Root = t.TempDir()
			}
			app, _, registry := noteArchiveTestApp(t, repo)
			original := createTestRoute(t, app, repo, testRouteID)
			plansDir := registry.PlansDir(repo)
			if err := os.MkdirAll(plansDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if outside {
				plansDir = t.TempDir()
			}
			planDir := writeRunPlan(t, plansDir, "20260926-1200-foreign", plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
			if err := app.Run(context.Background(), []string{"route", "link", testRouteID, planDir}); err == nil || !strings.Contains(err.Error(), "repository") {
				t.Fatalf("cross-repository error = %v", err)
			}
			got, err := plannerroute.NewStore(registry.PlannerRoutesDir(repo)).Load(context.Background(), testRouteID)
			if err != nil || !reflect.DeepEqual(got, original) {
				t.Fatalf("rejection mutated route: %+v, %v", got, err)
			}
		})
	}
}

func TestRouteUsageAndCompletion(t *testing.T) {
	app, _, _ := noteTestApp(t, nil, taodata.Repo{ID: "tao-123"})
	for _, args := range [][]string{{}, {"unknown"}, {"show"}, {"list", "extra"}, {"link", testRouteID}, {"show", testRouteID, "extra"}} {
		if err := app.Run(context.Background(), append([]string{"route"}, args...)); err == nil {
			t.Fatalf("accepted invalid route arguments %v", args)
		}
	}
	completion := zshPositionalCompletion("route", "link")
	if !strings.Contains(completion, "_tao_at_positional 4 2") || !strings.Contains(completion, "complete plan-ids") {
		t.Fatalf("link plan completion = %q", completion)
	}
}
