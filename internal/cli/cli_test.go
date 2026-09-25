package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptinstall"
	planview "github.com/iamseth/tao/internal/view"
)

func init() {
	defaultPromptFreshnessCheck = func() ([]promptinstall.Result, error) { return nil, nil }
}

func TestColorHelpersCoverStatusAndDoneBranches(t *testing.T) {
	if got := colorStatus(plan.StatusBlocked, plan.StatusBlocked); got != "\x1b[33mblocked\x1b[0m" {
		t.Fatalf("blocked status color = %q, want amber", got)
	}
	if got := colorStatus(plan.StatusVerificationFailed, plan.StatusVerificationFailed); got != "\x1b[33mverification_failed\x1b[0m" {
		t.Fatalf("verification-failed status color = %q, want amber", got)
	}
	for _, test := range []struct {
		status string
		code   string
	}{
		{plan.StatusCompleted, "32"},
		{plan.StatusReviewed, "32"},
		{plan.StatusInProgress, "36"},
		{plan.StatusInReview, "34"},
		{plan.StatusBlocked, "33"},
		{plan.StatusPlanned, "33"},
		{plan.StatusPending, "33"},
		{plan.StatusChangesRequested, "33"},
		{"weird", "35"},
	} {
		want := "\x1b[" + test.code + "m" + test.status + "\x1b[0m"
		if got := colorStatus(test.status, test.status); got != want {
			t.Errorf("colorStatus(%q) = %q, want %q", test.status, got, want)
		}
	}
	for _, test := range []struct {
		completed int
		total     int
	}{
		{completed: 2, total: 2},
		{completed: 1, total: 2},
		{completed: 0, total: 0},
		{completed: 0, total: 2},
	} {
		if got := colorDone("value", test.completed, test.total); !strings.Contains(got, "value") {
			t.Fatalf("expected colored done to include value, got %q", got)
		}
	}
	if got := colorDuration("-", plan.StatusCompleted); !strings.Contains(got, "-") {
		t.Fatalf("expected colored empty duration, got %q", got)
	}
}

type testTerminalBuffer struct {
	bytes.Buffer
}

func (*testTerminalBuffer) IsTerminal() bool { return true }

func TestOutputSupportsColorRequiresTerminalAndHonorsEnvironment(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	if outputSupportsColor(&bytes.Buffer{}) {
		t.Fatal("bytes.Buffer should be treated as non-terminal output")
	}
	if !outputSupportsColor(&testTerminalBuffer{}) {
		t.Fatal("terminal writer should support color")
	}

	t.Setenv("NO_COLOR", "1")
	if outputSupportsColor(&testTerminalBuffer{}) {
		t.Fatal("NO_COLOR should disable terminal color")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if outputSupportsColor(&testTerminalBuffer{}) {
		t.Fatal("TERM=dumb should disable terminal color")
	}
}

func stripANSI(value string) string {
	for {
		start := strings.Index(value, "\x1b[")
		if start < 0 {
			return value
		}
		end := strings.IndexByte(value[start:], 'm')
		if end < 0 {
			return value
		}
		value = value[:start] + value[start+end+1:]
	}
}

func TestShortPlanIDFallback(t *testing.T) {
	if got := planview.ShortPlanID("notdated"); got != "notdated" {
		t.Fatalf("expected fallback id, got %q", got)
	}
}
