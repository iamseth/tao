package runheader

import (
	"fmt"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/view"
)

// LineCount is the fixed number of terminal rows occupied by the header.
const LineCount = 7

const fieldSeparator = " · "

type labeledField struct {
	label string
	value string
}

// Render turns state into a fixed-height terminal header. Width is measured in
// runes; ANSI color sequences, when requested, do not consume visible width.
func Render(state run.HeaderState, width int, useColor bool) []string {
	width = max(width, 0)
	lines := make([]string, 0, LineCount)
	lines = append(lines, border(width, '┌', '─', '┐', useColor))

	innerWidth := max(width-2, 0)
	slug, ok := plan.PlanSlug(state.PlanID)
	if !ok {
		slug = state.PlanID
	}
	slug = display(slug)
	identity := []labeledField{
		{label: "repo", value: display(state.RepoName)},
		{label: "plan", value: slug},
		{label: "id", value: display(state.PlanID)},
	}
	if strings.TrimSpace(state.PlanTitle) != "" {
		identity = append(identity, labeledField{label: "title", value: state.PlanTitle})
	}
	lines = append(lines, contentLine(renderFields(identity, innerWidth), width))

	review := "off"
	if state.ReviewEnabled {
		review = "on"
	}
	config := []labeledField{
		{label: "agent", value: display(state.Agent)},
		{label: "mode", value: display(state.ExecutionMode)},
		{label: "branch", value: display(state.Branch)},
		{label: "review", value: review},
	}
	if state.ReworkRound > 0 {
		rework := fmt.Sprintf("%d", state.ReworkRound)
		if state.MaxReworkAttempts > 0 {
			rework = fmt.Sprintf("%d/%d", state.ReworkRound, state.MaxReworkAttempts)
		}
		config = append(config, labeledField{label: "rework", value: rework})
	}
	lines = append(lines, contentLine(renderFields(config, innerWidth), width))

	current := phaseLabel(state)
	progress := make([]labeledField, 0, 4)
	if state.BatchPosition > 0 && state.BatchTotal > 0 {
		progress = append(progress, labeledField{label: "plan", value: fmt.Sprintf("%d/%d", state.BatchPosition, state.BatchTotal)})
	}
	progress = append(progress,
		labeledField{label: "slices", value: fmt.Sprintf("%d/%d", state.CompletedCount, state.TotalCount)},
		labeledField{label: "current", value: current},
		labeledField{label: "elapsed", value: elapsed(state.StartedAt)},
	)
	lines = append(lines, contentLine(renderFields(progress, innerWidth), width))

	cost := "not reported"
	if state.CostReported {
		cost = fmt.Sprintf("$%.2f", state.Cost)
	}
	metrics := []labeledField{
		{label: "sessions", value: fmt.Sprintf("%d", state.AgentSessionCount)},
		{label: "tokens", value: fmt.Sprintf("%d", state.TotalTokens)},
		{label: "cost", value: cost},
	}
	lines = append(lines, contentLine(renderFields(metrics, innerWidth), width))

	const checklistPrefix = "slices "
	checklistWidth := max(innerWidth-view.RuneWidth(checklistPrefix), 0)
	checklist := checklistPrefix + renderChecklist(state, checklistWidth)
	checklist = contentLine(truncate(checklist, innerWidth), width)
	if useColor {
		checklist = colorChecklist(checklist)
	}
	lines = append(lines, checklist)
	lines = append(lines, border(width, '└', '─', '┘', useColor))
	return lines
}

func renderFields(fields []labeledField, width int) string {
	if width <= 0 || len(fields) == 0 {
		return ""
	}
	values := make([][]rune, len(fields))
	limits := make([]int, len(fields))
	fixedWidth := view.RuneWidth(fieldSeparator) * (len(fields) - 1)
	for i, field := range fields {
		values[i] = []rune(display(field.value))
		limits[i] = len(values[i])
		fixedWidth += view.RuneWidth(field.label) + 1
	}

	availableValues := width - fixedWidth
	if availableValues < len(fields) {
		return truncate(joinFields(fields, values, limits), width)
	}
	for total(limits) > availableValues {
		longest := 0
		for i := 1; i < len(limits); i++ {
			if limits[i] > limits[longest] {
				longest = i
			}
		}
		limits[longest]--
	}
	return joinFields(fields, values, limits)
}

func joinFields(fields []labeledField, values [][]rune, limits []int) string {
	parts := make([]string, len(fields))
	for i, field := range fields {
		parts[i] = field.label + " " + truncateRunes(values[i], limits[i])
	}
	return strings.Join(parts, fieldSeparator)
}

func total(values []int) int {
	result := 0
	for _, value := range values {
		result += value
	}
	return result
}

func phaseLabel(state run.HeaderState) string {
	if state.CurrentSliceTitle != "" {
		return state.CurrentSliceTitle
	}
	phase := strings.TrimSpace(string(state.Phase))
	if phase == "" {
		return "-"
	}
	return strings.ReplaceAll(phase, "_", " ")
}

func elapsed(startedAt time.Time) string {
	if startedAt.IsZero() {
		return "-"
	}
	return plan.FormatDuration(max(time.Since(startedAt), 0))
}

func renderChecklist(state run.HeaderState, width int) string {
	if width <= 0 {
		return ""
	}
	if len(state.Slices) == 0 {
		return "-"
	}
	items := make([]string, len(state.Slices))
	current := -1
	for i, slice := range state.Slices {
		marker := "○"
		if slice.Status == plan.StatusCompleted {
			marker = "✓"
		}
		if slice.ID == state.CurrentSliceID {
			marker = "▶"
			current = i
		}
		items[i] = marker + display(slice.ID)
	}
	if current < 0 {
		for i, slice := range state.Slices {
			if slice.Status != plan.StatusCompleted {
				current = i
				items[i] = "▶" + display(slice.ID)
				break
			}
		}
	}
	if current < 0 {
		current = len(items) - 1
	}

	all := strings.Join(items, " ")
	if view.RuneWidth(all) <= width {
		return all
	}

	best := ""
	bestCount, bestBalance := 0, len(items)+1
	for start := 0; start <= current; start++ {
		for end := current; end < len(items); end++ {
			candidate := checklistWindow(items, start, end)
			if view.RuneWidth(candidate) > width {
				continue
			}
			count := end - start + 1
			balance := abs((current - start) - (end - current))
			if count > bestCount || count == bestCount && balance < bestBalance {
				best, bestCount, bestBalance = candidate, count, balance
			}
		}
	}
	if best != "" {
		return best
	}

	leading, trailing := current > 0, current < len(items)-1
	reserved := 0
	if leading {
		reserved += 2
	}
	if trailing {
		reserved += 2
	}
	if reserved >= width {
		return truncate(strings.TrimSpace(strings.Repeat("… ", boolInt(leading))+strings.Repeat("… ", boolInt(trailing))), width)
	}
	item := truncate(items[current], width-reserved)
	parts := make([]string, 0, 3)
	if leading {
		parts = append(parts, "…")
	}
	parts = append(parts, item)
	if trailing {
		parts = append(parts, "…")
	}
	return strings.Join(parts, " ")
}

func checklistWindow(items []string, start, end int) string {
	parts := make([]string, 0, end-start+3)
	if start > 0 {
		parts = append(parts, "…")
	}
	parts = append(parts, items[start:end+1]...)
	if end < len(items)-1 {
		parts = append(parts, "…")
	}
	return strings.Join(parts, " ")
}

func contentLine(content string, width int) string {
	switch width {
	case 0:
		return ""
	case 1:
		return "│"
	case 2:
		return "││"
	default:
		inner := truncate(content, width-2)
		return "│" + view.Pad(inner, width-2) + "│"
	}
}

func border(width int, left, fill, right rune, useColor bool) string {
	var result string
	switch width {
	case 0:
		return ""
	case 1:
		result = string(fill)
	case 2:
		result = strings.Repeat(string(fill), 2)
	default:
		result = string(left) + strings.Repeat(string(fill), width-2) + string(right)
	}
	if useColor {
		return color(result, "36")
	}
	return result
}

func colorChecklist(value string) string {
	value = strings.ReplaceAll(value, "✓", color("✓", "32"))
	value = strings.ReplaceAll(value, "▶", color("▶", "36"))
	return strings.ReplaceAll(value, "○", color("○", "90"))
}

func color(value, code string) string {
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func display(value string) string {
	value = terminalSafe(value)
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

func terminalSafe(value string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || r >= 0x80 && r <= 0x9f {
			return '\uFFFD'
		}
		return r
	}, value)
}

func truncate(value string, width int) string {
	return truncateRunes([]rune(value), width)
}

func truncateRunes(value []rune, width int) string {
	if width <= 0 {
		return ""
	}
	if len(value) <= width {
		return string(value)
	}
	if width == 1 {
		return "…"
	}
	return string(value[:width-1]) + "…"
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
