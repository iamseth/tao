package view

import (
	"reflect"
	"testing"
)

func TestColumnWidths(t *testing.T) {
	tests := []struct {
		name    string
		headers []string
		rows    [][]string
		want    []int
	}{
		{name: "empty", want: []int{}},
		{name: "headers only", headers: []string{"NAME", "STATE"}, want: []int{4, 5}},
		{
			name:    "ragged rows",
			headers: []string{"A"},
			rows:    [][]string{{"alpha", "b"}, {}, {"x", "bravo", "charlie"}},
			want:    []int{5, 5, 7},
		},
		{
			name:    "unicode uses runes",
			headers: []string{"PLAN", "状態"},
			rows:    [][]string{{"café", "準備中"}},
			want:    []int{4, 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ColumnWidths(test.headers, test.rows); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ColumnWidths() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRuneWidthAndPad(t *testing.T) {
	if got := RuneWidth("café"); got != 4 {
		t.Fatalf("RuneWidth() = %d, want 4", got)
	}
	if got := Pad("café", 6); got != "café  " {
		t.Fatalf("Pad() = %q, want %q", got, "café  ")
	}
	if got := Pad("already", 3); got != "already" {
		t.Fatalf("Pad() must not truncate: %q", got)
	}
}
