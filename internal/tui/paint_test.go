package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/term"
	"github.com/iamseth/tao/internal/theme"
)

func TestTerminalFrameOverwritesWithoutBlanking(t *testing.T) {
	tests := []struct {
		name, frame string
		size        term.Size
		want        string
	}{
		{"shorter content", clearScreenSequence + "abc\nx", term.Size{Width: 8, Height: 4}, "\x1b[1;1Habc\x1b[K\x1b[2;1Hx\x1b[K\x1b[3;1H\x1b[J"},
		{"right and bottom margins", clearScreenSequence + "abcd\n界界\n", term.Size{Width: 4, Height: 2}, "\x1b[1;1Habcd\x1b[2;1H界界"},
		{"styled line", clearScreenSequence + "\x1b[31mx\x1b[0m", term.Size{Width: 4, Height: 1}, "\x1b[1;1H\x1b[31mx\x1b[0m\x1b[K"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := terminalFrame(tt.frame, tt.size); got != tt.want {
				t.Fatalf("frame = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestChangesBackgroundRefreshIsQuiet(t *testing.T) {
	m := DetailChangesModel{
		Status: "ready", DiffStatus: "ready",
		Snapshot: DetailChangesSnapshot{Availability: "ready", Signature: "same", Files: []DetailChangedFile{{Path: "a"}}},
		Diff:     DetailFileDiff{Path: "a", Signature: "same"},
	}
	render := func() string { return strings.Join(renderChangesPane(m, 120, 24, theme.Palette{}, time.Time{}), "\n") }
	before := render()
	m.Updating = true
	m.DiffStatus = "loading"
	if got := render(); got != before {
		t.Fatalf("background refresh changed cached presentation:\nbefore: %s\nafter: %s", before, got)
	}
	m.Diff = DetailFileDiff{}
	if got := render(); !strings.Contains(got, "Loading diff") {
		t.Fatalf("initial loading feedback missing: %s", got)
	}
}
