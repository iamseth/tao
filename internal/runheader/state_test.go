package runheader

import (
	"reflect"
	"testing"
	"time"
)

func TestStateClone(t *testing.T) {
	state := testHeaderState()
	state.StartedAt = time.Date(2026, 9, 26, 1, 0, 0, 0, time.UTC)
	cloned := state.Clone()
	if !reflect.DeepEqual(cloned, state) {
		t.Fatalf("Clone() = %+v, want %+v", cloned, state)
	}

	cloned.Slices[0].Title = "changed clone"
	if state.Slices[0].Title != "Terminal seam" {
		t.Fatal("mutating cloned slices changed the original")
	}
	state.Slices[1].Title = "changed original"
	if cloned.Slices[1].Title != "Render header" {
		t.Fatal("mutating original slices changed the clone")
	}
}

func TestStateCloneZero(t *testing.T) {
	state := State{}
	if cloned := state.Clone(); !reflect.DeepEqual(cloned, state) {
		t.Fatalf("Clone() = %+v, want zero state", cloned)
	}
}
