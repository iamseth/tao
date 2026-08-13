package rework

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

// PlanResolver loads the latest persisted detail for a plan.
type PlanResolver func(context.Context, string) (*plan.PlanDetail, error)

// RecordFactory binds a loaded plan detail to its lifecycle mutation boundary.
type RecordFactory func(*plan.PlanDetail) (Record, error)

// Clock supplies the time used for a rework mutation.
type Clock func() time.Time

// ExecuteFunc executes the plan once.
type ExecuteFunc func(context.Context) error

// PersistProgressFunc durably records automatic-rework progress before a rerun.
type PersistProgressFunc func(context.Context, int, int, string) error

// DecisionFunc applies at most one automatic-rework decision.
type DecisionFunc func(context.Context, string, int, int, string, int) (Decision, error)

// ProgressLogger reports a successfully reopened rework round.
type ProgressLogger func(int) error

// DecisionCheck refreshes dynamic policy and stop state before a decision. The
// returned maximum applies to that decision; false ends the run without
// another decision.
type DecisionCheck func(context.Context) (maxAttempts int, proceed bool, err error)

const equivalentFindingsStopReason = "automatic rework stalled on equivalent consecutive findings"

// StopKind identifies why bounded automatic rework stopped.
type StopKind string

const (
	StopKindNone            StopKind = ""
	StopKindCapExhausted    StopKind = "cap_exhausted"
	StopKindFindingsStalled StopKind = "findings_stalled"
	StopKindRecurringFiles  StopKind = "recurring_files"
)

// StopKindForPersistedReason upgrades the frozen reason field in historical
// rework_stopped events. Live decisions set StopKind directly.
func StopKindForPersistedReason(reason string) StopKind {
	if reason == equivalentFindingsStopReason {
		return StopKindFindingsStalled
	}
	if _, ok := recurringFilesFromStopReason(reason); ok {
		return StopKindRecurringFiles
	}
	var maxAttempts int
	if _, err := fmt.Sscanf(reason, "automatic rework cap exhausted after %d cycles", &maxAttempts); err == nil && reason == fmt.Sprintf("automatic rework cap exhausted after %d cycles", maxAttempts) {
		return StopKindCapExhausted
	}
	return StopKindNone
}

// Decision describes the outcome of inspecting and possibly reopening a plan.
type Decision struct {
	Reworked       bool
	Round          int
	BaselineRound  int
	Fingerprint    string
	StopKind       StopKind
	StopReason     string
	RecurringFiles []string
	Findings       []plan.ReviewFinding
}

// GuardAutoReworkRestart interprets persisted automatic-rework events and
// refuses a fresh budget after a stop unless allowRestart is true. Stopped
// reports whether the latest relevant event is rework_stopped.
func GuardAutoReworkRestart(detail *plan.PlanDetail, allowRestart bool) (Decision, bool, error) {
	decision, stopped := mostRecentAutoReworkStop(detail)
	if !stopped || allowRestart {
		return decision, stopped, nil
	}
	return decision, true, autoReworkRestartRefusal(decision)
}

func mostRecentAutoReworkStop(detail *plan.PlanDetail) (Decision, bool) {
	if detail == nil || detail.State.Status != plan.StatusChangesRequested {
		return Decision{}, false
	}
	for _, event := range slices.Backward(detail.Events) {
		switch event.Type {
		case plan.EventTypeReworkRound, plan.EventTypePlanReopened:
			return Decision{}, false
		case plan.EventTypeReworkStopped:
			reason := event.Reason
			if reason == "" {
				reason = event.Message
			}
			if reason == "" {
				reason = "automatic rework previously stopped"
			}
			recurringFiles, _ := recurringFilesFromStopReason(reason)
			return Decision{StopKind: StopKindForPersistedReason(reason), StopReason: reason, RecurringFiles: recurringFiles, Findings: ReviewFindings(detail)}, true
		}
	}
	return Decision{}, false
}

func autoReworkRestartRefusal(decision Decision) error {
	return fmt.Errorf("%s\n\nA new automatic-rework budget was not started. To deliberately continue, rerun with --rework-restart", FormatStopMessage(decision))
}

// FormatStopMessage turns an automatic-rework stop decision into operator-facing output.
func FormatStopMessage(decision Decision) string {
	if decision.StopReason == "" {
		return ""
	}
	switch decision.StopKind {
	case StopKindCapExhausted:
		return "Automatic rework stopped: attempt cap reached.\nReason: " + decision.StopReason + "\nRead the review and address the remaining findings before re-running."
	case StopKindRecurringFiles:
		return formatRecurringFilesStopMessage(decision)
	case StopKindFindingsStalled:
	default:
		return decision.StopReason
	}

	var message strings.Builder
	message.WriteString("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!\n")
	message.WriteString("AUTOMATIC REWORK STOPPED: THE LOOP IS GOING IN CIRCLES\n")
	message.WriteString("Equivalent blocking findings survived consecutive rework rounds.\n")
	message.WriteString("Reason: " + decision.StopReason + "\n")
	message.WriteString("Read the review and address these findings before re-running:\n")
	for _, finding := range decision.Findings {
		message.WriteString(formatBlockingFinding(finding))
	}
	message.WriteString("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
	return message.String()
}

func formatRecurringFilesStopMessage(decision Decision) string {
	files := slices.Clone(decision.RecurringFiles)
	if len(files) == 0 {
		files, _ = recurringFilesFromStopReason(decision.StopReason)
	}
	slices.Sort(files)
	files = slices.Compact(files)

	var message strings.Builder
	message.WriteString("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!\n")
	message.WriteString("AUTOMATIC REWORK STOPPED: THE SAME FILES KEEP RECURRING\n")
	message.WriteString("Different blocking findings returned in these files across three consecutive reviews:\n")
	for _, file := range files {
		message.WriteString("- " + file + "\n")
	}
	message.WriteString("Reason: " + decision.StopReason + "\n")
	message.WriteString("Read the latest review and address these findings before re-running:\n")
	for _, finding := range decision.Findings {
		message.WriteString(formatBlockingFinding(finding))
	}
	message.WriteString("!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!!")
	return message.String()
}

func formatBlockingFinding(finding plan.ReviewFinding) string {
	severity := strings.TrimSpace(finding.Severity)
	if severity == "" {
		severity = "unspecified"
	}
	location := strings.TrimSpace(finding.File)
	if location == "" {
		location = "review"
	} else if finding.Line > 0 {
		location = fmt.Sprintf("%s:%d", location, finding.Line)
	}
	message := strings.TrimSpace(finding.Message)
	if message == "" {
		message = "Blocking review finding"
	}
	result := fmt.Sprintf("- [%s] %s: %s\n", severity, location, message)
	if suggestion := strings.TrimSpace(finding.Suggestion); suggestion != "" {
		result += "  Suggested fix: " + suggestion + "\n"
	}
	return result
}

// Driver owns automatic-rework decisions and the bounded execution loop.
type Driver struct {
	Resolve     PlanResolver
	Record      RecordFactory
	Now         Clock
	AppendEvent func(string, plan.Event) error

	// DecideOne lets owners retain an existing decision seam while delegating
	// round driving to Loop.
	DecideOne DecisionFunc
}

// ExecutionState is automatic-rework progress recovered by a caller. A nil
// recovered state tells Run to initialize a fresh budget from persisted plan
// detail. DecideBeforeExecute records that the caller already acknowledged a
// persisted stop and needs the plan reopened before execution.
type ExecutionState struct {
	Budget              Budget
	DecideBeforeExecute bool
}

// RunOptions configures the shared automatic-rework execution entry.
type RunOptions struct {
	Enabled      bool
	MaxAttempts  int
	AllowRestart bool
	Recovered    *ExecutionState

	Execute         ExecuteFunc
	PersistProgress PersistProgressFunc
	LogProgress     ProgressLogger
	BeforeDecision  DecisionCheck
}

// LoopOptions configures one bounded automatic-rework loop.
type LoopOptions struct {
	Baseline            int
	Attempts            int
	PreviousFingerprint string
	MaxAttempts         int
	// DecideBeforeExecute reopens an already-reviewed plan before the first
	// execution. Normal runs preserve the execute-then-decide sequence.
	DecideBeforeExecute bool
	Execute             ExecuteFunc
	PersistProgress     PersistProgressFunc
	LogProgress         ProgressLogger
	// BeforeDecision may refresh policy and stop state before each decision.
	// Its returned maximum replaces MaxAttempts for that decision; false ends
	// the loop without another decision. CheckBeforeDecision is the error-aware
	// form used by Run and takes precedence when both are set.
	BeforeDecision      func() (int, bool)
	CheckBeforeDecision DecisionCheck
}

// Decide inspects the latest completed review and applies at most one rework mutation.
func (d Driver) Decide(ctx context.Context, planID string, baseline, attempts int, previous string, maxAttempts int) (Decision, error) {
	budget := Budget{BaselineRound: baseline, Attempts: attempts, PreviousFindingFingerprint: previous}
	if d.DecideOne != nil {
		return d.DecideOne(ctx, planID, budget.BaselineRound, budget.Attempts, budget.PreviousFindingFingerprint, maxAttempts)
	}
	if d.Resolve == nil {
		return Decision{}, errors.New("automatic rework plan resolver is nil")
	}
	detail, err := d.Resolve(ctx, planID)
	if err != nil {
		return Decision{}, err
	}
	review := plan.PersistedReview(detail)
	findings := ReviewFindings(detail)
	if detail == nil || detail.State.Status != plan.StatusChangesRequested || review == nil || review.Status != plan.ReviewStatusCompleted || review.Verdict != plan.ReviewVerdictChangesRequested || len(findings) == 0 {
		return Decision{}, nil
	}
	round := RoundCount(detail)
	if len(GenerateSlices(detail, findings, round+1)) == 0 {
		return Decision{}, nil
	}
	if pullRequestBaseline, reset := pullRequestReworkBaseline(detail, budget.BaselineRound); reset {
		budget.BaselineRound = pullRequestBaseline
		budget.Attempts = max(round-pullRequestBaseline, 0)
		if round == pullRequestBaseline {
			budget.PreviousFindingFingerprint = ""
		}
	} else {
		budget.Attempts = budget.AttemptsAtRound(round)
	}
	fingerprint := ReworkFindingsFingerprint(findings)
	if budget.Attempts >= maxAttempts {
		decision := d.stoppedDecision(detail, planID, budget.BaselineRound, budget.Attempts, round, fingerprint, StopKindCapExhausted, fmt.Sprintf("automatic rework cap exhausted after %d cycles", maxAttempts), findings, nil)
		return decision, nil
	}
	if budget.PreviousFindingFingerprint != "" && budget.PreviousFindingFingerprint == fingerprint {
		decision := d.stoppedDecision(detail, planID, budget.BaselineRound, budget.Attempts, round, fingerprint, StopKindFindingsStalled, equivalentFindingsStopReason, findings, nil)
		return decision, nil
	}
	if recurringFiles := recurringReworkFiles(detail, budget.BaselineRound, findings); len(recurringFiles) > 0 {
		reason := recurringFilesStopReason(recurringFiles)
		decision := d.stoppedDecision(detail, planID, budget.BaselineRound, budget.Attempts, round, fingerprint, StopKindRecurringFiles, reason, findings, recurringFiles)
		return decision, nil
	}
	if d.Record == nil {
		return Decision{}, errors.New("automatic rework record factory is nil")
	}
	record, err := d.Record(detail)
	if err != nil {
		return Decision{}, err
	}
	reopenedAt := d.now()
	if _, err := Reopen(record, reopenedAt); err != nil {
		return Decision{}, err
	}
	decision := Decision{Reworked: true, Round: RoundCount(record.Detail()), BaselineRound: budget.BaselineRound, Fingerprint: fingerprint}
	attempt := budget.Attempts + 1
	d.appendEvent(detail.Dir, plan.Event{Type: plan.EventTypeReworkRound, Timestamp: reopenedAt, PlanID: planID, Round: decision.Round, Attempts: attempt, Fingerprint: fingerprint, Message: fmt.Sprintf("Automatic rework round %d (attempt %d of %d)", decision.Round, attempt, maxAttempts)})
	return decision, nil
}

func (d Driver) stoppedDecision(detail *plan.PlanDetail, planID string, baseline, attempts, round int, fingerprint string, kind StopKind, reason string, findings []plan.ReviewFinding, recurringFiles []string) Decision {
	decision := Decision{
		Round:          round,
		BaselineRound:  baseline,
		Fingerprint:    fingerprint,
		StopKind:       kind,
		StopReason:     reason,
		RecurringFiles: slices.Clone(recurringFiles),
		Findings:       cloneFindings(findings),
	}
	d.appendEvent(detail.Dir, plan.Event{Type: plan.EventTypeReworkStopped, Timestamp: d.now(), PlanID: planID, Round: round, Attempts: attempts, Fingerprint: fingerprint, Reason: reason, Message: reason})
	return decision
}

func (d Driver) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d Driver) appendEvent(dir string, event plan.Event) {
	if d.AppendEvent != nil {
		_ = d.AppendEvent(dir, event)
	}
}

// Run executes a plan through one automatic-rework policy boundary. Disabled
// policy executes once without loading rework state. Enabled fresh runs load a
// baseline and enforce the persisted-stop restart guard; recovered runs use the
// caller's durable budget verbatim.
func (d Driver) Run(ctx context.Context, planID string, opts RunOptions) error {
	if opts.Execute == nil {
		return errors.New("automatic rework execute function is nil")
	}

	state := ExecutionState{}
	enabled := opts.Enabled && opts.MaxAttempts > 0
	if enabled {
		if opts.Recovered != nil {
			state = *opts.Recovered
		} else {
			if d.Resolve == nil {
				return errors.New("automatic rework plan resolver is nil")
			}
			detail, err := d.Resolve(ctx, planID)
			if err != nil {
				return err
			}
			_, stopped, err := GuardAutoReworkRestart(detail, opts.AllowRestart)
			if err != nil {
				return err
			}
			state.Budget = freshAutomaticReworkBudget(detail)
			state.DecideBeforeExecute = stopped && opts.AllowRestart
		}
	}

	loopOptions := LoopOptions{
		Execute:             opts.Execute,
		PersistProgress:     opts.PersistProgress,
		LogProgress:         opts.LogProgress,
		MaxAttempts:         opts.MaxAttempts,
		CheckBeforeDecision: opts.BeforeDecision,
	}
	if enabled {
		loopOptions.Baseline = state.Budget.BaselineRound
		loopOptions.Attempts = state.Budget.Attempts
		loopOptions.PreviousFingerprint = state.Budget.PreviousFindingFingerprint
		loopOptions.DecideBeforeExecute = state.DecideBeforeExecute
	} else {
		loopOptions.MaxAttempts = 0
		loopOptions.CheckBeforeDecision = nil
	}
	return d.Loop(ctx, planID, loopOptions)
}

// freshAutomaticReworkBudget anchors a new run at its current round. In
// particular, a pull-request reopen becomes the baseline rather than an
// automatic attempt, while review-driven reopens inside Loop continue to use
// the invocation's existing budget.
func freshAutomaticReworkBudget(detail *plan.PlanDetail) Budget {
	return Budget{BaselineRound: RoundCount(detail)}
}

// pullRequestReworkBaseline recognizes only rounds generated through the
// pull-request authority arm. An older caller-owned baseline may therefore be
// reset without granting the same exemption to Tao-review-driven rounds.
func pullRequestReworkBaseline(detail *plan.PlanDetail, baseline int) (int, bool) {
	latest := baseline
	if detail == nil {
		return latest, false
	}
	for _, slice := range detail.Slices.Slices {
		round := RoundFromSliceID(slice.ID)
		if round > latest && strings.HasPrefix(slice.Context, pullRequestFindingContext) {
			latest = round
		}
	}
	return latest, latest > baseline
}

// Loop executes a plan and continues through all actionable automatic-rework rounds.
func (d Driver) Loop(ctx context.Context, planID string, opts LoopOptions) error {
	if opts.Execute == nil {
		return errors.New("automatic rework execute function is nil")
	}

	budget := Budget{BaselineRound: opts.Baseline, Attempts: opts.Attempts, PreviousFindingFingerprint: opts.PreviousFingerprint}
	decide := func() (Decision, bool, error) {
		maxAttempts := opts.MaxAttempts
		if opts.CheckBeforeDecision != nil {
			var proceed bool
			var err error
			maxAttempts, proceed, err = opts.CheckBeforeDecision(ctx)
			if err != nil {
				return Decision{}, false, err
			}
			if !proceed {
				return Decision{}, false, nil
			}
		} else if opts.BeforeDecision != nil {
			var proceed bool
			maxAttempts, proceed = opts.BeforeDecision()
			if !proceed {
				return Decision{}, false, nil
			}
		}
		decision, err := d.Decide(ctx, planID, budget.BaselineRound, budget.Attempts, budget.PreviousFindingFingerprint, maxAttempts)
		if err != nil {
			return Decision{}, false, fmt.Errorf("automatic rework mutation failed: %w", err)
		}
		return decision, true, nil
	}
	applyDecision := func(decision Decision) (bool, error) {
		if decision.BaselineRound > budget.BaselineRound {
			budget.BaselineRound = decision.BaselineRound
			budget.Attempts = 0
			budget.PreviousFindingFingerprint = ""
		}
		budget.Attempts = budget.AttemptsAtRound(decision.Round)
		if decision.StopReason != "" {
			return false, errors.New(FormatStopMessage(decision))
		}
		if !decision.Reworked {
			return false, nil
		}
		budget.PreviousFindingFingerprint = decision.Fingerprint
		if opts.PersistProgress != nil {
			if err := opts.PersistProgress(ctx, budget.Attempts, decision.Round, budget.PreviousFindingFingerprint); err != nil {
				return false, err
			}
		}
		if opts.LogProgress != nil {
			if err := opts.LogProgress(decision.Round); err != nil {
				return false, err
			}
		}
		return true, nil
	}

	if opts.DecideBeforeExecute {
		decision, proceed, err := decide()
		if err != nil || !proceed {
			return err
		}
		if _, err := applyDecision(decision); err != nil {
			return err
		}
	}
	if err := opts.Execute(ctx); err != nil {
		return err
	}
	if opts.MaxAttempts == 0 && opts.BeforeDecision == nil && opts.CheckBeforeDecision == nil {
		return nil
	}

	for {
		decision, proceed, err := decide()
		if err != nil || !proceed {
			return err
		}
		reworked, err := applyDecision(decision)
		if err != nil || !reworked {
			return err
		}
		if err := opts.Execute(ctx); err != nil {
			return err
		}
	}
}
