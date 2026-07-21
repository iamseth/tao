package merge

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/rework"
	runpkg "github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/prompts"
)

const (
	defaultBatchReviewMaxAttempts       = runtimeconfig.DefaultMaxReworkAttempts
	envAggregateReviewConvergenceWindow = "TAO_AGGREGATE_REVIEW_CONVERGENCE_WINDOW"
)

// BatchReviewStore persists normalized transitions and full aggregate output.
type BatchReviewStore interface {
	BatchTransitionStore
	WriteAggregateReview(id string, attempt int, output string) (string, error)
}

// BatchReviewer is the aggregate-review orchestration boundary.
type BatchReviewer interface {
	Review(context.Context, BatchState, string, BatchReviewOptions) (BatchReviewResult, error)
}

type BatchReviewOptions struct {
	VerifyCommand string
	MaxAttempts   int
	AutoEject     bool
}

type BatchReviewResult struct {
	State         BatchState
	ReenterPhases bool
}

// BatchAggregateReviewer verifies and reviews the exact combined diff. Agents
// may edit during requested rework, but Tao alone stages and commits those edits.
type BatchAggregateReviewer struct {
	Store   BatchReviewStore
	Service Service
	Agent   BatchResolutionAgent
	Now     func() time.Time
}

func (r BatchAggregateReviewer) Review(ctx context.Context, state BatchState, integrationRoot string, options BatchReviewOptions) (BatchReviewResult, error) {
	result := BatchReviewResult{State: state}
	if r.Store == nil || r.Agent == nil {
		return result, errors.New("batch review store and agent are required")
	}
	if state.Ejection != nil && state.Ejection.Status != batchEjectionCompleted {
		rebuilt, ejectErr := (BatchIntegrator{Store: r.Store, Service: r.Service, Now: r.Now}).Eject(ctx, state, integrationRoot, BatchEjectOptions{VerifyCommand: options.VerifyCommand})
		result.State = rebuilt.State
		if ejectErr != nil {
			return result, ejectErr
		}
		if rebuilt.State.Status == BatchStatusResolving {
			result.ReenterPhases = true
			return result, nil
		}
		if rebuilt.State.Status != BatchStatusReviewing {
			return result, fmt.Errorf("reduced batch requires reviewing status, got %s", rebuilt.State.Status)
		}
		state = rebuilt.State
	}
	if state.Status != BatchStatusReviewing {
		return result, fmt.Errorf("aggregate review requires reviewing status, got %s", state.Status)
	}
	git, err := r.Service.gitClientForRoot(integrationRoot)
	if err != nil {
		return result, err
	}
	maxAttempts, err := batchReviewMaxAttempts(options.MaxAttempts)
	if err != nil {
		return r.block(result, state, BatchBlockKindResumable, err.Error())
	}
	convergenceWindow, err := batchReviewConvergenceWindow()
	if err != nil {
		return r.block(result, state, BatchBlockKindResumable, err.Error())
	}
	verify := resolveMergeVerifyCommandAtRoot(integrationRoot, Options{VerifyCommand: options.VerifyCommand})
	if strings.TrimSpace(verify.command) == "" {
		return r.block(result, state, BatchBlockKindResumable, "aggregate review requires a full verification command")
	}
	state, err = r.recoverAggregateRework(ctx, git, state)
	if err != nil {
		return result, err
	}

	for {
		head, revErr := git.RevParse(ctx, "HEAD")
		if revErr != nil {
			return r.block(result, state, BatchBlockKindResumable, "capture aggregate review head: "+revErr.Error())
		}
		head = strings.TrimSpace(head)
		state.IntegrationHead = head
		output := ""
		if state.Review == nil || state.Review.Status != "reworking" {
			started := r.timestamp()
			state.Verification = &BatchVerification{Command: verify.command, HeadSHA: head, StartedAt: started}
			state, err = r.persist(state)
			if err != nil {
				return result, fmt.Errorf("persist aggregate verification intent: %w", err)
			}
			var verifyErr error
			output, verifyErr = r.Service.runMergeVerifyAtRoot(ctx, integrationRoot, verify.command)
			state.Verification.CompletedAt = r.timestamp()
			state.Verification.Output = output
			state.Verification.Passed = verifyErr == nil
			if verifyErr != nil {
				state.Verification.Error = verifyErr.Error()
				state, err = r.persist(state)
				if err != nil {
					return result, fmt.Errorf("persist aggregate verification failure: %w", err)
				}
				return r.block(result, state, BatchBlockKindResumable, "aggregate verification failed: "+verifyErr.Error())
			}
			state, err = r.persist(state)
			if err != nil {
				return result, fmt.Errorf("persist aggregate verification success: %w", err)
			}

			prompt, promptErr := r.renderPrompt(ctx, git, state, verify.command, output)
			if promptErr != nil {
				return r.block(result, state, BatchBlockKindResumable, promptErr.Error())
			}
			reviewAttempt := 1
			if state.Review != nil {
				reviewAttempt = state.Review.Attempts + 1
			}
			beforeReviewStatus, _ := git.StatusPorcelain(ctx)
			beforeReviewRefs, refsErr := snapshotBatchProtectedRefs(ctx, git, state)
			if refsErr != nil {
				return r.block(result, state, BatchBlockKindResumable, "capture protected refs before aggregate review: "+refsErr.Error())
			}
			reviewOutput, reviewErr := r.Agent.Resolve(ctx, integrationRoot, prompt)
			afterReviewStatus, statusErr := git.StatusPorcelain(ctx)
			afterReviewHead, headErr := git.RevParse(ctx, "HEAD")
			refsErr = compareBatchProtectedRefs(ctx, git, beforeReviewRefs)
			if statusErr != nil || headErr != nil || refsErr != nil || afterReviewStatus != beforeReviewStatus || strings.TrimSpace(afterReviewHead) != head {
				return r.blockResumableAfterRestore(ctx, result, state, git, head, "aggregate review agent modified the integration workspace or protected refs")
			}
			if reviewErr != nil {
				state.Review = &BatchReview{Status: "error", BaseSHA: state.DefaultStartSHA, HeadSHA: head, Attempts: reviewAttempt, CompletedAt: r.timestamp()}
				state, err = r.persist(state)
				if err != nil {
					return result, errors.Join(reviewErr, err)
				}
				return r.block(result, state, BatchBlockKindResumable, "aggregate review failed: "+reviewErr.Error())
			}
			artifactSequence := max(state.AggregateReviewSequence+1, reviewAttempt)
			artifact, artifactErr := r.Store.WriteAggregateReview(state.ID, artifactSequence, reviewOutput)
			if artifactErr != nil {
				return r.block(result, state, BatchBlockKindResumable, "persist aggregate review output: "+artifactErr.Error())
			}
			parsed := runpkg.ParseReviewOutput(reviewOutput)
			fingerprint := ""
			if parsed.Verdict == plan.ReviewVerdictChangesRequested {
				fingerprint = rework.FindingsFingerprint(parsed.Findings)
			}
			previousFingerprint := state.Attempts.ReviewFingerprint
			previousRoundHead := ""
			if history := state.Attempts.ReviewHistory; len(history) > 0 {
				previousRoundHead = history[len(history)-1].HeadSHA
			}
			resolutionSHAs := []string(nil)
			if state.Review != nil {
				resolutionSHAs = append(resolutionSHAs, state.Review.ResolutionSHAs...)
			}
			state.Review = &BatchReview{Status: "completed", Verdict: parsed.Verdict, Summary: parsed.Summary, Findings: parsed.Findings, BaseSHA: state.DefaultStartSHA, HeadSHA: head, Fingerprint: fingerprint, Attempts: reviewAttempt, Artifact: artifact, ResolutionSHAs: resolutionSHAs, CompletedAt: r.timestamp()}
			state.AggregateReviewSequence = artifactSequence
			state.Attempts.ReviewFingerprint = fingerprint
			convergence := aggregateReviewConvergence{}
			if parsed.Verdict == plan.ReviewVerdictChangesRequested {
				state.Attempts.ReviewHistory, convergence = updateAggregateReviewHistory(state.Attempts.ReviewHistory, head, parsed.Findings, convergenceWindow)
			} else {
				// These fields are explicitly emitted in JSON so a later merge-write
				// cannot retain findings from a completed convergence sequence.
				state.Attempts.ReviewHistory = nil
			}
			state, err = r.persist(state)
			if err != nil {
				return result, fmt.Errorf("persist aggregate review metadata: %w", err)
			}

			switch parsed.Verdict {
			case plan.ReviewVerdictApprove:
				state.NonConvergence = nil
				state.Status, state.BlockedReason, state.BlockKind, state.ResumeStatus = BatchStatusReadyToLand, "", "", ""
				state, err = r.persist(state)
				result.State = state
				return result, err
			case plan.ReviewVerdictComment:
				return r.block(result, state, BatchBlockKindTerminal, "aggregate review returned comment; explicit approval required")
			case plan.ReviewVerdictChangesRequested:
				if len(parsed.Findings) == 0 {
					return r.block(result, state, BatchBlockKindTerminal, "aggregate review requested changes without actionable findings")
				}
				legacyEquivalentFingerprint := previousRoundHead != head && previousFingerprint != "" && previousFingerprint == fingerprint
				if legacyEquivalentFingerprint || latestDistinctReviewFingerprintsEquivalent(state.Attempts.ReviewHistory) {
					return r.block(result, state, BatchBlockKindTerminal, "aggregate review stalled on equivalent findings")
				}
				if convergence.NotConverging {
					planID := ""
					if convergence.AllFindingsHaveFiles {
						planID = attributeAggregateReviewFiles(ctx, git, effectiveBatchCandidates(state), convergence.Files)
					}
					reason := aggregateReviewNonConvergenceReason(convergence.Files, planID)
					state.NonConvergence = &BatchNonConvergence{Files: append([]string(nil), convergence.Files...), PlanID: planID, Reason: reason}
					if !options.AutoEject || state.Ejection != nil || planID == "" || len(effectiveBatchCandidates(state)) <= 1 {
						return r.block(result, state, BatchBlockKindTerminal, reason)
					}
					rebuilt, ejectErr := (BatchIntegrator{Store: r.Store, Service: r.Service, Now: r.Now}).Eject(ctx, state, integrationRoot, BatchEjectOptions{PlanID: planID, Reason: reason, VerifyCommand: options.VerifyCommand})
					result.State = rebuilt.State
					if ejectErr != nil {
						return result, ejectErr
					}
					if rebuilt.State.Status == BatchStatusResolving {
						result.ReenterPhases = true
						return result, nil
					}
					if rebuilt.State.Status != BatchStatusReviewing {
						return result, fmt.Errorf("reduced batch requires reviewing status, got %s", rebuilt.State.Status)
					}
					state = rebuilt.State
					continue
				}
				if state.Attempts.AggregateRework >= maxAttempts {
					return r.block(result, state, BatchBlockKindTerminal, "aggregate rework attempt cap exhausted")
				}
			default:
				return r.block(result, state, BatchBlockKindTerminal, "aggregate review returned an unsupported verdict")
			}

			state.Attempts.AggregateRework++
			state.Review.Status = "reworking"
			state, err = r.persist(state) // durable intent and exact base precede agent mutation
			if err != nil {
				return result, fmt.Errorf("persist aggregate rework intent: %w", err)
			}
		} else if state.Verification != nil {
			output = state.Verification.Output
		}
		beforeReworkRefs, refsErr := snapshotBatchProtectedRefs(ctx, git, state)
		if refsErr != nil {
			return r.block(result, state, BatchBlockKindResumable, "capture protected refs before aggregate rework: "+refsErr.Error())
		}
		beforeHead := head
		reworkPrompt, renderErr := prompts.RenderMergeResolve(prompts.MergeResolveData{
			BatchID: state.ID, PlanID: "aggregate-review", SourceHead: head, IntegrationBase: state.DefaultStartSHA,
			VerifyCommand: verify.command, SourceReview: formatFindings(state.Review.Findings), Diff: state.DefaultStartSHA + ".." + head,
			PriorPlans: strings.Join(state.ChosenOrder, "\n"), VerificationOutput: output,
		})
		if renderErr != nil {
			return r.block(result, state, BatchBlockKindResumable, renderErr.Error())
		}
		summary, agentErr := r.Agent.Resolve(ctx, integrationRoot, reworkPrompt)
		changes, statusErr := concretePorcelainChanges(ctx, git)
		afterHead, headErr := git.RevParse(ctx, "HEAD")
		refsErr = compareBatchProtectedRefs(ctx, git, beforeReworkRefs)
		if headErr != nil || refsErr != nil || strings.TrimSpace(afterHead) != beforeHead {
			return r.blockResumableAfterRestore(ctx, result, state, git, beforeHead, "aggregate rework agent changed protected Git refs")
		}
		if statusErr != nil {
			return r.blockResumableAfterRestore(ctx, result, state, git, beforeHead, "inspect aggregate rework: "+statusErr.Error())
		}
		if agentErr != nil {
			return r.blockResumableAfterRestore(ctx, result, state, git, beforeHead, "aggregate rework agent failed: "+agentErr.Error())
		}
		validation := validateAgentEdits(integrationRoot, changes.changedPaths, changes.markerScanPaths)
		switch validation.issue {
		case agentEditIssueConflictMarkers:
			reason := "aggregate rework agent left conflict markers in " + strings.Join(validation.markerPaths, ", ")
			return r.blockResumableAfterRestore(ctx, result, state, git, beforeHead, reason)
		case agentEditIssueUnscannablePaths:
			reason := "aggregate rework agent edits could not be safely scanned: " + validation.scanErr.Error()
			return r.blockResumableAfterRestore(ctx, result, state, git, beforeHead, reason)
		}
		if strings.TrimSpace(summary) == "" || validation.issue == agentEditIssueNoChanges || validation.issue == agentEditIssueUnsafePaths {
			return r.blockResumableAfterRestore(ctx, result, state, git, beforeHead, "aggregate rework agent returned no safe edits")
		}
		stage, ok := git.(interface {
			Add(context.Context, ...string) error
		})
		if !ok {
			return r.blockResumableAfterRestore(ctx, result, state, git, beforeHead, "Git client cannot stage aggregate rework")
		}
		if err := stage.Add(ctx, "."); err != nil {
			return r.blockResumableAfterRestore(ctx, result, state, git, beforeHead, "stage aggregate rework: "+err.Error())
		}
		// The applying review is write-ahead intent for the deterministic commit.
		// IntegrationHead deliberately remains its parent until settlement.
		state.Review.Status = "applying"
		state, err = r.persist(state)
		if err != nil {
			return result, fmt.Errorf("persist aggregate rework commit intent: %w", err)
		}
		if err := git.Commit(ctx, aggregateResolutionCommitMessage(state.ID, state.Attempts.AggregateRework)); err != nil {
			return r.block(result, state, BatchBlockKindResumable, "commit aggregate rework: "+err.Error())
		}
		newHead, revErr := git.RevParse(ctx, "HEAD")
		if revErr != nil {
			return r.block(result, state, BatchBlockKindResumable, "capture aggregate rework commit: "+revErr.Error())
		}
		state, err = r.settleAggregateRework(state, strings.TrimSpace(newHead))
		if err != nil {
			return result, err
		}
	}
}

func (r BatchAggregateReviewer) recoverAggregateRework(ctx context.Context, git GitClient, state BatchState) (BatchState, error) {
	if state.Review == nil || (state.Review.Status != "reworking" && state.Review.Status != "applying") {
		return state, nil
	}
	currentHead, err := git.RevParse(ctx, "HEAD")
	if err != nil {
		return state, fmt.Errorf("recover aggregate rework commit: %w", err)
	}
	currentHead = strings.TrimSpace(currentHead)
	if state.Review.Status == "applying" && currentHead != state.IntegrationHead {
		matched, matchErr := aggregateReworkCommitMatches(ctx, git, state, "HEAD")
		if matchErr != nil {
			return state, matchErr
		}
		if matched {
			return r.settleAggregateRework(state, currentHead)
		}
	}
	if err := restoreBatchIntegration(ctx, git, state.IntegrationHead); err != nil {
		return state, fmt.Errorf("restore interrupted aggregate rework: %w", err)
	}
	state.Review.Status = "reworking"
	persisted, err := r.persist(state)
	if err != nil {
		return state, fmt.Errorf("persist recovered aggregate rework: %w", err)
	}
	return persisted, nil
}

func (r BatchAggregateReviewer) settleAggregateRework(state BatchState, head string) (BatchState, error) {
	resolutionSHAs := append([]string(nil), state.Review.ResolutionSHAs...)
	if len(resolutionSHAs) == 0 || resolutionSHAs[len(resolutionSHAs)-1] != head {
		resolutionSHAs = append(resolutionSHAs, head)
	}
	state.IntegrationHead = head
	state.Verification = nil
	state.Review = &BatchReview{
		Status:         "pending",
		BaseSHA:        state.DefaultStartSHA,
		HeadSHA:        head,
		Attempts:       state.Review.Attempts,
		ResolutionSHAs: resolutionSHAs,
	}
	persisted, err := r.persist(state)
	if err != nil {
		return state, fmt.Errorf("persist aggregate rework commit: %w", err)
	}
	return persisted, nil
}

func aggregateReworkCommitMatches(ctx context.Context, git GitClient, state BatchState, revision string) (bool, error) {
	if state.Review == nil || state.Review.Status != "applying" || state.Attempts.AggregateRework <= 0 {
		return false, nil
	}
	parent, err := git.RevParse(ctx, revision+"^")
	if err != nil {
		return false, fmt.Errorf("recover aggregate rework commit: inspect commit parent: %w", err)
	}
	message, err := git.CommitMessage(ctx, revision)
	if err != nil {
		return false, fmt.Errorf("recover aggregate rework commit: inspect commit message: %w", err)
	}
	return strings.TrimSpace(parent) == state.IntegrationHead && strings.TrimSpace(message) == strings.TrimSpace(aggregateResolutionCommitMessage(state.ID, state.Attempts.AggregateRework)), nil
}

func (r BatchAggregateReviewer) renderPrompt(ctx context.Context, git GitClient, state BatchState, command, verification string) (string, error) {
	stat := state.DefaultStartSHA + ".." + state.IntegrationHead
	if client, ok := git.(interface {
		DiffStat(context.Context, string) (string, error)
	}); ok {
		if value, err := client.DiffStat(ctx, stat); err == nil {
			stat = value
		}
	}
	var candidates []string
	for _, candidate := range effectiveBatchCandidates(state) {
		candidates = append(candidates, fmt.Sprintf("%s source=%s review=%s..%s summary=%s", candidate.PlanID, candidate.SourceTip, candidate.ReviewBase, candidate.ReviewHead, candidate.ReviewSummary))
	}
	var resolutions []string
	for _, integration := range state.Integrations {
		if len(integration.Resolutions) > 0 {
			resolutions = append(resolutions, integration.PlanID+" "+integration.IntegrationSHA)
		}
	}
	if state.Review != nil {
		resolutions = append(resolutions, state.Review.ResolutionSHAs...)
	}
	return prompts.RenderMergeReview(prompts.MergeReviewData{BatchID: state.ID, DefaultStart: state.DefaultStartSHA, IntegrationHead: state.IntegrationHead, VerifyCommand: command, Candidates: strings.Join(candidates, "\n"), ResolutionCommits: strings.Join(resolutions, "\n"), DiffStat: stat, Verification: verification})
}

func (r BatchAggregateReviewer) persist(state BatchState) (BatchState, error) {
	state.UpdatedAt = r.timestamp()
	return r.Store.Transition(state, state.UpdatedAt)
}

func (r BatchAggregateReviewer) blockResumableAfterRestore(ctx context.Context, result BatchReviewResult, state BatchState, git GitClient, head, reason string) (BatchReviewResult, error) {
	if err := restoreBatchIntegration(ctx, git, head); err != nil {
		reason = errors.Join(errors.New(reason), fmt.Errorf("restore integration workspace to %s: %w", head, err)).Error()
	}
	return r.block(result, state, BatchBlockKindResumable, reason)
}

func (r BatchAggregateReviewer) block(result BatchReviewResult, state BatchState, kind BatchBlockKind, reason string) (BatchReviewResult, error) {
	BlockBatch(&state, kind, reason)
	persisted, err := r.persist(state)
	result.State = persisted
	if err != nil {
		return result, errors.Join(errors.New(reason), err)
	}
	return result, errors.New(reason)
}

func (r BatchAggregateReviewer) timestamp() string {
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	return now.UTC().Format(time.RFC3339Nano)
}

type aggregateReviewConvergence struct {
	NotConverging        bool
	Files                []string
	AllFindingsHaveFiles bool
}

func updateAggregateReviewHistory(history []BatchReviewRound, head string, findings []plan.ReviewFinding, window int) ([]BatchReviewRound, aggregateReviewConvergence) {
	files := make([]string, 0, len(findings))
	seen := make(map[string]bool, len(findings))
	allFindingsHaveFiles := true
	for _, finding := range findings {
		file := normalizeReviewFile(finding.File)
		if file == "" {
			allFindingsHaveFiles = false
			continue
		}
		if !seen[file] {
			seen[file] = true
			files = append(files, file)
		}
	}
	slices.Sort(files)
	round := BatchReviewRound{HeadSHA: head, Fingerprint: rework.FindingsFingerprint(findings), FindingFiles: files, FindingCount: len(findings), AllFindingsHaveFiles: allFindingsHaveFiles}
	history = append([]BatchReviewRound(nil), history...)
	if head != "" && len(history) > 0 && history[len(history)-1].HeadSHA == head {
		// A completed review may be durable before its rework or ejection intent.
		// Retrying that unchanged head replaces the interrupted observation rather
		// than manufacturing another convergence round.
		history[len(history)-1] = round
	} else {
		history = append(history, round)
	}
	if len(history) > window {
		history = append([]BatchReviewRound(nil), history[len(history)-window:]...)
	}
	if len(history) < window {
		return history, aggregateReviewConvergence{}
	}

	recurring := append([]string(nil), history[0].FindingFiles...)
	strictlyDecreasing := true
	windowFindingsHaveFiles := history[0].AllFindingsHaveFiles
	for i := 1; i < len(history); i++ {
		recurring = intersectReviewFiles(recurring, history[i].FindingFiles)
		if history[i].FindingCount >= history[i-1].FindingCount {
			strictlyDecreasing = false
		}
		windowFindingsHaveFiles = windowFindingsHaveFiles && history[i].AllFindingsHaveFiles
	}
	if len(recurring) > 0 {
		return history, aggregateReviewConvergence{NotConverging: true, Files: recurring, AllFindingsHaveFiles: windowFindingsHaveFiles}
	}
	if !strictlyDecreasing {
		return history, aggregateReviewConvergence{NotConverging: true, Files: unionReviewFiles(history), AllFindingsHaveFiles: windowFindingsHaveFiles}
	}
	return history, aggregateReviewConvergence{}
}

func latestDistinctReviewFingerprintsEquivalent(history []BatchReviewRound) bool {
	if len(history) < 2 {
		return false
	}
	latest := history[len(history)-1]
	if latest.HeadSHA == "" || latest.Fingerprint == "" {
		return false
	}
	for i := len(history) - 2; i >= 0; i-- {
		if history[i].HeadSHA == latest.HeadSHA {
			continue
		}
		return history[i].Fingerprint != "" && history[i].Fingerprint == latest.Fingerprint
	}
	return false
}

func unionReviewFiles(history []BatchReviewRound) []string {
	seen := make(map[string]bool)
	var result []string
	for _, round := range history {
		for _, file := range round.FindingFiles {
			if !seen[file] {
				seen[file] = true
				result = append(result, file)
			}
		}
	}
	slices.Sort(result)
	return result
}

func intersectReviewFiles(left, right []string) []string {
	included := make(map[string]bool, len(right))
	for _, file := range right {
		included[file] = true
	}
	result := make([]string, 0, len(left))
	for _, file := range left {
		if included[file] {
			result = append(result, file)
		}
	}
	return result
}

func normalizeReviewFile(file string) string {
	file = strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
	file = strings.TrimPrefix(file, "./")
	if file == "" || file == "." {
		return ""
	}
	return path.Clean(file)
}

func attributeAggregateReviewFiles(ctx context.Context, git GitClient, candidates []BatchCandidate, files []string) string {
	if len(files) == 0 {
		return ""
	}
	attributed := ""
	for _, file := range files {
		matches := make([]string, 0, 1)
		for _, candidate := range candidates {
			if strings.TrimSpace(candidate.ReviewBase) == "" || strings.TrimSpace(candidate.SourceTip) == "" {
				return ""
			}
			changed, err := git.ChangedFiles(ctx, candidate.ReviewBase+".."+candidate.SourceTip)
			if err != nil {
				return ""
			}
			if slices.ContainsFunc(changed, func(changedFile string) bool { return normalizeReviewFile(changedFile) == file }) {
				matches = append(matches, candidate.PlanID)
			}
		}
		if len(matches) != 1 || (attributed != "" && attributed != matches[0]) {
			return ""
		}
		attributed = matches[0]
	}
	return attributed
}

func aggregateReviewNonConvergenceReason(files []string, planID string) string {
	reason := "aggregate review not converging"
	if len(files) > 0 {
		reason += " on " + strings.Join(files, ", ")
	}
	if planID != "" {
		reason += " (plan " + planID + ")"
	}
	return reason
}

func batchReviewConvergenceWindow() (int, error) {
	value := runtimeconfig.DefaultAggregateReviewConvergenceWindow
	if raw, ok := os.LookupEnv(envAggregateReviewConvergenceWindow); ok && strings.TrimSpace(raw) != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 2 {
			return 0, fmt.Errorf("%s must be an integer of at least 2", envAggregateReviewConvergenceWindow)
		}
		value = parsed
	}
	return value, nil
}

func batchReviewMaxAttempts(value int) (int, error) {
	if value > 0 {
		return value, nil
	}
	_, maxAttempts, err := runtimeconfig.ParseAutoReworkEnv(
		false,
		defaultBatchReviewMaxAttempts,
		"",
		os.Getenv(runtimeconfig.EnvMaxReworkAttempts),
	)
	return maxAttempts, err
}

func formatFindings(findings []plan.ReviewFinding) string {
	var lines []string
	for _, finding := range findings {
		lines = append(lines, fmt.Sprintf("%s %s:%d %s; suggestion: %s", finding.Severity, finding.File, finding.Line, finding.Message, finding.Suggestion))
	}
	return strings.Join(lines, "\n")
}

func aggregateResolutionCommitMessage(batchID string, attempt int) string {
	return fmt.Sprintf("fix: resolve aggregate merge review\n\nTao-Merge-Batch: %s\nTao-Review-Attempt: %d", batchID, attempt)
}
