package insights

import (
	"bufio"
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
	"unicode"

	"github.com/iamseth/tao/internal/plan"
)

const maxExemplars = 2

// PlanLister is the repository boundary needed for cross-plan aggregation.
type PlanLister interface {
	ListPlans(context.Context, plan.PlanFilter) ([]plan.PlanSummary, error)
}

// Report contains advisory telemetry derived from one repository's plan history.
type Report struct {
	PlansScanned   int            `json:"plans_scanned"`
	PlansSkipped   int            `json:"plans_skipped"`
	BlockedReasons []ReasonBucket `json:"blocked_reasons"`
	ReworkPlans    []ReworkPlan   `json:"rework_plans"`
	Signals        SignalCounts   `json:"signals"`
	OutputTokens   Percentiles    `json:"output_tokens"`
	Cost           Percentiles    `json:"cost"`
	OutlierPlans   []PlanOutlier  `json:"outlier_plans"`
}

// ReasonBucket groups equivalent slice-blocked messages while retaining bounded examples.
type ReasonBucket struct {
	Reason    string   `json:"reason"`
	Count     int      `json:"count"`
	Exemplars []string `json:"exemplars"`
}

// ReworkPlan identifies a plan with at least three rework rounds.
type ReworkPlan struct {
	PlanID         string   `json:"plan_id"`
	Rounds         int      `json:"rounds"`
	StoppedReasons []string `json:"stopped_reasons,omitempty"`
}

// SignalCounts counts recurring operational failure events.
type SignalCounts struct {
	SessionTimeout             int `json:"session_timeout"`
	SliceResumeFailed          int `json:"slice_resume_failed"`
	VerificationCommandInvalid int `json:"verification_command_invalid"`
	PlanCommitFallback         int `json:"plan_commit_fallback"`
	PlanCommitGuard            int `json:"plan_commit_guard"`
}

// Percentiles contains nearest-rank percentiles over individual agent sessions.
type Percentiles struct {
	Sessions int     `json:"sessions"`
	P50      float64 `json:"p50"`
	P90      float64 `json:"p90"`
	P95      float64 `json:"p95"`
}

// PlanOutlier flags plans containing a session strictly above a repository p95.
type PlanOutlier struct {
	PlanID              string  `json:"plan_id"`
	OutputTokens        int64   `json:"output_tokens"`
	Cost                float64 `json:"cost"`
	OutputTokensOutlier bool    `json:"output_tokens_outlier"`
	CostOutlier         bool    `json:"cost_outlier"`
}

type accumulator struct {
	buckets  map[string]*ReasonBucket
	sessions []session
}

type planData struct {
	reworkEvents   []plan.Event
	stopReasons    []string
	blockedReasons []string
	sessions       map[string][]plan.AgentMetricEvent
	signals        SignalCounts
}

type session struct {
	planID       string
	outputTokens int64
	cost         float64
}

// Aggregate walks repository plans and streams each event log. Individual unreadable
// plan logs are counted and skipped so historical damage cannot suppress the report.
func Aggregate(ctx context.Context, repository PlanLister) (Report, error) {
	summaries, err := repository.ListPlans(ctx, plan.PlanFilter{})
	if err != nil {
		return Report{}, err
	}

	report := Report{}
	acc := accumulator{buckets: make(map[string]*ReasonBucket)}
	for _, summary := range summaries {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		if summary.Status == plan.StatusInvalid {
			report.PlansSkipped++
			continue
		}
		data, err := readPlanEvents(summary.Dir)
		if err != nil {
			report.PlansSkipped++
			continue
		}
		report.PlansScanned++
		mergePlan(&report, &acc, summary.ID, data)
	}
	finalize(&report, &acc)
	return report, nil
}

func readPlanEvents(dir string) (planData, error) {
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
	case plan.EventTypeSessionTimeout:
		data.signals.SessionTimeout++
	case plan.EventTypeSliceResumeFailed:
		data.signals.SliceResumeFailed++
	case plan.EventTypeVerificationCommandInvalid:
		data.signals.VerificationCommandInvalid++
	case plan.EventTypePlanCommitFallback:
		data.signals.PlanCommitFallback++
	case plan.EventTypePlanCommitGuard:
		data.signals.PlanCommitGuard++
	case plan.EventTypeAgentMetrics, "opencode_metrics":
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

func mergePlan(report *Report, acc *accumulator, planID string, data planData) {
	report.Signals.SessionTimeout += data.signals.SessionTimeout
	report.Signals.SliceResumeFailed += data.signals.SliceResumeFailed
	report.Signals.VerificationCommandInvalid += data.signals.VerificationCommandInvalid
	report.Signals.PlanCommitFallback += data.signals.PlanCommitFallback
	report.Signals.PlanCommitGuard += data.signals.PlanCommitGuard

	rework := plan.SummarizeRework(data.reworkEvents)
	if rework.Rounds >= 3 {
		report.ReworkPlans = append(report.ReworkPlans, ReworkPlan{PlanID: planID, Rounds: rework.Rounds, StoppedReasons: data.stopReasons})
	}
	for _, reason := range data.blockedReasons {
		addBlocked(acc.buckets, reason)
	}
	for _, events := range data.sessions {
		summary := plan.SummarizeAgentMetrics(events)
		acc.sessions = append(acc.sessions, session{planID: planID, outputTokens: summary.Totals.OutputTokens, cost: summary.Totals.Cost})
	}
}

func addBlocked(buckets map[string]*ReasonBucket, exemplar string) {
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

func finalize(report *Report, acc *accumulator) {
	for _, bucket := range acc.buckets {
		report.BlockedReasons = append(report.BlockedReasons, *bucket)
	}
	sort.Slice(report.BlockedReasons, func(i, j int) bool {
		if report.BlockedReasons[i].Count != report.BlockedReasons[j].Count {
			return report.BlockedReasons[i].Count > report.BlockedReasons[j].Count
		}
		return report.BlockedReasons[i].Reason < report.BlockedReasons[j].Reason
	})
	sort.Slice(report.ReworkPlans, func(i, j int) bool { return report.ReworkPlans[i].PlanID < report.ReworkPlans[j].PlanID })

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
		outlier := outliers[item.planID]
		if outlier == nil {
			outlier = &PlanOutlier{PlanID: item.planID}
			outliers[item.planID] = outlier
		}
		outlier.OutputTokens = max(outlier.OutputTokens, item.outputTokens)
		outlier.Cost = max(outlier.Cost, item.cost)
		outlier.OutputTokensOutlier = outlier.OutputTokensOutlier || outputOutlier
		outlier.CostOutlier = outlier.CostOutlier || costOutlier
	}
	for _, outlier := range outliers {
		report.OutlierPlans = append(report.OutlierPlans, *outlier)
	}
	sort.Slice(report.OutlierPlans, func(i, j int) bool { return report.OutlierPlans[i].PlanID < report.OutlierPlans[j].PlanID })
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
