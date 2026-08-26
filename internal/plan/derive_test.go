package plan

import (
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestSummarizeProjectsStructuredDecisionOverview(t *testing.T) {
	detail := &PlanDetail{
		State: State{Plan: PlanState{
			ID: "plan", Title: "Explain the work",
			Decision: &Decision{
				Problem: "Make planning decisions explainable.", WhyNow: "The dependency is ready.", ExpectedBenefit: "Operators can compare plans.", Readiness: DecisionReadinessReady,
				SuccessCriteria: []string{"Rationale is visible.", "Legacy plans remain valid."},
				Disposition:     DecisionDispositionReady, DispositionReason: "The shared projection is small.",
				Priority: Priority{Level: PriorityOverallLevelMust, Impact: PriorityLevelHigh, Urgency: PriorityLevelMedium, Effort: PriorityEffortSmall, Risk: PriorityLevelLow, Confidence: PriorityLevelHigh, Rationale: "High impact for low effort."},
			},
			Sequence: &Sequence{Position: 2, Total: 3, Relationships: []PlanRelation{{PlanID: "plan-a", Type: PlanRelationAfter, Reason: "Consumes its model."}}},
		}},
		PlanningBrief: PlanningBriefArtifact{Content: "## User Goal\nThis fallback must not replace structured data.\n"},
	}

	overview := Summarize(detail, time.Time{}).Overview
	if overview.Source != DecisionOverviewSourceStructured || overview.Problem != "Make planning decisions explainable." || overview.WhyNow != "The dependency is ready." || overview.ExpectedBenefit != "Operators can compare plans." {
		t.Fatalf("structured overview identity = %+v", overview)
	}
	if overview.Readiness != DecisionReadinessReady || overview.Disposition != DecisionDispositionReady || overview.DispositionReason != "The shared projection is small." {
		t.Fatalf("structured decision fields = %+v", overview)
	}
	if overview.Priority == nil || overview.Priority.Level != PriorityOverallLevelMust || overview.Priority.Impact != PriorityLevelHigh || overview.Priority.Effort != PriorityEffortSmall || overview.Priority.Confidence != PriorityLevelHigh || overview.Priority.Rationale != "High impact for low effort." {
		t.Fatalf("structured priority = %+v", overview.Priority)
	}
	if overview.Sequence == nil || overview.Sequence.Position != 2 || len(overview.Sequence.Relationships) != 1 || overview.Sequence.Relationships[0].Type != PlanRelationAfter {
		t.Fatalf("structured sequence = %+v", overview.Sequence)
	}
	if !slicesEqual(overview.SuccessCriteria, []string{"Rationale is visible.", "Legacy plans remain valid."}) {
		t.Fatalf("structured criteria = %v", overview.SuccessCriteria)
	}
}

func TestSummarizeProjectsLegacyBriefWithoutInferringRank(t *testing.T) {
	detail := &PlanDetail{
		State: State{Plan: PlanState{ID: "legacy", Title: "A vague title"}},
		PlanningBrief: PlanningBriefArtifact{Content: `# Planning Brief

## User Goal
Make the decision rationale visible.

## Why Now
Two list views need the same data.

## Expected Benefit
No consumer parses Markdown independently.

## Success Criteria
- Legacy plans remain valid.
- No priority is inferred.
`},
		PlanNarrative: PlanNarrativeArtifact{Content: "## Goal\nDo not prefer this narrative.\n"},
	}

	overview := Summarize(detail, time.Time{}).Overview
	if overview.Source != DecisionOverviewSourcePlanningBrief || overview.Problem != "Make the decision rationale visible." || overview.WhyNow != "Two list views need the same data." || overview.ExpectedBenefit != "No consumer parses Markdown independently." {
		t.Fatalf("legacy brief overview = %+v", overview)
	}
	if !slicesEqual(overview.SuccessCriteria, []string{"Legacy plans remain valid.", "No priority is inferred."}) {
		t.Fatalf("legacy criteria = %v", overview.SuccessCriteria)
	}
	if overview.Priority != nil || overview.Disposition != "" || overview.Readiness != "" {
		t.Fatalf("legacy overview inferred rank or decision: %+v", overview)
	}
}

func TestProjectDecisionOverviewUsesNarrativeOnlyAfterMissingLegacyBriefProse(t *testing.T) {
	detail := &PlanDetail{
		PlanningBrief: PlanningBriefArtifact{Content: "## Constraints\nKeep it small.\n"},
		PlanNarrative: PlanNarrativeArtifact{Content: "# Plan\n\n## Goal\nRecover the legacy narrative.\n\n## Benefit\nThe old plan remains understandable.\n"},
	}
	overview := ProjectDecisionOverview(detail)
	if overview.Source != DecisionOverviewSourcePlanNarrative || overview.Problem != "Recover the legacy narrative." || overview.ExpectedBenefit != "The old plan remains understandable." {
		t.Fatalf("legacy narrative overview = %+v", overview)
	}

	detail.PlanNarrative.Content = "# Plan\n\nUnsectioned prose.\n\n```md\n## Goal\nFenced example.\n```\n"
	overview = ProjectDecisionOverview(detail)
	if overview.Source != DecisionOverviewSourceUnavailable || overview.Problem != "" || overview.Priority != nil || overview.Disposition != "" {
		t.Fatalf("malformed legacy prose overview = %+v", overview)
	}
	if got := ProjectDecisionOverview(nil); got.Source != DecisionOverviewSourceUnavailable || got.Priority != nil {
		t.Fatalf("nil overview = %+v", got)
	}
}

func TestProjectDecisionOverviewRecognizesOnlyValidATXClosingSequences(t *testing.T) {
	tests := []struct {
		name    string
		heading string
		want    string
		source  DecisionOverviewSource
	}{
		{name: "attached hash is heading text", heading: "## Goal#", source: DecisionOverviewSourceUnavailable},
		{name: "single closing hash", heading: "## Goal #", want: "Project this goal.", source: DecisionOverviewSourcePlanNarrative},
		{name: "multiple closing hashes", heading: "## Goal ###", want: "Project this goal.", source: DecisionOverviewSourcePlanNarrative},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := &PlanDetail{PlanNarrative: PlanNarrativeArtifact{Content: tt.heading + "\nProject this goal.\n"}}
			overview := ProjectDecisionOverview(detail)
			if overview.Source != tt.source || overview.Problem != tt.want {
				t.Fatalf("overview = %+v, want source %q and problem %q", overview, tt.source, tt.want)
			}
		})
	}
}

func TestProjectDecisionOverviewRecognizesTabSeparatedATXHeadings(t *testing.T) {
	t.Run("target heading", func(t *testing.T) {
		detail := &PlanDetail{PlanNarrative: PlanNarrativeArtifact{Content: "##\tGoal\nProject this goal.\n"}}
		overview := ProjectDecisionOverview(detail)
		if overview.Source != DecisionOverviewSourcePlanNarrative || overview.Problem != "Project this goal." {
			t.Fatalf("overview = %+v, want tab-separated goal", overview)
		}
	})

	t.Run("same-level boundary", func(t *testing.T) {
		detail := &PlanDetail{PlanNarrative: PlanNarrativeArtifact{Content: "## Goal\nProject this goal.\n\n##\tConstraints\nDo not project this prose.\n"}}
		overview := ProjectDecisionOverview(detail)
		if overview.Source != DecisionOverviewSourcePlanNarrative || overview.Problem != "Project this goal." {
			t.Fatalf("overview = %+v, want body stopped at tab-separated boundary", overview)
		}
	})
}

func TestProjectDecisionOverviewHonorsMarkdownCodeIndentation(t *testing.T) {
	t.Run("indented heading cannot create section", func(t *testing.T) {
		detail := &PlanDetail{PlanNarrative: PlanNarrativeArtifact{Content: "    ## Goal\n    Example text is code, not rationale.\n"}}
		overview := ProjectDecisionOverview(detail)
		if overview.Source != DecisionOverviewSourceUnavailable || overview.Problem != "" {
			t.Fatalf("indented code projected as decision overview: %+v", overview)
		}
	})

	t.Run("indented fence cannot obscure section", func(t *testing.T) {
		detail := &PlanDetail{PlanNarrative: PlanNarrativeArtifact{Content: "    ```md\n    example code\n    ```\n\n## Goal\nProject this real goal.\n"}}
		overview := ProjectDecisionOverview(detail)
		if overview.Source != DecisionOverviewSourcePlanNarrative || overview.Problem != "Project this real goal." {
			t.Fatalf("overview = %+v, want real goal after indented code", overview)
		}
	})

	t.Run("three-space indentation remains valid", func(t *testing.T) {
		detail := &PlanDetail{PlanNarrative: PlanNarrativeArtifact{Content: "   ## Goal\nProject this goal.\n"}}
		overview := ProjectDecisionOverview(detail)
		if overview.Source != DecisionOverviewSourcePlanNarrative || overview.Problem != "Project this goal." {
			t.Fatalf("overview = %+v, want three-space-indented heading", overview)
		}
	})
}

func TestProjectDecisionOverviewIgnoresHeadingsAfterNonClosingFenceMarkers(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
	}{
		{
			name: "mixed delimiter",
			markdown: "```md\n" +
				"~~~\n" +
				"## Goal\n" +
				"Do not project this fenced example.\n" +
				"```\n",
		},
		{
			name: "shorter matching delimiter",
			markdown: "````md\n" +
				"```\n" +
				"## Goal\n" +
				"Do not project this nested example.\n" +
				"````\n",
		},
		{
			name: "backtick delimiter with trailing text",
			markdown: "```md\n" +
				"```not-a-close\n" +
				"## Goal\n" +
				"Do not project content after a false closing fence.\n" +
				"```\n",
		},
		{
			name: "tilde delimiter with trailing text",
			markdown: "~~~md\n" +
				"~~~~not-a-close\n" +
				"## Goal\n" +
				"Do not project content after a false closing fence.\n" +
				"~~~\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			overview := ProjectDecisionOverview(&PlanDetail{PlanNarrative: PlanNarrativeArtifact{Content: tt.markdown}})
			if overview.Source != DecisionOverviewSourceUnavailable || overview.Problem != "" {
				t.Fatalf("fenced legacy narrative projected as decision overview: %+v", overview)
			}
		})
	}
}

func TestProjectDecisionOverviewBoundsListsTextAndControlsDeterministically(t *testing.T) {
	criteria := make([]string, decisionOverviewMaxCriteria+3)
	relationships := make([]PlanRelation, decisionOverviewMaxRelationships+3)
	for i := range criteria {
		criteria[i] = "criterion\x1b\n" + strings.Repeat("界", decisionOverviewItemRunes+20)
	}
	for i := range relationships {
		relationships[i] = PlanRelation{PlanID: strings.Repeat("p", decisionOverviewPlanIDRunes+20), Type: PlanRelationRelated, Reason: strings.Repeat("r", decisionOverviewItemRunes+20)}
	}
	detail := &PlanDetail{State: State{Plan: PlanState{
		Title: strings.Repeat("界", decisionOverviewTextRunes+20),
		Decision: &Decision{
			Problem: strings.Repeat("p", decisionOverviewTextRunes+20), WhyNow: "urgent\x1b[31m\nnow", ExpectedBenefit: strings.Repeat("b", decisionOverviewTextRunes+20),
			SuccessCriteria: criteria, Disposition: DecisionDispositionReady,
			Priority: Priority{Rationale: strings.Repeat("r", decisionOverviewTextRunes+20)},
		},
		Sequence: &Sequence{Position: 1, Total: 9, Relationships: relationships},
	}}}

	first := ProjectDecisionOverview(detail)
	second := ProjectDecisionOverview(detail)
	if first.Problem != second.Problem || !slicesEqual(first.SuccessCriteria, second.SuccessCriteria) {
		t.Fatalf("projection is not deterministic: first=%+v second=%+v", first, second)
	}
	for name, value := range map[string]string{"problem": first.Problem, "why now": first.WhyNow, "benefit": first.ExpectedBenefit, "priority": first.Priority.Rationale} {
		if utf8.RuneCountInString(value) > decisionOverviewTextRunes || strings.ContainsAny(value, "\x00\x1b\n\r\t") {
			t.Errorf("%s is not bounded plain text: runes=%d value=%q", name, utf8.RuneCountInString(value), value)
		}
	}
	if len(first.SuccessCriteria) != decisionOverviewMaxCriteria || len(first.Sequence.Relationships) != decisionOverviewMaxRelationships {
		t.Fatalf("bounded lists = criteria %d relationships %d", len(first.SuccessCriteria), len(first.Sequence.Relationships))
	}
	for _, criterion := range first.SuccessCriteria {
		if utf8.RuneCountInString(criterion) > decisionOverviewItemRunes || strings.ContainsRune(criterion, '\x1b') {
			t.Fatalf("unbounded criterion: %q", criterion)
		}
	}
	if len(detail.State.Plan.Decision.SuccessCriteria) != decisionOverviewMaxCriteria+3 || len(detail.State.Plan.Sequence.Relationships) != decisionOverviewMaxRelationships+3 {
		t.Fatal("projection mutated durable metadata")
	}
}

func slicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func TestSummarizeCountsOriginalAndReworkSlicesAcrossRounds(t *testing.T) {
	detail := &PlanDetail{
		State: State{Status: StatusInProgress, Plan: PlanState{ID: "plan", PendingSlices: []string{"002-original", "r102-fix", "r202-more"}}},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-original", Status: StatusCompleted},
			{ID: "002-original", Status: StatusPending},
			{ID: "r101-fix", Status: StatusCompleted},
			{ID: "r102-fix", Status: StatusPending},
			{ID: "r1ab-legacy", Status: StatusCompleted},
			{ID: "r201-more", Status: StatusCompleted},
			{ID: "r202-more", Status: StatusPending},
		}},
	}

	derived := Derive(detail, time.Time{})
	if derived.OriginalCompletedCount != 1 || derived.OriginalTotalCount != 2 {
		t.Fatalf("original completed/total = %d/%d, want 1/2", derived.OriginalCompletedCount, derived.OriginalTotalCount)
	}
	if derived.ReworkCompletedCount != 3 || derived.ReworkTotalCount != 5 {
		t.Fatalf("rework completed/total = %d/%d, want 3/5", derived.ReworkCompletedCount, derived.ReworkTotalCount)
	}

	summary := Summarize(detail, time.Time{})
	if summary.OriginalCompletedCount != 1 || summary.OriginalTotalCount != 2 || summary.ReworkCompletedCount != 3 || summary.ReworkTotalCount != 5 {
		t.Fatalf("summary composition = %+v, want original 1/2 and rework 3/5", summary)
	}
}

func TestDeriveReviewedReflectsPlanReview(t *testing.T) {
	detail := reviewedCapabilityDetail()

	derived := Derive(detail, time.Time{})
	if derived.Capabilities.Reviewed {
		t.Fatalf("expected missing review to keep derived capabilities unreviewed: %+v", derived.Capabilities)
	}
	if capabilities := AnalyzeRunCapabilities(detail); capabilities.Reviewed {
		t.Fatalf("expected missing review to keep analyzed capabilities unreviewed: %+v", capabilities)
	}

	reviewedAt := time.Date(2026, 6, 28, 6, 45, 0, 0, time.UTC)
	detail.State.Plan.Review = &PlanReview{Verdict: "pass", Summary: "ready", ReviewedAt: reviewedAt}

	derived = Derive(detail, time.Time{})
	if !derived.Capabilities.Reviewed {
		t.Fatalf("expected persisted review to mark derived capabilities reviewed: %+v", derived.Capabilities)
	}
	if capabilities := AnalyzeRunCapabilities(detail); !capabilities.Reviewed {
		t.Fatalf("expected persisted review to mark analyzed capabilities reviewed: %+v", capabilities)
	}

	summary := Summarize(detail, time.Time{})
	if !summary.Reviewed || summary.ReviewVerdict != "pass" {
		t.Fatalf("expected review metadata in summary, got %+v", summary)
	}
}

func TestDeriveCompletionDoesNotRequireReview(t *testing.T) {
	detail := reviewedCapabilityDetail()

	derived := Derive(detail, time.Time{})
	if !derived.Complete || !derived.Capabilities.Complete {
		t.Fatalf("expected slice-complete plan without review to remain complete: %+v", derived)
	}
	if derived.Capabilities.Reviewed {
		t.Fatalf("expected missing review to remain informational, got %+v", derived.Capabilities)
	}

	summary := Summarize(detail, time.Time{})
	if !summary.Complete || summary.Status != StatusInReview || summary.Reviewed || summary.ReviewVerdict != "" {
		t.Fatalf("expected slice-complete unreviewed summary to be in_review, got %+v", summary)
	}
}

func TestPlanLifecycleStatusReflectsReviewAndMergeStages(t *testing.T) {
	detail := reviewedCapabilityDetail()
	if got := PlanLifecycleStatus(detail); got != StatusInReview {
		t.Fatalf("unreviewed slice-complete status = %q, want %q", got, StatusInReview)
	}

	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested}
	if got := PlanLifecycleStatus(detail); got != StatusChangesRequested {
		t.Fatalf("changes-requested status = %q, want %q", got, StatusChangesRequested)
	}

	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove}
	if got := PlanLifecycleStatus(detail); got != StatusReviewed {
		t.Fatalf("approved review status = %q, want %q", got, StatusReviewed)
	}

	detail.Events = append(detail.Events, Event{Type: EventTypePlanMerged})
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("merged status = %q, want %q", got, StatusCompleted)
	}
	if !PlanIsMerged(detail.Events) {
		t.Fatal("plan_merged evidence must remain the actual-merge signal")
	}
}

func TestPlanIsPullRequestCompleteRequiresCurrentMatchingEvidence(t *testing.T) {
	tests := []struct {
		name     string
		review   *PlanReview
		pr       *PullRequest
		reopened bool
		want     bool
	}{
		{name: "exact matching head", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123"}, pr: &PullRequest{HeadSHA: "head123"}, want: true},
		{name: "missing pull request", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123"}},
		{name: "missing pull request head", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123"}, pr: &PullRequest{}},
		{name: "missing review head", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove}, pr: &PullRequest{HeadSHA: "head123"}},
		{name: "mismatched head", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123"}, pr: &PullRequest{HeadSHA: "head456"}},
		{name: "case mismatched head", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123"}, pr: &PullRequest{HeadSHA: "HEAD123"}},
		{name: "whitespace head", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: " "}, pr: &PullRequest{HeadSHA: " "}},
		{name: "error review", review: &PlanReview{Status: ReviewStatusError, Verdict: ReviewVerdictApprove, Head: "head123"}, pr: &PullRequest{HeadSHA: "head123"}},
		{name: "changes requested", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested, Head: "head123"}, pr: &PullRequest{HeadSHA: "head123"}},
		{name: "comment review", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictComment, Head: "head123"}, pr: &PullRequest{HeadSHA: "head123"}},
		{name: "reopened after approval", review: &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123"}, pr: &PullRequest{HeadSHA: "head123"}, reopened: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detail := reviewedCapabilityDetail()
			detail.State.Plan.Review = clonePlanReview(tt.review)
			detail.State.Plan.PullRequest = clonePullRequest(tt.pr)
			if tt.review != nil {
				detail.Events = []Event{{Type: EventTypePlanReviewed, Review: clonePlanReview(tt.review)}}
			}
			if tt.reopened {
				detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
			}
			if got := PlanIsPullRequestComplete(detail); got != tt.want {
				t.Fatalf("PlanIsPullRequestComplete() = %t, want %t", got, tt.want)
			}
		})
	}

	if PlanIsPullRequestComplete(nil) {
		t.Fatal("nil plan cannot have pull request completion evidence")
	}
}

func TestPlanLifecycleStatusProjectsExistingPullRequestCompletion(t *testing.T) {
	detail := reviewedCapabilityDetail()
	review := &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123"}
	detail.State.Status = StatusReviewed
	detail.State.Plan.Review = review
	detail.State.Plan.PullRequest = &PullRequest{Number: 42, URL: "https://example.test/pull/42", HeadSHA: "head123"}
	detail.Events = []Event{{Type: EventTypePlanReviewed, Review: review}, {Type: EventTypePullRequestCreated, PullRequest: detail.State.Plan.PullRequest}}

	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("matching persisted PR/review status = %q, want %q", got, StatusCompleted)
	}
	if detail.State.Status != StatusReviewed {
		t.Fatalf("read-side projection mutated persisted status to %q", detail.State.Status)
	}
	if PlanIsMerged(detail.Events) {
		t.Fatal("PR completion must not imply plan_merged evidence")
	}
}

func TestPlanLifecycleStatusReopenInvalidatesPullRequestCompletion(t *testing.T) {
	detail := reviewedCapabilityDetail()
	review := &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123"}
	detail.State.Status = StatusCompleted
	detail.State.Plan.Review = review
	detail.State.Plan.PullRequest = &PullRequest{Number: 42, HeadSHA: "head123"}
	detail.Events = []Event{{Type: EventTypePlanReviewed, Review: review}, {Type: EventTypePullRequestCreated, PullRequest: detail.State.Plan.PullRequest}}
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("pre-reopen status = %q, want %q", got, StatusCompleted)
	}

	detail.State.Status = StatusInProgress
	detail.State.Plan.PendingSlices = []string{"002-fix"}
	detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: "002-fix", Status: StatusPending})
	detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
	if PlanIsPullRequestComplete(detail) {
		t.Fatal("reopen must supersede prior PR completion evidence")
	}
	if got := PlanLifecycleStatus(detail); got != StatusInProgress {
		t.Fatalf("reopened status = %q, want %q", got, StatusInProgress)
	}

	detail.State.Status = StatusInReview
	detail.State.Plan.PendingSlices = nil
	detail.Slices.Slices[1].Status = StatusCompleted
	fresh := &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head456"}
	detail.State.Plan.Review = fresh
	detail.Events = append(detail.Events, Event{Type: EventTypePlanReviewed, Review: fresh})
	if got := PlanLifecycleStatus(detail); got != StatusReviewed {
		t.Fatalf("fresh review with stale PR head status = %q, want %q", got, StatusReviewed)
	}

	detail.State.Plan.PullRequest = &PullRequest{Number: 42, HeadSHA: "head456"}
	detail.Events = append(detail.Events, Event{Type: EventTypePullRequestCreated, PullRequest: detail.State.Plan.PullRequest})
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("fresh matching review/PR status = %q, want %q", got, StatusCompleted)
	}
}

// TestPlanLifecycleStatusLegacyCompletedWithoutMergeEvent covers plans whose
// state.Status is completed but whose event log has no plan_merged event:
// pre-upgrade releases wrote completed at final slice completion, so the plan
// finished under the old semantics (and was typically merged manually or
// before merge events existed). The persisted status is trusted — demoting
// such plans would mass-revert historical completed plans to in_review on
// upgrade, with no reliable recovery once branches and head snapshots are
// gone. Current writes reach completed through RecordMerged or matching PR
// evidence; the latter is handled by its explicit predicate before this arm.
func TestPlanLifecycleStatusLegacyCompletedWithoutMergeEvent(t *testing.T) {
	detail := reviewedCapabilityDetail()
	detail.State.Status = StatusCompleted
	detail.Events = nil
	if PlanIsMerged(detail.Events) {
		t.Fatal("fixture must have no plan_merged event")
	}
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("legacy completed status = %q, want %q", got, StatusCompleted)
	}

	// A non-merge event (e.g. plan_reviewed) does not disqualify the legacy arm.
	detail.Events = []Event{{Type: EventTypePlanReviewed}}
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("legacy completed status with review event = %q, want %q", got, StatusCompleted)
	}

	detail.Events = []Event{{Type: EventTypePlanMerged}}
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("recorded merge status = %q, want %q", got, StatusCompleted)
	}
}

// TestPlanLifecycleStatusReopenedAfterMergeIsNotCompleted covers the case where a
// merged plan is reopened for rework: the stale plan_merged event must not keep
// projecting completed while the reopened plan has pending work.
func TestPlanLifecycleStatusReopenedAfterMergeIsNotCompleted(t *testing.T) {
	detail := reviewedCapabilityDetail()
	detail.Events = []Event{{Type: EventTypePlanMerged}}
	if got := PlanLifecycleStatus(detail); got != StatusCompleted {
		t.Fatalf("merged status = %q, want %q", got, StatusCompleted)
	}

	// Reopen for rework: pending slice added, status back to in_progress, and a
	// plan_reopened event appended after the merge.
	detail.State.Status = StatusInProgress
	detail.State.Plan.PendingSlices = []string{"002-b"}
	detail.Slices.Slices = append(detail.Slices.Slices, Slice{ID: "002-b", Status: StatusPending})
	detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
	if got := PlanLifecycleStatus(detail); got != StatusInProgress {
		t.Fatalf("reopened-after-merge status = %q, want %q", got, StatusInProgress)
	}
	if PlanIsMerged(detail.Events) {
		t.Fatal("reopened plan must not report as merged")
	}
}

// TestPlanLifecycleStatusReworkClearsStaleReviewVerdict covers a plan reviewed
// changes_requested, reopened, then reworked to slice-complete: it must project
// in_review (awaiting a fresh verdict), not the stale changes_requested verdict.
func TestPlanLifecycleStatusReworkClearsStaleReviewVerdict(t *testing.T) {
	detail := reviewedCapabilityDetail()
	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested}
	detail.Events = []Event{{Type: EventTypePlanReviewed, Review: detail.State.Plan.Review}}
	if got := PlanLifecycleStatus(detail); got != StatusChangesRequested {
		t.Fatalf("pre-rework status = %q, want %q", got, StatusChangesRequested)
	}

	// Reopen, then complete the rework slice so the queue drains again.
	detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
	if got := PlanLifecycleStatus(detail); got != StatusInReview {
		t.Fatalf("reworked slice-complete status = %q, want %q (stale verdict must be ignored)", got, StatusInReview)
	}
	if AnalyzeRunCapabilities(detail).Reviewed {
		t.Fatal("reopened plan must not report a current review until re-reviewed")
	}

	// A fresh review after the reopen is honored again.
	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove}
	detail.Events = append(detail.Events, Event{Type: EventTypePlanReviewed, Review: detail.State.Plan.Review})
	if got := PlanLifecycleStatus(detail); got != StatusReviewed {
		t.Fatalf("post-rework fresh-review status = %q, want %q", got, StatusReviewed)
	}
	if !AnalyzeRunCapabilities(detail).Reviewed {
		t.Fatal("fresh review after reopen must mark the plan reviewed")
	}
}

// TestLifecycleCompleteAllowsSkippedFinalSlice guards against the regression
// where a plan that skipped its final pending slice was never Complete because
// lifecycleComplete gated on a raw pending count that treats skipped slices as
// pending. Such a plan could neither finalize nor merge — permanently stuck.
func TestLifecycleCompleteAllowsSkippedFinalSlice(t *testing.T) {
	completedAt := time.Date(2026, 6, 28, 6, 40, 0, 0, time.UTC)
	detail := &PlanDetail{
		State: State{
			// in_review (not completed) so the completion decision exercises the
			// slicesComplete path rather than the status==Completed escape hatch.
			Status: StatusInReview,
			Plan: PlanState{
				ID:              "plan",
				CompletedSlices: []string{"001-a"},
			},
		},
		Slices: SlicesFile{Slices: []Slice{
			{ID: "001-a", Status: StatusCompleted, Timing: SliceTiming{CompletedAt: &completedAt}},
			{ID: "002-b", Status: StatusSkipped},
		}},
	}
	if !AnalyzeRunCapabilities(detail).Complete {
		t.Fatal("plan whose final slice is skipped (queue drained) must be Complete")
	}
	if got := PlanLifecycleStatus(detail); got != StatusInReview {
		t.Fatalf("skipped-final-slice status = %q, want %q", got, StatusInReview)
	}
}

// TestSummarizeClearsStaleReviewVerdictAfterReopen guards the PlanSummary
// projection: after a reopen supersedes the last review, ReviewVerdict must not
// leak the stale persisted verdict (Reviewed already flips false via CurrentReview).
func TestSummarizeClearsStaleReviewVerdictAfterReopen(t *testing.T) {
	now := time.Date(2026, 6, 28, 7, 0, 0, 0, time.UTC)
	detail := reviewedCapabilityDetail()
	detail.State.Plan.Review = &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested}
	detail.Events = []Event{{Type: EventTypePlanReviewed, Review: detail.State.Plan.Review}}
	if got := Summarize(detail, now).ReviewVerdict; got != ReviewVerdictChangesRequested {
		t.Fatalf("pre-reopen ReviewVerdict = %q, want %q", got, ReviewVerdictChangesRequested)
	}

	detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
	if got := Summarize(detail, now).ReviewVerdict; got != "" {
		t.Fatalf("post-reopen ReviewVerdict = %q, want empty (superseded by reopen)", got)
	}
}

func TestReviewAccessorsSeparateCurrentFromPersisted(t *testing.T) {
	proposal := &ReviewCommitMessage{Subject: "fix(plan): proposal", Body: "body"}
	review := PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Summary: "ready", CommitMessage: proposal}
	detail := reviewedCapabilityDetail()
	SetPersistedReview(detail, review)
	persisted := PersistedReview(detail)
	if persisted == nil {
		t.Fatal("expected persisted review after SetPersistedReview")
	}
	if persisted.Findings == nil {
		t.Fatal("SetPersistedReview must publish canonical empty findings")
	}
	proposal.Subject = "mutated"
	if persisted.CommitMessage == nil || persisted.CommitMessage.Subject != "fix(plan): proposal" {
		t.Fatalf("SetPersistedReview did not clone replacement metadata: %+v", persisted.CommitMessage)
	}

	detail.Events = []Event{{Type: EventTypePlanReviewed, Review: persisted}}
	if got := CurrentReview(detail); got != persisted {
		t.Fatalf("CurrentReview without reopen = %p, want persisted review %p", got, persisted)
	}
	if got := PersistedReview(detail); got != persisted {
		t.Fatalf("PersistedReview without reopen = %p, want persisted review %p", got, persisted)
	}

	detail.Events = append(detail.Events, Event{Type: EventTypePlanReopened})
	if got := CurrentReview(detail); got != nil {
		t.Fatalf("CurrentReview after reopen = %#v, want nil", got)
	}
	if got := PersistedReview(detail); got != persisted {
		t.Fatalf("PersistedReview after reopen = %p, want persisted review %p", got, persisted)
	}

	if got := CurrentReview(nil); got != nil {
		t.Fatalf("CurrentReview(nil) = %#v, want nil", got)
	}
	if got := PersistedReview(nil); got != nil {
		t.Fatalf("PersistedReview(nil) = %#v, want nil", got)
	}
	SetPersistedReview(nil, review)
}

// TestReviewSupersededByReopenIgnoresFailedReviewEvents guards the reopen
// guard against being reset by a failed-review event: RecordReviewError copies
// its head snapshot from pre-reopen state, so letting it supersede the reopen
// would restore merge trust in stale heads while the rework is unreviewed —
// external-merge detection could then re-record the old merge and delete the
// branch holding the rework commits.
func TestReviewSupersededByReopenIgnoresFailedReviewEvents(t *testing.T) {
	failed := []Event{
		{Type: EventTypePlanMerged},
		{Type: EventTypePlanReopened},
		{Type: EventTypePlanReviewed, Review: &PlanReview{Status: ReviewStatusError}},
	}
	if !ReviewSupersededByReopen(failed) {
		t.Fatal("failed review after reopen must not supersede the reopen")
	}

	completed := []Event{
		{Type: EventTypePlanMerged},
		{Type: EventTypePlanReopened},
		{Type: EventTypePlanReviewed, Review: &PlanReview{Status: ReviewStatusError}},
		{Type: EventTypePlanReviewed, Review: &PlanReview{Status: ReviewStatusCompleted}},
	}
	if ReviewSupersededByReopen(completed) {
		t.Fatal("completed review after reopen must supersede the reopen")
	}

	legacy := []Event{
		{Type: EventTypePlanMerged},
		{Type: EventTypePlanReopened},
		{Type: EventTypePlanReviewed},
	}
	if ReviewSupersededByReopen(legacy) {
		t.Fatal("legacy review event without payload keeps the historical reset")
	}
}

func TestRunCapabilitiesNeedsApprovalFromRunnableError(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Approval: &Approval{Required: true, Reason: "human sign-off"}}}},
	}
	capabilities := AnalyzeRunCapabilities(detail)
	if capabilities.CanRun {
		t.Fatalf("expected approval-gated slice to not be runnable: %+v", capabilities)
	}
	if !capabilities.NeedsApproval {
		t.Fatalf("expected NeedsApproval=true, got %+v", capabilities)
	}
	if capabilities.ApprovalSliceID != "001-a" {
		t.Fatalf("expected ApprovalSliceID=001-a, got %+v", capabilities)
	}
	if capabilities.ApprovalReason != "human sign-off" {
		t.Fatalf("expected ApprovalReason=human sign-off, got %+v", capabilities)
	}
	if capabilities.DisabledReason != "slice 001-a requires approval: human sign-off" {
		t.Fatalf("expected DisabledReason to match legacy prose, got %q", capabilities.DisabledReason)
	}
}

func TestRunCapabilitiesNeedsApprovalFromContinueError(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusBlocked, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusBlocked, Approval: &Approval{Required: true, Reason: "team review"}}}},
	}
	capabilities := AnalyzeRunCapabilities(detail)
	if !capabilities.NeedsApproval {
		t.Fatalf("expected NeedsApproval=true from ContinueError path, got %+v", capabilities)
	}
	if capabilities.ApprovalSliceID != "001-a" {
		t.Fatalf("expected ApprovalSliceID=001-a, got %+v", capabilities)
	}
	if capabilities.ApprovalReason != "team review" {
		t.Fatalf("expected ApprovalReason=team review, got %+v", capabilities)
	}
}

func TestRunCapabilitiesNoApprovalWhenRunnable(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
	}
	capabilities := AnalyzeRunCapabilities(detail)
	if capabilities.NeedsApproval || capabilities.ApprovalSliceID != "" || capabilities.ApprovalReason != "" {
		t.Fatalf("expected no approval gate for runnable plan, got %+v", capabilities)
	}
}

func TestApprovalRequiredErrorCarriesTypedFields(t *testing.T) {
	detail := &PlanDetail{
		State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan", PendingSlices: []string{"001-a"}}},
		Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending, Approval: &Approval{Required: true, Reason: "security audit"}}}},
	}
	lifecycle := AnalyzeLifecycle(detail)
	var approvalErr *ApprovalRequiredError
	if !errors.As(lifecycle.RunnableError, &approvalErr) {
		t.Fatalf("expected ApprovalRequiredError from RunnableError, got %T: %v", lifecycle.RunnableError, lifecycle.RunnableError)
	}
	if approvalErr.SliceID != "001-a" {
		t.Fatalf("expected SliceID=001-a, got %q", approvalErr.SliceID)
	}
	if approvalErr.Reason != "security audit" {
		t.Fatalf("expected Reason=security audit, got %q", approvalErr.Reason)
	}
	want := "slice 001-a requires approval: security audit"
	if got := approvalErr.Error(); got != want {
		t.Fatalf("expected Error()=%q, got %q", want, got)
	}
}

func TestSliceCompletionPending(t *testing.T) {
	detail := &PlanDetail{Slices: SlicesFile{Slices: []Slice{{
		ID:           "001-a",
		CommitIntent: &SliceCommitIntent{Policy: "slice"},
	}}}}
	if !SliceCompletionPending(detail) {
		t.Fatal("automatic slice intent without completion must remain pending")
	}

	detail.Slices.Slices[0].Completion = &SliceCompletionOutcome{Outcome: SliceCompletionCommitted, CommitSHA: "abc123"}
	if SliceCompletionPending(detail) {
		t.Fatal("valid committed completion must settle the pending signal")
	}
	if SliceCompletionPending(nil) {
		t.Fatal("nil detail must not report pending completion")
	}
}

func TestHasUnresolvedReworkStopUsesLatestAttemptSignal(t *testing.T) {
	tests := []struct {
		name   string
		events []Event
		want   bool
	}{
		{name: "none"},
		{name: "stopped", events: []Event{{Type: EventTypeReworkStopped}}, want: true},
		{name: "reopened", events: []Event{{Type: EventTypeReworkStopped}, {Type: EventTypePlanReopened}}},
		{name: "restart round", events: []Event{{Type: EventTypeReworkStopped}, {Type: EventTypeReworkRound}}},
		{name: "new stop", events: []Event{{Type: EventTypeReworkStopped}, {Type: EventTypePlanReopened}, {Type: EventTypeReworkStopped}}, want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := HasUnresolvedReworkStop(test.events); got != test.want {
				t.Fatalf("HasUnresolvedReworkStop() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestSummarizeExposesAttentionSignals(t *testing.T) {
	detail := &PlanDetail{
		State: State{Status: StatusChangesRequested, Plan: PlanState{ID: "plan"}},
		Slices: SlicesFile{Slices: []Slice{{
			ID:           "001-a",
			CommitIntent: &SliceCommitIntent{Policy: "slice"},
		}}},
		Events: []Event{{Type: EventTypeReworkStopped}},
	}
	summary := Summarize(detail, time.Time{})
	if !summary.SliceCompletionPending || !summary.UnresolvedReworkStop {
		t.Fatalf("summary attention signals = completion:%t rework:%t", summary.SliceCompletionPending, summary.UnresolvedReworkStop)
	}
}

func TestDeriveNextActionLifecyclePrecedence(t *testing.T) {
	pending := func() *PlanDetail {
		return &PlanDetail{
			State:  State{Status: StatusPlanned, Plan: PlanState{ID: "plan-a", PendingSlices: []string{"001-a"}}},
			Slices: SlicesFile{Slices: []Slice{{ID: "001-a", Status: StatusPending}}},
		}
	}
	complete := reviewedCapabilityDetail
	approve := func() *PlanReview {
		return &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove, Head: "head123"}
	}
	changes := func() *PlanReview {
		return &PlanReview{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested, Head: "head123"}
	}

	tests := []struct {
		name       string
		makeDetail func() *PlanDetail
		wantKind   PlanActionKind
		wantClass  PlanActionClass
		wantReason string
	}{
		{
			name: "unsettled post-intent outranks approval and merge evidence",
			makeDetail: func() *PlanDetail {
				d := pending()
				d.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "sign off"}
				d.Slices.Slices[0].CommitIntent = &SliceCommitIntent{Policy: "slice"}
				d.Events = []Event{{Type: EventTypePlanMerged}}
				return d
			},
			wantKind: PlanActionRecoverSliceCompletion, wantClass: PlanActionClassRecovery, wantReason: "not settled",
		},
		{
			name: "rework stop outranks approval",
			makeDetail: func() *PlanDetail {
				d := pending()
				d.Slices.Slices[0].Approval = &Approval{Required: true}
				d.Events = []Event{{Type: EventTypeReworkStopped}}
				return d
			},
			wantKind: PlanActionRestartRework, wantClass: PlanActionClassRecovery, wantReason: "explicit bounded restart",
		},
		{
			name: "approval outranks interrupted active run",
			makeDetail: func() *PlanDetail {
				d := pending()
				d.State.Status = StatusInProgress
				d.State.Plan.CurrentSlice = ptrString("001-a")
				d.Slices.Slices[0].Status = StatusInProgress
				d.Slices.Slices[0].Approval = &Approval{Required: true, Reason: "sign off"}
				return d
			},
			wantKind: PlanActionApprove, wantClass: PlanActionClassProgress, wantReason: "sign off",
		},
		{
			name: "blocked recovery",
			makeDetail: func() *PlanDetail {
				d := pending()
				d.State.Status = StatusBlocked
				d.State.Plan.CurrentSlice = ptrString("001-a")
				d.Slices.Slices[0].Status = StatusBlocked
				return d
			},
			wantKind: PlanActionContinue, wantClass: PlanActionClassRecovery, wantReason: "blocker",
		},
		{
			name: "interrupted pre-intent",
			makeDetail: func() *PlanDetail {
				d := pending()
				d.State.Status = StatusInProgress
				d.State.Plan.CurrentSlice = ptrString("001-a")
				d.Slices.Slices[0].Status = StatusInProgress
				return d
			},
			wantKind: PlanActionRun, wantClass: PlanActionClassRecovery, wantReason: "interrupted",
		},
		{name: "runnable", makeDetail: pending, wantKind: PlanActionRun, wantClass: PlanActionClassProgress, wantReason: "runnable"},
		{
			name:       "awaiting review",
			makeDetail: complete,
			wantKind:   PlanActionReview, wantClass: PlanActionClassProgress, wantReason: "approved review",
		},
		{
			name:       "changes requested",
			makeDetail: func() *PlanDetail { d := complete(); d.State.Plan.Review = changes(); return d },
			wantKind:   PlanActionRework, wantClass: PlanActionClassProgress, wantReason: "actionable changes",
		},
		{
			name:       "reviewed and approved",
			makeDetail: func() *PlanDetail { d := complete(); d.State.Plan.Review = approve(); return d },
			wantKind:   PlanActionMerge, wantClass: PlanActionClassProgress, wantReason: "approves",
		},
		{
			name: "pull request completed without merge assertion",
			makeDetail: func() *PlanDetail {
				d := complete()
				d.State.Plan.Review = approve()
				d.State.Plan.PullRequest = &PullRequest{HeadSHA: "head123"}
				return d
			},
			wantKind: PlanActionNone, wantClass: PlanActionClassTerminal, wantReason: "remote integration is not asserted",
		},
		{
			name:       "recorded merge",
			makeDetail: func() *PlanDetail { d := complete(); d.Events = []Event{{Type: EventTypePlanMerged}}; return d },
			wantKind:   PlanActionNone, wantClass: PlanActionClassTerminal, wantReason: "proves the plan is integrated",
		},
		{
			name:       "legacy completed",
			makeDetail: func() *PlanDetail { d := complete(); d.State.Status = StatusCompleted; return d },
			wantKind:   PlanActionNone, wantClass: PlanActionClassTerminal, wantReason: "without asserting merge evidence",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			action := Derive(tt.makeDetail(), time.Time{}).NextAction
			if action.Primary.Kind != tt.wantKind || action.Primary.Class != tt.wantClass || !strings.Contains(action.Primary.Reason, tt.wantReason) {
				t.Fatalf("primary action = %+v, want kind=%q class=%q reason containing %q", action.Primary, tt.wantKind, tt.wantClass, tt.wantReason)
			}
			if action.Primary.Reason == "" {
				t.Fatal("primary recommendation must always carry one concise reason")
			}
		})
	}
}

func TestDeriveNextActionDescribesSliceRecoveryWithoutPartialCommand(t *testing.T) {
	detail := &PlanDetail{
		State: State{Status: StatusInProgress, Plan: PlanState{ID: "plan-a", CurrentSlice: ptrString("001-a")}},
		Slices: SlicesFile{Slices: []Slice{{
			ID:           "001-a",
			Status:       StatusInProgress,
			CommitIntent: &SliceCommitIntent{Policy: "slice"},
		}}},
	}

	action := DeriveNextAction(detail).Primary
	if action.Command != "" {
		t.Fatalf("recovery command = %q, want no incomplete executable command", action.Command)
	}
	if !strings.Contains(action.Instruction, "original complete tao slice-complete invocation") {
		t.Fatalf("recovery instruction = %q, want explicit original-invocation guidance", action.Instruction)
	}
}

func TestDeriveNextActionClassifiesForcedAlternativesAsAdministrative(t *testing.T) {
	for _, review := range []*PlanReview{
		{Status: ReviewStatusCompleted, Verdict: ReviewVerdictChangesRequested},
		{Status: ReviewStatusCompleted, Verdict: ReviewVerdictApprove},
	} {
		detail := reviewedCapabilityDetail()
		detail.State.Plan.Review = review
		next := DeriveNextAction(detail)
		if len(next.Alternatives) != 1 {
			t.Fatalf("alternatives = %+v, want one forced administrative exception", next.Alternatives)
		}
		alternative := next.Alternatives[0]
		if alternative.Class != PlanActionClassAdministrative || alternative.Kind != PlanActionMerge || alternative.Command != "tao merge --force plan" {
			t.Fatalf("forced alternative was not visibly administrative: %+v", alternative)
		}
	}
}

func ptrString(value string) *string { return &value }

func reviewedCapabilityDetail() *PlanDetail {
	completedAt := time.Date(2026, 6, 28, 6, 40, 0, 0, time.UTC)
	return &PlanDetail{
		State: State{
			Status: StatusPlanned,
			Plan: PlanState{
				ID:              "plan",
				CompletedSlices: []string{"001-a"},
			},
		},
		Slices: SlicesFile{Slices: []Slice{{
			ID:     "001-a",
			Status: StatusCompleted,
			Timing: SliceTiming{CompletedAt: &completedAt},
		}}},
	}
}
