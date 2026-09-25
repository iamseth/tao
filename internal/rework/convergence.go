package rework

import (
	"encoding/json"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

const (
	recurringFilesStopReasonPrefix = "automatic rework stalled on files recurring across three consecutive reviews: "
	fileRecurrenceStopReasonPrefix = "automatic rework stalled on files recurring in three review rounds: "
	anchorReversalStopReasonPrefix = "automatic rework stopped on repeated finding anchors across review rounds: "
	planBudgetStopReasonPrefix     = "automatic rework stopped after plan agent budget warning: "
)

// AdvisoryKind identifies a location-only recurrence signal, not a stop reason.
type AdvisoryKind string

const (
	AdvisoryKindAnchorRecurrence AdvisoryKind = "anchor_recurrence"
	AdvisoryKindFileRecurrence   AdvisoryKind = "file_recurrence"
)

// Advisory is presentation-only evidence from the current rework window.
// Location is a normalized file:line anchor or file, and Rounds are distinct
// and ascending. It grants no lifecycle or recovery authority.
type Advisory struct {
	Kind     AdvisoryKind
	Location string
	Rounds   []int
}

// locationAdvisories consumes the shared projection without changing its
// baseline, normalization, or conservative incomplete-history semantics.
// Anchors precede files; locations are sorted within each kind.
func locationAdvisories(churn plan.ReworkChurn) []Advisory {
	var advisories []Advisory
	for _, item := range anchorReversalsInChurn(churn) {
		advisories = append(advisories, Advisory{Kind: AdvisoryKindAnchorRecurrence, Location: item.Anchor, Rounds: item.Rounds})
	}
	for _, item := range recurringFilesInChurn(churn) {
		if _, ok := normalizeReviewFindingFile(item.File); !ok {
			continue
		}
		advisories = append(advisories, Advisory{Kind: AdvisoryKindFileRecurrence, Location: item.File, Rounds: item.Rounds})
	}
	return advisories
}

type planBudgetWarning struct {
	Metric    string  `json:"metric"`
	Observed  float64 `json:"observed"`
	Threshold float64 `json:"threshold"`
}

type recurringFileRounds struct {
	File   string `json:"file"`
	Rounds []int  `json:"rounds"`
}

type anchorReversalRounds struct {
	Anchor string `json:"anchor"`
	Rounds []int  `json:"rounds"`
}

func trippedPlanBudgetWarning(warnings []plan.AgentBudgetWarning) (planBudgetWarning, bool) {
	for _, warning := range warnings {
		if warning.Scope != "plan" || strings.TrimSpace(warning.Metric) == "" ||
			math.IsNaN(warning.Observed) || math.IsInf(warning.Observed, 0) ||
			math.IsNaN(warning.Threshold) || math.IsInf(warning.Threshold, 0) ||
			warning.Threshold < 0 || warning.Observed <= warning.Threshold {
			continue
		}
		return planBudgetWarning{Metric: warning.Metric, Observed: warning.Observed, Threshold: warning.Threshold}, true
	}
	return planBudgetWarning{}, false
}

func planBudgetStopReason(warning planBudgetWarning) string {
	encoded, _ := json.Marshal(warning)
	return planBudgetStopReasonPrefix + string(encoded)
}

func planBudgetWarningFromStopReason(reason string) (planBudgetWarning, bool) {
	encoded, ok := strings.CutPrefix(reason, planBudgetStopReasonPrefix)
	if !ok {
		return planBudgetWarning{}, false
	}
	var warning planBudgetWarning
	if err := json.Unmarshal([]byte(encoded), &warning); err != nil || strings.TrimSpace(warning.Metric) == "" ||
		math.IsNaN(warning.Observed) || math.IsInf(warning.Observed, 0) ||
		math.IsNaN(warning.Threshold) || math.IsInf(warning.Threshold, 0) ||
		warning.Threshold < 0 || warning.Observed <= warning.Threshold {
		return planBudgetWarning{}, false
	}
	return warning, true
}

func anchorReversalsInChurn(churn plan.ReworkChurn) []anchorReversalRounds {
	reversals := make([]anchorReversalRounds, 0)
	for anchor, rounds := range churn.AnchorRounds {
		if len(rounds) < 2 {
			continue
		}
		if _, ok := normalizeReviewFindingAnchor(anchor); !ok {
			continue
		}
		reversals = append(reversals, anchorReversalRounds{Anchor: anchor, Rounds: slices.Clone(rounds)})
	}
	slices.SortFunc(reversals, func(a, b anchorReversalRounds) int {
		return strings.Compare(a.Anchor, b.Anchor)
	})
	return reversals
}

func anchorReversalStopReason(reversals []anchorReversalRounds) string {
	reversals = slices.Clone(reversals)
	slices.SortFunc(reversals, func(a, b anchorReversalRounds) int {
		return strings.Compare(a.Anchor, b.Anchor)
	})
	encoded, _ := json.Marshal(reversals)
	return anchorReversalStopReasonPrefix + string(encoded)
}

func anchorReversalsFromStopReason(reason string) ([]anchorReversalRounds, bool) {
	encoded, ok := strings.CutPrefix(reason, anchorReversalStopReasonPrefix)
	if !ok {
		return nil, false
	}
	var persisted []anchorReversalRounds
	if err := json.Unmarshal([]byte(encoded), &persisted); err != nil || len(persisted) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(persisted))
	for index := range persisted {
		anchor, rounds, ok := normalizePersistedRounds(persisted[index].Anchor, persisted[index].Rounds, 2, seen, normalizeReviewFindingAnchor)
		if !ok {
			return nil, false
		}
		persisted[index].Anchor = anchor
		persisted[index].Rounds = rounds
	}
	slices.SortFunc(persisted, func(a, b anchorReversalRounds) int {
		return strings.Compare(a.Anchor, b.Anchor)
	})
	return persisted, true
}

func normalizeReviewFindingAnchor(value string) (string, bool) {
	separator := strings.LastIndexByte(value, ':')
	if separator < 1 || separator == len(value)-1 {
		return "", false
	}
	file, lineText := value[:separator], value[separator+1:]
	file, ok := normalizeReviewFindingFile(file)
	if !ok {
		return "", false
	}
	line, err := strconv.Atoi(lineText)
	if err != nil || line <= 0 {
		return "", false
	}
	return file + ":" + strconv.Itoa(line), true
}

func recurringFilesInChurn(churn plan.ReworkChurn) []recurringFileRounds {
	recurring := make([]recurringFileRounds, 0)
	for file, rounds := range churn.FileRounds {
		if len(rounds) < 3 {
			continue
		}
		recurring = append(recurring, recurringFileRounds{File: file, Rounds: slices.Clone(rounds)})
	}
	slices.SortFunc(recurring, func(a, b recurringFileRounds) int {
		return strings.Compare(a.File, b.File)
	})
	return recurring
}

func fileRecurrenceStopReason(recurring []recurringFileRounds) string {
	recurring = slices.Clone(recurring)
	slices.SortFunc(recurring, func(a, b recurringFileRounds) int {
		return strings.Compare(a.File, b.File)
	})
	encoded, _ := json.Marshal(recurring)
	return fileRecurrenceStopReasonPrefix + string(encoded)
}

// recurringFilesStopReason retains the historical consecutive-review encoding
// for persisted stop compatibility.
func recurringFilesStopReason(files []string) string {
	files = slices.Clone(files)
	slices.Sort(files)
	files = slices.Compact(files)
	encoded, _ := json.Marshal(files)
	return recurringFilesStopReasonPrefix + string(encoded)
}

func recurringFileRoundsFromStopReason(reason string) ([]recurringFileRounds, bool) {
	encoded, ok := strings.CutPrefix(reason, fileRecurrenceStopReasonPrefix)
	if !ok {
		return nil, false
	}
	var persisted []recurringFileRounds
	if err := json.Unmarshal([]byte(encoded), &persisted); err != nil || len(persisted) == 0 {
		return nil, false
	}
	seen := make(map[string]struct{}, len(persisted))
	for index := range persisted {
		file, rounds, ok := normalizePersistedRounds(persisted[index].File, persisted[index].Rounds, 3, seen, normalizeReviewFindingFile)
		if !ok {
			return nil, false
		}
		persisted[index].File = file
		persisted[index].Rounds = rounds
	}
	slices.SortFunc(persisted, func(a, b recurringFileRounds) int {
		return strings.Compare(a.File, b.File)
	})
	return persisted, true
}

func normalizePersistedRounds(value string, rounds []int, minimum int, seen map[string]struct{}, normalize func(string) (string, bool)) (string, []int, bool) {
	value, ok := normalize(value)
	if !ok || len(rounds) < minimum {
		return "", nil, false
	}
	if _, duplicate := seen[value]; duplicate {
		return "", nil, false
	}
	seen[value] = struct{}{}
	slices.Sort(rounds)
	rounds = slices.Compact(rounds)
	if len(rounds) < minimum || rounds[0] <= 0 {
		return "", nil, false
	}
	return value, rounds, true
}

func recurringFilesFromStopReason(reason string) ([]string, bool) {
	if recurring, ok := recurringFileRoundsFromStopReason(reason); ok {
		files := make([]string, len(recurring))
		for index := range recurring {
			files[index] = recurring[index].File
		}
		return files, true
	}

	encoded, ok := strings.CutPrefix(reason, recurringFilesStopReasonPrefix)
	if !ok {
		return nil, false
	}
	var persisted []string
	if err := json.Unmarshal([]byte(encoded), &persisted); err != nil || len(persisted) == 0 {
		return nil, false
	}
	files := make([]string, 0, len(persisted))
	for _, value := range persisted {
		file, ok := normalizeReviewFindingFile(value)
		if !ok {
			return nil, false
		}
		files = append(files, file)
	}
	slices.Sort(files)
	files = slices.Compact(files)
	return files, true
}
