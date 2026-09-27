package plan

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLatestPlanOwnedBlocker(t *testing.T) {
	if got := LatestPlanOwnedBlocker(nil, "001-a"); got != nil {
		t.Fatalf("nil detail returned %+v", got)
	}
	detail := &PlanDetail{}
	valid := Event{Type: EventTypeSliceBlocked, SliceID: "001-a", BlockerClassification: BlockerClassificationPlanOwned, HeadSHA: "head", Fingerprint: "first"}
	detail.Events = []Event{{Type: EventTypeSliceBlocked, SliceID: "001-a", Reason: "plan-owned lint failure"}}
	if got := LatestPlanOwnedBlocker(detail, "001-a"); got != nil {
		t.Fatalf("prose authorized ownership: %+v", got)
	}
	detail.Events = append(detail.Events, valid)
	latest := valid
	latest.Fingerprint = "latest"
	detail.Events = append(detail.Events, latest)
	for _, change := range []func(*Event){
		func(e *Event) { e.Type = EventTypeSliceCompleted },
		func(e *Event) { e.SliceID = "002-b" },
		func(e *Event) { e.BlockerClassification = "" },
		func(e *Event) { e.BlockerClassification = "unknown" },
		func(e *Event) { e.HeadSHA = "" },
		func(e *Event) { e.Fingerprint = "" },
	} {
		invalid := valid
		change(&invalid)
		detail.Events = append(detail.Events, invalid)
	}
	if got := LatestPlanOwnedBlocker(detail, "001-a"); got == nil || !reflect.DeepEqual(*got, latest) {
		t.Fatalf("latest blocker = %+v, want %+v", got, latest)
	}
}

func TestPlanOwnershipBase(t *testing.T) {
	for _, detail := range []*PlanDetail{nil, {}, {State: State{Repo: Repo{BaseCommit: " \n "}}}} {
		if got := PlanOwnershipBase(detail); got != "" {
			t.Fatalf("missing base = %q", got)
		}
	}
	if got := PlanOwnershipBase(&PlanDetail{State: State{Repo: Repo{BaseCommit: " abc123\n"}}}); got != "abc123" {
		t.Fatalf("base = %q", got)
	}
}

func TestClassifyBlockerPaths(t *testing.T) {
	for _, tt := range []struct {
		name           string
		failing, owned []string
		want           bool
	}{
		{name: "empty"},
		{name: "unknown ownership", failing: []string{"a.go"}},
		{name: "normalized", failing: []string{` ./internal\plan/a.go `, "internal/plan/./a.go"}, owned: []string{`internal\plan\a.go`}, want: true},
		{name: "subset", failing: []string{"a.go"}, owned: []string{"b.go", "a.go"}, want: true},
		{name: "non-owned", failing: []string{"a.go", "b.go"}, owned: []string{"a.go"}},
		{name: "blank", failing: []string{" "}, owned: []string{" "}},
		{name: "invalid mixed with owned", failing: []string{"a.go", "../outside.go"}, owned: []string{"a.go", "../outside.go"}},
		{name: "absolute", failing: []string{"/a.go"}, owned: []string{"/a.go"}},
		{name: "windows absolute", failing: []string{`C:\a.go`}, owned: []string{`C:\a.go`}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyBlockerPaths(tt.failing, tt.owned); got != tt.want {
				t.Fatalf("classified = %v, want %v", got, tt.want)
			}
		})
	}
	owned := make([]string, 65)
	for i := range owned {
		owned[i] = fmt.Sprintf("%02d.go", i)
	}
	if !ClassifyBlockerPaths([]string{owned[64]}, owned) {
		t.Fatal("ownership set must not be truncated")
	}
	failing := append(append([]string(nil), owned...), "unowned.go")
	if ClassifyBlockerPaths(failing, owned) {
		t.Fatal("classification must check paths beyond the event storage bound")
	}
}

func TestWorktreeFingerprint(t *testing.T) {
	for _, parts := range [][]string{nil, {""}, {"", ""}} {
		if got := WorktreeFingerprint(parts...); got != "" {
			t.Fatalf("empty parts fingerprint = %q", got)
		}
	}
	if got := WorktreeFingerprint("abc"); got != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatalf("SHA-256 fingerprint = %q", got)
	}
	got := WorktreeFingerprint("head", "diff")
	if len(got) != 64 || got != WorktreeFingerprint("head", "diff") {
		t.Fatalf("non-deterministic fingerprint: %q", got)
	}
	if got != WorktreeFingerprint("head\x00diff") {
		t.Fatal("parts were not joined with NUL")
	}
	for _, parts := range [][]string{{"diff", "head"}, {"headdiff"}, {"hea", "ddiff"}, {"head", "diff", ""}} {
		if got == WorktreeFingerprint(parts...) {
			t.Fatalf("fingerprint did not distinguish %q", parts)
		}
	}
}

func TestBlockSliceWithEvidencePersists(t *testing.T) {
	dir := t.TempDir()
	detail := startSliceDetail(dir)
	writeStartSliceArtifacts(t, dir, detail)
	evidence := &SliceBlockedEvidence{GateCommand: "go test ./internal/plan", FailingPaths: []string{` ./internal\plan/a.go `}, PlanOwned: true, HeadSHA: "head", WorktreeFingerprint: "fingerprint"}
	if err := testRecord(dir, detail).BlockSliceWithEvidence("001-a", " lint failed ", evidence, editTime()); err != nil {
		t.Fatal(err)
	}
	events, warnings, err := readEvents(filepath.Join(dir, "events.jsonl"))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("read events: %v, %v", err, warnings)
	}
	got := LatestPlanOwnedBlocker(&PlanDetail{Events: events}, "001-a")
	if got == nil || got.Command != evidence.GateCommand || got.HeadSHA != evidence.HeadSHA || got.Fingerprint != evidence.WorktreeFingerprint || !reflect.DeepEqual(got.Paths, []string{"internal/plan/a.go"}) {
		t.Fatalf("persisted evidence = %+v", got)
	}
	if note := readSlicesFile(t, dir).Slices[0].BlockerNote; note != "lint failed" {
		t.Fatalf("blocker note = %q", note)
	}
}
