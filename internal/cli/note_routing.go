package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"strings"
	"unicode/utf8"

	agentpkg "github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/build"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plannerroute"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/prompts"
)

type plannerRouting struct {
	Enabled    bool
	Context    plannerroute.Context
	Assignment plannerroute.Assignment
	id         string
	store      *plannerroute.Store
}

func (a App) resolvePlannerRouting(fs *flag.FlagSet, registered taodata.Repo, item note.Note, baseline runtimeconfig.AgentKind, permission agentpkg.PermissionMode) (plannerRouting, error) {
	snapshot := a.envSnapshot()
	config := snapshot.Defaults().PlannerRouting
	mode := plannerroute.Mode(config.Mode)
	if flagWasProvided(fs, "planner-routing") {
		var err error
		mode, err = plannerroute.ParseMode(strings.TrimSpace(flagStringValue(fs, "planner-routing")))
		if err != nil {
			return plannerRouting{}, err
		}
	} else if err := snapshot.Require(runtimeconfig.EnvPlannerRouting); err != nil {
		return plannerRouting{}, err
	}
	if mode == plannerroute.ModeOff {
		return plannerRouting{}, nil
	}
	if err := snapshot.Require(runtimeconfig.EnvPlannerRoutingArms, runtimeconfig.EnvPlannerRoutingFloor); err != nil {
		return plannerRouting{}, err
	}
	arms := plannerroute.ArmsFromConfig(config.Arms)
	version, err := prompts.TemplateVersion("note-slice")
	if err != nil {
		return plannerRouting{}, err
	}
	for i := range arms {
		arms[i].Arm.PromptVersion = version
		arms[i].Arm.PermissionMode = string(permission)
	}
	policy, err := plannerroute.NewPolicy(mode, arms, config.Floor)
	if err != nil {
		return plannerRouting{}, err
	}
	var installed []runtimeconfig.AgentKind
	for _, descriptor := range agentpkg.Installed() {
		installed = append(installed, descriptor.Kind)
	}
	eligible, err := plannerroute.Eligible(policy, installed)
	if err != nil {
		return plannerRouting{}, err
	}
	var override *plannerroute.Arm
	if flagWasProvided(fs, "planner-arm") {
		selector := strings.TrimSpace(flagStringValue(fs, "planner-arm"))
		if selector == "" {
			return plannerRouting{}, errors.New("--planner-arm requires a runtime")
		}
		kind, err := runtimeconfig.ParseAgentKind(selector)
		if err != nil {
			return plannerRouting{}, err
		}
		for _, arm := range eligible {
			if arm.Arm.Runtime == kind {
				selected := arm.Arm
				override = &selected
				break
			}
		}
		if override == nil {
			return plannerRouting{}, fmt.Errorf("planner arm %q is not an installed eligible runtime", selector)
		}
	}
	assignment, err := plannerroute.Assign(policy, eligible, plannerroute.UnitKey{RepoID: registered.ID, Kind: "note", ID: item.ID}, override)
	if err != nil {
		return plannerRouting{}, err
	}
	return plannerRouting{
		Enabled: true, Assignment: assignment,
		Context: plannerroute.Context{
			FeatureSchema: plannerroute.FeatureSchema, RepoID: registered.ID, RepoName: registered.Name,
			UnitKind: "note", UnitID: item.ID, NoteTags: item.Tags,
			NoteTextBucket:  plannerroute.NoteTextBucket(utf8.RuneCountInString(item.Text)),
			BaselineRuntime: baseline, PromptVersion: version, PermissionMode: string(permission), BuildVersion: build.Version(),
		},
	}, nil
}

func (r plannerRouting) agentKind(baseline runtimeconfig.AgentKind) runtimeconfig.AgentKind {
	if r.Enabled && r.Assignment.Mode == plannerroute.ModeRandomized {
		return r.Assignment.Selected.Runtime
	}
	return baseline
}

// startPlannerRouting runs under the note promotion lock, before plan allocation.
func (a App) startPlannerRouting(ctx context.Context, registered taodata.Repo, routing plannerRouting) (plannerRouting, error) {
	if !routing.Enabled {
		return routing, nil
	}
	now := a.now().UTC()
	id, err := plannerroute.NewRouteID(now)
	if err == nil {
		routing.id = id
		routing.store = plannerroute.NewStore(a.registry().PlannerRoutesDir(registered))
		err = routing.store.Create(ctx, plannerroute.Record{
			Schema: plannerroute.Schema, ID: id, RepoID: registered.ID, CreatedAt: now,
			Context: routing.Context, Assignment: routing.Assignment,
			Entries: []plannerroute.Entry{{Kind: plannerroute.EntryAssigned, At: now}},
		})
	}
	if err != nil {
		return plannerRouting{}, a.plannerRoutingLedgerError(routing, nil, err)
	}
	label := "assigned"
	if routing.Assignment.Mode == plannerroute.ModeShadow {
		label = "shadow recommendation"
	} else if routing.Assignment.ManualOverride {
		label = "manual override"
	}
	if err := writef(a.Out, "Planner route %s: mode %s, arm %s (%s)\n", id, routing.Assignment.Mode, routing.Assignment.Selected.Runtime, label); err != nil {
		return plannerRouting{}, err
	}
	return routing, nil
}

// Recording survives cancellation of planning; each ledger lock acquisition
// has a fixed timeout. Its facts never authorize plan execution or note promotion.
func (a App) finishPlannerRouting(ctx context.Context, routing plannerRouting, result *planning.GeneratePlanResult, generationErr error, interrupted bool) error {
	if !routing.Enabled {
		return nil
	}
	var observed *planning.Treatment
	attempt := plannerroute.Attempt{Outcome: "failed"}
	if result != nil && generationErr == nil {
		observed = &result.Treatment
		attempt.Outcome = "plan_created"
	}
	var failed *planning.GenerationError
	if errors.As(generationErr, &failed) {
		observed = failed.Treatment
		attempt.Stage = string(failed.Stage)
	}
	if interrupted {
		attempt.Outcome = "interrupted"
	}
	treatment := plannerroute.Treatment{RuntimeLabel: "unknown", ProviderID: "unknown", ModelID: "unknown", MetricsAvailability: "unknown"}
	if observed != nil {
		treatment = plannerroute.Treatment{
			RuntimeLabel: observed.RuntimeLabel, ProviderID: observed.ProviderID, ModelID: observed.ModelID,
			MetricsAvailability: observed.MetricsAvailability,
		}
	}
	_, treatedErr := routing.store.Append(ctx, routing.id, plannerroute.Entry{Kind: plannerroute.EntryTreated, At: a.now().UTC(), Treatment: &treatment})
	_, attemptErr := routing.store.Append(ctx, routing.id, plannerroute.Entry{Kind: plannerroute.EntryAttempt, At: a.now().UTC(), Attempt: &attempt})
	var linkErr error
	if result != nil && generationErr == nil {
		_, linkErr = routing.store.Link(ctx, routing.id, result.Allocation.ID, result.Allocation.Dir)
	}
	return a.plannerRoutingLedgerError(routing, result, errors.Join(treatedErr, attemptErr, linkErr))
}

func (a App) plannerRoutingLedgerError(routing plannerRouting, result *planning.GeneratePlanResult, err error) error {
	if err == nil {
		return nil
	}
	if routing.Assignment.Mode == plannerroute.ModeShadow {
		_ = writef(a.noteErrorOutput(), "warning: planner routing ledger unavailable: %v\n", err)
		return nil
	}
	if result != nil {
		return fmt.Errorf("plan %s was created at %s, but planner route %s could not be recorded: %w; recover with tao route link %s %s", result.Allocation.ID, result.Allocation.Dir, routing.id, err, routing.id, result.Allocation.ID)
	}
	return fmt.Errorf("planner routing ledger unavailable: %w", err)
}
