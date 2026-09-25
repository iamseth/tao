package cli

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/monitor/rowlabel"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term"
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
	switch rowlabel.StatusRoleFor(status) {
	case rowlabel.StatusRoleSuccess:
		return color(value, "32")
	case rowlabel.StatusRoleActive:
		return color(value, "36")
	case rowlabel.StatusRoleReview:
		return color(value, "34")
	case rowlabel.StatusRoleWarn:
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

func outputSupportsColor(out io.Writer) bool {
	return term.ColorEnabled(outputIsTerminal(out), os.Getenv)
}

func outputIsTerminal(out io.Writer) bool {
	return term.IsTerminal(out)
}

func color(value, code string) string {
	return "\x1b[" + code + "m" + value + "\x1b[0m"
}
