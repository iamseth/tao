package insights

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plannerroute"
)

const maxExemplars = 2

// PlanLister is the repository boundary needed for cross-plan aggregation.
type PlanLister interface {
	ListPlans(context.Context, plan.PlanFilter) ([]plan.PlanSummary, error)
}

// RouteLister supplies advisory repository-scoped planner assignments.
type RouteLister interface {
	ListRoutes(context.Context) ([]plannerroute.Record, []string, error)
}

// SourceLister supplies registered repository data stores. Sources deliberately
// contain no checkout path, so callers can inspect history for unhealthy roots.
type SourceLister interface {
	ListInsightSources(context.Context) ([]RepositorySource, error)
}

// RepositorySource identifies one repository and its data-home plan store.
type RepositorySource struct {
	ID     string
	Name   string
	Plans  PlanLister
	Routes RouteLister
}

// Report contains advisory telemetry derived from plan history.
type Report struct {
	PlansScanned       int                  `json:"plans_scanned"`
	PlansSkipped       int                  `json:"plans_skipped"`
	RepositoryCoverage RepositoryCoverage   `json:"repository_coverage"`
	BlockedReasons     []ReasonBucket       `json:"blocked_reasons"`
	ReworkPlans        []ReworkPlan         `json:"rework_plans"`
	Signals            SignalCounts         `json:"signals"`
	SignalEvidence     SignalEvidence       `json:"signal_evidence"`
	OutputTokens       Percentiles          `json:"output_tokens"`
	Cost               Percentiles          `json:"cost"`
	OutlierPlans       []PlanOutlier        `json:"outlier_plans"`
	RecentLogs         RecentLogReport      `json:"recent_logs"`
	PlannerRouting     PlannerRoutingReport `json:"planner_routing"`
}

// PlannerRoutingReport joins advisory assignments to current summary status.
// LinkedPlans counts resolvable links; missing links are reported separately.
type PlannerRoutingReport struct {
	Records             int                   `json:"records"`
	Shadow              int                   `json:"shadow"`
	Randomized          int                   `json:"randomized"`
	Overrides           int                   `json:"overrides"`
	AttemptsWithoutPlan int                   `json:"attempts_without_plan"`
	LinkedPlans         int                   `json:"linked_plans"`
	MissingLinkedPlans  int                   `json:"missing_linked_plans"`
	Censored            int                   `json:"censored"`
	Matured             int                   `json:"matured"`
	Policies            []RoutingPolicyCohort `json:"policies"`
	Warnings            []string              `json:"warnings,omitempty"`
}

type RoutingPolicyCohort struct {
	PolicyVersion string             `json:"policy_version"`
	Mode          string             `json:"mode"`
	Arms          []RoutingArmCohort `json:"arms"`
}

// WeightSum includes every weighted assignment. WeightedCompletionRate uses
// only matured, randomized, non-override assignments in its denominator.
type RoutingArmCohort struct {
	ArmKey                 string   `json:"arm_key"`
	Assigned               int      `json:"assigned"`
	Overrides              int      `json:"overrides"`
	Linked                 int      `json:"linked"`
	Matured                int      `json:"matured"`
	Completed              int      `json:"completed"`
	Abandoned              int      `json:"abandoned"`
	WeightSum              float64  `json:"weight_sum"`
	WeightedCompletionRate *float64 `json:"weighted_completion_rate,omitempty"`
}

type routingCohortKey struct{ policy, mode, arm string }
type routingAccumulator struct {
	cohort                         RoutingArmCohort
	maturedWeight, completedWeight float64
}

// RepositoryCoverage records which registered stores contributed to a report.
type RepositoryCoverage struct {
	Scanned      int                    `json:"scanned"`
	Skipped      int                    `json:"skipped"`
	Unreadable   int                    `json:"unreadable"`
	Empty        int                    `json:"empty"`
	Repositories []RepositoryScanResult `json:"repositories,omitempty"`
}

// RepositoryScanResult reports the deterministic outcome for one source.
type RepositoryScanResult struct {
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name,omitempty"`
	Status         string `json:"status"`
}

// EvidenceExemplar qualifies an exemplar with its stable repository identity.
type EvidenceExemplar struct {
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name,omitempty"`
	Value          string `json:"value"`
}

// ReasonRepository records one repository's contribution to a blocked-reason bucket.
type ReasonRepository struct {
	RepositoryID   string `json:"repository_id"`
	RepositoryName string `json:"repository_name,omitempty"`
	Count          int    `json:"count"`
}

// ReasonBucket groups equivalent slice-blocked messages while retaining bounded examples.
type ReasonBucket struct {
	Reason             string             `json:"reason"`
	Count              int                `json:"count"`
	Exemplars          []string           `json:"exemplars"`
	QualifiedExemplars []EvidenceExemplar `json:"qualified_exemplars,omitempty"`
	Repositories       []ReasonRepository `json:"repositories,omitempty"`
}

// ReworkPlan identifies a plan with at least three rework rounds.
type ReworkPlan struct {
	RepositoryID   string   `json:"repository_id,omitempty"`
	RepositoryName string   `json:"repository_name,omitempty"`
	PlanID         string   `json:"plan_id"`
	Rounds         int      `json:"rounds"`
	StoppedReasons []string `json:"stopped_reasons,omitempty"`
}

// SignalCounts counts recurring operational failure events.
type SignalCounts struct {
	SessionTimeout             int `json:"session_timeout"`
	SliceResumeFailed          int `json:"slice_resume_failed"`
	VerificationCommandInvalid int `json:"verification_command_invalid"`
	VerificationRepairStopped  int `json:"verification_repair_stopped"`
	PlanCommitFallback         int `json:"plan_commit_fallback"`
	PlanCommitGuard            int `json:"plan_commit_guard"`
}

// SignalEvidence describes the observed breadth and recency of structured
// operational events. Resume attempts remain separate from resume failures.
type SignalEvidence struct {
	SessionTimeout             SignalObservation `json:"session_timeout"`
	SliceResumeAttempted       SignalObservation `json:"slice_resume_attempted"`
	SliceResumeFailed          SignalObservation `json:"slice_resume_failed"`
	VerificationCommandInvalid SignalObservation `json:"verification_command_invalid"`
	VerificationRepairStopped  SignalObservation `json:"verification_repair_stopped"`
	PlanCommitFallback         SignalObservation `json:"plan_commit_fallback"`
	PlanCommitGuard            SignalObservation `json:"plan_commit_guard"`
}

// SignalObservation summarizes one structured event type. MissingTimestamps
// records events whose occurrence cannot be ordered; a positive Count with no
// LatestTimestamp means every observed event lacked a timestamp.
type SignalObservation struct {
	Count             int        `json:"count"`
	Plans             int        `json:"plans"`
	Repositories      int        `json:"repositories"`
	MissingTimestamps int        `json:"missing_timestamps"`
	LatestTimestamp   *time.Time `json:"latest_timestamp,omitempty"`
}

// Percentiles contains nearest-rank percentiles over individual agent sessions.
type Percentiles struct {
	Sessions int     `json:"sessions"`
	P50      float64 `json:"p50"`
	P90      float64 `json:"p90"`
	P95      float64 `json:"p95"`
}

// PlanOutlier flags plans containing a session strictly above the report-wide p95.
type PlanOutlier struct {
	RepositoryID        string  `json:"repository_id,omitempty"`
	RepositoryName      string  `json:"repository_name,omitempty"`
	PlanID              string  `json:"plan_id"`
	OutputTokens        int64   `json:"output_tokens"`
	Cost                float64 `json:"cost"`
	OutputTokensOutlier bool    `json:"output_tokens_outlier"`
	CostOutlier         bool    `json:"cost_outlier"`
}

type accumulator struct {
	buckets  map[string]*ReasonBucket
	sessions []session
	signals  map[string]*signalAccumulator
	logs     *logAccumulator
	routes   map[routingCohortKey]*routingAccumulator
}

type planData struct {
	reworkEvents   []plan.Event
	stopReasons    []string
	blockedReasons []string
	sessions       map[string][]plan.AgentMetricEvent
	signals        []signalEvent
}

type signalEvent struct {
	typeName  string
	timestamp time.Time
}

type signalAccumulator struct {
	count             int
	plans             map[sourcePlanIdentity]struct{}
	repositories      map[string]struct{}
	missingTimestamps int
	latest            time.Time
}

type sourcePlanIdentity struct {
	repository string
	plan       string
}

type session struct {
	repositoryID   string
	repositoryName string
	planID         string
	outputTokens   int64
	cost           float64
}

type sourceIdentity struct {
	id   string
	name string
}

// Aggregate walks one repository's plans and preserves the repository-scoped API.
// Individual unreadable plan logs are counted and skipped so historical damage
// cannot suppress the report.
func Aggregate(ctx context.Context, repository PlanLister) (Report, error) {
	return AggregateWithRoutes(ctx, repository, nil)
}

// AggregateWithRoutes adds optional, non-authoritative routing evidence.
func AggregateWithRoutes(ctx context.Context, repository PlanLister, routes RouteLister) (Report, error) {
	report := Report{}
	acc := newAccumulator()
	now := time.Now()
	if err := aggregateSource(ctx, &report, &acc, sourceIdentity{}, repository, routes, now); err != nil {
		return Report{}, err
	}
	if err := scanRecentLogs(ctx, &report, acc.logs, now); err != nil {
		return Report{}, err
	}
	finalize(&report, &acc)
	return report, nil
}

// AggregateSources combines raw event and session evidence from every source.
// A damaged source is reported and skipped without suppressing readable stores.
func AggregateSources(ctx context.Context, lister SourceLister) (Report, error) {
	sources, err := lister.ListInsightSources(ctx)
	if err != nil {
		return Report{}, err
	}
	sources = slices.Clone(sources)
	slices.SortFunc(sources, func(a, b RepositorySource) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Name, b.Name))
	})

	report := Report{}
	acc := newAccumulator()
	now := time.Now()
	for _, source := range sources {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		result := RepositoryScanResult{RepositoryID: source.ID, RepositoryName: source.Name}
		switch {
		case strings.TrimSpace(source.ID) == "" || source.Plans == nil:
			result.Status = "skipped"
			report.RepositoryCoverage.Skipped++
		default:
			before := report.PlansScanned + report.PlansSkipped
			err := aggregateSource(ctx, &report, &acc, sourceIdentity{id: source.ID, name: source.Name}, source.Plans, source.Routes, now)
			switch {
			case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
				return Report{}, err
			case err != nil:
				result.Status = "unreadable"
				report.RepositoryCoverage.Unreadable++
			case report.PlansScanned+report.PlansSkipped == before:
				result.Status = "empty"
				report.RepositoryCoverage.Empty++
			default:
				result.Status = "scanned"
				report.RepositoryCoverage.Scanned++
			}
		}
		report.RepositoryCoverage.Repositories = append(report.RepositoryCoverage.Repositories, result)
	}
	if err := scanRecentLogs(ctx, &report, acc.logs, now); err != nil {
		return Report{}, err
	}
	finalize(&report, &acc)
	return report, nil
}

func aggregateSource(ctx context.Context, report *Report, acc *accumulator, repository sourceIdentity, plans PlanLister, routes RouteLister, now time.Time) error {
	summaries, err := plans.ListPlans(ctx, plan.PlanFilter{})
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil {
		summaries = nil
	}
	if routeErr := aggregateRoutes(ctx, &report.PlannerRouting, acc.routes, routes, summaries); routeErr != nil {
		return routeErr
	}
	if err != nil {
		return err
	}
	summaries = slices.Clone(summaries)
	slices.SortFunc(summaries, func(a, b plan.PlanSummary) int {
		return cmp.Or(cmp.Compare(a.ID, b.ID), cmp.Compare(a.Dir, b.Dir))
	})
	for _, summary := range summaries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if summary.Status == plan.StatusInvalid {
			report.PlansSkipped++
			continue
		}
		discoverRecentLog(report, acc.logs, now, planSummary{id: summary.ID, dir: summary.Dir, lastActivity: summary.LastActivityAt}, repository)
		data, err := readPlanEvents(ctx, summary.Dir)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			report.PlansSkipped++
			continue
		}
		report.PlansScanned++
		mergePlan(report, acc, repository, summary.ID, data)
	}
	return nil
}

func aggregateRoutes(ctx context.Context, report *PlannerRoutingReport, cohorts map[routingCohortKey]*routingAccumulator, routes RouteLister, summaries []plan.PlanSummary) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if routes == nil {
		return nil
	}
	records, warnings, err := routes.ListRoutes(ctx)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if err != nil {
		warnings = append(warnings, "planner routing unavailable: "+err.Error())
	}
	warnings = slices.Clone(warnings)
	slices.Sort(warnings)
	for _, warning := range warnings {
		addRoutingWarning(report, warning)
	}
	statuses := make(map[string]string, len(summaries))
	for _, summary := range summaries {
		if summary.Status != plan.StatusInvalid {
			statuses[summary.ID] = summary.Status
		}
	}
	records = slices.Clone(records)
	slices.SortFunc(records, func(a, b plannerroute.Record) int { return cmp.Compare(a.ID, b.ID) })
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return err
		}
		a := record.Assignment
		report.Records++
		switch a.Mode {
		case plannerroute.ModeShadow:
			report.Shadow++
		case plannerroute.ModeRandomized:
			report.Randomized++
		}
		key := routingCohortKey{a.PolicyVersion, string(a.Mode), a.Selected.Key()}
		bucket := cohorts[key]
		if bucket == nil {
			bucket = &routingAccumulator{cohort: RoutingArmCohort{ArmKey: routingText(key.arm)}}
			cohorts[key] = bucket
		}
		arm := &bucket.cohort
		arm.Assigned++
		if a.ManualOverride {
			report.Overrides++
			arm.Overrides++
		}
		weight := routingWeight(a)
		if a.Mode == plannerroute.ModeRandomized && !a.ManualOverride && weight == 0 {
			addRoutingWarning(report, "planner route "+record.ID+": invalid selected-arm probability; weight omitted")
		}
		arm.WeightSum += weight
		id := record.LinkedPlanID()
		if id == "" {
			for _, entry := range record.Entries {
				if entry.Kind == plannerroute.EntryAttempt {
					report.AttemptsWithoutPlan++
					break
				}
			}
			continue
		}
		status, found := statuses[id]
		if !found {
			report.MissingLinkedPlans++
			continue
		}
		report.LinkedPlans++
		arm.Linked++
		switch status {
		case plan.StatusCompleted:
			arm.Completed++
			bucket.completedWeight += weight
		case plan.StatusAbandoned:
			arm.Abandoned++
		default:
			report.Censored++
			continue
		}
		report.Matured++
		arm.Matured++
		bucket.maturedWeight += weight
	}
	return ctx.Err()
}

func routingWeight(a plannerroute.Assignment) float64 {
	if a.Mode != plannerroute.ModeRandomized || a.ManualOverride {
		return 0
	}
	for _, eligible := range a.Eligible {
		if eligible.Arm != a.Selected {
			continue
		}
		p := eligible.Probability
		if p <= 0 || p > 1 || math.IsNaN(p) || math.IsInf(1/p, 0) {
			return 0
		}
		return 1 / p
	}
	return 0
}

func routingText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
	return strings.ToValidUTF8(sanitizeExcerpt(value), "")
}

func addRoutingWarning(report *PlannerRoutingReport, warning string) {
	if len(report.Warnings) < maxExemplars {
		report.Warnings = append(report.Warnings, routingText(warning))
	}
}

func finalizeRoutes(report *PlannerRoutingReport, cohorts map[routingCohortKey]*routingAccumulator) {
	keys := make([]routingCohortKey, 0, len(cohorts))
	for key := range cohorts {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b routingCohortKey) int {
		return cmp.Or(cmp.Compare(a.policy, b.policy), cmp.Compare(a.mode, b.mode), cmp.Compare(a.arm, b.arm))
	})
	var previous routingCohortKey
	for i, key := range keys {
		if i == 0 || key.policy != previous.policy || key.mode != previous.mode {
			report.Policies = append(report.Policies, RoutingPolicyCohort{PolicyVersion: routingText(key.policy), Mode: routingText(key.mode)})
		}
		bucket := cohorts[key]
		if bucket.maturedWeight > 0 {
			rate := bucket.completedWeight / bucket.maturedWeight
			bucket.cohort.WeightedCompletionRate = &rate
		}
		policy := &report.Policies[len(report.Policies)-1]
		policy.Arms = append(policy.Arms, bucket.cohort)
		previous = key
	}
}

func readPlanEvents(ctx context.Context, dir string) (planData, error) {
	data := planData{sessions: make(map[string][]plan.AgentMetricEvent)}
	file, err := os.Open(filepath.Join(dir, "events.jsonl")) // #nosec G304 -- plan directories come from the repository listing.
	if errors.Is(err, os.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return planData{}, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return planData{}, err
		}
		line++
		var event plan.Event
		if json.Unmarshal(scanner.Bytes(), &event) != nil || event.Type == "" {
			continue
		}
		consumeEvent(&data, event, line)
	}
	if err := scanner.Err(); err != nil {
		return planData{}, err
	}
	return data, nil
}

func consumeEvent(data *planData, event plan.Event, line int) {
	switch event.Type {
	case plan.EventTypeSliceBlocked:
		data.blockedReasons = append(data.blockedReasons, eventReason(event))
	case plan.EventTypeReworkRound:
		data.reworkEvents = append(data.reworkEvents, event)
	case plan.EventTypeReworkStopped:
		data.reworkEvents = append(data.reworkEvents, event)
		appendUnique(&data.stopReasons, eventReason(event))
	case plan.EventTypeSessionTimeout,
		plan.EventTypeSliceResumeAttempted,
		plan.EventTypeSliceResumeFailed,
		plan.EventTypeVerificationCommandInvalid,
		plan.EventTypeVerificationRepairStopped,
		plan.EventTypePlanCommitFallback,
		plan.EventTypePlanCommitGuard:
		data.signals = append(data.signals, signalEvent{typeName: event.Type, timestamp: event.Timestamp})
	case plan.EventTypeAgentMetrics:
		if event.Metrics == nil {
			return
		}
		key := event.Metrics.SessionID
		if key == "" {
			key = fmt.Sprintf("line:%d", line)
		}
		data.sessions[key] = append(data.sessions[key], plan.AgentMetricEvent{PlanID: event.PlanID, SliceID: event.SliceID, Timestamp: event.Timestamp, Metrics: *event.Metrics})
	}
}

func mergePlan(report *Report, acc *accumulator, repository sourceIdentity, planID string, data planData) {
	for _, event := range data.signals {
		addSignal(acc.signals, repository, planID, event)
	}

	rework := plan.SummarizeRework(data.reworkEvents)
	if rework.Rounds >= 3 {
		report.ReworkPlans = append(report.ReworkPlans, ReworkPlan{RepositoryID: repository.id, RepositoryName: repository.name, PlanID: planID, Rounds: rework.Rounds, StoppedReasons: data.stopReasons})
	}
	for _, reason := range data.blockedReasons {
		addBlocked(acc.buckets, repository, reason)
	}
	for _, events := range data.sessions {
		summary := plan.SummarizeAgentMetrics(events)
		acc.sessions = append(acc.sessions, session{repositoryID: repository.id, repositoryName: repository.name, planID: planID, outputTokens: summary.Totals.OutputTokens, cost: summary.Totals.Cost})
	}
}

func newAccumulator() accumulator {
	return accumulator{
		buckets: make(map[string]*ReasonBucket),
		signals: make(map[string]*signalAccumulator),
		logs:    newLogAccumulator(),
		routes:  make(map[routingCohortKey]*routingAccumulator),
	}
}

func addSignal(signals map[string]*signalAccumulator, repository sourceIdentity, planID string, event signalEvent) {
	signal := signals[event.typeName]
	if signal == nil {
		signal = &signalAccumulator{
			plans:        make(map[sourcePlanIdentity]struct{}),
			repositories: make(map[string]struct{}),
		}
		signals[event.typeName] = signal
	}
	repositoryKey := repository.id
	if repositoryKey == "" {
		repositoryKey = "single-repository"
	}
	signal.count++
	signal.plans[sourcePlanIdentity{repository: repositoryKey, plan: planID}] = struct{}{}
	signal.repositories[repositoryKey] = struct{}{}
	if event.timestamp.IsZero() {
		signal.missingTimestamps++
	} else if event.timestamp.After(signal.latest) {
		signal.latest = event.timestamp.UTC()
	}
}

func addBlocked(buckets map[string]*ReasonBucket, repository sourceIdentity, exemplar string) {
	reason := NormalizeBlockedReason(exemplar)
	bucket := buckets[reason]
	if bucket == nil {
		bucket = &ReasonBucket{Reason: reason}
		buckets[reason] = bucket
	}
	bucket.Count++
	if len(bucket.Exemplars) < maxExemplars {
		appendUnique(&bucket.Exemplars, exemplar)
	}
	qualified := EvidenceExemplar{RepositoryID: repository.id, RepositoryName: repository.name, Value: exemplar}
	if repository.id != "" && len(bucket.QualifiedExemplars) < maxExemplars && !slices.Contains(bucket.QualifiedExemplars, qualified) {
		bucket.QualifiedExemplars = append(bucket.QualifiedExemplars, qualified)
	}
	if repository.id != "" {
		index := slices.IndexFunc(bucket.Repositories, func(item ReasonRepository) bool {
			return item.RepositoryID == repository.id
		})
		if index >= 0 {
			bucket.Repositories[index].Count++
		} else {
			bucket.Repositories = append(bucket.Repositories, ReasonRepository{
				RepositoryID: repository.id, RepositoryName: repository.name, Count: 1,
			})
		}
	}
}

// NormalizeBlockedReason maps common operational variants to stable report buckets.
func NormalizeBlockedReason(message string) string {
	normalized := strings.ToLower(strings.TrimSpace(message))
	switch {
	case containsAny(normalized, "connection refused", "unreachable", "external service", "network unavailable", "service unavailable"):
		return "unreachable_service"
	case containsAny(normalized, "unrelated", "pre-existing", "preexisting", "outside scope"):
		return "unrelated_failure"
	case containsAny(normalized, "invalid verification", "verification command", "invalid command"):
		return "invalid_verification_command"
	case containsAny(normalized, "timed out", "timeout"):
		return "timeout"
	case containsAny(normalized, "dependency", "prerequisite"):
		return "dependency"
	}
	if prefix, _, ok := strings.Cut(normalized, ":"); ok {
		normalized = prefix
	}
	var result strings.Builder
	underscore := false
	for _, r := range normalized {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			result.WriteRune(r)
			underscore = false
		} else if result.Len() > 0 && !underscore {
			result.WriteByte('_')
			underscore = true
		}
	}
	key := strings.Trim(result.String(), "_")
	if key == "" {
		return "unknown"
	}
	return key
}

func finalizeSignals(report *Report, signals map[string]*signalAccumulator) {
	observation := func(typeName string) SignalObservation {
		signal := signals[typeName]
		if signal == nil {
			return SignalObservation{}
		}
		result := SignalObservation{
			Count:             signal.count,
			Plans:             len(signal.plans),
			Repositories:      len(signal.repositories),
			MissingTimestamps: signal.missingTimestamps,
		}
		if !signal.latest.IsZero() {
			latest := signal.latest
			result.LatestTimestamp = &latest
		}
		return result
	}
	report.SignalEvidence = SignalEvidence{
		SessionTimeout:             observation(plan.EventTypeSessionTimeout),
		SliceResumeAttempted:       observation(plan.EventTypeSliceResumeAttempted),
		SliceResumeFailed:          observation(plan.EventTypeSliceResumeFailed),
		VerificationCommandInvalid: observation(plan.EventTypeVerificationCommandInvalid),
		VerificationRepairStopped:  observation(plan.EventTypeVerificationRepairStopped),
		PlanCommitFallback:         observation(plan.EventTypePlanCommitFallback),
		PlanCommitGuard:            observation(plan.EventTypePlanCommitGuard),
	}
	report.Signals = SignalCounts{
		SessionTimeout:             report.SignalEvidence.SessionTimeout.Count,
		SliceResumeFailed:          report.SignalEvidence.SliceResumeFailed.Count,
		VerificationCommandInvalid: report.SignalEvidence.VerificationCommandInvalid.Count,
		VerificationRepairStopped:  report.SignalEvidence.VerificationRepairStopped.Count,
		PlanCommitFallback:         report.SignalEvidence.PlanCommitFallback.Count,
		PlanCommitGuard:            report.SignalEvidence.PlanCommitGuard.Count,
	}
}

func finalize(report *Report, acc *accumulator) {
	finalizeRoutes(&report.PlannerRouting, acc.routes)
	finalizeSignals(report, acc.signals)
	finalizeLogSignals(report, acc.logs)
	for _, bucket := range acc.buckets {
		slices.SortFunc(bucket.Repositories, func(a, b ReasonRepository) int {
			return cmp.Or(cmp.Compare(a.RepositoryID, b.RepositoryID), cmp.Compare(a.RepositoryName, b.RepositoryName))
		})
		report.BlockedReasons = append(report.BlockedReasons, *bucket)
	}
	slices.SortFunc(report.BlockedReasons, func(a, b ReasonBucket) int {
		return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Reason, b.Reason))
	})
	slices.SortFunc(report.ReworkPlans, func(a, b ReworkPlan) int {
		return cmp.Or(cmp.Compare(a.RepositoryID, b.RepositoryID), cmp.Compare(a.PlanID, b.PlanID))
	})

	outputs := make([]float64, 0, len(acc.sessions))
	costs := make([]float64, 0, len(acc.sessions))
	for _, item := range acc.sessions {
		outputs = append(outputs, float64(item.outputTokens))
		costs = append(costs, item.cost)
	}
	report.OutputTokens = calculatePercentiles(outputs)
	report.Cost = calculatePercentiles(costs)

	outliers := make(map[string]*PlanOutlier)
	for _, item := range acc.sessions {
		outputOutlier := float64(item.outputTokens) > report.OutputTokens.P95
		costOutlier := item.cost > report.Cost.P95
		if !outputOutlier && !costOutlier {
			continue
		}
		key := item.repositoryID + "\x00" + item.planID
		outlier := outliers[key]
		if outlier == nil {
			outlier = &PlanOutlier{RepositoryID: item.repositoryID, RepositoryName: item.repositoryName, PlanID: item.planID}
			outliers[key] = outlier
		}
		outlier.OutputTokens = max(outlier.OutputTokens, item.outputTokens)
		outlier.Cost = max(outlier.Cost, item.cost)
		outlier.OutputTokensOutlier = outlier.OutputTokensOutlier || outputOutlier
		outlier.CostOutlier = outlier.CostOutlier || costOutlier
	}
	for _, outlier := range outliers {
		report.OutlierPlans = append(report.OutlierPlans, *outlier)
	}
	slices.SortFunc(report.OutlierPlans, func(a, b PlanOutlier) int {
		return cmp.Or(cmp.Compare(a.RepositoryID, b.RepositoryID), cmp.Compare(a.PlanID, b.PlanID))
	})
}

func calculatePercentiles(values []float64) Percentiles {
	if len(values) == 0 {
		return Percentiles{}
	}
	sort.Float64s(values)
	return Percentiles{Sessions: len(values), P50: nearestRank(values, 0.50), P90: nearestRank(values, 0.90), P95: nearestRank(values, 0.95)}
}

func nearestRank(values []float64, percentile float64) float64 {
	index := max(0, int(math.Ceil(percentile*float64(len(values))))-1)
	return values[index]
}

func eventReason(event plan.Event) string {
	if reason := strings.TrimSpace(event.Reason); reason != "" {
		return reason
	}
	return strings.TrimSpace(event.Message)
}

func appendUnique(values *[]string, value string) {
	if value == "" {
		return
	}
	if slices.Contains(*values, value) {
		return
	}
	*values = append(*values, value)
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
