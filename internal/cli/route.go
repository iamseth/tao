package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plannerroute"
	"github.com/iamseth/tao/internal/taodata"
)

var routeCommand = commandMetadata{
	name:                  "route",
	usageLines:            []string{"route show [--repo REPO] [--json] <route-id>", "route list [--repo REPO] [--json]", "route link [--repo REPO] [--json] <route-id> <plan-id>"},
	completionDescription: "Inspect and link planner routes",
	long:                  "Inspect the repository-scoped planner routing ledger or link a route to a plan in the same repository. Routing records are non-authoritative and never change plan artifacts.",
	examples:              "  tao route list --json\n  tao route show <route-id>\n  tao route link --repo <repo-id> <route-id> <plan-id>",
	subcommands: []commandSubcommand{
		{name: "show", description: "Show a planner route", completion: completionContext{positional: completionPositional{index: 1, label: "route ID"}}},
		{name: "list", description: "List planner routes and ledger warnings"},
		{name: "link", description: "Link a planner route to a plan", completion: completionContext{positional: completionPositional{index: 2, label: "plan", completer: completePlanIDs}}},
	},
	registerFlags: registerRouteFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"repo": {kind: completionValueText, label: "repository"},
	}},
	execute: func(c commandContext) error { return c.app.route(c.ctx, c.args) },
}

func registerRouteFlags(fs *flag.FlagSet) {
	fs.String("repo", "", "registered repository ID prefix or exact name")
	fs.Bool("json", false, "write JSON")
}

func (a App) route(ctx context.Context, args []string) error {
	fs, positional, err := a.parseArgs("route", args, registerRouteFlags)
	if err != nil {
		return err
	}
	if len(positional) == 0 {
		return errors.New("usage: tao route show|list|link [--repo REPO] [--json]")
	}
	subcommand := positional[0]
	positional = positional[1:]
	switch subcommand {
	case "show":
		err = requirePositionals(positional, 1, "usage: tao route show [--repo REPO] [--json] <route-id>")
	case "list":
		err = requireNoArgs(positional, "usage: tao route list [--repo REPO] [--json]")
	case "link":
		err = requirePositionals(positional, 2, "usage: tao route link [--repo REPO] [--json] <route-id> <plan-id>")
	default:
		return fmt.Errorf("unknown route subcommand %q; use show, list, or link", subcommand)
	}
	if err != nil {
		return err
	}
	registered, err := taodata.ResolveRepo(ctx, a.registry(), flagStringValue(fs, "repo"))
	if err != nil {
		return err
	}
	store := plannerroute.NewStore(a.registry().PlannerRoutesDir(registered))
	asJSON := flagBoolValue(fs, "json")
	if subcommand == "list" {
		return a.routeList(ctx, store, asJSON)
	}
	var record plannerroute.Record
	if subcommand == "show" {
		record, err = store.Load(ctx, positional[0])
	} else {
		plansDir := a.registry().PlansDir(registered)
		var detail *plan.PlanDetail
		detail, err = a.repository(plansDir).ResolvePlan(ctx, positional[1])
		if err == nil {
			err = validateRoutePlanRepository(detail, registered, plansDir)
		}
		if err == nil {
			record, err = store.Link(ctx, positional[0], detail.State.Plan.ID, detail.Dir)
		}
	}
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(a.Out).Encode(record)
	}
	if subcommand == "link" {
		return writef(a.Out, "Linked route %s to plan %s\n", record.ID, record.LinkedPlanID())
	}
	return a.routeShow(record)
}

// ResolvePlan accepts filesystem paths as well as IDs. Check both the ledger's
// repository namespace and the plan's recorded root before writing a link; no
// execution or verification gate is appropriate for this observational ledger.
func validateRoutePlanRepository(detail *plan.PlanDetail, registered taodata.Repo, plansDir string) error {
	if detail == nil {
		return errors.New("plan loader returned no plan")
	}
	root := strings.TrimSpace(detail.State.Repo.Root)
	if root == "" || registered.Root == "" || filepath.Clean(root) != filepath.Clean(registered.Root) {
		return fmt.Errorf("recorded repository root %q does not match registered repository %s root %q", root, registered.ID, registered.Root)
	}
	plansRoot, err := filepath.EvalSymlinks(plansDir)
	if err != nil {
		return fmt.Errorf("resolve repository plans directory: %w", err)
	}
	planDir, err := filepath.EvalSymlinks(detail.Dir)
	if err != nil {
		return fmt.Errorf("resolve plan directory: %w", err)
	}
	plansRoot, err = filepath.Abs(plansRoot)
	if err != nil {
		return err
	}
	planDir, err = filepath.Abs(planDir)
	if err != nil {
		return err
	}
	if filepath.Dir(planDir) != plansRoot {
		return fmt.Errorf("plan directory %q is outside repository plans directory %q", detail.Dir, plansDir)
	}
	if detail.State.Plan.ID == "" || filepath.Base(planDir) != detail.State.Plan.ID {
		return errors.New("plan directory does not match its recorded ID")
	}
	return nil
}

func (a App) routeList(ctx context.Context, store *plannerroute.Store, asJSON bool) error {
	records, warnings, err := store.List(ctx)
	if err != nil {
		return err
	}
	if asJSON {
		if records == nil {
			records = []plannerroute.Record{}
		}
		if warnings == nil {
			warnings = []string{}
		}
		return json.NewEncoder(a.Out).Encode(struct {
			Schema   string                `json:"schema"`
			Routes   []plannerroute.Record `json:"routes"`
			Warnings []string              `json:"warnings"`
		}{Schema: "tao.route.list.v1", Routes: records, Warnings: warnings})
	}
	for _, warning := range warnings {
		if err := writef(a.Err, "warning: %s\n", warning); err != nil {
			return err
		}
	}
	for _, record := range records {
		linked := record.LinkedPlanID()
		if linked == "" {
			linked = "none"
		}
		if err := writef(a.Out, "%s  %s  %s  %s\n", record.ID, record.Assignment.Mode, record.Assignment.Selected.Key(), linked); err != nil {
			return err
		}
	}
	return nil
}

func (a App) routeShow(record plannerroute.Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Route: %s\nRepository: %s\nCreated: %s\n", record.ID, record.RepoID, record.CreatedAt.Format(time.RFC3339))
	c := record.Context
	fmt.Fprintf(&b, "Context:\n  Repository: %s (%s)\n  Unit: %s %s\n  Tags: %s\n  Note text bucket: %s\n  Baseline runtime: %s\n  Prompt version: %s\n  Permission mode: %s\n  Build version: %s\n", c.RepoName, c.RepoID, c.UnitKind, c.UnitID, strings.Join(c.NoteTags, ", "), c.NoteTextBucket, c.BaselineRuntime, c.PromptVersion, c.PermissionMode, c.BuildVersion)
	assignment := record.Assignment
	fmt.Fprintf(&b, "Assignment:\n  Mode: %s\n  Policy: %s\n  Selected arm: %s\n  Draw: %g\n  Manual override: %t\n", assignment.Mode, assignment.PolicyVersion, assignment.Selected.Key(), assignment.Draw, assignment.ManualOverride)
	if assignment.OverrideArm != nil {
		fmt.Fprintf(&b, "  Override arm: %s\n", assignment.OverrideArm.Key())
	}
	for _, eligible := range assignment.Eligible {
		fmt.Fprintf(&b, "  Eligible: %s (probability %g)\n", eligible.Arm.Key(), eligible.Probability)
	}
	b.WriteString("Entries:\n")
	for _, entry := range record.Entries {
		fmt.Fprintf(&b, "  %s %s", entry.At.Format(time.RFC3339), entry.Kind)
		if entry.Treatment != nil {
			t := entry.Treatment
			fmt.Fprintf(&b, " runtime=%s provider=%s model=%s metrics=%s failover=%s", t.RuntimeLabel, t.ProviderID, t.ModelID, t.MetricsAvailability, t.Failover)
		}
		if entry.Attempt != nil {
			fmt.Fprintf(&b, " stage=%s outcome=%s", entry.Attempt.Stage, entry.Attempt.Outcome)
		}
		if entry.Link != nil {
			fmt.Fprintf(&b, " plan=%s directory=%s", entry.Link.PlanID, entry.Link.PlanDir)
		}
		b.WriteByte('\n')
	}
	return writef(a.Out, "%s", b.String())
}
