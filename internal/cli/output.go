package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runqueue"
)

func writef(w io.Writer, format string, args ...any) error {
	_, err := fmt.Fprintf(w, format, args...)
	return err
}

func writeln(w io.Writer, value string) error {
	_, err := fmt.Fprintln(w, value)
	return err
}

func writeLines(w io.Writer, lines ...string) error {
	for _, line := range lines {
		if err := writeln(w, line); err != nil {
			return err
		}
	}
	return nil
}

func listPlanLabel(summary plan.PlanSummary) string {
	if slug, ok := plan.PlanSlug(summary.ID); ok {
		return slug
	}
	if strings.TrimSpace(summary.Title) != "" {
		return summary.Title
	}
	return summary.ID
}

func wrapText(value string, width int) []string {
	value = strings.TrimSpace(value)
	if value == "" || width <= 0 {
		return []string{value}
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, len(words))
	line := words[0]
	for _, word := range words[1:] {
		if utf8.RuneCountInString(line)+1+utf8.RuneCountInString(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	return append(lines, line)
}

func colorStatus(value, status string) string {
	switch status {
	case plan.StatusCompleted, plan.StatusReviewed:
		return color(value, "32")
	case plan.StatusInProgress:
		return color(value, "36")
	case plan.StatusInReview:
		return color(value, "34")
	case plan.StatusBlocked, plan.StatusPlanned, plan.StatusPending, plan.StatusChangesRequested:
		return color(value, "33")
	default:
		return color(value, "35")
	}
}

func colorDuration(value, status string) string {
	if strings.TrimSpace(value) == "-" {
		return color(value, "90")
	}
	return colorStatus(value, status)
}

func colorDone(value string, completed, total int) string {
	switch {
	case total > 0 && completed == total:
		return colorGreen(value)
	case completed > 0:
		return color(value, "36")
	case total == 0:
		return color(value, "90")
	default:
		return color(value, "33")
	}
}

func colorGreen(value string) string {
	return color(value, "32")
}

type terminalWriter interface {
	IsTerminal() bool
}

func outputSupportsColor(out io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	return outputIsTerminal(out)
}

func outputIsTerminal(out io.Writer) bool {
	if terminal, ok := out.(terminalWriter); ok {
		return terminal.IsTerminal()
	}
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func colorQueueStatus(value string, status runqueue.QueueStatus) string {
	switch status {
	case runqueue.QueueStatusRunning:
		return color(value, "36")
	case runqueue.QueueStatusPending:
		return color(value, "33")
	case runqueue.QueueStatusSucceeded:
		return color(value, "32")
	case runqueue.QueueStatusFailed:
		return color(value, "31")
	case runqueue.QueueStatusSkipped:
		return color(value, "35")
	default:
		return color(value, "90")
	}
}

func color(value, code string) string {
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}

func pad(value string, width int) string {
	visibleWidth := utf8.RuneCountInString(value)
	if visibleWidth >= width {
		return value
	}
	return value + strings.Repeat(" ", width-visibleWidth)
}
