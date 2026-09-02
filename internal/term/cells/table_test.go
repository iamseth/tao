package cells

import "testing"

func TestWideRuneIntervalsSortedAndNonOverlapping(t *testing.T) {
	for index, interval := range wideRuneIntervals {
		if interval.first > interval.last {
			t.Fatalf("interval %d has first %U after last %U", index, interval.first, interval.last)
		}
		if index > 0 && wideRuneIntervals[index-1].last >= interval.first {
			t.Fatalf("intervals %d and %d overlap or are unsorted: %U-%U and %U-%U",
				index-1, index,
				wideRuneIntervals[index-1].first, wideRuneIntervals[index-1].last,
				interval.first, interval.last,
			)
		}
	}
}

func TestRuneWidths(t *testing.T) {
	tests := []struct {
		name string
		rune rune
		want int
	}{
		{name: "watch", rune: '\u231A', want: 2},
		{name: "high voltage", rune: '\u26A1', want: 2},
		{name: "check mark button", rune: '\u2705', want: 2},
		{name: "star", rune: '\u2B50', want: 2},
		{name: "CJK", rune: '\u754C', want: 2},
		{name: "fullwidth Latin", rune: '\uFF21', want: 2},
		{name: "emoji", rune: '\U0001F600', want: 2},
		{name: "ASCII", rune: 'A', want: 1},
		{name: "neutral Latin", rune: '\u00E9', want: 1},
		{name: "combining acute", rune: '\u0301', want: 0},
		{name: "zero width space", rune: '\u200B', want: 0},
		{name: "skin tone modifier", rune: '\U0001F3FD', want: 0},
		{name: "conjoining Jamo", rune: '\u1160', want: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := runeCellWidth(test.rune); got != test.want {
				t.Fatalf("runeCellWidth(%U) = %d, want %d", test.rune, got, test.want)
			}
		})
	}
}
