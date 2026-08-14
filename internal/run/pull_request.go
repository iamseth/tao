package run

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	commitcontract "github.com/iamseth/tao/internal/commit"
	"github.com/iamseth/tao/internal/forge"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/prbody"
)

type deterministicPullRequestCreator struct {
	execution     runExecution
	bodyGenerator PullRequestBodyGenerator
	pullRequests  forge.PullRequests
}

func defaultPullRequestCreatorWithBody(execution runExecution, bodyGenerator PullRequestBodyGenerator) PullRequestCreator {
	return deterministicPullRequestCreator{
		execution:     execution,
		bodyGenerator: bodyGenerator,
		pullRequests:  forge.NewGitHub(execution.Dependencies.CommandRunner),
	}
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

	pullRequests := c.pullRequestService()
	if intent != nil && pullRequestIntentHasIdentity(*intent) {
		identity, metadata, found, viewErr := pullRequests.View(ctx, forge.ViewRequest{RepoRoot: run.RepoRoot, Number: intent.Number, FallbackCreatedAt: intent.CreatedAt})
		if viewErr != nil {
			return plan.PullRequest{}, fmt.Errorf("recover pull request intent for #%d: %w", intent.Number, viewErr)
		}
		pr := lifecyclePullRequest(identity, run)
		if !found || !pullRequestMatchesIntent(pr, *intent) {
			return plan.PullRequest{}, fmt.Errorf("recover pull request intent for #%d: discovered pull request does not match recorded number and URL", intent.Number)
		}
		// Preserve the creation timestamp recorded with the emitted identity so
		// final recording matches the durable recovery intent exactly.
		pr.CreatedAt = intent.CreatedAt
		if err := pullRequests.EnsureMetadata(ctx, forge.MetadataRequest{RepoRoot: run.RepoRoot, PullRequest: identity, Metadata: metadata, Label: label}); err != nil {
			return plan.PullRequest{}, err
		}
		return pr, nil
	}

	identity, _, found, err := pullRequests.Find(ctx, forge.FindRequest{
		RepoRoot:   run.RepoRoot,
		BaseBranch: baseBranch,
		Branch:     run.Branch,
		FallbackNow: func() time.Time {
			return now(c.execution).UTC()
		},
	})
	if err != nil {
		return plan.PullRequest{}, err
	}
	if found {
		// Discovery alone never proves that Tao created the pull request. In
		// particular, a legacy branch/head-only intent must not authorize Tao to
		// mutate a matching pull request that a human may have created.
		return lifecyclePullRequest(identity, run), nil
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

	outcome := pullRequests.Create(ctx, forge.CreateRequest{
		RepoRoot:   run.RepoRoot,
		BaseBranch: baseBranch,
		Branch:     run.Branch,
		Title:      title,
		BodyFile:   bodyFile,
		Label:      label,
		FallbackNow: func() time.Time {
			return now(c.execution).UTC()
		},
	})
	createdPR := lifecyclePullRequest(outcome.PullRequest, run)
	if outcome.OperationErr != nil {
		if !pullRequestIntentHasIdentity(createdPR) {
			// A matching pull request discovered after a failed create may have
			// been opened concurrently by a human. Without an identity emitted by
			// this create attempt, Tao has no ownership evidence and must not
			// repair metadata or persist branch/head-only recovery intent.
			return plan.PullRequest{}, outcome.OperationErr
		}
		if persistErr := c.persistPullRequestIntent(run, createdPR); persistErr != nil {
			return plan.PullRequest{}, fmt.Errorf("%w; persist partial pull request recovery intent: %w", outcome.OperationErr, persistErr)
		}

		identity, metadata, found, viewErr := pullRequests.View(ctx, forge.ViewRequest{RepoRoot: run.RepoRoot, Number: createdPR.Number, FallbackCreatedAt: createdPR.CreatedAt})
		if viewErr != nil {
			return plan.PullRequest{}, fmt.Errorf("%w; verify emitted pull request identity #%d: %w", outcome.OperationErr, createdPR.Number, viewErr)
		}
		pr := lifecyclePullRequest(identity, run)
		if !found || !pullRequestMatchesIntent(pr, createdPR) {
			return plan.PullRequest{}, fmt.Errorf("%w; emitted pull request identity #%d did not match GitHub", outcome.OperationErr, createdPR.Number)
		}
		pr.CreatedAt = createdPR.CreatedAt
		if metadataErr := pullRequests.EnsureMetadata(ctx, forge.MetadataRequest{RepoRoot: run.RepoRoot, PullRequest: identity, Metadata: metadata, Label: label}); metadataErr != nil {
			return plan.PullRequest{}, metadataErr
		}
		return pr, nil
	}
	if outcome.ParseErr != nil {
		return plan.PullRequest{}, outcome.ParseErr
	}
	return createdPR, nil
}

func (c deterministicPullRequestCreator) pullRequestService() forge.PullRequests {
	if c.pullRequests != nil {
		return c.pullRequests
	}
	return forge.NewGitHub(c.execution.Dependencies.CommandRunner)
}

func lifecyclePullRequest(identity forge.PullRequest, run PullRequestRun) plan.PullRequest {
	return plan.PullRequest{
		Number:    identity.Number,
		URL:       identity.URL,
		CreatedAt: identity.CreatedAt,
		Branch:    run.Branch,
		HeadSHA:   run.HeadSHA,
	}
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

func (c deterministicPullRequestCreator) pullRequestBody(ctx context.Context, run PullRequestRun, baseBranch string, title string, diffStat string) (string, error) {
	draft := prbody.Build(projectPullRequestBodyInput(run.Detail, diffStat))
	validation := prbody.ValidationInput{PlanID: run.PlanID, DiffStat: diffStat, DeterministicDraft: draft}
	if err := prbody.Validate(draft, validation); err != nil {
		return "", fmt.Errorf("validate deterministic pull request body: %w", err)
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
	if err := prbody.Validate(body, validation); err != nil {
		c.warnBodyGeneration(err)
		return draft, nil
	}
	return body + "\n", nil
}

func projectPullRequestBodyInput(detail *plan.PlanDetail, diffStat string) prbody.Input {
	input := prbody.Input{DiffStat: diffStat}
	if detail == nil {
		return input
	}
	input.PlanID = detail.State.Plan.ID
	if review := plan.CurrentReview(detail); review != nil && review.CommitMessage != nil {
		input.CommitMessageBody = review.CommitMessage.Body
	}
	for _, slice := range detail.Slices.Slices {
		for _, result := range slice.VerificationResults {
			input.VerificationResults = append(input.VerificationResults, prbody.VerificationResult{Command: result.Command, Result: result.Result})
		}
	}
	return input
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

// extractPullRequest remains the provider-neutral agent-session adapter. The
// parsing policy itself belongs to internal/forge.
func extractPullRequest(output string, createdAt time.Time) (plan.PullRequest, error) {
	identity, err := forge.ParsePullRequestOutput(output, createdAt)
	if err != nil {
		return plan.PullRequest{}, err
	}
	return lifecyclePullRequest(identity, PullRequestRun{}), nil
}
