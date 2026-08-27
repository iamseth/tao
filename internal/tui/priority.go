package tui

import (
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
)

// orderPlannedRows applies advisory business ordering only to ordinary planned
// rows. Other rows retain both their positions and relative order.
func orderPlannedRows(rows []monitor.Row) {
	var positions []int
	var planned []monitor.Row
	for index, row := range rows {
		if row.Kind != monitor.RowKindRepositoryWarning && row.Status == plan.StatusPlanned {
			positions = append(positions, index)
			planned = append(planned, row)
		}
	}
	ordered := priorityOrder(planned)
	for index, position := range positions {
		rows[position] = ordered[index]
	}
}

func priorityOrder(rows []monitor.Row) []monitor.Row {
	ordered := append([]monitor.Row(nil), rows...)
	if len(ordered) < 2 {
		return ordered
	}

	// Sequence edges are considered only inside the same disposition class, so
	// advisory relationships cannot promote deferred work ahead of ready work.
	byKey := make(map[string]int, len(ordered))
	duplicate := make(map[string]bool)
	for index, row := range ordered {
		key := row.RepositoryID + "\x00" + row.PlanID
		if _, exists := byKey[key]; exists {
			duplicate[key] = true
		}
		byKey[key] = index
	}
	adjacent := make([][]int, len(ordered))
	indegree := make([]int, len(ordered))
	for index, row := range ordered {
		for _, relationship := range row.Relationships {
			if relationship.State != monitor.RelationshipIncomplete && relationship.State != monitor.RelationshipComplete {
				continue
			}
			targetKey := row.RepositoryID + "\x00" + relationship.PlanID
			target, exists := byKey[targetKey]
			if !exists || duplicate[targetKey] || dispositionRank(row) != dispositionRank(ordered[target]) {
				continue
			}
			from, to := index, target
			if relationship.Type == plan.PlanRelationAfter {
				from, to = target, index
			} else if relationship.Type != plan.PlanRelationBefore {
				continue
			}
			adjacent[from] = append(adjacent[from], to)
			indegree[to]++
		}
	}

	result := make([]monitor.Row, 0, len(ordered))
	used := make([]bool, len(ordered))
	for len(result) < len(ordered) {
		best := -1
		for index := range ordered {
			if used[index] || indegree[index] != 0 {
				continue
			}
			if best < 0 || lessPriority(ordered[index], ordered[best]) {
				best = index
			}
		}
		// Resolved cyclic edges are excluded, but this guard keeps malformed
		// caller-provided rows deterministic and lossless.
		if best < 0 {
			for index := range ordered {
				if !used[index] && (best < 0 || lessPriority(ordered[index], ordered[best])) {
					best = index
				}
			}
		}
		used[best] = true
		result = append(result, ordered[best])
		for _, next := range adjacent[best] {
			indegree[next]--
		}
	}
	return result
}

func lessPriority(left, right monitor.Row) bool {
	if leftRank, rightRank := dispositionRank(left), dispositionRank(right); leftRank != rightRank {
		return leftRank < rightRank
	}
	if leftRank, rightRank := categoricalPriorityRank(left), categoricalPriorityRank(right); leftRank != rightRank {
		return leftRank < rightRank
	}
	return comparePriorityActivity(left.UpdatedAt, right.UpdatedAt) < 0
}

func dispositionRank(row monitor.Row) int {
	switch row.Overview.Disposition {
	case plan.DecisionDispositionReady:
		return 0
	case "":
		return 1
	case plan.DecisionDispositionConditional:
		return 2
	case plan.DecisionDispositionDeferred:
		return 3
	case plan.DecisionDispositionObsolete:
		return 4
	default:
		return 1
	}
}

func categoricalPriorityRank(row monitor.Row) int {
	if row.Overview.Priority == nil {
		return 3
	}
	switch row.Overview.Priority.Level {
	case plan.PriorityOverallLevelMust:
		return 0
	case plan.PriorityOverallLevelShould:
		return 1
	case plan.PriorityOverallLevelCould:
		return 2
	default:
		return 3
	}
}

func comparePriorityActivity(left, right *time.Time) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return 1
	}
	if right == nil {
		return -1
	}
	if left.After(*right) {
		return -1
	}
	if left.Before(*right) {
		return 1
	}
	return 0
}
