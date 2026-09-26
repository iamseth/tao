package plan

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agentinput"
)

func TestSliceRulings(t *testing.T) {
	var many []string
	for i := range MaxSliceRulings + 3 {
		many = append(many, fmt.Sprintf("Ruling: decision %d", i))
	}
	long := "Ruling: " + strings.Repeat("界", MaxSliceRulingRunes)
	tests := []struct {
		name  string
		notes string
		want  []string
	}{
		{name: "empty"},
		{name: "ordinary notes", notes: "Implemented the parser.\nTests passed."},
		{name: "single", notes: "Ruling: use the declared identifier", want: []string{"Ruling: use the declared identifier"}},
		{name: "interleaved", notes: "Summary\nRuling: first\nOrdinary note\nRuling: second\nDone", want: []string{"Ruling: first", "Ruling: second"}},
		{name: "whitespace", notes: "\t Ruling: keep internal  spacing \t\r\n\u2003Ruling: next\u2003", want: []string{"Ruling: keep internal  spacing", "Ruling: next"}},
		{name: "exact prefix only", notes: "ruling: lowercase\nRULING: uppercase\n- Ruling: list item\nText Ruling: embedded"},
		{name: "empty ruling", notes: "Ruling:\nRuling: \t\r\nRuling:\u2003"},
		{name: "continuation excluded", notes: "Ruling: first line\n  continuation\n\tmore context", want: []string{"Ruling: first line"}},
		{name: "no separator required", notes: "Ruling:decision", want: []string{"Ruling:decision"}},
		{name: "entry cap", notes: strings.Join(many, "\n"), want: many[:MaxSliceRulings]},
		{name: "empty lines do not consume cap", notes: strings.Repeat("Ruling: \n", MaxSliceRulings) + strings.Join(many, "\n"), want: many[:MaxSliceRulings]},
		{name: "rune cap", notes: long, want: []string{agentinput.CapRunes(long, MaxSliceRulingRunes)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SliceRulings(tt.notes); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SliceRulings() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestCompleteSlicePreservesRulings(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	record := testRecord(dir, detail)
	startedAt := detail.State.CreatedAt.Add(time.Minute)
	if err := record.StartSlice("001-a", SliceStartRequest{StartedAt: startedAt}); err != nil {
		t.Fatal(err)
	}
	const notes = "Implemented parser.\nRuling: use the declared identifier\nTests passed."
	if err := record.CompleteSlice("001-a", notes, nil, startedAt.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	slice := readSlicesFile(t, dir).Slices[0]
	if slice.Status != StatusCompleted || slice.Notes != notes {
		t.Fatalf("persisted slice status = %q, notes = %q", slice.Status, slice.Notes)
	}
	want := []string{"Ruling: use the declared identifier"}
	if got := SliceRulings(slice.Notes); !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted SliceRulings() = %#v, want %#v", got, want)
	}
}
