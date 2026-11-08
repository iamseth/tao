package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"strings"

	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/taodata"
)

var initCommand = commandMetadata{
	name:                  "init",
	usageLines:            []string{"init [--slug SLUG [--note ID]] [--json]"},
	completionDescription: "Register this repository in Tao data home",
	long:                  "Register the current checkout in Tao's data home. Optionally allocate a new plan directory and print the registration payload as JSON for planning agents. With --slug, --note checks that a repository-local source note is open or legacy planning-promoted before allocation; it does not reserve or link the note.",
	examples: "  tao init\n" +
		"  tao init --slug kubectl-style-help\n" +
		"  tao init --slug kubectl-style-help --json",
	registerFlags: registerInitFlags,
	execute: func(c commandContext) error {
		return c.app.initRepo(c.ctx, c.args)
	},
}

type initSourceNote struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
}

type initResponse struct {
	SourceNote *initSourceNote         `json:"source_note,omitempty"`
	Schema     string                  `json:"schema"`
	DataHome   string                  `json:"data_home"`
	Repo       taodata.Repo            `json:"repo"`
	Plan       *taodata.PlanAllocation `json:"plan,omitempty"`
}

func registerInitFlags(fs *flag.FlagSet) {
	fs.String("slug", "", "allocate a plan directory with this slug")
	fs.String("note", "", "check an eligible source note before allocation (requires --slug)")
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

	slug := flagStringValue(fs, "slug")
	noteProvided := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "note" {
			noteProvided = true
		}
	})
	if noteProvided && strings.TrimSpace(flagStringValue(fs, "note")) == "" {
		return fmt.Errorf("--note requires a non-empty note selector")
	}
	if noteProvided && strings.TrimSpace(slug) == "" {
		return fmt.Errorf("--note requires a non-empty --slug")
	}
	registry := taodata.NewRegistry("")
	repo, err := registry.RegisterCurrent(ctx)
	if err != nil {
		return err
	}
	response := initResponse{Schema: "tao.init.v1", DataHome: registry.DataHome, Repo: repo}
	if noteProvided {
		item, err := a.noteRepository(repo).Get(ctx, flagStringValue(fs, "note"))
		if err != nil {
			return err
		}
		if item.Repo.ID != repo.ID {
			return fmt.Errorf("note repository does not match current repository")
		}
		legacyPlanning := item.Status == note.StatusPromoted && item.Promotion != nil &&
			item.Promotion.PlanningSession != nil && item.Promotion.Plan == nil
		if item.Status != note.StatusOpen && !legacyPlanning {
			destinations := ""
			if item.Archive != nil {
				if item.Archive.Plan != nil {
					destinations += fmt.Sprintf("; archive plan %s", item.Archive.Plan.ID)
				}
				if item.Archive.PlanningSession != nil {
					destinations += fmt.Sprintf("; planning session %s", item.Archive.PlanningSession.ID)
				}
			}
			if item.Promotion != nil {
				if item.Promotion.Plan != nil {
					destinations += fmt.Sprintf("; promotion plan %s", item.Promotion.Plan.ID)
				}
				if item.Promotion.PlanningSession != nil {
					destinations += fmt.Sprintf("; planning session %s", item.Promotion.PlanningSession.ID)
				}
			}
			return fmt.Errorf("source note %s has status %s%s; only open or legacy planning-promoted notes may allocate a plan", item.ID, item.Status, destinations)
		}
		response.SourceNote = &initSourceNote{ID: item.ID, Repository: repo.ID}
	}
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
