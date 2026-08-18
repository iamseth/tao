package runheader

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/run"
)

// LineCount is the fixed number of terminal rows occupied by the header.
const LineCount = 7

const (
	fieldSeparator = " · "
	maxProgressBar = 20
)

// Render turns state into a fixed-height, borderless terminal header. Width is
// measured in terminal cells; ANSI color sequences do not consume visible width.
func Render(state run.HeaderState, width int, useColor bool) []string {
	width = max(width, 0)

	identity := renderIdentity(state, width)
	active := renderActive(state)
	progress := renderProgress(state, width)
	checklist := "SLICES  " + renderChecklist(state, max(width-cellWidth("SLICES  "), 0))
	metrics := renderMetrics(state)

	lines := []string{
		line(identity, width),
		line(active, width),
		line(progress, width),
		line(checklist, width),
		line(metrics, width),
		strings.Repeat("─", width),
		line("LIVE OUTPUT", width),
	}
	if useColor {
		lines[1] = colorFirst(lines[1], "▶", "36")
		lines[3] = colorChecklist(lines[3])
		lines[5] = color(lines[5], "90")
		lines[6] = colorFirst(lines[6], "LIVE OUTPUT", "36")
	}
	return lines
}

func renderIdentity(state run.HeaderState, width int) string {
	slug, ok := plan.PlanSlug(state.PlanID)
	if !ok {
		slug = state.PlanID
	}
	identity := display(state.RepoName) + " / " + display(slug)
	context := make([]string, 0, 6)
	if state.BatchPosition > 0 && state.BatchTotal > 0 {
		context = append(context, fmt.Sprintf("batch %d/%d", state.BatchPosition, state.BatchTotal))
	}
	if state.ReworkRound > 0 {
		rework := strconv.Itoa(state.ReworkRound)
		if state.MaxReworkAttempts > 0 {
			rework = fmt.Sprintf("%d/%d", state.ReworkRound, state.MaxReworkAttempts)
		}
		context = append(context, "rework "+rework)
	}
	context = append(context, display(state.Agent), display(state.ExecutionMode), display(state.Branch))
	if state.ReviewEnabled {
		context = append(context, "review on")
	} else {
		context = append(context, "review off")
	}

	parts := append([]string{identity}, context...)
	if strings.TrimSpace(state.PlanTitle) != "" {
		withoutTitle := strings.Join(parts, fieldSeparator)
		titleWidth := width - cellWidth(withoutTitle) - 2*cellWidth(fieldSeparator)
		if titleWidth > 0 {
			parts = append([]string{identity, truncateCells(display(state.PlanTitle), titleWidth)}, context...)
		}
	}
	return strings.Join(parts, fieldSeparator)
}

func renderActive(state run.HeaderState) string {
	current := phaseLabel(state)
	if state.CurrentSliceTitle != "" || state.CurrentSliceID != "" {
		current = strings.TrimSpace(strings.Join([]string{slicePrefix(state.CurrentSliceID), display(state.CurrentSliceTitle)}, " "))
		current = "▶ " + current
	} else {
		current = "PHASE  " + current
	}
	return current + fieldSeparator + "elapsed " + elapsed(state.StartedAt)
}

func renderProgress(state run.HeaderState, width int) string {
	total := max(state.TotalCount, 0)
	completed := max(state.CompletedCount, 0)
	percent := 0
	if total > 0 {
		percent = min(completed*100/total, 100)
	}
	stats := fmt.Sprintf("%d/%d · %d%%", completed, total, percent)
	barWidth := min(maxProgressBar, max(width-cellWidth(stats)-3, 0))
	if barWidth == 0 {
		return stats
	}
	filled := 0
	if total > 0 {
		filled = min(completed*barWidth/total, barWidth)
	}
	return "[" + strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + "] " + stats
}

func renderMetrics(state run.HeaderState) string {
	cost := "cost —"
	if state.CostReported {
		cost = fmt.Sprintf("$%.2f", state.Cost)
	}
	return fmt.Sprintf("AGENT  %d %s%s%s tokens%s%s",
		state.AgentSessionCount,
		plural(state.AgentSessionCount, "session", "sessions"),
		fieldSeparator,
		compactCount(state.TotalTokens),
		fieldSeparator,
		cost,
	)
}

func plural(value int, one, many string) string {
	if value == 1 {
		return one
	}
	return many
}

func compactCount(value int64) string {
	absolute := math.Abs(float64(value))
	for _, unit := range []struct {
		threshold float64
		suffix    string
	}{{1_000_000_000, "b"}, {1_000_000, "m"}, {1_000, "k"}} {
		if absolute >= unit.threshold {
			result := strconv.FormatFloat(float64(value)/unit.threshold, 'f', 1, 64)
			result = strings.TrimSuffix(result, ".0")
			return result + unit.suffix
		}
	}
	return strconv.FormatInt(value, 10)
}

func phaseLabel(state run.HeaderState) string {
	phase := strings.TrimSpace(string(state.Phase))
	if phase == "" {
		return "-"
	}
	return display(strings.ReplaceAll(phase, "_", " "))
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
		title := display(slice.Title)
		items[i] = strings.TrimSpace(marker + " " + slicePrefix(slice.ID) + " " + title)
	}
	if current < 0 {
		for i, slice := range state.Slices {
			if slice.Status != plan.StatusCompleted {
				current = i
				items[i] = strings.Replace(items[i], "○ ", "▶ ", 1)
				break
			}
		}
	}
	if current < 0 {
		current = len(items) - 1
	}

	all := strings.Join(items, "   ")
	if cellWidth(all) <= width {
		return all
	}

	best := ""
	bestCount, bestBalance := 0, len(items)+1
	for start := 0; start <= current; start++ {
		for end := current; end < len(items); end++ {
			candidate := checklistWindow(items, start, end)
			if cellWidth(candidate) > width {
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
		reserved += cellWidth("…   ")
	}
	if trailing {
		reserved += cellWidth("   …")
	}
	item := truncateCells(items[current], max(width-reserved, 0))
	parts := make([]string, 0, 3)
	if leading {
		parts = append(parts, "…")
	}
	parts = append(parts, item)
	if trailing {
		parts = append(parts, "…")
	}
	return truncateCells(strings.Join(parts, "   "), width)
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
	return strings.Join(parts, "   ")
}

func slicePrefix(id string) string {
	id = display(id)
	if before, _, ok := strings.Cut(id, "-"); ok && before != "" {
		return before
	}
	return id
}

func line(content string, width int) string {
	content = truncateCells(content, width)
	return content + strings.Repeat(" ", max(width-cellWidth(content), 0))
}

func colorChecklist(value string) string {
	value = strings.ReplaceAll(value, "✓", color("✓", "32"))
	value = strings.ReplaceAll(value, "▶", color("▶", "36"))
	return strings.ReplaceAll(value, "○", color("○", "90"))
}

func colorFirst(value, target, code string) string {
	return strings.Replace(value, target, color(target, code), 1)
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

func truncateCells(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if cellWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	target := width - 1
	used := 0
	var result strings.Builder
	for _, r := range value {
		runeWidth := terminalRuneWidth(r)
		if used+runeWidth > target {
			break
		}
		result.WriteRune(r)
		used += runeWidth
	}
	return result.String() + "…"
}

func cellWidth(value string) int {
	width := 0
	for _, r := range value {
		width += terminalRuneWidth(r)
	}
	return width
}

func terminalRuneWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r) {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		r >= 0x2e80 && r <= 0xa4cf && r != 0x303f ||
		r >= 0xac00 && r <= 0xd7a3 || r >= 0xf900 && r <= 0xfaff ||
		r >= 0xfe10 && r <= 0xfe19 || r >= 0xfe30 && r <= 0xfe6f ||
		r >= 0xff00 && r <= 0xff60 || r >= 0xffe0 && r <= 0xffe6 ||
		r >= 0x1f300 && r <= 0x1faff || r >= 0x20000 && r <= 0x3fffd) {
		return 2
	}
	return 1
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
