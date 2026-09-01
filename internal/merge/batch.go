package merge

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
)

// BatchBlocker describes a candidate that could not pass the immutable batch
// snapshot or planning phase. Blockers are data, rather than fail-fast errors,
// so callers can report the complete set to the user.
type BatchBlocker struct {
	PlanID string `json:"plan_id"`
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
}

// BatchDeferral describes a valid candidate intentionally left out of the
// proposed low-conflict order.
type BatchDeferral struct {
	PlanID       string `json:"plan_id"`
	Reason       string `json:"reason"`
	OverlapCount int    `json:"overlap_count"`
}

// BatchCandidate is the invocation-time snapshot used by later batch phases.
// No later phase needs to reinterpret mutable review or branch metadata to know
// what was approved during preflight.
type BatchCandidate struct {
	PlanID                string                    `json:"plan_id"`
	PlanTitle             string                    `json:"plan_title,omitempty"`
	PlanDir               string                    `json:"plan_dir"`
	RepoRoot              string                    `json:"repo_root"`
	Branch                string                    `json:"branch"`
	ReviewBase            string                    `json:"review_base"`
	ReviewHead            string                    `json:"review_head"`
	ReviewSummary         string                    `json:"review_summary,omitempty"`
	ReviewCommitMessage   *plan.ReviewCommitMessage `json:"review_commit_message,omitempty"`
	CommitMessage         string                    `json:"commit_message,omitempty"`
	CommitMessageResolved bool                      `json:"commit_message_resolved,omitempty"`
	SourceTip             string                    `json:"source_tip"`
	DefaultBranch         string                    `json:"default_branch"`
	DefaultStartSHA       string                    `json:"default_start_sha"`
	Blockers              []BatchBlocker            `json:"blockers,omitempty"`
	Deferred              *BatchDeferral            `json:"deferred,omitempty"`
}

// BatchPreflightResult contains every invocation-time reviewed and approved
// plan, including candidates with blockers.
type BatchPreflightResult struct {
	Candidates      []BatchCandidate
	Blockers        []BatchBlocker
	DefaultBranch   string
	DefaultStartSHA string
	RepoRoot        string
}

// BatchPlanRepository is the read-only plan boundary used by batch discovery.
type BatchPlanRepository interface {
	plan.Repository
	plan.Resolver
}

// BatchHealthCheck is injectable so discovery remains deterministic in tests.
type BatchHealthCheck func(context.Context, plan.Repo) error

// BatchCandidateDiscovery discovers and strictly preflights the unmerged,
// reviewed and approved set visible at invocation time. It never mutates plan
// or Git state.
type BatchCandidateDiscovery struct {
	Repository BatchPlanRepository
	Merge      Service
	Health     BatchHealthCheck
}

func (d BatchCandidateDiscovery) Discover(ctx context.Context) (BatchPreflightResult, error) {
	var result BatchPreflightResult
	if d.Repository == nil {
		return result, fmt.Errorf("batch plan repository is nil")
	}
	summaries, err := d.Repository.ListPlans(ctx, plan.PlanFilter{})
	if err != nil {
		return result, fmt.Errorf("list batch plans: %w", err)
	}
	selected := make([]plan.PlanSummary, 0, len(summaries))
	for _, summary := range summaries {
		if summary.Status == plan.StatusAbandoned || !summary.Reviewed || summary.ReviewVerdict != plan.ReviewVerdictApprove {
			continue
		}
		// Legacy completed summaries have no PR metadata and retain their
		// historical exclusion. A PR-complete summary must be resolved below so
		// completed is not mistaken for proof of integration.
		if summary.Status == plan.StatusCompleted && summary.PullRequest == nil {
			continue
		}
		selected = append(selected, summary)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].ID < selected[j].ID })
	if len(selected) == 0 {
		return result, nil
	}
	git, err := d.Merge.gitClient()
	if err != nil {
		return result, err
	}

	for _, summary := range selected {
		candidate := BatchCandidate{PlanID: summary.ID, PlanDir: summary.Dir}
		detail, resolveErr := d.Repository.ResolvePlan(ctx, summary.ID)
		if resolveErr != nil {
			d.addBlocker(&result, &candidate, "resolve", resolveErr)
			result.Candidates = append(result.Candidates, candidate)
			continue
		}
		// An abandoned detail is excluded even when a stale summary still carries
		// old approval or completed-slice projections.
		if detail.State.Status == plan.StatusAbandoned {
			continue
		}
		// PR completion projects completed before integration, so lifecycle
		// status is not merge evidence. Current plan_merged evidence excludes an
		// integrated plan; the second condition preserves the compatibility
		// exclusion for legacy completed artifacts without matching PR evidence.
		if plan.PlanIsMerged(detail.Events) || (summary.Status == plan.StatusCompleted && !plan.PlanIsPullRequestComplete(detail)) {
			continue
		}
		candidate.PlanID = detail.State.Plan.ID
		candidate.PlanTitle = strings.TrimSpace(detail.State.Plan.Title)
		candidate.PlanDir = detail.Dir
		candidate.RepoRoot = strings.TrimSpace(detail.State.Repo.Root)
		if review := plan.PersistedReview(detail); review != nil {
			candidate.ReviewBase = strings.TrimSpace(review.Base)
			candidate.ReviewHead = strings.TrimSpace(review.Head)
			candidate.ReviewSummary = strings.TrimSpace(review.Summary)
			if review.CommitMessage != nil {
				proposal := *review.CommitMessage
				candidate.ReviewCommitMessage = &proposal
			}
		}
		candidate.Branch, err = resolvePlanBranch(detail)
		if err != nil {
			d.addBlocker(&result, &candidate, "snapshot", err)
		}

		health := d.Health
		if health == nil {
			health = defaultBatchHealthCheck
		}
		if err := health(ctx, detail.State.Repo); err != nil {
			d.addBlocker(&result, &candidate, "repository", err)
		}
		if result.RepoRoot == "" {
			result.RepoRoot = candidate.RepoRoot
		} else if candidate.RepoRoot != result.RepoRoot {
			d.addBlocker(&result, &candidate, "repository", fmt.Errorf("repository root %q does not match batch repository %q", candidate.RepoRoot, result.RepoRoot))
		}

		candidate.DefaultBranch, err = resolveDefaultBranch(ctx, git, detail)
		if err != nil {
			d.addBlocker(&result, &candidate, "snapshot", err)
		} else {
			candidate.DefaultStartSHA, err = git.RevParse(ctx, candidate.DefaultBranch)
			candidate.DefaultStartSHA = strings.TrimSpace(candidate.DefaultStartSHA)
			if err != nil || candidate.DefaultStartSHA == "" {
				if err == nil {
					err = fmt.Errorf("default branch %s resolved to an empty revision", candidate.DefaultBranch)
				}
				d.addBlocker(&result, &candidate, "snapshot", err)
			}
		}
		if result.DefaultBranch == "" && candidate.DefaultBranch != "" {
			result.DefaultBranch = candidate.DefaultBranch
			result.DefaultStartSHA = candidate.DefaultStartSHA
		} else if candidate.DefaultBranch != "" && (candidate.DefaultBranch != result.DefaultBranch || candidate.DefaultStartSHA != result.DefaultStartSHA) {
			d.addBlocker(&result, &candidate, "repository", fmt.Errorf("default snapshot %s@%s does not match batch snapshot %s@%s", candidate.DefaultBranch, candidate.DefaultStartSHA, result.DefaultBranch, result.DefaultStartSHA))
		}

		if candidate.Branch != "" {
			candidate.SourceTip, err = git.RevParse(ctx, candidate.Branch)
			candidate.SourceTip = strings.TrimSpace(candidate.SourceTip)
			if err != nil || candidate.SourceTip == "" {
				if err == nil {
					err = fmt.Errorf("plan branch %s resolved to an empty revision", candidate.Branch)
				}
				d.addBlocker(&result, &candidate, "snapshot", err)
			}
		}
		if err := d.Merge.CheckPreMergeGate(ctx, detail, Options{}); err != nil {
			d.addBlocker(&result, &candidate, "preflight", err)
		}
		if candidate.ReviewCommitMessage != nil {
			message, messageErr := singleMergeCommitMessage(*candidate.ReviewCommitMessage, candidate.PlanID, candidate.SourceTip)
			if messageErr != nil {
				d.addBlocker(&result, &candidate, "message", fmt.Errorf("approved review commit proposal is invalid: %w", messageErr))
			} else {
				candidate.CommitMessage = message
			}
		}
		result.Candidates = append(result.Candidates, candidate)
	}
	return result, nil
}

// ValidateCandidateSnapshot reloads a durable batch snapshot before resume or
// restart so later phases cannot reuse stale approval after abandonment.
func (d BatchCandidateDiscovery) ValidateCandidateSnapshot(ctx context.Context, candidates []BatchCandidate) error {
	if d.Repository == nil {
		return fmt.Errorf("batch plan repository is nil")
	}
	for _, candidate := range candidates {
		input := strings.TrimSpace(candidate.PlanDir)
		if input == "" {
			input = candidate.PlanID
		}
		detail, err := d.Repository.ResolvePlan(ctx, input)
		if err != nil {
			return fmt.Errorf("reload plan %s for batch lifecycle gate: %w", candidate.PlanID, err)
		}
		if err := plan.RequireNotAbandoned(detail); err != nil {
			return fmt.Errorf("plan %s batch lifecycle gate: %w", candidate.PlanID, err)
		}
	}
	return nil
}

func defaultBatchHealthCheck(ctx context.Context, repo plan.Repo) error {
	health := (taodata.RepoHealthChecker{}).Check(ctx, taodata.Repo{Root: repo.Root, Name: repo.Name, Branch: repo.Branch})
	if health.Error {
		return fmt.Errorf("repository is unhealthy [%s]: %s", health.Status, health.Message)
	}
	return nil
}

func (d BatchCandidateDiscovery) addBlocker(result *BatchPreflightResult, candidate *BatchCandidate, stage string, err error) {
	blocker := BatchBlocker{PlanID: candidate.PlanID, Stage: stage, Reason: err.Error()}
	candidate.Blockers = append(candidate.Blockers, blocker)
	result.Blockers = append(result.Blockers, blocker)
}

// BatchSquashSimulation is the non-mutating estimate returned by the planner's
// injected squash seam. Conflicted candidates are deferred.
type BatchSquashSimulation struct {
	OverlapCount int
	Conflicted   bool
	Reason       string
}

// BatchPlanningSeams contains only read-only planning operations.
type BatchPlanningSeams struct {
	IsAncestor     func(context.Context, string, string) (bool, error)
	SimulateSquash func(context.Context, []BatchCandidate, BatchCandidate) (BatchSquashSimulation, error)
}

// BatchPlanningResult is a deterministic proposal, not a claim of globally
// optimal ordering.
type BatchPlanningResult struct {
	Ordered  []BatchCandidate
	Deferred []BatchDeferral
	Blockers []BatchBlocker
}

// PlanBatchCandidates orders eligible candidates while respecting source-tip
// ancestry. Among currently-ready candidates it prefers the simulation with
// fewer overlaps, with plan ID as the stable final tie-breaker.
// PlanBatchCandidatesWithGit connects the pure ordering algorithm to the
// service's read-only Git boundary. Overlap is the number of paths already
// touched by the proposed prefix; actual squash conflicts remain authoritative
// during staged integration.
func (s Service) PlanBatchCandidatesWithGit(ctx context.Context, candidates []BatchCandidate) (BatchPlanningResult, error) {
	git, err := s.gitClient()
	if err != nil {
		return BatchPlanningResult{}, err
	}
	changed := make(map[string]map[string]bool, len(candidates))
	loadChanged := func(candidate BatchCandidate) (map[string]bool, error) {
		if files, ok := changed[candidate.PlanID]; ok {
			return files, nil
		}
		files, err := git.ChangedFiles(ctx, candidate.DefaultStartSHA+".."+candidate.SourceTip)
		if err != nil {
			return nil, err
		}
		set := make(map[string]bool, len(files))
		for _, file := range files {
			set[file] = true
		}
		changed[candidate.PlanID] = set
		return set, nil
	}
	result := PlanBatchCandidates(ctx, candidates, BatchPlanningSeams{
		IsAncestor: git.IsAncestor,
		SimulateSquash: func(_ context.Context, prefix []BatchCandidate, candidate BatchCandidate) (BatchSquashSimulation, error) {
			candidateFiles, err := loadChanged(candidate)
			if err != nil {
				return BatchSquashSimulation{}, err
			}
			prefixFiles := make(map[string]bool)
			for _, prior := range prefix {
				files, err := loadChanged(prior)
				if err != nil {
					return BatchSquashSimulation{}, err
				}
				for file := range files {
					prefixFiles[file] = true
				}
			}
			overlap := 0
			for file := range candidateFiles {
				if prefixFiles[file] {
					overlap++
				}
			}
			return BatchSquashSimulation{OverlapCount: overlap}, nil
		},
	})
	return result, nil
}

func PlanBatchCandidates(ctx context.Context, candidates []BatchCandidate, seams BatchPlanningSeams) BatchPlanningResult {
	var result BatchPlanningResult
	if seams.IsAncestor == nil || seams.SimulateSquash == nil {
		result.Blockers = append(result.Blockers, BatchBlocker{Stage: "planning", Reason: "batch planning seams are incomplete"})
		return result
	}
	items := make([]BatchCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if len(candidate.Blockers) == 0 {
			items = append(items, candidate)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].PlanID < items[j].PlanID })
	dependencies := make(map[string]map[string]bool, len(items))
	for _, item := range items {
		dependencies[item.PlanID] = make(map[string]bool)
	}
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			ij, err := seams.IsAncestor(ctx, items[i].SourceTip, items[j].SourceTip)
			if err != nil {
				result.Blockers = append(result.Blockers, BatchBlocker{PlanID: items[i].PlanID, Stage: "ancestry", Reason: err.Error()})
				continue
			}
			ji, err := seams.IsAncestor(ctx, items[j].SourceTip, items[i].SourceTip)
			if err != nil {
				result.Blockers = append(result.Blockers, BatchBlocker{PlanID: items[j].PlanID, Stage: "ancestry", Reason: err.Error()})
				continue
			}
			switch {
			case ij && !ji:
				dependencies[items[j].PlanID][items[i].PlanID] = true
			case ji && !ij:
				dependencies[items[i].PlanID][items[j].PlanID] = true
			case ij && ji:
				// Identical tips are equivalent; retain a stable order.
				dependencies[items[j].PlanID][items[i].PlanID] = true
			}
		}
	}
	if len(result.Blockers) != 0 {
		return result
	}

	remaining := append([]BatchCandidate(nil), items...)
	settled := make(map[string]bool, len(items))
	for len(remaining) > 0 {
		type scored struct {
			index int
			sim   BatchSquashSimulation
		}
		var ready []scored
		for i, candidate := range remaining {
			depsMet := true
			for dependency := range dependencies[candidate.PlanID] {
				if !settled[dependency] {
					depsMet = false
					break
				}
			}
			if !depsMet {
				continue
			}
			sim, err := seams.SimulateSquash(ctx, result.Ordered, candidate)
			if err != nil {
				result.Blockers = append(result.Blockers, BatchBlocker{PlanID: candidate.PlanID, Stage: "simulation", Reason: err.Error()})
				return result
			}
			ready = append(ready, scored{index: i, sim: sim})
		}
		if len(ready) == 0 {
			for _, candidate := range remaining {
				result.Deferred = append(result.Deferred, BatchDeferral{PlanID: candidate.PlanID, Reason: "ancestry dependency was deferred"})
			}
			break
		}
		sort.SliceStable(ready, func(i, j int) bool {
			left, right := ready[i], ready[j]
			if left.sim.Conflicted != right.sim.Conflicted {
				return !left.sim.Conflicted
			}
			if left.sim.OverlapCount != right.sim.OverlapCount {
				return left.sim.OverlapCount < right.sim.OverlapCount
			}
			return remaining[left.index].PlanID < remaining[right.index].PlanID
		})
		best := ready[0]
		candidate := remaining[best.index]
		remaining = append(remaining[:best.index], remaining[best.index+1:]...)
		if best.sim.Conflicted {
			reason := strings.TrimSpace(best.sim.Reason)
			if reason == "" {
				reason = "squash simulation predicts a conflict"
			}
			deferral := BatchDeferral{PlanID: candidate.PlanID, Reason: reason, OverlapCount: best.sim.OverlapCount}
			candidate.Deferred = &deferral
			result.Deferred = append(result.Deferred, deferral)
			continue
		}
		result.Ordered = append(result.Ordered, candidate)
		settled[candidate.PlanID] = true
	}
	return result
}
