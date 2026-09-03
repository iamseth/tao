package merge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/prompts"
)

var (
	ErrSingleReviewRejected    = errors.New("single-plan integration review rejected")
	ErrSingleReviewNotApproved = errors.New("single-plan integration review did not approve")
)

// SingleReviewOutcome classifies the one-shot reviewer result. Only approve
// grants authority; every other value is bounded diagnostic evidence.
type SingleReviewOutcome string

const (
	SingleReviewOutcomeApprove          SingleReviewOutcome = "approve"
	SingleReviewOutcomeComment          SingleReviewOutcome = "comment"
	SingleReviewOutcomeChangesRequested SingleReviewOutcome = "changes_requested"
	SingleReviewOutcomeMalformed        SingleReviewOutcome = "malformed_output"
	SingleReviewOutcomeProviderFailure  SingleReviewOutcome = "provider_failure"
	SingleReviewOutcomeTimeout          SingleReviewOutcome = "timeout"
	SingleReviewOutcomeMutation         SingleReviewOutcome = "mutation"
)

// SingleIntegrationReviewer is the read-only seam used after Tao commits one
// conflict-resolved squash integration.
type SingleIntegrationReviewer interface {
	ReviewResolvedIntegration(context.Context, SingleReviewRequest) (SingleReviewResult, error)
}

// SingleReviewRequest binds untrusted descriptive and verification evidence to
// the committed resolution's exact default parent and integration head.
type SingleReviewRequest struct {
	Intent               plan.SingleMergeCommitIntent
	SourceBranch         string
	IntegrationRoot      string
	PlanTitle            string
	SourceReview         string
	VerifyCommand        string
	VerificationHead     string
	VerificationEvidence string
	ProtectedBranches    []string
}

// SingleReviewResult exposes provider data for best-effort generic plan
// telemetry. It does not grant recovery authority.
type SingleReviewResult struct {
	Intent     plan.SingleMergeCommitIntent
	Provider   BatchAgentSessionResult
	Outcome    SingleReviewOutcome
	Authorized bool
	Recovered  bool
}

// GuardedSingleIntegrationReviewer performs exactly one read-only provider
// call and persists only a bounded projection in the resolution transaction.
type GuardedSingleIntegrationReviewer struct {
	Git      GitClient
	Recorder SingleResolutionRecorder
	Agent    SingleMergeAgent
	Now      func() time.Time
}

func (r GuardedSingleIntegrationReviewer) ReviewResolvedIntegration(ctx context.Context, request SingleReviewRequest) (result SingleReviewResult, err error) {
	result.Intent = request.Intent
	if r.Git == nil || r.Recorder == nil {
		return result, fmt.Errorf("%w: git client and recorder are required", ErrSingleReviewRejected)
	}
	if err := request.Intent.Validate(); err != nil {
		return result, fmt.Errorf("%w: invalid single-merge intent: %w", ErrSingleReviewRejected, err)
	}
	resolution := request.Intent.Resolution
	if resolution == nil || (resolution.Phase != plan.SingleMergeResolutionPhaseCommitted && resolution.Phase != plan.SingleMergeResolutionPhaseReviewed) {
		return result, fmt.Errorf("%w: resolution is not committed", ErrSingleReviewRejected)
	}
	if strings.TrimSpace(request.VerificationHead) != resolution.IntegrationHead {
		return result, fmt.Errorf("%w: verification evidence is not bound to integration head %s", ErrSingleReviewRejected, resolution.IntegrationHead)
	}
	branches, err := singleReviewProtectedBranches(request)
	if err != nil {
		return result, fmt.Errorf("%w: %w", ErrSingleReviewRejected, err)
	}
	if err := r.validateExactBoundary(ctx, request, branches); err != nil {
		return result, fmt.Errorf("%w: %w", ErrSingleReviewRejected, err)
	}
	if resolution.Phase == plan.SingleMergeResolutionPhaseReviewed {
		result.Recovered = true
		result.Outcome = outcomeForPersistedReview(resolution.Review)
		result.Authorized = resolution.Review.IsApproved()
		if result.Authorized {
			return result, nil
		}
		return result, fmt.Errorf("%w: recovered %s verdict", ErrSingleReviewNotApproved, result.Outcome)
	}
	if r.Agent == nil {
		return result, fmt.Errorf("%w: reviewer agent is required", ErrSingleReviewRejected)
	}

	revspec := request.Intent.DefaultParent + ".." + resolution.IntegrationHead
	diff, diffTruncated, err := r.Git.DiffBounded(ctx, revspec, prompts.SingleMergeReviewDiffCaptureLimit)
	if err != nil {
		return result, fmt.Errorf("%w: render exact integration diff: %w", ErrSingleReviewRejected, err)
	}
	stat, err := r.Git.DiffStat(ctx, revspec)
	if err != nil {
		return result, fmt.Errorf("%w: render exact integration diff stat: %w", ErrSingleReviewRejected, err)
	}
	prompt, err := prompts.RenderSingleMergeReview(prompts.SingleMergeReviewData{
		PlanID: request.Intent.PlanID, DefaultStart: request.Intent.DefaultParent,
		IntegrationHead: resolution.IntegrationHead, VerifyCommand: request.VerifyCommand,
		Candidate:    fmt.Sprintf("plan=%s title=%s source_head=%s", request.Intent.PlanID, request.PlanTitle, request.Intent.SourceHead),
		SourceReview: request.SourceReview, ResolutionSummary: resolution.Summary,
		Diff: diff, DiffStat: stat, DiffTruncated: diffTruncated,
		Verification: fmt.Sprintf("command=%s\nhead=%s\n%s", request.VerifyCommand, request.VerificationHead, request.VerificationEvidence),
	})
	if err != nil {
		return result, fmt.Errorf("%w: render review prompt: %w", ErrSingleReviewRejected, err)
	}

	beforeStatus, err := r.Git.StatusPorcelain(ctx)
	if err != nil {
		return result, fmt.Errorf("%w: snapshot worktree status: %w", ErrSingleReviewRejected, err)
	}
	beforeHead, err := r.Git.RevParse(ctx, "HEAD")
	if err != nil {
		return result, fmt.Errorf("%w: snapshot worktree HEAD: %w", ErrSingleReviewRejected, err)
	}
	boundary, err := snapshotWorktreePaths(ctx, r.Git)
	if err != nil {
		return result, fmt.Errorf("%w: snapshot reviewer drift boundary: %w", ErrSingleReviewRejected, err)
	}
	defer boundary.cleanup()
	gitBoundary, err := snapshotGitSessionBoundary(ctx, r.Git.Root())
	if err != nil {
		return result, fmt.Errorf("%w: snapshot Git metadata and refs: %w", ErrSingleReviewRejected, err)
	}
	defer gitBoundary.cleanup()

	result.Provider, err = r.Agent.Resolve(ctx, BatchAgentSessionRequest{
		Operation: BatchAgentOperationSinglePlanReview, Attempt: 1,
		IntegrationRoot: request.IntegrationRoot, Prompt: prompt, CandidatePlanID: request.Intent.PlanID,
		ProtectedGitObjectRoot: gitBoundary.objects.root,
		ProtectedGitWritePaths: gitBoundary.protectedGitWritePaths(),
	})
	providerErr := err
	cleanupCtx, cancelCleanup := singleAgentCleanupContext(ctx)
	defer cancelCleanup()
	gitBoundaryErr := compareGitSessionBoundary(cleanupCtx, gitBoundary)
	ignoredChanged, boundaryErr := ignoredWorktreeChanged(cleanupCtx, r.Git, boundary)
	afterStatus, statusErr := r.Git.StatusPorcelain(cleanupCtx)
	afterHead, headErr := r.Git.RevParse(cleanupCtx, "HEAD")
	mutated := gitBoundaryErr != nil || boundaryErr != nil || ignoredChanged || statusErr != nil || headErr != nil || afterStatus != beforeStatus || strings.TrimSpace(afterHead) != strings.TrimSpace(beforeHead)
	if mutated {
		// The reviewer has a read-only view of the integration worktree and Git
		// metadata. Any drift belongs to a concurrent process, so fail closed
		// without overwriting that process's work in an attempted rollback.
		summary := "The integration worktree, HEAD, ignored paths, or Git metadata changed concurrently during independent review; Tao rejected the verdict and preserved that external work instead of rolling it back."
		return r.persistNonApproval(result, request.Intent, SingleReviewOutcomeMutation, summary, errors.Join(gitBoundaryErr, boundaryErr, statusErr, headErr))
	}
	if providerErr != nil {
		var timeout *agent.SessionTimeoutError
		if errors.As(providerErr, &timeout) {
			return r.persistNonApproval(result, request.Intent, SingleReviewOutcomeTimeout, "Independent integration review timed out and did not authorize this transaction; manual intervention is required before another review.", providerErr)
		}
		return r.persistNonApproval(result, request.Intent, SingleReviewOutcomeProviderFailure, "Independent integration review provider failed and did not authorize this transaction; manual intervention is required before another review.", providerErr)
	}

	parsed, parseErr := decodeSingleIntegrationReview(result.Provider.Output)
	if parseErr != nil || (parsed.Verdict == plan.ReviewVerdictApprove && len(parsed.Findings) != 0) {
		return r.persistNonApproval(result, request.Intent, SingleReviewOutcomeMalformed, "Independent integration reviewer returned malformed or oversized structured output and did not authorize this transaction; manual intervention is required before another review.", parseErr)
	}
	result.Outcome = SingleReviewOutcome(parsed.Verdict)
	findings := boundedSingleReviewFindings(parsed.Findings)
	review := plan.SingleMergeResolutionReview{
		Status: plan.ReviewStatusCompleted, Verdict: parsed.Verdict, Summary: boundSingleReviewText(parsed.Summary),
		FindingsCount: len(findings), Findings: findings,
		Base: request.Intent.DefaultParent, Head: resolution.IntegrationHead,
		Agent: boundedReviewAgent(result.Provider.Provider.AgentLabel), ReviewedAt: r.timestamp(resolution.CommittedAt),
	}
	if err := r.persistReview(&result, request.Intent, review); err != nil {
		return result, err
	}
	if parsed.Verdict != plan.ReviewVerdictApprove {
		return result, fmt.Errorf("%w: reviewer returned %s", ErrSingleReviewNotApproved, parsed.Verdict)
	}
	result.Authorized = true
	return result, nil
}

const singleIntegrationReviewJSONLimit = 512 * 1024

type singleIntegrationReview struct {
	Verdict  string
	Summary  string
	Findings []plan.ReviewFinding
}

// decodeSingleIntegrationReview deliberately enforces a stricter contract than
// ordinary and legacy plan review parsing. This one-shot result can authorize a
// merge, so every required field must be present with its declared JSON type.
func decodeSingleIntegrationReview(output string) (singleIntegrationReview, error) {
	block, err := singleIntegrationReviewJSONBlock(output)
	if err != nil {
		return singleIntegrationReview{}, err
	}
	var payload struct {
		Verdict  *string            `json:"verdict"`
		Summary  *string            `json:"summary"`
		Findings *[]json.RawMessage `json:"findings"`
	}
	decoder := json.NewDecoder(strings.NewReader(block))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return singleIntegrationReview{}, fmt.Errorf("decode tao-review-json: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return singleIntegrationReview{}, errors.New("decode tao-review-json: multiple JSON values")
		}
		return singleIntegrationReview{}, fmt.Errorf("decode tao-review-json trailing content: %w", err)
	}
	if payload.Verdict == nil || payload.Summary == nil || payload.Findings == nil {
		return singleIntegrationReview{}, errors.New("tao-review-json is missing verdict, summary, or findings")
	}
	if strings.TrimSpace(*payload.Summary) == "" {
		return singleIntegrationReview{}, errors.New("tao-review-json summary is empty")
	}
	switch *payload.Verdict {
	case plan.ReviewVerdictApprove, plan.ReviewVerdictChangesRequested, plan.ReviewVerdictComment:
	default:
		return singleIntegrationReview{}, fmt.Errorf("tao-review-json verdict %q is invalid", *payload.Verdict)
	}
	findings := make([]plan.ReviewFinding, 0, len(*payload.Findings))
	for index, raw := range *payload.Findings {
		if string(raw) == "null" {
			return singleIntegrationReview{}, fmt.Errorf("tao-review-json finding %d is null", index)
		}
		var finding plan.ReviewFinding
		findingDecoder := json.NewDecoder(strings.NewReader(string(raw)))
		findingDecoder.DisallowUnknownFields()
		if err := findingDecoder.Decode(&finding); err != nil {
			return singleIntegrationReview{}, fmt.Errorf("decode tao-review-json finding %d: %w", index, err)
		}
		findings = append(findings, finding)
	}
	return singleIntegrationReview{Verdict: *payload.Verdict, Summary: *payload.Summary, Findings: findings}, nil
}

func singleIntegrationReviewJSONBlock(output string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(output, "\r\n", "\n"), "\r", "\n"), "\n")
	var current []string
	var block string
	blocks := 0
	inBlock := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !inBlock {
			if trimmed == "```tao-review-json" || trimmed == "``` tao-review-json" {
				current = current[:0]
				inBlock = true
			}
			continue
		}
		if trimmed == "```" {
			blocks++
			block = strings.TrimSpace(strings.Join(current, "\n"))
			inBlock = false
			continue
		}
		current = append(current, line)
	}
	if inBlock {
		return "", errors.New("tao-review-json fence is not closed")
	}
	if blocks != 1 {
		return "", fmt.Errorf("expected exactly one tao-review-json block, got %d", blocks)
	}
	if len(block) > singleIntegrationReviewJSONLimit {
		return "", fmt.Errorf("tao-review-json exceeds %d bytes", singleIntegrationReviewJSONLimit)
	}
	return block, nil
}

func (r GuardedSingleIntegrationReviewer) validateExactBoundary(ctx context.Context, request SingleReviewRequest, branches []string) error {
	if err := validateSingleResolutionRoot(r.Git, request.IntegrationRoot); err != nil {
		return err
	}
	currentBranch, err := r.Git.CurrentBranch(ctx)
	if err != nil || strings.TrimSpace(currentBranch) != request.Intent.DefaultBranch {
		return fmt.Errorf("integration worktree is not on default branch %s", request.Intent.DefaultBranch)
	}
	head, err := r.Git.RevParse(ctx, "HEAD")
	if err != nil || strings.TrimSpace(head) != request.Intent.Resolution.IntegrationHead {
		return fmt.Errorf("integration HEAD does not match committed resolution")
	}
	exact, err := inspectExactResolutionCommit(ctx, r.Git, request.Intent, strings.TrimSpace(head))
	if err != nil || !exact {
		return fmt.Errorf("integration commit does not match durable parent and message")
	}
	status, err := r.Git.StatusPorcelain(ctx)
	if err != nil || strings.TrimSpace(status) != "" {
		return fmt.Errorf("integration worktree is not clean")
	}
	refs, err := snapshotProtectedRefs(ctx, r.Git, branches)
	if err != nil {
		return err
	}
	if refs["refs/heads/"+request.Intent.DefaultBranch] != request.Intent.Resolution.IntegrationHead {
		return errors.New("default ref does not match committed resolution")
	}
	sourceBranch := strings.TrimSpace(request.SourceBranch)
	if refs["refs/heads/"+sourceBranch] != request.Intent.SourceHead {
		return errors.New("source ref does not match durable source head")
	}
	return nil
}

func singleReviewProtectedBranches(request SingleReviewRequest) ([]string, error) {
	source, err := cleanProtectedBranch(request.SourceBranch)
	if err != nil {
		return nil, err
	}
	branches := []string{request.Intent.DefaultBranch, source}
	for _, branch := range request.ProtectedBranches {
		clean, cleanErr := cleanProtectedBranch(branch)
		if cleanErr != nil {
			return nil, cleanErr
		}
		branches = append(branches, clean)
	}
	slices.Sort(branches)
	return slices.Compact(branches), nil
}

func (r GuardedSingleIntegrationReviewer) persistNonApproval(result SingleReviewResult, intent plan.SingleMergeCommitIntent, outcome SingleReviewOutcome, summary string, cause error) (SingleReviewResult, error) {
	result.Outcome = outcome
	review := plan.SingleMergeResolutionReview{
		Status: plan.ReviewStatusCompleted, Verdict: plan.ReviewVerdictComment,
		Summary: boundSingleReviewText(summary), Findings: []plan.ReviewFinding{},
		Base: intent.DefaultParent, Head: intent.Resolution.IntegrationHead,
		Agent: boundedReviewAgent(result.Provider.Provider.AgentLabel), ReviewedAt: r.timestamp(intent.Resolution.CommittedAt),
	}
	if err := r.persistReview(&result, intent, review); err != nil {
		return result, errors.Join(cause, err)
	}
	return result, errors.Join(fmt.Errorf("%w: %s", ErrSingleReviewNotApproved, outcome), cause)
}

func (r GuardedSingleIntegrationReviewer) persistReview(result *SingleReviewResult, intent plan.SingleMergeCommitIntent, review plan.SingleMergeResolutionReview) error {
	reviewed := *intent.Resolution
	reviewed.Phase = plan.SingleMergeResolutionPhaseReviewed
	reviewed.Review = &review
	if err := r.Recorder.AdvanceSingleMergeResolution(intent, reviewed); err != nil {
		return fmt.Errorf("persist independent integration review: %w", err)
	}
	result.Intent.Resolution = &reviewed
	return nil
}

func outcomeForPersistedReview(review *plan.SingleMergeResolutionReview) SingleReviewOutcome {
	if review == nil {
		return SingleReviewOutcomeMalformed
	}
	for prefix, outcome := range map[string]SingleReviewOutcome{
		"The integration worktree, HEAD, ignored paths, or Git metadata":   SingleReviewOutcomeMutation,
		"Reviewer mutated the integration worktree":                        SingleReviewOutcomeMutation,
		"Independent integration review timed out":                         SingleReviewOutcomeTimeout,
		"Independent integration review provider failed":                   SingleReviewOutcomeProviderFailure,
		"Independent integration reviewer returned malformed or oversized": SingleReviewOutcomeMalformed,
	} {
		if strings.HasPrefix(review.Summary, prefix) {
			return outcome
		}
	}
	switch review.Verdict {
	case plan.ReviewVerdictApprove:
		return SingleReviewOutcomeApprove
	case plan.ReviewVerdictChangesRequested:
		return SingleReviewOutcomeChangesRequested
	default:
		return SingleReviewOutcomeComment
	}
}

func boundedReviewAgent(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(value, "\r", " "), "\n", " "))
	value = strings.TrimSpace(boundRunes(value, 128))
	if value == "" {
		return "unknown"
	}
	return value
}

func boundedSingleReviewFindings(findings []plan.ReviewFinding) []plan.ReviewFinding {
	if len(findings) > 50 {
		findings = findings[:50]
	}
	bounded := make([]plan.ReviewFinding, 0, len(findings))
	for _, finding := range findings {
		finding.Severity = strings.TrimSpace(boundRunes(strings.ReplaceAll(finding.Severity, "\r", ""), 64))
		finding.File = strings.TrimSpace(boundRunes(strings.ReplaceAll(finding.File, "\r", ""), 512))
		finding.Message = strings.TrimSpace(boundRunes(strings.ReplaceAll(finding.Message, "\r", ""), 4*1024))
		finding.Suggestion = strings.TrimSpace(boundRunes(strings.ReplaceAll(finding.Suggestion, "\r", ""), 4*1024))
		if finding.Line < 0 {
			finding.Line = 0
		}
		bounded = append(bounded, finding)
	}
	return bounded
}

func boundSingleReviewText(value string) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\r", ""))
	if value == "" {
		value = "Independent integration review did not authorize this merge."
	}
	return boundRunes(value, 8*1024)
}

func boundRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func (r GuardedSingleIntegrationReviewer) timestamp(notBefore time.Time) time.Time {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	now = now.UTC()
	if now.Before(notBefore) {
		return notBefore.UTC()
	}
	return now
}
