package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/promptinstall"
	"github.com/iamseth/tao/internal/theme"
	planview "github.com/iamseth/tao/internal/view"
)

func init() {
	defaultPromptFreshnessCheck = func() ([]promptinstall.Result, error) { return nil, nil }
}

func TestColorHelpersCoverStatusAndDoneBranches(t *testing.T) {
	palette := theme.Default().Palette(theme.ProfileANSI16)
	for _, test := range []struct {
		status string
		role   theme.Role
	}{
		{plan.StatusCompleted, theme.RoleSuccess},
		{plan.StatusReviewed, theme.RoleSuccess},
		{plan.StatusInProgress, theme.RoleAccent},
		{plan.StatusInReview, theme.RolePlanNext},
		{plan.StatusBlocked, theme.RoleWarn},
		{plan.StatusVerificationFailed, theme.RoleWarn},
		{plan.StatusPlanned, theme.RoleWarn},
		{plan.StatusPending, theme.RoleWarn},
		{plan.StatusChangesRequested, theme.RoleWarn},
		{"weird", theme.RoleRepo},
	} {
		want := palette.Paint(test.role, test.status)
		if got := colorStatus(palette, test.status, test.status); got != want {
			t.Errorf("colorStatus(%q) = %q, want %q", test.status, got, want)
		}
		if got, want := colorDuration(palette, "1m", test.status), palette.Paint(test.role, "1m"); got != want {
			t.Errorf("colorDuration(%q) = %q, want %q", test.status, got, want)
		}
	}
	for _, test := range []struct {
		completed int
		total     int
		role      theme.Role
	}{
		{completed: 2, total: 2, role: theme.RoleSuccess},
		{completed: 1, total: 2, role: theme.RoleAccent},
		{completed: 0, total: 0, role: theme.RoleNeutral2},
		{completed: 0, total: 2, role: theme.RoleWarn},
	} {
		if got, want := colorDone(palette, "value", test.completed, test.total), palette.Paint(test.role, "value"); got != want {
			t.Fatalf("done color = %q, want %q", got, want)
		}
	}
	if got, want := colorDuration(palette, "-", plan.StatusCompleted), palette.Paint(theme.RoleNeutral2, "-"); got != want {
		t.Fatalf("empty duration = %q, want %q", got, want)
	}
}

type testTerminalBuffer struct {
	bytes.Buffer
}

func (*testTerminalBuffer) IsTerminal() bool { return true }

func TestOutputPaletteRequiresTerminalAndHonorsEnvironment(t *testing.T) {
	outputPalette := (App{}).outputPalette
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("COLORTERM", "")
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")
	if outputPalette(&bytes.Buffer{}).Enabled() {
		t.Fatal("bytes.Buffer should be treated as non-terminal output")
	}
	if got := outputPalette(&testTerminalBuffer{}).Profile; got != theme.ProfileANSI256 {
		t.Fatalf("terminal profile = %s, want 256-color", got)
	}

	t.Setenv("NO_COLOR", "1")
	if outputPalette(&testTerminalBuffer{}).Enabled() {
		t.Fatal("NO_COLOR should disable terminal color")
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	if outputPalette(&testTerminalBuffer{}).Enabled() {
		t.Fatal("TERM=dumb should disable terminal color")
	}
	t.Setenv("TERM", "xterm")
	t.Setenv("CLICOLOR_FORCE", "1")
	if got := outputPalette(&bytes.Buffer{}).Profile; got != theme.ProfileANSI16 {
		t.Fatalf("forced redirected profile = %s, want 16-color", got)
	}
	t.Setenv("COLORTERM", "truecolor")
	if got := outputPalette(&testTerminalBuffer{}).Profile; got != theme.ProfileTrueColor {
		t.Fatalf("terminal profile = %s, want truecolor", got)
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
