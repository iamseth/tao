package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/agentsession"
	"github.com/iamseth/tao/internal/plan"
	reworkpkg "github.com/iamseth/tao/internal/rework"
	runpkg "github.com/iamseth/tao/internal/run"
)

var reworkCommand = commandMetadata{
	name:                  "rework",
	minPrefix:             "rew",
	usageLines:            []string{"rework (rew) [--force] [--run] [--from-pr] [--from-authors owner|all] [--dry-run] <plan-id-or-slug-or-path>"},
	completionDescription: "Reopen a reviewed plan with deterministic rework slices",
	long:                  "Reopen a reviewed plan from its persisted Tao review or recorded pull-request feedback. With --from-pr, Tao classifies unresolved review threads, persists the triage, and converts change requests into ordinary rework slices.",
	examples: "  tao rework my-plan\n" +
		"  tao rework --run 20260628-1618-kubectl-style-help\n" +
		"  tao rework --from-pr --dry-run my-plan\n" +
		"  tao rework --from-pr --from-authors all my-plan\n" +
		"  tao rework --force my-plan",
	registerFlags: registerReworkFlags,
	completion: completionContext{
		positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
	},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.rework(c.ctx, c.repo, c.args)
	},
}

func registerReworkFlags(fs *flag.FlagSet) {
	fs.Bool("force", false, "reopen even when the review gate would refuse")
	fs.Bool("run", false, "run the plan after reopening")
	fs.Bool("from-pr", false, "reopen from unresolved threads on the recorded pull request")
	fs.String("from-authors", string(reworkpkg.PRThreadAuthorsOwner), "pull-request thread authors: owner or all")
	fs.Bool("dry-run", false, "classify, persist, and print pull-request triage without reopening")
}

func (a App) rework(ctx context.Context, repo queueRepository, args []string) error {
	if repo == nil {
		return fmt.Errorf("rework requires a plan repository")
	}
	fs, positional, err := a.parseArgs("rework", args, registerReworkFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao rework [--force] [--run] [--from-pr] [--from-authors owner|all] [--dry-run] <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	input := positional[0]
	force := flagBoolValue(fs, "force")
	runAfter := flagBoolValue(fs, "run")
	fromPR := flagBoolValue(fs, "from-pr")
	dryRun := flagBoolValue(fs, "dry-run")
	authorScope := reworkpkg.PRThreadAuthorScope(strings.TrimSpace(flagStringValue(fs, "from-authors")))
	if err := validateReworkFlagCombination(fs, fromPR, force, runAfter, dryRun, authorScope); err != nil {
		return err
	}

	detail, err := repo.ResolvePlan(ctx, input)
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("plan %q not found", input)
	}

	now := a.now().UTC()
	return runpkg.WithPlanRunLock(ctx, detail, now, func(ownedCtx context.Context) error {
		// Resolve by the exact directory after acquisition so every gate and
		// mutation uses authoritative state rather than pre-lock selection data.
		refreshed, err := repo.ResolvePlan(ownedCtx, detail.Dir)
		if err != nil {
			return err
		}
		if refreshed == nil {
			return fmt.Errorf("plan %q not found", detail.Dir)
		}
		record, err := repo.PlanRecord(refreshed)
		if err != nil {
			return err
		}
		if fromPR {
			return a.reworkFromPullRequest(ownedCtx, repo, record, now, authorScope, dryRun, runAfter)
		}

		var newSlices []plan.Slice
		if !force {
			newSlices, err = reworkpkg.Reopen(record, now)
			if err != nil {
				return err
			}
		} else {
			findings := reworkpkg.ReviewFindings(refreshed)
			if len(findings) == 0 {
				findings = forcedReworkFindings(refreshed)
			}
			generationDetail := *refreshed
			generationDetail.State.UpdatedAt = now
			newSlices = reworkpkg.GenerateSlices(&generationDetail, findings, nextReworkRound(refreshed))
			if len(newSlices) == 0 {
				return fmt.Errorf("rework refused: plan %s has no review findings to convert", reworkPlanID(refreshed))
			}
			if err := reopenPlanRecord(record, newSlices, now, true); err != nil {
				return err
			}
		}
		if err := a.writeReworkResult(record.Detail(), newSlices, runAfter); err != nil {
			return err
		}
		if runAfter {
			return a.run(ownedCtx, repo, []string{record.Dir()})
		}
		return nil
	})
}

func validateReworkFlagCombination(fs *flag.FlagSet, fromPR, force, runAfter, dryRun bool, scope reworkpkg.PRThreadAuthorScope) error {
	if !fromPR {
		if flagWasProvided(fs, "from-authors") {
			return fmt.Errorf("--from-authors requires --from-pr")
		}
		if dryRun {
			return fmt.Errorf("--dry-run requires --from-pr")
		}
		return nil
	}
	if force {
		return fmt.Errorf("--from-pr cannot be combined with --force; pull-request rework uses recorded PR approval as its authority")
	}
	if dryRun && runAfter {
		return fmt.Errorf("--dry-run cannot be combined with --run; rerun without --dry-run to reopen and run the plan")
	}
	if scope != reworkpkg.PRThreadAuthorsOwner && scope != reworkpkg.PRThreadAuthorsAll {
		return fmt.Errorf("invalid --from-authors value %q (want owner or all)", scope)
	}
	return nil
}

var readReworkPRThreads = func(ctx context.Context, app App, request reworkpkg.PRThreadReadRequest) (reworkpkg.PRThreadReadResult, error) {
	return (reworkpkg.PRThreadReader{CommandRunner: app.CommandRunner}).Read(ctx, request)
}

var classifyReworkPRThreads = func(ctx context.Context, app App, repoRoot string, threads []reworkpkg.PRThread) ([]reworkpkg.PRThreadClassification, error) {
	return (reworkpkg.PRThreadClassifier{Text: reworkTriageTextSession{app: app}}).Classify(ctx, repoRoot, threads)
}

type reworkTriageTextSession struct{ app App }

func (s reworkTriageTextSession) GenerateText(ctx context.Context, repoRoot, prompt string) (string, error) {
	defaults, err := cliEnvDefaults()
	if err != nil {
		return "", err
	}
	descriptor, ok := agent.Lookup(defaults.Agent)
	if !ok {
		return "", fmt.Errorf("unsupported agent %q", defaults.Agent)
	}
	starter := s.app.ProcessStarter
	if starter == nil {
		starter = agent.DefaultProcessStarter
	}
	runner := agentsession.New(agentsession.Config{
		Descriptor: descriptor, Deps: agent.RuntimeDeps{ProcessStarter: starter},
		SkipPermissions: defaults.SkipPermissions, Timeout: defaults.SessionTimeoutValue(),
		Progress: s.app.Out, CommandRunner: s.app.CommandRunner,
	})
	result, err := runner.Run(ctx, agentsession.Request{RepoRoot: repoRoot, Prompt: prompt, CollectMetrics: true})
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(result.FinalText)
	if text == "" {
		text = strings.TrimSpace(result.Output)
	}
	return text, nil
}

func (a App) reworkFromPullRequest(ctx context.Context, repo queueRepository, record *plan.PlanRecord, now time.Time, scope reworkpkg.PRThreadAuthorScope, dryRun, runAfter bool) error {
	detail := record.Detail()
	request, err := pullRequestThreadReadRequest(detail, scope)
	if err != nil {
		return err
	}
	result, err := readReworkPRThreads(ctx, a, request)
	if err != nil {
		return err
	}
	if !triageMatchesThreads(detail.State.Plan.PRFeedbackTriage, result.Threads) && len(result.Threads) > 0 {
		classifications, err := classifyReworkPRThreads(ctx, a, request.RepoRoot, result.Threads)
		if err != nil {
			return err
		}
		triage := make(plan.PRFeedbackTriageResult, len(classifications))
		for _, classification := range classifications {
			triage[classification.ThreadNodeID] = plan.PRFeedbackTriageEntry{Kind: string(classification.Kind), Rationale: classification.Rationale}
		}
		if err := record.RecordPRFeedbackTriage(triage, now); err != nil {
			return err
		}
		detail = record.Detail()
	}
	if err := writePullRequestTriage(a.Out, result.Threads, detail.State.Plan.PRFeedbackTriage); err != nil {
		return err
	}
	if dryRun {
		if len(result.Threads) == 0 {
			return writeln(a.Out, "Dry run: no unresolved threads to persist; plan not reopened and no rework slices created.")
		}
		return writeln(a.Out, "Dry run: triage persisted; plan not reopened and no rework slices created.")
	}

	newSlices, err := reworkpkg.ReopenFromPullRequest(record, result.Threads, now)
	if err != nil {
		return err
	}
	if err := a.writeReworkResult(record.Detail(), newSlices, runAfter); err != nil {
		return err
	}
	if runAfter {
		return a.run(ctx, repo, []string{record.Dir()})
	}
	return nil
}

func pullRequestThreadReadRequest(detail *plan.PlanDetail, scope reworkpkg.PRThreadAuthorScope) (reworkpkg.PRThreadReadRequest, error) {
	id := reworkPlanID(detail)
	if detail == nil || detail.State.Plan.PullRequest == nil || detail.State.Plan.PullRequest.Number <= 0 {
		return reworkpkg.PRThreadReadRequest{}, fmt.Errorf("rework --from-pr requires plan %s to have a recorded pull request; create one with `tao run --pull-request %s`", id, id)
	}
	pr := detail.State.Plan.PullRequest
	owner, name, number, err := parseRecordedPullRequestURL(pr.URL)
	if err != nil {
		return reworkpkg.PRThreadReadRequest{}, fmt.Errorf("rework --from-pr cannot resolve plan %s recorded pull request: %w", id, err)
	}
	if number != pr.Number {
		return reworkpkg.PRThreadReadRequest{}, fmt.Errorf("rework --from-pr cannot resolve plan %s recorded pull request: URL number %d does not match recorded number %d", id, number, pr.Number)
	}
	return reworkpkg.PRThreadReadRequest{
		RepoRoot: detail.State.Repo.Root, RepositoryOwner: owner, RepositoryName: name,
		PullRequestNumber: pr.Number, AuthorScope: scope,
	}, nil
}

func parseRecordedPullRequestURL(raw string) (string, string, int, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid URL %q", raw)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if parsed.Host == "" || len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[2] != "pull" {
		return "", "", 0, fmt.Errorf("invalid pull-request URL %q", raw)
	}
	number, err := strconv.Atoi(parts[3])
	if err != nil || number <= 0 {
		return "", "", 0, fmt.Errorf("invalid pull-request URL %q", raw)
	}
	return parts[0], parts[1], number, nil
}

func triageMatchesThreads(triage plan.PRFeedbackTriageResult, threads []reworkpkg.PRThread) bool {
	if len(triage) == 0 || len(triage) != len(threads) {
		return false
	}
	for _, thread := range threads {
		if _, ok := triage[strings.TrimSpace(thread.NodeID)]; !ok {
			return false
		}
	}
	return true
}

func writePullRequestTriage(out io.Writer, threads []reworkpkg.PRThread, triage plan.PRFeedbackTriageResult) error {
	if err := writeln(out, "Pull-request feedback triage:"); err != nil {
		return err
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "PATH\tAUTHOR\tCLASSIFICATION\tACTION"); err != nil {
		return err
	}
	for _, thread := range threads {
		entry, ok := triage[strings.TrimSpace(thread.NodeID)]
		kind := strings.TrimSpace(entry.Kind)
		if !ok || kind == "" {
			kind = "unclassified"
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\n", displayPRThreadPath(thread.Path), prThreadAuthor(thread), kind, prThreadAction(reworkpkg.PRThreadKind(kind))); err != nil {
			return err
		}
	}
	return writer.Flush()
}

func displayPRThreadPath(path string) string {
	if path = strings.TrimSpace(path); path != "" {
		return path
	}
	return "-"
}

func prThreadAuthor(thread reworkpkg.PRThread) string {
	if len(thread.Comments) > 0 {
		if author := strings.TrimSpace(thread.Comments[0].AuthorLogin); author != "" {
			return author
		}
	}
	return "-"
}

func prThreadAction(kind reworkpkg.PRThreadKind) string {
	switch kind {
	case reworkpkg.PRThreadKindChange:
		return "create rework slice"
	case reworkpkg.PRThreadKindQuestion:
		return "report question"
	case reworkpkg.PRThreadKindScope:
		return "report scope feedback"
	case reworkpkg.PRThreadKindUnmappable:
		return "refuse until mapped"
	default:
		return "refuse unclassified"
	}
}

func reopenPlanRecord(record *plan.PlanRecord, newSlices []plan.Slice, now time.Time, force bool) error {
	if record == nil {
		return fmt.Errorf("plan record is nil")
	}
	detail := record.Detail()
	if !force || detail == nil || plan.ReopenableStatus(detail.State.Status) {
		return record.Reopen(newSlices, now)
	}
	return record.ReopenForced(newSlices, now)
}

func (a App) writeReworkResult(detail *plan.PlanDetail, newSlices []plan.Slice, runAfter bool) error {
	id := reworkPlanID(detail)
	if err := writef(a.Out, "Rework slices created for %s:\n", id); err != nil {
		return err
	}
	for _, slice := range newSlices {
		if err := writef(a.Out, "- %s: %s\n", slice.ID, slice.Title); err != nil {
			return err
		}
	}
	if runAfter {
		return writef(a.Out, "Running: tao run %s\n", id)
	}
	return renderPrimaryNextAction(a.Out, plan.DeriveNextAction(detail))
}

func nextReworkRound(detail *plan.PlanDetail) int {
	maxRound := 0
	if detail != nil {
		for _, slice := range detail.Slices.Slices {
			if round := reworkpkg.RoundFromSliceID(slice.ID); round > maxRound {
				maxRound = round
			}
		}
	}
	return maxRound + 1
}

func forcedReworkFindings(detail *plan.PlanDetail) []plan.ReviewFinding {
	id := reworkPlanID(detail)
	return []plan.ReviewFinding{{
		Severity:   "forced",
		File:       firstReworkExpectedFile(detail),
		Message:    "Forced rework requested for plan " + id,
		Suggestion: "Inspect the persisted review and address any remaining issues.",
	}}
}

func firstReworkExpectedFile(detail *plan.PlanDetail) string {
	if detail != nil {
		for _, slice := range detail.Slices.Slices {
			for _, file := range slice.ExpectedFiles {
				if strings.TrimSpace(file) != "" {
					return strings.TrimSpace(file)
				}
			}
		}
	}
	return "."
}

func reworkPlanID(detail *plan.PlanDetail) string {
	if detail == nil || strings.TrimSpace(detail.State.Plan.ID) == "" {
		return "plan"
	}
	return strings.TrimSpace(detail.State.Plan.ID)
}
