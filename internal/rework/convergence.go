package rework

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/iamseth/tao/internal/plan"
)

const recurringFilesStopReasonPrefix = "automatic rework stalled on files recurring across three consecutive reviews: "

type reworkRoundFiles struct {
	files    map[string]struct{}
	complete bool
}

// recurringReworkFiles returns safe finding files shared by the current review
// and the two most recent contiguous generated rework rounds in this budget.
func recurringReworkFiles(detail *plan.PlanDetail, baseline int, current []plan.ReviewFinding) []string {
	if detail == nil {
		return nil
	}
	baseline = max(baseline, 0)
	rounds := make(map[int]*reworkRoundFiles)
	latest := baseline
	for _, slice := range detail.Slices.Slices {
		round := RoundFromSliceID(slice.ID)
		if round == 0 || round <= baseline {
			continue
		}
		observation, ok := rounds[round]
		if !ok {
			observation = &reworkRoundFiles{files: make(map[string]struct{}), complete: true}
			rounds[round] = observation
		}
		latest = max(latest, round)
		if len(slice.ExpectedFiles) == 0 {
			observation.complete = false
			continue
		}
		file, ok := normalizeReviewFindingFile(slice.ExpectedFiles[0])
		if !ok {
			observation.complete = false
			continue
		}
		observation.files[file] = struct{}{}
	}
	if latest-baseline < 2 {
		return nil
	}
	for round := baseline + 1; round <= latest; round++ {
		observation, ok := rounds[round]
		if !ok || !observation.complete || len(observation.files) == 0 {
			return nil
		}
	}

	currentFiles := make(map[string]struct{}, len(current))
	for _, finding := range current {
		if file, ok := normalizeReviewFindingFile(finding.File); ok {
			currentFiles[file] = struct{}{}
		}
	}
	previous := rounds[latest-1].files
	latestFiles := rounds[latest].files
	recurring := make([]string, 0, len(currentFiles))
	for file := range currentFiles {
		if _, ok := previous[file]; !ok {
			continue
		}
		if _, ok := latestFiles[file]; ok {
			recurring = append(recurring, file)
		}
	}
	slices.Sort(recurring)
	return recurring
}

func recurringFilesStopReason(files []string) string {
	files = slices.Clone(files)
	slices.Sort(files)
	files = slices.Compact(files)
	encoded, _ := json.Marshal(files)
	return recurringFilesStopReasonPrefix + string(encoded)
}

func recurringFilesFromStopReason(reason string) ([]string, bool) {
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
