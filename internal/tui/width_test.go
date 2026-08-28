package tui

import "testing"

func TestVisibleWidth(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
	}{
		{name: "Japanese", value: "日本語リポジトリ", want: 16},
		// The fixture contains 16 narrow cells and one wide emoji cell.
		{name: "emoji", value: "emoji-🧭-workspace", want: 18},
		{name: "combining", value: "combining-e\u0301-repo", want: 16},
		{name: "ellipsis", value: "…", want: 1},
		{name: "styled", value: "\x1b[31m日本\x1b[0m", want: 4},
		{name: "CSI", value: "a\x1b[2Kb", want: 2},
		{name: "OSC BEL", value: "a\x1b]0;ignored\ab", want: 2},
		{name: "OSC ST", value: "a\x1b]8;;https://example.test\x1b\\b\x1b]8;;\x1b\\", want: 2},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := visibleWidth(test.value); got != test.want {
				t.Fatalf("visibleWidth(%q) = %d, want %d", test.value, got, test.want)
			}
		})
	}
}

func TestPadCells(t *testing.T) {
	tests := []struct {
		name  string
		value string
		width int
		want  string
	}{
		{name: "plain", value: "日本", width: 6, want: "日本  "},
		{name: "combining", value: "e\u0301", width: 3, want: "e\u0301  "},
		{name: "active style", value: "\x1b[31m日", width: 4, want: "\x1b[31m日  \x1b[0m"},
		{name: "closed style", value: "\x1b[31m日\x1b[0m", width: 3, want: "\x1b[31m日\x1b[0m "},
		{name: "already wide enough", value: "🧭", width: 1, want: "🧭"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := padCells(test.value, test.width); got != test.want {
				t.Fatalf("padCells(%q, %d) = %q, want %q", test.value, test.width, got, test.want)
			}
		})
	}
}

func TestTruncateCells(t *testing.T) {
	tests := []struct {
		name  string
		value string
		width int
		want  string
	}{
		{name: "zero", value: "value", width: 0, want: ""},
		{name: "wide rune does not split", value: "日本語", width: 5, want: "日本"},
		{name: "emoji", value: "a🧭b", width: 3, want: "a🧭"},
		{name: "trailing combining mark", value: "ae\u0301x", width: 2, want: "ae\u0301"},
		{name: "active style is closed", value: "\x1b[31m日本語", width: 4, want: "\x1b[31m日本\x1b[0m"},
		{name: "closed style stays closed", value: "\x1b[31m日\x1b[0m本", width: 2, want: "\x1b[31m日\x1b[0m"},
		{name: "OSC is preserved", value: "\x1b]0;title\aab", width: 1, want: "\x1b]0;title\aa"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := truncateCells(test.value, test.width); got != test.want {
				t.Fatalf("truncateCells(%q, %d) = %q, want %q", test.value, test.width, got, test.want)
			}
		})
	}
}
