package rowlabel

import (
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/term/cells"
)

func TestStatusRoleFor(t *testing.T) {
	for _, test := range []struct {
		status string
		want   StatusRole
	}{
		{plan.StatusCompleted, StatusRoleSuccess},
		{plan.StatusReviewed, StatusRoleSuccess},
		{plan.StatusInProgress, StatusRoleActive},
		{plan.StatusInReview, StatusRoleReview},
		{plan.StatusBlocked, StatusRoleWarn},
		{plan.StatusPlanned, StatusRoleWarn},
		{plan.StatusPending, StatusRoleWarn},
		{plan.StatusChangesRequested, StatusRoleWarn},
		{plan.StatusVerificationFailed, StatusRoleWarn},
		{plan.StatusAbandoned, StatusRoleNeutral},
		{"unknown", StatusRoleNeutral},
		{"", StatusRoleNeutral},
	} {
		t.Run(test.status, func(t *testing.T) {
			if got := StatusRoleFor(test.status); got != test.want {
				t.Fatalf("StatusRoleFor(%q) = %v, want %v", test.status, got, test.want)
			}
		})
	}
}

func TestSlicesLabel(t *testing.T) {
	tests := []struct {
		name string
		row  monitor.Row
		want string
	}{
		{name: "empty", want: "0/0"},
		{name: "original", row: monitor.Row{OriginalCompletedCount: 2, OriginalTotalCount: 3}, want: "2/3"},
		{name: "rework", row: monitor.Row{OriginalCompletedCount: 2, OriginalTotalCount: 3, ReworkCompletedCount: 1, ReworkTotalCount: 2}, want: "3/3+2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SlicesLabel(test.row); got != test.want {
				t.Fatalf("SlicesLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPlanLabel(t *testing.T) {
	tests := []struct {
		name string
		row  monitor.Row
		want string
	}{
		{name: "slug", row: monitor.Row{PlanID: "20260828-181339-tui-plans-rows", PlanTitle: "TUI Plans tab"}, want: "tui-plans-rows"},
		{name: "trimmed ID", row: monitor.Row{PlanID: " custom-id ", PlanTitle: "Title"}, want: "custom-id"},
		{name: "title fallback", row: monitor.Row{PlanID: "  ", PlanTitle: " Title "}, want: " Title "},
		{name: "blank", row: monitor.Row{PlanTitle: " \t"}, want: "-"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PlanLabel(test.row); got != test.want {
				t.Fatalf("PlanLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPhaseLabelRequiresLiveRunLockForStalledLabel(t *testing.T) {
	base := monitor.Row{Liveness: monitor.LivenessStale, HeartbeatAge: 45 * time.Second, Phase: "verify"}
	tests := []struct {
		name string
		row  monitor.Row
		want string
	}{
		{name: "missing lock", row: base, want: "verify"},
		{name: "dead lock", row: func() monitor.Row { row := base; row.RunLockPresent = true; return row }(), want: "verify"},
		{name: "live lock", row: func() monitor.Row { row := base; row.RunLockPresent = true; row.RunLockProcessAlive = true; return row }(), want: "stalled? (45s old)"},
		{name: "abandoned", row: monitor.Row{Status: plan.StatusAbandoned, Liveness: monitor.LivenessStale, RunLockPresent: true, RunLockProcessAlive: true}, want: "-"},
		{name: "running slice", row: monitor.Row{Phase: " running_slice ", SliceID: " 002-render "}, want: "002-render"},
		{name: "slice without phase", row: monitor.Row{SliceID: "002-render"}, want: "002-render"},
		{name: "other phase", row: monitor.Row{Phase: " verify ", SliceID: "002-render"}, want: "verify"},
		{name: "blank", want: "-"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PhaseLabel(test.row); got != test.want {
				t.Fatalf("PhaseLabel() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPhaseLabelTruncatesByCells(t *testing.T) {
	got := PhaseLabel(monitor.Row{Phase: "running_slice", SliceID: strings.Repeat("界", 11)})
	if width := cells.Width(got); width != MaxSliceIDCells {
		t.Fatalf("PhaseLabel() width = %d, want %d: %q", width, MaxSliceIDCells, got)
	}
}

func TestDurationLabel(t *testing.T) {
	for _, test := range []struct {
		duration time.Duration
		want     string
	}{
		{-time.Second, "0s"},
		{0, "0s"},
		{time.Second - 1, "0s"},
		{time.Minute - 1, "59s"},
		{time.Minute, "1m"},
		{time.Hour - 1, "59m"},
		{time.Hour, "1h"},
		{25 * time.Hour, "25h"},
	} {
		if got := DurationLabel(test.duration); got != test.want {
			t.Errorf("DurationLabel(%s) = %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestDisplayValue(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{"", "-"},
		{" \t\n", "-"},
		{" value ", " value "},
	} {
		if got := DisplayValue(test.value); got != test.want {
			t.Errorf("DisplayValue(%q) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestIsStalled(t *testing.T) {
	for _, liveness := range []monitor.Liveness{monitor.LivenessMissing, monitor.LivenessLive, monitor.LivenessStale} {
		for _, present := range []bool{false, true} {
			for _, alive := range []bool{false, true} {
				row := monitor.Row{Liveness: liveness, RunLockPresent: present, RunLockProcessAlive: alive}
				want := liveness == monitor.LivenessStale && present && alive
				if got := IsStalled(row); got != want {
					t.Errorf("IsStalled(liveness=%s, present=%t, alive=%t) = %t, want %t", liveness, present, alive, got, want)
				}
			}
		}
	}
}
