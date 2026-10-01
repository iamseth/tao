package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/plan"
	runpkg "github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
)

var reviewCommand = commandMetadata{
	name:                  "review",
	minPrefix:             "rev",
	usageLines:            []string{"review (rev) [--run [--model NAME]] <plan-id-or-slug-or-path>"},
	completionDescription: "Show or refresh the persisted plan review",
	long:                  "Show the persisted LLM review for a plan, or run a fresh review and display its metadata. Reviews are stored with Tao plan metadata, not in the worktree.",
	examples: "  tao review my-plan\n" +
		"  tao review --run my-plan",
	registerFlags: registerReviewFlags,
	completion: completionContext{
		positional: completionPositional{index: 1, label: "plan", completer: completePlanIDs},
	},
	repository: repositoryDefault,
	execute: func(c commandContext) error {
		return c.app.review(c.ctx, c.repo, c.args)
	},
}

func registerReviewFlags(fs *flag.FlagSet) {
	fs.Bool("run", false, "run a fresh review before displaying the result")
	fs.String("model", "", "override the agent model for a fresh review (--run)")
}

func (a App) review(ctx context.Context, repo runpkg.Repository, args []string) error {
	fs, positional, err := a.parseArgs("review", args, registerReviewFlags)
	if err != nil {
		return err
	}
	if err := requirePositionals(positional, 1, "usage: tao review [--run [--model NAME]] <plan-id-or-slug-or-path>"); err != nil {
		return err
	}
	model, err := modelFlagValue(fs)
	if err != nil {
		return err
	}
	if flagBoolValue(fs, "run") {
		var overrides runtimeconfig.RunOptionsPatch
		if model != "" {
			overrides = overrides.WithModelForAllRoles(model)
		}
		return a.runPlanReview(ctx, repo, positional[0], overrides)
	}
	detail, err := repo.ResolvePlan(ctx, positional[0])
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("plan %q not found", positional[0])
	}
	return renderPersistedPlanReview(a.Out, detail)
}

func (a App) runPlanReview(ctx context.Context, repo runpkg.Repository, input string, overrides runtimeconfig.RunOptionsPatch) error {
	defaults, err := a.envDefaultsFor(
		runtimeconfig.EnvAgent, runtimeconfig.EnvSessionTimeout, runtimeconfig.EnvSkipPermissions,
		runtimeconfig.EnvModel, runtimeconfig.EnvReviewModel,
	)
	if err != nil {
		return err
	}
	repositoryDefaults, err := a.currentRepositoryRunOptions(ctx)
	if err != nil {
		return err
	}
	request, err := defaults.newRunRequestWithRepository(input, repositoryDefaults, overrides)
	if err != nil {
		return err
	}
	snapshot := a.envSnapshot()
	runner := runpkg.NewService(repo, a.Out, runpkg.Options{
		ExecutionConfig: runpkg.ExecutionConfig{RuntimeEnv: &snapshot, ResolvedRunOptions: request.ResolvedRunOptions, SkipPermissions: defaults.SkipPermissions},
		RunDependencies: runpkg.RunDependencies{CommandRunner: a.CommandRunner, ProcessStarter: a.ProcessStarter, StatusReporter: a.StatusReporter, SessionLogWriter: a.Out, Now: a.Now,
			ReviewRangePresenter: func(detail *plan.PlanDetail, base, head string) {
				presentUnchangedReviewRange(a.Out, detail, base, head)
			},
		},
	})
	review, err := runner.Review(ctx, request)
	if err != nil {
		return err
	}
	// Review persistence can change lifecycle state. Reload before offering an
	// advisory next step rather than reasoning from the pre-review detail.
	detail, err := repo.ResolvePlan(ctx, request.Input)
	if err != nil {
		return err
	}
	if detail == nil {
		return fmt.Errorf("plan %q not found after review", request.Input)
	}
	if err := writef(a.Out, "Review completed: %s\n", request.Input); err != nil {
		return err
	}
	if err := renderPlanReviewMetadata(a.Out, review); err != nil {
		return err
	}
	return renderReviewGuidance(a.Out, detail)
}

func presentUnchangedReviewRange(out io.Writer, detail *plan.PlanDetail, base, head string) {
	review := plan.PersistedReview(detail)
	if out == nil || review == nil || review.Status != plan.ReviewStatusCompleted || review.Verdict == plan.ReviewVerdictApprove ||
		base == "" || head == "" || review.Base != base || review.Head != head {
		return
	}
	reviewedAt := "unknown"
	if !review.ReviewedAt.IsZero() {
		reviewedAt = review.ReviewedAt.UTC().Format(time.RFC3339)
	}
	guidance := "For a deliberate different-model retry, use tao review --run --model <name> <plan>."
	// Historical reviews can explain the notice, but cannot authorize actions.
	if !plan.ReviewSupersededByReopen(detail.Events) {
		actions := plan.DeriveNextAction(detail)
		if actions.Primary.Kind == plan.PlanActionRework {
			guidance += " Alternatively: " + actions.Primary.Command + "."
		}
		for _, action := range actions.Alternatives {
			if action.Kind == plan.PlanActionMerge && action.Class == plan.PlanActionClassAdministrative {
				guidance += " Administrative exception only: " + action.Command + " intentionally bypasses review and merge safeguards."
			}
		}
	}
	// Quote persisted text to keep control characters from escaping this line.
	_ = writef(out, "Notice: unchanged committed review range %q..%q; prior verdict %q, reviewed at %s; review proceeds (prompts, models, and dirty worktree contents may differ). %s\n", base, head, review.Verdict, reviewedAt, strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, guidance))
}

func renderPersistedPlanReview(out io.Writer, detail *plan.PlanDetail) error {
	if strings.TrimSpace(detail.Review.Content) != "" {
		if plan.ReviewSupersededByReopen(detail.Events) {
			if err := writeSupersededReviewArtifact(out, detail.Review.Content); err != nil {
				return err
			}
		} else if err := writeReviewArtifact(out, detail.Review.Content); err != nil {
			return err
		}
		return renderReviewGuidance(out, detail)
	}
	review := plan.PersistedReview(detail)
	if review != nil {
		if plan.ReviewSupersededByReopen(detail.Events) {
			if err := writeln(out, "Review superseded by reopened work."); err != nil {
				return err
			}
		}
		if err := writef(out, "Review: %s\n", detail.State.Plan.ID); err != nil {
			return err
		}
		if err := renderPlanReviewMetadata(out, *review); err != nil {
			return err
		}
		return renderReviewGuidance(out, detail)
	}
	id := reviewPlanID(detail)
	if err := writef(out, "No review yet for %s.\n", id); err != nil {
		return err
	}
	return renderReviewGuidance(out, detail)
}

func writeReviewArtifact(out io.Writer, content string) error {
	if _, err := io.WriteString(out, content); err != nil {
		return err
	}
	if !strings.HasSuffix(content, "\n") {
		return writeln(out, "")
	}
	return nil
}

func writeSupersededReviewArtifact(out io.Writer, content string) error {
	if err := writeln(out, "Review superseded by reopened work."); err != nil {
		return err
	}
	if err := writeln(out, "Historical review content:"); err != nil {
		return err
	}
	return writeReviewArtifact(out, content)
}

// renderReviewGuidance derives advisory next-step output from the current plan
// detail. Domain commands still enforce their own authoritative lifecycle and
// exact-revision gates.
func renderReviewGuidance(out io.Writer, detail *plan.PlanDetail) error {
	if plan.PlanIsMerged(detail.Events) {
		return writeln(out, "Plan already merged; no further action needed.")
	}
	if plan.PlanIsPullRequestComplete(detail) {
		return writeln(out, "Next: use the host's Squash and merge action. Tao does not merge the PR. After the merged change is present on your local default branch, optionally run `tao cleanup --dry-run`, then `tao cleanup`.")
	}
	return renderPrimaryNextAction(out, plan.DeriveNextAction(detail))
}

func reviewPlanID(detail *plan.PlanDetail) string {
	if detail != nil && detail.State.Plan.ID != "" {
		return detail.State.Plan.ID
	}
	return "plan"
}

func renderPlanReviewMetadata(out io.Writer, review plan.PlanReview) error {
	if review.Status != "" {
		if err := writef(out, "Review Status: %s\n", review.Status); err != nil {
			return err
		}
	}
	if review.Verdict != "" {
		if err := writef(out, "Verdict: %s\n", review.Verdict); err != nil {
			return err
		}
	}
	if review.Summary != "" {
		if err := writef(out, "Summary: %s\n", review.Summary); err != nil {
			return err
		}
	}
	if review.CommitMessage != nil {
		if err := writef(out, "Commit Subject: %s\nCommit Body:\n%s\n", review.CommitMessage.Subject, review.CommitMessage.Body); err != nil {
			return err
		}
	}
	if err := writef(out, "Findings: %d\n", review.FindingsCount); err != nil {
		return err
	}
	if review.Base != "" {
		if err := writef(out, "Base: %s\n", review.Base); err != nil {
			return err
		}
	}
	if review.Head != "" {
		if err := writef(out, "Head: %s\n", review.Head); err != nil {
			return err
		}
	}
	if review.Agent != "" {
		if err := writef(out, "Agent: %s\n", review.Agent); err != nil {
			return err
		}
	}
	if !review.ReviewedAt.IsZero() {
		if err := writef(out, "Reviewed At: %s\n", review.ReviewedAt.UTC().Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}
