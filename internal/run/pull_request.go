package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

var githubPullRequestURLRE = regexp.MustCompile(`https://github\.com/[^\s)]+/pull/(\d+)`)

const pullRequestMergeGuidance = "## Merge\n\n" +
	"When this pull request is ready to integrate, use the host's **Squash and merge** action. Tao does not merge the PR. " +
	"After the merged change is present on your local default branch, optionally run `tao cleanup --dry-run`, then `tao cleanup`, to remove eligible local plan branches and worktrees."

type deterministicPullRequestCreator struct {
	execution     runExecution
	bodyGenerator PullRequestBodyGenerator
}

func defaultPullRequestCreatorWithBody(execution runExecution, bodyGenerator PullRequestBodyGenerator) PullRequestCreator {
	return deterministicPullRequestCreator{execution: execution, bodyGenerator: bodyGenerator}
}

func (c deterministicPullRequestCreator) CreatePullRequest(ctx context.Context, run PullRequestRun) (plan.PullRequest, error) {
	run = c.normalizeRun(ctx, run)
	if strings.TrimSpace(run.RepoRoot) == "" {
		return plan.PullRequest{}, fmt.Errorf("create pull request: repo root is empty")
	}
	if strings.TrimSpace(run.Branch) == "" {
		return plan.PullRequest{}, fmt.Errorf("create pull request: branch is empty")
	}

	git := gitClient(c.execution, run.RepoRoot)
	baseBranch, err := git.DefaultBranch(ctx)
	if err != nil {
		return plan.PullRequest{}, err
	}
	if err := c.pushBranch(ctx, run); err != nil {
		return plan.PullRequest{}, err
	}
	if pr, ok, _ := c.viewPullRequest(ctx, run, run.Branch); ok {
		return pr, nil
	}

	title := pullRequestTitle(run)
	diffStat, _ := git.DiffStat(ctx, baseBranch+"...HEAD")
	body := c.pullRequestBody(ctx, run, baseBranch, title, diffStat)
	bodyFile, cleanup, err := writePullRequestBodyFile(body)
	if err != nil {
		return plan.PullRequest{}, err
	}
	defer cleanup()

	output, err := c.ghOutput(ctx, run.RepoRoot, "pr", "create", "--base", baseBranch, "--head", run.Branch, "--title", title, "--body-file", bodyFile)
	if err != nil {
		if pr, ok, _ := c.viewPullRequest(ctx, run, run.Branch); ok {
			return pr, nil
		}
		return plan.PullRequest{}, err
	}
	pr, extractErr := extractPullRequest(output, now(c.execution).UTC())
	if extractErr != nil {
		if pr, ok, _ := c.viewPullRequest(ctx, run, run.Branch); ok {
			return pr, nil
		}
		return plan.PullRequest{}, extractErr
	}
	pr.Branch = run.Branch
	pr.HeadSHA = run.HeadSHA
	return pr, nil
}

func (c deterministicPullRequestCreator) normalizeRun(ctx context.Context, run PullRequestRun) PullRequestRun {
	git := gitClient(c.execution, run.RepoRoot)
	if strings.TrimSpace(run.Branch) == "" {
		if branch, err := git.CurrentBranch(ctx); err == nil {
			run.Branch = branch
		}
	}
	if strings.TrimSpace(run.HeadSHA) == "" {
		if head, err := git.RevParse(ctx, "HEAD"); err == nil {
			run.HeadSHA = head
		}
	}
	return run
}

func (c deterministicPullRequestCreator) pushBranch(ctx context.Context, run PullRequestRun) error {
	return c.gitRun(ctx, run.RepoRoot, "push", "--set-upstream", "origin", run.Branch)
}

func (c deterministicPullRequestCreator) viewPullRequest(ctx context.Context, run PullRequestRun, branch string) (plan.PullRequest, bool, error) {
	output, err := c.ghOutput(ctx, run.RepoRoot, "pr", "view", "--head", branch, "--json", "number,url,createdAt")
	if err != nil {
		return plan.PullRequest{}, false, err
	}
	pr, err := parseGHPullRequest(output, now(c.execution).UTC())
	if err != nil {
		return plan.PullRequest{}, false, err
	}
	pr.Branch = run.Branch
	pr.HeadSHA = run.HeadSHA
	return pr, true, nil
}

func (c deterministicPullRequestCreator) pullRequestBody(ctx context.Context, run PullRequestRun, baseBranch string, title string, diffStat string) string {
	draft := deterministicPullRequestBody(run, baseBranch, title, diffStat)
	if c.bodyGenerator == nil {
		return draft
	}
	body, err := c.bodyGenerator.GeneratePullRequestBody(ctx, PullRequestBodyRun{PlanDir: run.PlanDir, PlanID: run.PlanID, Detail: run.Detail, RepoRoot: run.RepoRoot, Branch: run.Branch, HeadSHA: run.HeadSHA, BaseBranch: baseBranch, Title: title, DraftBody: draft})
	if err != nil {
		c.warnBodyGeneration(err)
		return draft
	}
	body = strings.TrimSpace(body)
	if body == "" {
		c.warnBodyGeneration(fmt.Errorf("agent returned empty pull request body"))
		return draft
	}
	if strings.Contains(body, "tao merge") && strings.Contains(body, "--record-only") {
		c.warnBodyGeneration(fmt.Errorf("agent returned obsolete pull request merge guidance"))
		return draft
	}
	return appendPullRequestMergeGuidance(body)
}

func appendPullRequestMergeGuidance(body string) string {
	body = strings.TrimSpace(body)
	if strings.Contains(body, "**Squash and merge**") && strings.Contains(body, "`tao cleanup --dry-run`") && strings.Contains(body, "local default branch") {
		return body
	}
	return body + "\n\n" + pullRequestMergeGuidance
}

func (c deterministicPullRequestCreator) warnBodyGeneration(err error) {
	if err == nil {
		return
	}
	writer := c.execution.Dependencies.SessionLogWriter
	if writer == nil {
		writer = c.execution.Dependencies.OutputWriter
	}
	if writer == nil {
		return
	}
	_, _ = fmt.Fprintf(writer, "Warning: pull request body agent failed; using deterministic body: %v\n", err)
}

func (c deterministicPullRequestCreator) gitRun(ctx context.Context, repoRoot string, args ...string) error {
	runner := c.execution.Dependencies.CommandRunner
	if runner == nil {
		runner = defaultCommandRunner
	}
	gitArgs := append([]string{"-C", repoRoot}, args...)
	var stderr bytes.Buffer
	if err := runner(ctx, "", "git", gitArgs, io.Discard, &stderr); err != nil {
		return formatCommandError("git", args, err, stderr.String())
	}
	return nil
}

func (c deterministicPullRequestCreator) ghOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	runner := c.execution.Dependencies.CommandRunner
	if runner == nil {
		runner = defaultCommandRunner
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runner(ctx, repoRoot, "gh", args, &stdout, &stderr); err != nil {
		return "", formatCommandError("gh", args, err, stderr.String())
	}
	return stdout.String(), nil
}

func formatCommandError(name string, args []string, err error, stderr string) error {
	if text := strings.TrimSpace(stderr); text != "" {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, text)
	}
	return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
}

func writePullRequestBodyFile(body string) (string, func(), error) {
	file, err := os.CreateTemp("", "tao-pr-body-*.md")
	if err != nil {
		return "", nil, fmt.Errorf("create pull request body file: %w", err)
	}
	cleanup := func() { _ = os.Remove(file.Name()) }
	if _, err := file.WriteString(body); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, fmt.Errorf("write pull request body file: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("close pull request body file: %w", err)
	}
	return file.Name(), cleanup, nil
}

func parseGHPullRequest(output string, fallbackCreatedAt time.Time) (plan.PullRequest, error) {
	var payload struct {
		Number    int    `json:"number"`
		URL       string `json:"url"`
		CreatedAt string `json:"createdAt"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		return plan.PullRequest{}, fmt.Errorf("parse gh pull request json: %w", err)
	}
	if payload.Number <= 0 || strings.TrimSpace(payload.URL) == "" {
		return plan.PullRequest{}, fmt.Errorf("gh pull request json missing number or url")
	}
	createdAt := fallbackCreatedAt
	if payload.CreatedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, payload.CreatedAt); err == nil {
			createdAt = parsed
		}
	}
	return plan.PullRequest{Number: payload.Number, URL: payload.URL, CreatedAt: createdAt}, nil
}

func pullRequestTitle(run PullRequestRun) string {
	if run.Detail != nil && strings.TrimSpace(run.Detail.State.Plan.Title) != "" {
		return strings.TrimSpace(run.Detail.State.Plan.Title)
	}
	if strings.TrimSpace(run.PlanID) != "" {
		return "Tao plan " + strings.TrimSpace(run.PlanID)
	}
	return "Tao plan"
}

func deterministicPullRequestBody(run PullRequestRun, baseBranch string, title string, diffStat string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Summary\n\n- Completed Tao plan `%s`: %s\n", emptyPRField(run.PlanID), title)
	if run.Branch != "" || baseBranch != "" {
		fmt.Fprintf(&b, "- Branch `%s` into `%s`\n", emptyPRField(run.Branch), emptyPRField(baseBranch))
	}
	if run.HeadSHA != "" {
		fmt.Fprintf(&b, "- Head `%s`\n", run.HeadSHA)
	}
	if run.Detail != nil {
		writePullRequestSliceSummary(&b, run.Detail)
		writePullRequestVerification(&b, run.Detail)
		writePullRequestReview(&b, run.Detail)
	}
	if strings.TrimSpace(diffStat) != "" {
		fmt.Fprintf(&b, "\n## Scope\n\n```text\n%s\n```\n", strings.TrimSpace(diffStat))
	}
	fmt.Fprintf(&b, "\n%s\n", pullRequestMergeGuidance)
	return strings.TrimSpace(b.String()) + "\n"
}

func writePullRequestSliceSummary(b *strings.Builder, detail *plan.PlanDetail) {
	if len(detail.Slices.Slices) == 0 {
		return
	}
	b.WriteString("\n## Completed slices\n\n")
	for _, slice := range detail.Slices.Slices {
		if slice.Status != plan.StatusCompleted {
			continue
		}
		title := strings.TrimSpace(slice.Title)
		if title == "" {
			title = strings.TrimSpace(slice.Goal)
		}
		fmt.Fprintf(b, "- `%s` — %s\n", slice.ID, emptyPRField(title))
	}
}

func writePullRequestVerification(b *strings.Builder, detail *plan.PlanDetail) {
	var rows []string
	for _, slice := range detail.Slices.Slices {
		for _, result := range slice.VerificationResults {
			command := strings.TrimSpace(result.Command)
			if command == "" {
				command = "verification command"
			}
			rows = append(rows, fmt.Sprintf("- `%s`: %s", command, emptyPRField(result.Result)))
		}
	}
	if len(rows) == 0 {
		return
	}
	b.WriteString("\n## Verification\n\n")
	for _, row := range rows {
		b.WriteString(row)
		b.WriteByte('\n')
	}
}

func writePullRequestReview(b *strings.Builder, detail *plan.PlanDetail) {
	review := plan.PersistedReview(detail)
	if review == nil {
		return
	}
	b.WriteString("\n## Review\n\n")
	fmt.Fprintf(b, "- Verdict: `%s`\n", emptyPRField(review.Verdict))
	fmt.Fprintf(b, "- Findings: %d\n", review.FindingsCount)
	if strings.TrimSpace(review.Summary) != "" {
		fmt.Fprintf(b, "- Summary: %s\n", strings.TrimSpace(review.Summary))
	}
}

func emptyPRField(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return strings.TrimSpace(value)
}

func extractPullRequest(output string, createdAt time.Time) (plan.PullRequest, error) {
	matches := githubPullRequestURLRE.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return plan.PullRequest{}, fmt.Errorf("could not find GitHub pull request URL in agent output")
	}
	firstURL := matches[0][0]
	firstNumber := matches[0][1]
	distinctURLs := map[string]bool{firstURL: true}
	for _, match := range matches[1:] {
		if distinctURLs[match[0]] {
			continue
		}
		distinctURLs[match[0]] = true
		if len(distinctURLs) > 1 {
			return plan.PullRequest{}, fmt.Errorf("agent output contains multiple distinct GitHub pull request URLs")
		}
	}
	number, err := strconv.Atoi(firstNumber)
	if err != nil {
		return plan.PullRequest{}, fmt.Errorf("parse pull request number: %w", err)
	}
	return plan.PullRequest{Number: number, URL: firstURL, CreatedAt: createdAt}, nil
}
