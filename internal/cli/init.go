package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/taodata"
)

var initCommand = commandMetadata{
	name:                  "init",
	usageLines:            []string{"init [--slug SLUG --json]"},
	completionDescription: "Register this repository in Tao data home",
	long:                  "Register the current checkout in Tao's data home. Optionally allocate a new plan directory and print the registration payload as JSON for planning agents.",
	examples: "  tao init\n" +
		"  tao init --slug kubectl-style-help\n" +
		"  tao init --slug kubectl-style-help --json",
	registerFlags: registerInitFlags,
	execute: func(c commandContext) error {
		return c.app.initRepo(c.ctx, c.args)
	},
}

type initResponse struct {
	Schema   string                  `json:"schema"`
	DataHome string                  `json:"data_home"`
	Repo     taodata.Repo            `json:"repo"`
	Plan     *taodata.PlanAllocation `json:"plan,omitempty"`
}

func registerInitFlags(fs *flag.FlagSet) {
	fs.String("slug", "", "allocate a plan directory with this slug")
	fs.Bool("json", false, "write JSON")
}

func (a App) initRepo(ctx context.Context, args []string) error {
	fs, positional, err := a.parseArgs("init", args, registerInitFlags)
	if err != nil {
		return err
	}
	if len(positional) > 0 {
		return fmt.Errorf("init accepts no positional arguments")
	}

	registry := taodata.NewRegistry("")
	repo, err := registry.RegisterCurrent(ctx)
	if err != nil {
		return err
	}
	response := initResponse{Schema: "tao.init.v1", DataHome: registry.DataHome, Repo: repo}
	slug := flagStringValue(fs, "slug")
	if slug != "" {
		plan, err := registry.AllocatePlan(repo, slug)
		if err != nil {
			return err
		}
		git := gitops.NewClient(repo.Root, a.cleanupRunner())
		if head, err := git.RevParse(ctx, "HEAD"); err == nil {
			plan.BaseCommit = head
		}
		response.Plan = &plan
	}
	if flagBoolValue(fs, "json") {
		content, err := json.MarshalIndent(response, "", "  ")
		if err != nil {
			return err
		}
		return writeln(a.Out, string(content))
	}
	if err := writeln(a.Out, "registered Tao repository"); err != nil {
		return err
	}
	if err := writef(a.Out, "data home: %s\n", response.DataHome); err != nil {
		return err
	}
	if err := writef(a.Out, "repo: %s (%s)\n", repo.Name, repo.ID); err != nil {
		return err
	}
	if response.Plan != nil {
		return writef(a.Out, "plan dir: %s\n", response.Plan.Dir)
	}
	return nil
}
