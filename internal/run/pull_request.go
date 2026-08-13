package run

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/plan"
)

var (
	githubPullRequestURLRE      = regexp.MustCompile(`https://github\.com/[^\s)]+/pull/(\d+)`)
	pullRequestTaoWordRE        = regexp.MustCompile(`(?i)\btao\b`)
	pullRequestSliceWordRE      = regexp.MustCompile(`(?i)\bslices?\b`)
	pullRequestLifecycleRE      = regexp.MustCompile(`(?i)\blifecycle\b`)
	pullRequestSquashAndMergeRE = regexp.MustCompile(`(?i)squash and merge`)
	pullRequestMergeGuidanceRE  = regexp.MustCompile(`(?i)merge guidance`)
	pullRequestCleanupDryRunRE  = regexp.MustCompile(`(?i)cleanup --dry-run`)
)

const (
	pullRequestLabelColor        = "1D76DB"
	pullRequestLabelDescription  = "Repository change category"
	pullRequestNarrativeFallback = "See the commit description for change context."
)

type deterministicPullRequestCreator struct {
	execution     runExecution
	bodyGenerator PullRequestBodyGenerator
}

type pullRequestMetadata struct {
	Labels    []string
	Assignees []string
}

type githubRepositoryIdentity struct {
	Owner string
	Name  string
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
	title, label, err := pullRequestPreflight(run)
	if err != nil {
		return plan.PullRequest{}, err
	}
	intent := run.Detail.State.Plan.PullRequestIntent
	if intent != nil && (intent.Branch != run.Branch || intent.HeadSHA != run.HeadSHA) {
		return plan.PullRequest{}, fmt.Errorf("recover pull request intent for #%d: recorded branch and head do not match requested branch and head", intent.Number)
	}
	if err := c.pushBranch(ctx, run); err != nil {
		return plan.PullRequest{}, err
	}
	if intent != nil && pullRequestIntentHasIdentity(*intent) {
		pr, metadata, found, viewErr := c.viewPullRequestByNumber(ctx, run, intent.Number)
		if viewErr != nil {
			return plan.PullRequest{}, fmt.Errorf("recover pull request intent for #%d: %w", intent.Number, viewErr)
		}
		if !found || !pullRequestMatchesIntent(pr, *intent) {
			return plan.PullRequest{}, fmt.Errorf("recover pull request intent for #%d: discovered pull request does not match recorded number and URL", intent.Number)
		}
		// Preserve the creation timestamp recorded with the emitted identity so
		// final recording matches the durable recovery intent exactly.
		pr.CreatedAt = intent.CreatedAt
		if err := c.ensurePullRequestMetadata(ctx, run.RepoRoot, pr, metadata, label); err != nil {
			return plan.PullRequest{}, err
		}
		return pr, nil
	}
	pr, _, found, err := c.findPullRequest(ctx, run, baseBranch, run.Branch)
	if err != nil {
		return plan.PullRequest{}, err
	}
	if found {
		// Discovery alone never proves that Tao created the pull request. In
		// particular, a legacy branch/head-only intent must not authorize Tao to
		// mutate a matching pull request that a human may have created.
		return pr, nil
	}

	diffStat, err := git.DiffStat(ctx, baseBranch+"...HEAD")
	if err != nil {
		return plan.PullRequest{}, fmt.Errorf("build pull request scope: %w", err)
	}
	body, err := c.pullRequestBody(ctx, run, baseBranch, title, diffStat)
	if err != nil {
		return plan.PullRequest{}, err
	}
	bodyFile, cleanup, err := writePullRequestBodyFile(body)
	if err != nil {
		return plan.PullRequest{}, err
	}
	defer cleanup()

	if label != "" {
		label, err = c.ensureLabel(ctx, run.RepoRoot, label)
		if err != nil {
			return plan.PullRequest{}, err
		}
	}
	args := []string{"pr", "create", "--base", baseBranch, "--head", run.Branch, "--title", title, "--body-file", bodyFile}
	if label != "" {
		args = append(args, "--label", label)
	}
	args = append(args, "--assignee", "@me")
	output, err := c.ghOutput(ctx, run.RepoRoot, args...)
	if err != nil {
		createErr := fmt.Errorf("create pull request with assignee @me: %w", err)
		createdPR, extractErr := extractPullRequest(output, now(c.execution).UTC())
		if extractErr != nil {
			// A matching pull request discovered after a failed create may have
			// been opened concurrently by a human. Without an identity emitted by
			// this create attempt, Tao has no ownership evidence and must not
			// repair metadata or persist branch/head-only recovery intent.
			return plan.PullRequest{}, createErr
		}
		createdPR.Branch = run.Branch
		createdPR.HeadSHA = run.HeadSHA
		if persistErr := c.persistPullRequestIntent(run, createdPR); persistErr != nil {
			return plan.PullRequest{}, fmt.Errorf("%w; persist partial pull request recovery intent: %w", createErr, persistErr)
		}

		pr, metadata, found, viewErr := c.viewPullRequestByNumber(ctx, run, createdPR.Number)
		if viewErr != nil {
			return plan.PullRequest{}, fmt.Errorf("%w; verify emitted pull request identity #%d: %w", createErr, createdPR.Number, viewErr)
		}
		if !found || !pullRequestMatchesIntent(pr, createdPR) {
			return plan.PullRequest{}, fmt.Errorf("%w; emitted pull request identity #%d did not match GitHub", createErr, createdPR.Number)
		}
		pr.CreatedAt = createdPR.CreatedAt
		if metadataErr := c.ensurePullRequestMetadata(ctx, run.RepoRoot, pr, metadata, label); metadataErr != nil {
			return plan.PullRequest{}, metadataErr
		}
		return pr, nil
	}
	createdPR, extractErr := extractPullRequest(output, now(c.execution).UTC())
	if extractErr != nil {
		return plan.PullRequest{}, extractErr
	}
	createdPR.Branch = run.Branch
	createdPR.HeadSHA = run.HeadSHA
	return createdPR, nil
}

func pullRequestIntentHasIdentity(intent plan.PullRequest) bool {
	return intent.Number > 0 && strings.TrimSpace(intent.URL) != ""
}

func pullRequestMatchesIntent(pr plan.PullRequest, intent plan.PullRequest) bool {
	return pr.Number == intent.Number && strings.TrimSpace(pr.URL) == strings.TrimSpace(intent.URL)
}

func (c deterministicPullRequestCreator) persistPullRequestIntent(run PullRequestRun, pr plan.PullRequest) error {
	record, err := planMutationRecord(c.execution, run.Detail)
	if err != nil {
		return err
	}
	return record.RecordPullRequestIntent(pr, run.Branch, run.HeadSHA)
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
	if run.Detail == nil || run.Detail.State.Plan.ChangeType == "" {
		return c.gitRun(ctx, run.RepoRoot, "push", "--set-upstream", "origin", run.Branch)
	}

	ref := "refs/heads/" + run.Branch
	remoteHead, found, err := c.remoteBranchHead(ctx, run.RepoRoot, ref)
	if err != nil {
		return err
	}
	head := strings.TrimSpace(run.HeadSHA)
	priorHead := recordedPushedHead(run)
	leaseHead := ""
	switch {
	case !found && priorHead != "":
		return fmt.Errorf("push typed branch %q: remote branch is missing, want recorded Tao head %s", run.Branch, priorHead)
	case found && remoteHead == head:
		// Preserve idempotent publication when a prior push reached the remote
		// before its local state was recorded.
		leaseHead = head
	case found && priorHead != "" && remoteHead == priorHead:
		// A recorded workspace branch and its last pushed head are durable
		// ownership evidence. Advance it only while the remote still equals the
		// recorded boundary, as happens when a published PR is reworked.
		leaseHead = priorHead
	case found && priorHead != "":
		return fmt.Errorf("push typed branch %q: remote branch is at %s, want recorded Tao head %s or new reviewed head %s", run.Branch, remoteHead, priorHead, head)
	case found:
		return fmt.Errorf("push typed branch %q: remote branch already exists at %s, want exact Tao head %s", run.Branch, remoteHead, head)
	}
	lease := "--force-with-lease=" + ref + ":" + leaseHead
	return c.gitRun(ctx, run.RepoRoot, "push", "--set-upstream", lease, "origin", run.Branch+":"+ref)
}

func recordedPushedHead(run PullRequestRun) string {
	if run.Detail == nil || run.Detail.State.Workspace == nil {
		return ""
	}
	workspace := run.Detail.State.Workspace
	if strings.TrimSpace(workspace.Branch) != strings.TrimSpace(run.Branch) {
		return ""
	}
	return strings.TrimSpace(workspace.PushedSHA)
}

func (c deterministicPullRequestCreator) remoteBranchHead(ctx context.Context, repoRoot, ref string) (string, bool, error) {
	output, err := c.gitOutput(ctx, repoRoot, "ls-remote", "--heads", "origin", ref)
	if err != nil {
		return "", false, fmt.Errorf("check typed branch remote ownership: %w", err)
	}
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[1] != ref {
			return "", false, fmt.Errorf("check typed branch remote ownership: unexpected git ls-remote output %q", line)
		}
		return fields[0], true, nil
	}
	return "", false, nil
}

func (c deterministicPullRequestCreator) findPullRequest(ctx context.Context, run PullRequestRun, baseBranch, branch string) (plan.PullRequest, pullRequestMetadata, bool, error) {
	origin, err := c.originRepositoryIdentity(ctx, run.RepoRoot)
	if err != nil {
		return plan.PullRequest{}, pullRequestMetadata{}, false, err
	}
	output, err := c.ghOutput(ctx, run.RepoRoot, "pr", "list", "--base", baseBranch, "--head", branch, "--state", "open", "--json", "number,url,createdAt,labels,assignees,headRefName,headRepository,headRepositoryOwner")
	if err != nil {
		return plan.PullRequest{}, pullRequestMetadata{}, false, err
	}
	var matches []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &matches); err != nil {
		return plan.PullRequest{}, pullRequestMetadata{}, false, fmt.Errorf("parse gh pull request list json: %w", err)
	}
	for _, match := range matches {
		var head struct {
			RefName    string `json:"headRefName"`
			Repository struct {
				NameWithOwner string `json:"nameWithOwner"`
			} `json:"headRepository"`
			Owner struct {
				Login string `json:"login"`
			} `json:"headRepositoryOwner"`
		}
		if err := json.Unmarshal(match, &head); err != nil {
			return plan.PullRequest{}, pullRequestMetadata{}, false, fmt.Errorf("parse gh pull request head identity: %w", err)
		}
		if head.RefName != branch || !strings.EqualFold(head.Owner.Login, origin.Owner) || !strings.EqualFold(head.Repository.NameWithOwner, origin.Owner+"/"+origin.Name) {
			continue
		}
		return c.parseViewedPullRequest(run, match)
	}
	return plan.PullRequest{}, pullRequestMetadata{}, false, nil
}

func (c deterministicPullRequestCreator) originRepositoryIdentity(ctx context.Context, repoRoot string) (githubRepositoryIdentity, error) {
	output, err := c.gitOutput(ctx, repoRoot, "remote", "get-url", "--push", "origin")
	if err != nil {
		return githubRepositoryIdentity{}, fmt.Errorf("identify origin repository for pull request discovery: %w", err)
	}
	identity, err := parseGitHubRepositoryIdentity(output)
	if err != nil {
		return githubRepositoryIdentity{}, fmt.Errorf("identify origin repository for pull request discovery: %w", err)
	}
	return identity, nil
}

func parseGitHubRepositoryIdentity(remoteURL string) (githubRepositoryIdentity, error) {
	remoteURL = strings.TrimSpace(remoteURL)
	var repositoryPath string
	if parsed, err := url.Parse(remoteURL); err == nil && parsed.Scheme != "" {
		repositoryPath = parsed.Path
	} else if colon := strings.Index(remoteURL, ":"); colon >= 0 {
		repositoryPath = remoteURL[colon+1:]
	}
	repositoryPath = strings.TrimSuffix(strings.Trim(repositoryPath, "/"), ".git")
	parts := strings.Split(repositoryPath, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return githubRepositoryIdentity{}, fmt.Errorf("unsupported origin URL %q", remoteURL)
	}
	return githubRepositoryIdentity{Owner: parts[0], Name: parts[1]}, nil
}

func (c deterministicPullRequestCreator) viewPullRequestByNumber(ctx context.Context, run PullRequestRun, number int) (plan.PullRequest, pullRequestMetadata, bool, error) {
	output, err := c.ghOutput(ctx, run.RepoRoot, "pr", "view", strconv.Itoa(number), "--json", "number,url,createdAt,labels,assignees")
	if err != nil {
		return plan.PullRequest{}, pullRequestMetadata{}, false, err
	}
	return c.parseViewedPullRequest(run, []byte(output))
}

func (c deterministicPullRequestCreator) parseViewedPullRequest(run PullRequestRun, output []byte) (plan.PullRequest, pullRequestMetadata, bool, error) {
	pr, err := parseGHPullRequest(string(output), now(c.execution).UTC())
	if err != nil {
		return plan.PullRequest{}, pullRequestMetadata{}, false, err
	}
	metadata, err := parsePullRequestMetadata(string(output))
	if err != nil {
		return plan.PullRequest{}, pullRequestMetadata{}, false, err
	}
	pr.Branch = run.Branch
	pr.HeadSHA = run.HeadSHA
	return pr, metadata, true, nil
}

func (c deterministicPullRequestCreator) ensurePullRequestMetadata(ctx context.Context, repoRoot string, pr plan.PullRequest, metadata pullRequestMetadata, label string) error {
	loginOutput, err := c.ghOutput(ctx, repoRoot, "api", "user", "--jq", ".login")
	if err != nil {
		return fmt.Errorf("identify authenticated GitHub user for pull request #%d: %w", pr.Number, err)
	}
	login := strings.TrimSpace(loginOutput)
	if login == "" {
		return fmt.Errorf("identify authenticated GitHub user for pull request #%d: GitHub returned an empty login", pr.Number)
	}

	args := []string{"pr", "edit", strconv.Itoa(pr.Number)}
	if label != "" && !containsFold(metadata.Labels, label) {
		label, err = c.ensureLabel(ctx, repoRoot, label)
		if err != nil {
			return err
		}
		args = append(args, "--add-label", label)
	}
	if !containsFold(metadata.Assignees, login) {
		args = append(args, "--add-assignee", "@me")
	}
	if len(args) == 3 {
		return nil
	}
	if _, err := c.ghOutput(ctx, repoRoot, args...); err != nil {
		return fmt.Errorf("repair pull request #%d required metadata: %w", pr.Number, err)
	}
	return nil
}

func parsePullRequestMetadata(output string) (pullRequestMetadata, error) {
	var payload struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		return pullRequestMetadata{}, fmt.Errorf("parse gh pull request metadata: %w", err)
	}
	metadata := pullRequestMetadata{
		Labels:    make([]string, 0, len(payload.Labels)),
		Assignees: make([]string, 0, len(payload.Assignees)),
	}
	for _, label := range payload.Labels {
		metadata.Labels = append(metadata.Labels, label.Name)
	}
	for _, assignee := range payload.Assignees {
		metadata.Assignees = append(metadata.Assignees, assignee.Login)
	}
	return metadata, nil
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func (c deterministicPullRequestCreator) ensureLabel(ctx context.Context, repoRoot, label string) (string, error) {
	output, err := c.ghOutput(ctx, repoRoot, "label", "list", "--search", label, "--json", "name", "--limit", "100")
	if err != nil {
		return "", fmt.Errorf("check pull request label %q: %w", label, err)
	}
	var labels []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &labels); err != nil {
		return "", fmt.Errorf("parse GitHub labels while checking %q: %w", label, err)
	}
	for _, existing := range labels {
		if strings.EqualFold(existing.Name, label) {
			return existing.Name, nil
		}
	}
	if _, err := c.ghOutput(ctx, repoRoot, "label", "create", label, "--color", pullRequestLabelColor, "--description", pullRequestLabelDescription); err != nil {
		return "", fmt.Errorf("create pull request label %q: %w", label, err)
	}
	return label, nil
}

func (c deterministicPullRequestCreator) pullRequestBody(ctx context.Context, run PullRequestRun, baseBranch string, title string, diffStat string) (string, error) {
	draft, err := deterministicPullRequestBody(run, baseBranch, title, diffStat)
	if err != nil {
		return "", err
	}
	if c.bodyGenerator == nil {
		return draft, nil
	}
	body, err := c.bodyGenerator.GeneratePullRequestBody(ctx, PullRequestBodyRun{PlanDir: run.PlanDir, PlanID: run.PlanID, Detail: run.Detail, RepoRoot: run.RepoRoot, Branch: run.Branch, HeadSHA: run.HeadSHA, BaseBranch: baseBranch, Title: title, DraftBody: draft})
	if err != nil {
		c.warnBodyGeneration(err)
		return draft, nil
	}
	body = strings.TrimSpace(body)
	if err := validatePullRequestBody(body, run.PlanID, diffStat, draft); err != nil {
		c.warnBodyGeneration(err)
		return draft, nil
	}
	return body + "\n", nil
}

func validatePullRequestBody(body, planID, diffStat, deterministicDraft string) error {
	if body == "" {
		return fmt.Errorf("agent returned empty pull request body")
	}
	body = strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(body, "\n")
	headings, headingLines := pullRequestLevelTwoHeadings(lines)
	requiredHeadings := []string{"## Problem", "## Fix", "## Tests", "## Deploy", "## Scope"}
	if len(headings) != len(requiredHeadings) {
		return fmt.Errorf("agent pull request body must contain exactly the five required level-two headings in order")
	}
	for i, required := range requiredHeadings {
		if headings[i] != required {
			return fmt.Errorf("agent pull request body level-two heading %d must be %s", i+1, required)
		}
	}
	scope := strings.TrimSpace(strings.Join(lines[headingLines[len(headingLines)-1]+1:], "\n"))
	expectedScope := strings.ReplaceAll(deterministicPullRequestScope(diffStat), "\r\n", "\n")
	if scope != strings.TrimSpace(expectedScope) {
		return fmt.Errorf("agent pull request body must preserve the complete collapsed Changed files block and exact diff stat within Scope")
	}
	tests := strings.TrimSpace(strings.Join(lines[headingLines[2]+1:headingLines[3]], "\n"))
	reviewerNarrative := strings.ToLower(strings.Join([]string{
		strings.Join(lines[headingLines[0]+1:headingLines[1]], "\n"),
		strings.Join(lines[headingLines[1]+1:headingLines[2]], "\n"),
		strings.Join(lines[headingLines[3]+1:headingLines[4]], "\n"),
	}, "\n"))
	if noise := forbiddenPullRequestNarrativeLanguage(reviewerNarrative); noise != "" {
		return fmt.Errorf("agent pull request body contains forbidden Tao-specific language %q in reviewer narrative", noise)
	}
	if id := strings.ToLower(strings.TrimSpace(planID)); id != "" && (strings.Contains(reviewerNarrative, id) || strings.Contains(strings.ToLower(tests), id)) {
		return fmt.Errorf("agent pull request body contains the plan ID in reviewer narrative")
	}
	if pullRequestTestsContainTaoCommand(tests) {
		return fmt.Errorf("agent pull request body contains a direct Tao lifecycle command in Tests")
	}
	expectedTests, err := pullRequestTestsSection(deterministicDraft)
	if err != nil {
		return fmt.Errorf("validate deterministic pull request Tests section: %w", err)
	}
	if tests != expectedTests {
		return fmt.Errorf("agent pull request body must preserve Tests exactly as drafted")
	}
	return nil
}

func forbiddenPullRequestNarrativeLanguage(value string) string {
	value = strings.ToLower(value)
	for _, noise := range [...]string{"tao", "slice", "lifecycle", "squash and merge", "merge guidance", "cleanup --dry-run"} {
		if strings.Contains(value, noise) {
			return noise
		}
	}
	return ""
}

func pullRequestTestsContainTaoCommand(tests string) bool {
	for _, line := range strings.Split(tests, "\n") {
		candidate := strings.TrimSpace(line)
		candidate = strings.TrimSpace(strings.TrimLeft(candidate, "-*+0123456789. "))
		if isTaoLifecycleVerificationCommand(candidate) {
			return true
		}
		for {
			start := strings.IndexByte(candidate, '`')
			if start < 0 {
				break
			}
			candidate = candidate[start+1:]
			end := strings.IndexByte(candidate, '`')
			if end < 0 {
				break
			}
			if isTaoLifecycleVerificationCommand(strings.TrimSpace(candidate[:end])) {
				return true
			}
			candidate = candidate[end+1:]
		}
	}
	return false
}

func pullRequestTestsSection(body string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	headings, headingLines := pullRequestLevelTwoHeadings(lines)
	if len(headings) != 5 || headings[2] != "## Tests" || headings[3] != "## Deploy" {
		return "", fmt.Errorf("deterministic draft does not contain the required Tests section")
	}
	return strings.TrimSpace(strings.Join(lines[headingLines[2]+1:headingLines[3]], "\n")), nil
}

func pullRequestLevelTwoHeadings(lines []string) ([]string, []int) {
	var headings []string
	var headingLines []int
	var fence byte
	var fenceLength int
	for i, line := range lines {
		trimmedLeft := strings.TrimLeft(line, " \t")
		marker, length := pullRequestFenceMarker(trimmedLeft)
		if fence != 0 {
			if marker == fence && length >= fenceLength && strings.TrimSpace(trimmedLeft[length:]) == "" {
				fence = 0
				fenceLength = 0
			}
			continue
		}
		if marker != 0 {
			fence = marker
			fenceLength = length
			continue
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "## ") || strings.HasPrefix(trimmed, "##\t") {
			headings = append(headings, trimmed)
			headingLines = append(headingLines, i)
			continue
		}
		if i > 0 && pullRequestSetextLevelTwoUnderline(line) {
			title := strings.TrimSpace(lines[i-1])
			if title != "" {
				headings = append(headings, title)
				headingLines = append(headingLines, i)
			}
		}
	}
	return headings, headingLines
}

func pullRequestSetextLevelTwoUnderline(line string) bool {
	content := strings.TrimLeft(line, " ")
	if len(line)-len(content) > 3 {
		return false
	}
	content = strings.TrimRight(content, " \t")
	return content != "" && strings.Trim(content, "-") == ""
}

func pullRequestFenceMarker(line string) (byte, int) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0
	}
	length := 1
	for length < len(line) && line[length] == line[0] {
		length++
	}
	if length < 3 {
		return 0, 0
	}
	return line[0], length
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
	_, err := c.gitOutput(ctx, repoRoot, args...)
	return err
}

func (c deterministicPullRequestCreator) gitOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	runner := c.execution.Dependencies.CommandRunner
	if runner == nil {
		runner = defaultCommandRunner
	}
	gitArgs := append([]string{"-C", repoRoot}, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runner(ctx, "", "git", gitArgs, &stdout, &stderr); err != nil {
		return "", formatCommandError("git", args, err, stderr.String())
	}
	return stdout.String(), nil
}

func (c deterministicPullRequestCreator) ghOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	runner := c.execution.Dependencies.CommandRunner
	if runner == nil {
		runner = defaultCommandRunner
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runner(ctx, repoRoot, "gh", args, &stdout, &stderr); err != nil {
		// gh pr create can emit the newly created PR URL before a later metadata
		// operation fails. Preserve stdout so callers can retain that exact
		// identity instead of relying on an ambiguous post-failure lookup.
		return stdout.String(), formatCommandError("gh", args, err, stderr.String())
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

func pullRequestPreflight(run PullRequestRun) (string, string, error) {
	if run.Detail == nil {
		return "", "", fmt.Errorf("pull request preflight requires plan detail")
	}
	review := plan.CurrentReview(run.Detail)
	if review == nil || !review.IsApproved() {
		return "", "", fmt.Errorf("pull request preflight requires a current approved review")
	}
	if strings.TrimSpace(run.HeadSHA) == "" || strings.TrimSpace(review.Head) != strings.TrimSpace(run.HeadSHA) {
		return "", "", fmt.Errorf("pull request preflight requires the approved review head %q to match branch head %q", review.Head, run.HeadSHA)
	}
	if review.CommitMessage == nil {
		return "", "", fmt.Errorf("pull request preflight requires the approved review commit proposal")
	}
	title := review.CommitMessage.Subject
	if err := commitcontract.ValidateProposalMessage(title, review.CommitMessage.Body); err != nil {
		return "", "", fmt.Errorf("pull request preflight rejected approved review commit proposal: %w", err)
	}
	changeType := run.Detail.State.Plan.ChangeType
	if changeType == "" {
		return title, "", nil
	}
	if err := plan.ValidateChangeType(changeType); err != nil {
		return "", "", fmt.Errorf("pull request preflight: %w", err)
	}
	subjectType, _, ok := strings.Cut(title, "(")
	if !ok || subjectType != string(changeType) {
		return "", "", fmt.Errorf("pull request preflight: approved review subject type %q does not match plan change type %q", subjectType, changeType)
	}
	return title, changeType.Category(), nil
}

func deterministicPullRequestBody(run PullRequestRun, _ string, _ string, diffStat string) (string, error) {
	problem, fix := pullRequestProblemAndFix(run.Detail)
	var b strings.Builder
	fmt.Fprintf(&b, "## Problem\n\n%s\n\n## Fix\n\n%s\n\n## Tests\n\n", problem, fix)
	writePullRequestTests(&b, run.Detail)
	b.WriteString("\n## Deploy\n\nNo special deployment steps are required.\n\n## Scope\n\n")
	b.WriteString(deterministicPullRequestScope(diffStat))
	body := b.String()
	if err := validatePullRequestBody(body, run.PlanID, diffStat, body); err != nil {
		return "", fmt.Errorf("validate deterministic pull request body: %w", err)
	}
	return body, nil
}

func deterministicPullRequestScope(diffStat string) string {
	var b strings.Builder
	b.WriteString("<details>\n<summary>Changed files</summary>\n\n")
	if strings.TrimSpace(diffStat) != "" {
		b.WriteString("```text\n")
		b.WriteString(diffStat)
		if !strings.HasSuffix(diffStat, "\n") {
			b.WriteByte('\n')
		}
		b.WriteString("```\n\n")
	} else {
		b.WriteString("No changed-file summary is available.\n\n")
	}
	b.WriteString("</details>\n")
	return b.String()
}

func pullRequestProblemAndFix(detail *plan.PlanDetail) (string, string) {
	if detail == nil {
		return pullRequestNarrativeFallback, pullRequestNarrativeFallback
	}
	review := plan.CurrentReview(detail)
	if review == nil || review.CommitMessage == nil {
		return pullRequestNarrativeFallback, pullRequestNarrativeFallback
	}
	body := strings.TrimSpace(review.CommitMessage.Body)
	whatPrefix := "What:\n"
	whyMarker := "\n\nWhy:\n"
	if !strings.HasPrefix(body, whatPrefix) {
		return pullRequestNarrativeFallback, pullRequestNarrativeFallback
	}
	what, why, ok := strings.Cut(strings.TrimPrefix(body, whatPrefix), whyMarker)
	if !ok || strings.TrimSpace(what) == "" || strings.TrimSpace(why) == "" {
		return pullRequestNarrativeFallback, pullRequestNarrativeFallback
	}
	planID := detail.State.Plan.ID
	return sanitizePullRequestText(why, planID), sanitizePullRequestText(what, planID)
}

func sanitizePullRequestText(value, planID string) string {
	if planID = strings.TrimSpace(planID); planID != "" {
		value = regexp.MustCompile(`(?i)`+regexp.QuoteMeta(planID)).ReplaceAllString(value, "")
	}
	value = pullRequestSquashAndMergeRE.ReplaceAllString(value, "combine the changes")
	value = pullRequestMergeGuidanceRE.ReplaceAllString(value, "integration guidance")
	value = pullRequestCleanupDryRunRE.ReplaceAllString(value, "cleanup preview")
	value = pullRequestTaoWordRE.ReplaceAllString(value, "the workflow")
	value = pullRequestSliceWordRE.ReplaceAllString(value, "changes")
	value = pullRequestLifecycleRE.ReplaceAllString(value, "workflow state")
	value = strings.TrimSpace(value)
	if value == "" || forbiddenPullRequestNarrativeLanguage(value) != "" {
		return pullRequestNarrativeFallback
	}
	return escapePullRequestLevelTwoHeadings(value)
}

func escapePullRequestLevelTwoHeadings(value string) string {
	lines := strings.Split(value, "\n")
	var fence byte
	var fenceLength int
	for i, line := range lines {
		trimmedLeft := strings.TrimLeft(line, " \t")
		marker, length := pullRequestFenceMarker(trimmedLeft)
		if fence != 0 {
			if marker == fence && length >= fenceLength && strings.TrimSpace(trimmedLeft[length:]) == "" {
				fence = 0
				fenceLength = 0
			}
			continue
		}
		if marker != 0 {
			fence = marker
			fenceLength = length
			continue
		}
		content := strings.TrimSpace(line)
		if strings.HasPrefix(content, "## ") || strings.HasPrefix(content, "##\t") {
			headingStart := strings.Index(line, "##")
			lines[i] = line[:headingStart] + `\` + line[headingStart:]
			continue
		}
		if i > 0 && strings.TrimSpace(lines[i-1]) != "" && pullRequestSetextLevelTwoUnderline(line) {
			underlineStart := strings.Index(line, "-")
			lines[i] = line[:underlineStart] + `\` + line[underlineStart:]
		}
	}
	return strings.Join(lines, "\n")
}

func writePullRequestTests(b *strings.Builder, detail *plan.PlanDetail) {
	wrote := false
	if detail != nil {
		for _, slice := range detail.Slices.Slices {
			for _, result := range slice.VerificationResults {
				command := strings.TrimSpace(result.Command)
				if command == "" || isTaoLifecycleVerificationCommand(command) {
					continue
				}
				outcome := strings.TrimSpace(result.Result)
				if outcome == "" {
					outcome = "recorded"
				}
				fmt.Fprintf(b, "- `%s`: %s\n", command, outcome)
				wrote = true
			}
		}
	}
	if !wrote {
		b.WriteString("No automated test results were recorded.\n")
	}
}

func isTaoLifecycleVerificationCommand(command string) bool {
	var fields []string
	for _, token := range shellCommandTokens(command) {
		if token.separator {
			if shellCommandRunsTao(fields) {
				return true
			}
			fields = fields[:0]
			continue
		}
		fields = append(fields, token.value)
	}
	return shellCommandRunsTao(fields)
}

type shellCommandToken struct {
	value     string
	separator bool
}

func shellCommandTokens(command string) []shellCommandToken {
	var tokens []shellCommandToken
	var current strings.Builder
	var quote byte
	escaped := false
	flush := func() {
		if current.Len() == 0 {
			return
		}
		tokens = append(tokens, shellCommandToken{value: current.String()})
		current.Reset()
	}
	for i := 0; i < len(command); i++ {
		char := command[i]
		if escaped {
			current.WriteByte(char)
			escaped = false
			continue
		}
		if quote != 0 {
			if char == quote {
				quote = 0
				continue
			}
			if char == '\\' && quote == '"' {
				escaped = true
				continue
			}
			current.WriteByte(char)
			continue
		}
		switch char {
		case '\\':
			escaped = true
		case '\'', '"':
			quote = char
		case ' ', '\t', '\r':
			flush()
		case '\n', ';':
			flush()
			tokens = append(tokens, shellCommandToken{separator: true})
		case '&', '|':
			flush()
			if i+1 < len(command) && command[i+1] == char {
				i++
			}
			tokens = append(tokens, shellCommandToken{separator: true})
		default:
			current.WriteByte(char)
		}
	}
	if escaped {
		current.WriteByte('\\')
	}
	flush()
	return tokens
}

func shellCommandRunsTao(fields []string) bool {
	for i := 0; i < len(fields); {
		for i < len(fields) && isShellEnvironmentAssignment(fields[i]) {
			i++
		}
		if i >= len(fields) {
			return false
		}
		if isTaoExecutable(fields[i]) {
			return true
		}
		if !isEnvironmentExecutable(fields[i]) {
			return false
		}
		i++
	environmentPrefix:
		for i < len(fields) {
			switch {
			case fields[i] == "--":
				i++
				break environmentPrefix
			case isShellEnvironmentAssignment(fields[i]):
				i++
			case environmentOptionTakesValue(fields[i]) && i+1 < len(fields):
				i += 2
			case strings.HasPrefix(fields[i], "-"):
				i++
			default:
				break environmentPrefix
			}
		}
	}
	return false
}

func isTaoExecutable(value string) bool {
	return value == "tao" || strings.HasSuffix(value, "/tao")
}

func isEnvironmentExecutable(value string) bool {
	return value == "env" || strings.HasSuffix(value, "/env")
}

func isShellEnvironmentAssignment(value string) bool {
	name, _, ok := strings.Cut(value, "=")
	if !ok || name == "" || !isShellNameStart(name[0]) {
		return false
	}
	for i := 1; i < len(name); i++ {
		if !isShellNameStart(name[i]) && (name[i] < '0' || name[i] > '9') {
			return false
		}
	}
	return true
}

func isShellNameStart(value byte) bool {
	return (value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || value == '_'
}

func environmentOptionTakesValue(value string) bool {
	return value == "-u" || value == "--unset" || value == "-C" || value == "--chdir" || value == "-S" || value == "--split-string"
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
