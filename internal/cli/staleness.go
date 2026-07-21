package cli

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

var stalenessCommand = commandMetadata{
	name:                  "staleness",
	minPrefix:             "stale",
	usageLines:            []string{"staleness (stale) <plan-id-or-slug-or-path>"},
	completionDescription: "Check whether a plan is stale against its recorded base commit",
	long:                  "Check whether a plan is stale compared with the repository base commit captured during planning. Tao reports changed history and pending-slice file overlaps as warnings.",
	examples: "  tao staleness my-plan\n" +
		"  tao stale 20260628-1618-kubectl-style-help",
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.staleness(c.ctx, c.repo, c.args)
	},
}

type planStalenessFinding struct {
	Severity string
	Message  string
}

func (a App) staleness(ctx context.Context, repo plan.Resolver, args []string) error {
	if err := requirePositionals(args, 1, "usage: tao staleness <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	detail, err := repo.ResolvePlan(ctx, args[0])
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("plan %q not found", args[0])
	}
	findings := a.planStalenessFindings(ctx, detail)
	return renderPlanStaleness(a.Out, detail, findings)
}

func (a App) planStalenessFindings(ctx context.Context, detail *plan.PlanDetail) []planStalenessFinding {
	findings := make([]planStalenessFinding, 0)
	root := strings.TrimSpace(detail.State.Repo.Root)
	base := strings.TrimSpace(detail.State.Repo.BaseCommit)
	if root == "" {
		return append(findings, planStalenessFinding{Severity: "warning", Message: "plan has no recorded repository root; cannot compare against current checkout"})
	}
	if base == "" {
		return append(findings, planStalenessFinding{Severity: "warning", Message: "plan has no recorded repo.base_commit; staleness cannot tell how much the repo changed since planning"})
	}
	git := gitops.NewClient(root, a.stalenessRunner())
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return append(findings, planStalenessFinding{Severity: "warning", Message: fmt.Sprintf("could not read current HEAD in %s: %v", root, err)})
	}
	if head == base {
		return findings
	}
	findings = append(findings, planStalenessFinding{Severity: "info", Message: fmt.Sprintf("repository HEAD changed since planning: %s -> %s", shortSHA(base), shortSHA(head))})
	if ancestor, _ := git.IsAncestor(ctx, base, "HEAD"); !ancestor {
		findings = append(findings, planStalenessFinding{Severity: "warning", Message: "recorded base commit is not an ancestor of current HEAD; the plan may have been created on a different history"})
	}
	files, err := git.ChangedFiles(ctx, base+".."+"HEAD")
	if err != nil {
		return append(findings, planStalenessFinding{Severity: "warning", Message: fmt.Sprintf("could not list changed files since recorded base: %v", err)})
	}
	changed := map[string]bool{}
	for _, file := range files {
		path := filepath.ToSlash(strings.TrimSpace(file))
		if path != "" {
			changed[path] = true
		}
	}
	if len(changed) == 0 {
		return findings
	}
	findings = append(findings, planStalenessFinding{Severity: "info", Message: fmt.Sprintf("%d file(s) changed since the plan was created", len(changed))})
	for _, message := range pendingSliceOverlapMessages(detail, changed) {
		findings = append(findings, planStalenessFinding{Severity: "warning", Message: message})
	}
	return findings
}

func pendingSliceOverlapMessages(detail *plan.PlanDetail, changed map[string]bool) []string {
	pending := map[string]bool{}
	for _, id := range detail.State.Plan.PendingSlices {
		pending[id] = true
	}
	messages := make([]string, 0)
	for _, slice := range detail.Slices.Slices {
		if !pending[slice.ID] || slice.Status == plan.StatusCompleted || slice.Status == plan.StatusSkipped {
			continue
		}
		overlaps := make([]string, 0)
		for _, file := range slice.ExpectedFiles {
			file = filepath.ToSlash(strings.TrimSpace(file))
			if file == "" {
				continue
			}
			if changed[file] || changedPathUnder(changed, file) {
				overlaps = append(overlaps, file)
			}
		}
		if len(overlaps) == 0 {
			continue
		}
		sort.Strings(overlaps)
		messages = append(messages, fmt.Sprintf("pending slice %s expects file(s) changed since planning: %s", slice.ID, strings.Join(overlaps, ", ")))
	}
	return messages
}

func changedPathUnder(changed map[string]bool, expected string) bool {
	prefix := strings.TrimSuffix(expected, "/") + "/"
	for path := range changed {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func renderPlanStaleness(out io.Writer, detail *plan.PlanDetail, findings []planStalenessFinding) error {
	if err := writef(out, "Staleness: %s\n", detail.State.Plan.ID); err != nil {
		return err
	}
	if detail.State.Repo.BaseCommit != "" {
		if err := writef(out, "Base Commit: %s\n", detail.State.Repo.BaseCommit); err != nil {
			return err
		}
	}
	if len(findings) == 0 {
		return writeln(out, "No staleness findings.")
	}
	if err := writeln(out, "Findings:"); err != nil {
		return err
	}
	for _, finding := range findings {
		if err := writef(out, "- %s: %s\n", finding.Severity, finding.Message); err != nil {
			return err
		}
	}
	return nil
}

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func (a App) stalenessRunner() commandrunner.Runner {
	if a.CommandRunner != nil {
		return a.CommandRunner
	}
	return commandrunner.DefaultLocal
}
