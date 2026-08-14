package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/gitops"
)

var githubPullRequestURLRE = regexp.MustCompile(`https://github\.com/[^\s)]+/pull/(\d+)`)

const (
	pullRequestLabelColor       = "1D76DB"
	pullRequestLabelDescription = "Repository change category"
	pullRequestJSONFields       = "number,url,createdAt,labels,assignees"
)

// PullRequest is forge-owned pull-request identity. Callers project it into
// their lifecycle model and attach branch/head evidence themselves.
type PullRequest struct {
	Number    int
	URL       string
	CreatedAt time.Time
}

// Metadata is the forge state needed to decide whether labels or assignment
// require repair.
type Metadata struct {
	Labels    []string
	Assignees []string
}

// FindRequest identifies an open pull request owned by the origin repository.
type FindRequest struct {
	RepoRoot   string
	BaseBranch string
	Branch     string
	// FallbackNow is evaluated only after gh returns, immediately before its
	// response is parsed. Recovery views use their fixed durable timestamp.
	FallbackNow func() time.Time
}

// ViewRequest identifies a pull request by its durable forge number.
type ViewRequest struct {
	RepoRoot          string
	Number            int
	FallbackCreatedAt time.Time
}

// CreateRequest contains the Tao-owned arguments to GitHub pull-request creation.
type CreateRequest struct {
	RepoRoot   string
	BaseBranch string
	Branch     string
	Title      string
	BodyFile   string
	Label      string
	// FallbackNow is evaluated only after gh returns, immediately before its
	// response is parsed. This keeps URL-only creation timestamps close to the
	// actual forge operation rather than earlier body-generation work.
	FallbackNow func() time.Time
}

// CreationOutcome retains command output and any identity parsed from it even
// when gh reports a later operation error.
type CreationOutcome struct {
	PullRequest  PullRequest
	Stdout       string
	OperationErr error
	ParseErr     error
}

// MetadataRequest describes required metadata on a known pull request.
type MetadataRequest struct {
	RepoRoot    string
	PullRequest PullRequest
	Metadata    Metadata
	Label       string
}

// PullRequests is the hosting-service boundary used by run orchestration.
type PullRequests interface {
	Find(context.Context, FindRequest) (PullRequest, Metadata, bool, error)
	View(context.Context, ViewRequest) (PullRequest, Metadata, bool, error)
	Create(context.Context, CreateRequest) CreationOutcome
	EnsureMetadata(context.Context, MetadataRequest) error
}

// GitHub implements PullRequests with git and gh commands.
type GitHub struct {
	runner commandrunner.Runner
}

// NewGitHub creates a GitHub CLI pull-request implementation.
func NewGitHub(runner commandrunner.Runner) GitHub {
	if runner == nil {
		runner = commandrunner.DefaultLocal
	}
	return GitHub{runner: runner}
}

// Find discovers an origin-owned open pull request for the exact base/head.
func (g GitHub) Find(ctx context.Context, request FindRequest) (PullRequest, Metadata, bool, error) {
	origin, err := g.originRepositoryIdentity(ctx, request.RepoRoot)
	if err != nil {
		return PullRequest{}, Metadata{}, false, err
	}
	output, err := g.ghOutput(ctx, request.RepoRoot, "pr", "list", "--base", request.BaseBranch, "--head", request.Branch, "--state", "open", "--json", pullRequestJSONFields+",headRefName,headRepository,headRepositoryOwner")
	if err != nil {
		return PullRequest{}, Metadata{}, false, err
	}
	fallbackCreatedAt := time.Now().UTC()
	if request.FallbackNow != nil {
		fallbackCreatedAt = request.FallbackNow().UTC()
	}
	var matches []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &matches); err != nil {
		return PullRequest{}, Metadata{}, false, fmt.Errorf("parse gh pull request list json: %w", err)
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
			return PullRequest{}, Metadata{}, false, fmt.Errorf("parse gh pull request head identity: %w", err)
		}
		if head.RefName != request.Branch || !strings.EqualFold(head.Owner.Login, origin.Owner) || !strings.EqualFold(head.Repository.NameWithOwner, origin.Owner+"/"+origin.Name) {
			continue
		}
		return parseViewedPullRequest(match, fallbackCreatedAt)
	}
	return PullRequest{}, Metadata{}, false, nil
}

// View loads one pull request and its required metadata fields.
func (g GitHub) View(ctx context.Context, request ViewRequest) (PullRequest, Metadata, bool, error) {
	output, err := g.ghOutput(ctx, request.RepoRoot, "pr", "view", strconv.Itoa(request.Number), "--json", pullRequestJSONFields)
	if err != nil {
		return PullRequest{}, Metadata{}, false, err
	}
	return parseViewedPullRequest([]byte(output), request.FallbackCreatedAt)
}

// Create ensures the requested label and invokes gh pr create. Its outcome
// preserves stdout and a parsed identity when gh emits a URL before failing.
func (g GitHub) Create(ctx context.Context, request CreateRequest) CreationOutcome {
	label := request.Label
	if label != "" {
		var err error
		label, err = g.ensureLabel(ctx, request.RepoRoot, label)
		if err != nil {
			return CreationOutcome{OperationErr: err}
		}
	}
	args := []string{"pr", "create", "--base", request.BaseBranch, "--head", request.Branch, "--title", request.Title, "--body-file", request.BodyFile}
	if label != "" {
		args = append(args, "--label", label)
	}
	args = append(args, "--assignee", "@me")
	output, operationErr := g.ghOutput(ctx, request.RepoRoot, args...)
	fallbackCreatedAt := time.Now().UTC()
	if request.FallbackNow != nil {
		fallbackCreatedAt = request.FallbackNow().UTC()
	}
	pr, parseErr := ParsePullRequestOutput(output, fallbackCreatedAt)
	outcome := CreationOutcome{PullRequest: pr, Stdout: output, ParseErr: parseErr}
	if operationErr != nil {
		outcome.OperationErr = fmt.Errorf("create pull request with assignee @me: %w", operationErr)
	}
	return outcome
}

// EnsureMetadata repairs a missing category label or authenticated-user assignment.
func (g GitHub) EnsureMetadata(ctx context.Context, request MetadataRequest) error {
	pr := request.PullRequest
	loginOutput, err := g.ghOutput(ctx, request.RepoRoot, "api", "user", "--jq", ".login")
	if err != nil {
		return fmt.Errorf("identify authenticated GitHub user for pull request #%d: %w", pr.Number, err)
	}
	login := strings.TrimSpace(loginOutput)
	if login == "" {
		return fmt.Errorf("identify authenticated GitHub user for pull request #%d: GitHub returned an empty login", pr.Number)
	}

	args := []string{"pr", "edit", strconv.Itoa(pr.Number)}
	if request.Label != "" && !containsFold(request.Metadata.Labels, request.Label) {
		label, err := g.ensureLabel(ctx, request.RepoRoot, request.Label)
		if err != nil {
			return err
		}
		args = append(args, "--add-label", label)
	}
	if !containsFold(request.Metadata.Assignees, login) {
		args = append(args, "--add-assignee", "@me")
	}
	if len(args) == 3 {
		return nil
	}
	if _, err := g.ghOutput(ctx, request.RepoRoot, args...); err != nil {
		return fmt.Errorf("repair pull request #%d required metadata: %w", pr.Number, err)
	}
	return nil
}

func (g GitHub) ensureLabel(ctx context.Context, repoRoot, label string) (string, error) {
	output, err := g.ghOutput(ctx, repoRoot, "label", "list", "--search", label, "--json", "name", "--limit", "100")
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
	if _, err := g.ghOutput(ctx, repoRoot, "label", "create", label, "--color", pullRequestLabelColor, "--description", pullRequestLabelDescription); err != nil {
		return "", fmt.Errorf("create pull request label %q: %w", label, err)
	}
	return label, nil
}

func (g GitHub) originRepositoryIdentity(ctx context.Context, repoRoot string) (repositoryIdentity, error) {
	output, err := gitops.NewClient(repoRoot, g.runner).OriginPushURL(ctx)
	if err != nil {
		return repositoryIdentity{}, fmt.Errorf("identify origin repository for pull request discovery: %w", err)
	}
	identity, err := parseGitHubRepositoryIdentity(output)
	if err != nil {
		return repositoryIdentity{}, fmt.Errorf("identify origin repository for pull request discovery: %w", err)
	}
	return identity, nil
}

type repositoryIdentity struct {
	Owner string
	Name  string
}

func parseGitHubRepositoryIdentity(remoteURL string) (repositoryIdentity, error) {
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
		return repositoryIdentity{}, fmt.Errorf("unsupported origin URL %q", remoteURL)
	}
	return repositoryIdentity{Owner: parts[0], Name: parts[1]}, nil
}

func parseViewedPullRequest(output []byte, fallbackCreatedAt time.Time) (PullRequest, Metadata, bool, error) {
	pr, err := parseGHPullRequest(string(output), fallbackCreatedAt)
	if err != nil {
		return PullRequest{}, Metadata{}, false, err
	}
	metadata, err := parsePullRequestMetadata(string(output))
	if err != nil {
		return PullRequest{}, Metadata{}, false, err
	}
	return pr, metadata, true, nil
}

func parseGHPullRequest(output string, fallbackCreatedAt time.Time) (PullRequest, error) {
	var payload struct {
		Number    int    `json:"number"`
		URL       string `json:"url"`
		CreatedAt string `json:"createdAt"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		return PullRequest{}, fmt.Errorf("parse gh pull request json: %w", err)
	}
	if payload.Number <= 0 || strings.TrimSpace(payload.URL) == "" {
		return PullRequest{}, fmt.Errorf("gh pull request json missing number or url")
	}
	createdAt := fallbackCreatedAt
	if payload.CreatedAt != "" {
		if parsed, err := time.Parse(time.RFC3339, payload.CreatedAt); err == nil {
			createdAt = parsed
		}
	}
	return PullRequest{Number: payload.Number, URL: payload.URL, CreatedAt: createdAt}, nil
}

func parsePullRequestMetadata(output string) (Metadata, error) {
	var payload struct {
		Labels []struct {
			Name string `json:"name"`
		} `json:"labels"`
		Assignees []struct {
			Login string `json:"login"`
		} `json:"assignees"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &payload); err != nil {
		return Metadata{}, fmt.Errorf("parse gh pull request metadata: %w", err)
	}
	metadata := Metadata{Labels: make([]string, 0, len(payload.Labels)), Assignees: make([]string, 0, len(payload.Assignees))}
	for _, label := range payload.Labels {
		metadata.Labels = append(metadata.Labels, label.Name)
	}
	for _, assignee := range payload.Assignees {
		metadata.Assignees = append(metadata.Assignees, assignee.Login)
	}
	return metadata, nil
}

// ParsePullRequestOutput extracts one unambiguous GitHub pull-request URL from
// command output. Repeated copies of the same URL are accepted.
func ParsePullRequestOutput(output string, createdAt time.Time) (PullRequest, error) {
	matches := githubPullRequestURLRE.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return PullRequest{}, fmt.Errorf("could not find GitHub pull request URL in agent output")
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
			return PullRequest{}, fmt.Errorf("agent output contains multiple distinct GitHub pull request URLs")
		}
	}
	number, err := strconv.Atoi(firstNumber)
	if err != nil {
		return PullRequest{}, fmt.Errorf("parse pull request number: %w", err)
	}
	return PullRequest{Number: number, URL: firstURL, CreatedAt: createdAt}, nil
}

func containsFold(values []string, want string) bool {
	for _, value := range values {
		if strings.EqualFold(value, want) {
			return true
		}
	}
	return false
}

func (g GitHub) ghOutput(ctx context.Context, repoRoot string, args ...string) (string, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := g.runner(ctx, repoRoot, "gh", args, &stdout, &stderr); err != nil {
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

var _ PullRequests = GitHub{}
