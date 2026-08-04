// Package staleness detects drift between a plan's recorded planning-time
// base commit and the repository's current history, including pending slices
// whose expected files changed since planning. Findings are advisory: they
// never mutate plan state or block execution.
package staleness

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
	"github.com/iamseth/tao/internal/plan"
)

// Finding is one advisory staleness observation.
type Finding struct {
	Severity string
	Message  string
}

// Findings compares the plan's recorded repository root and base commit with
// the checkout's current HEAD. Missing metadata and Git failures degrade to
// warnings rather than errors so the check stays usable on legacy plans.
func Findings(ctx context.Context, detail *plan.PlanDetail, runner commandrunner.Runner) []Finding {
	findings := make([]Finding, 0)
	root := strings.TrimSpace(detail.State.Repo.Root)
	base := strings.TrimSpace(detail.State.Repo.BaseCommit)
	if root == "" {
		return append(findings, Finding{Severity: "warning", Message: "plan has no recorded repository root; cannot compare against current checkout"})
	}
	if base == "" {
		return append(findings, Finding{Severity: "warning", Message: "plan has no recorded repo.base_commit; staleness cannot tell how much the repo changed since planning"})
	}
	git := gitops.NewClient(root, runner)
	head, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return append(findings, Finding{Severity: "warning", Message: fmt.Sprintf("could not read current HEAD in %s: %v", root, err)})
	}
	if head == base {
		return findings
	}
	findings = append(findings, Finding{Severity: "info", Message: fmt.Sprintf("repository HEAD changed since planning: %s -> %s", shortSHA(base), shortSHA(head))})
	if ancestor, _ := git.IsAncestor(ctx, base, "HEAD"); !ancestor {
		findings = append(findings, Finding{Severity: "warning", Message: "recorded base commit is not an ancestor of current HEAD; the plan may have been created on a different history"})
	}
	files, err := git.ChangedFiles(ctx, base+".."+"HEAD")
	if err != nil {
		return append(findings, Finding{Severity: "warning", Message: fmt.Sprintf("could not list changed files since recorded base: %v", err)})
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
	findings = append(findings, Finding{Severity: "info", Message: fmt.Sprintf("%d file(s) changed since the plan was created", len(changed))})
	for _, message := range pendingSliceOverlapMessages(detail, changed) {
		findings = append(findings, Finding{Severity: "warning", Message: message})
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

func shortSHA(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 12 {
		return value[:12]
	}
	return value
}
