package note

import (
	"fmt"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/agentinput"
	"github.com/iamseth/tao/internal/plan"
)

func TestLookupPlan(t *testing.T) {
	link := &PlanLink{ID: "target", Dir: "/untrusted/elsewhere"}
	for _, tc := range []struct {
		name       string
		item       Note
		summaries  []plan.PlanSummary
		resolution string
		merged     bool
	}{
		{"archive", Note{Archive: &ArchiveMetadata{Plan: link}}, []plan.PlanSummary{{ID: "target", Status: plan.StatusCompleted, PlanMerged: true}}, "resolved", true},
		{"promotion", Note{Promotion: &PromotionLinks{Plan: link}}, []plan.PlanSummary{{ID: "target", Status: plan.StatusCompleted}}, "resolved", false},
		{"missing", Note{Archive: &ArchiveMetadata{Plan: link}}, nil, "missing", false},
		{"invalid", Note{Archive: &ArchiveMetadata{Plan: link}}, []plan.PlanSummary{{ID: "target", Status: plan.StatusInvalid, PlanMerged: true}}, "unavailable", false},
		{"warning", Note{Archive: &ArchiveMetadata{Plan: link}}, []plan.PlanSummary{{ID: "target", Status: plan.StatusCompleted, PlanMerged: true, Warnings: []string{"bad"}}}, "unavailable", false},
		{"duplicate", Note{Archive: &ArchiveMetadata{Plan: link}}, []plan.PlanSummary{{ID: "target"}, {ID: "target"}}, "ambiguous", false},
		{"conflict", Note{Archive: &ArchiveMetadata{Plan: link}, Promotion: &PromotionLinks{Plan: &PlanLink{ID: "other"}}}, nil, "ambiguous", false},
		{"same links", Note{Archive: &ArchiveMetadata{Plan: link}, Promotion: &PromotionLinks{Plan: link}}, []plan.PlanSummary{{ID: "target", Status: plan.StatusPlanned}}, "resolved", false},
		{"open", Note{}, nil, "unlinked", false},
		{"planning session", Note{Promotion: &PromotionLinks{PlanningSession: &PlanningSessionLink{ID: "target"}}}, nil, "unlinked", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := LookupPlan(tc.item, tc.summaries)
			if got.Resolution != tc.resolution || got.PlanMerged != tc.merged {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

func TestReferencePlanProjectionAndBounds(t *testing.T) {
	id := "20261111-120000-deadbeef"
	notes := []Note{{ID: id, Archive: &ArchiveMetadata{Plan: &PlanLink{ID: "target", Dir: "/ignored"}}, Text: "12345678"}}
	got := ResolveReferences("note:deadbeef", notes, []plan.PlanSummary{{ID: "target", Status: plan.StatusCompleted, PlanMerged: true}})
	if len(got) != 1 || got[0].Reference != "note:deadbeef" || got[0].Plan.Destination != "target" || got[0].Plan.Status != plan.StatusCompleted || !got[0].Plan.PlanMerged {
		t.Fatalf("projection: %+v", got)
	}
	if got := ResolveReferences(strings.Repeat("x", agentinput.MaxTextRunes)+" deadbeef", notes, nil); len(got) != 0 {
		t.Fatal("rune bound ignored")
	}
	got = ResolveReferences("12345678 deadbeef", notes, nil)
	if len(got) != 2 || got[0].Reference != "12345678" || got[1].NoteID != id {
		t.Fatalf("first mention order: %+v", got)
	}
}

func TestResolveReferences(t *testing.T) {
	id := "20261111-120000-deadbeef"
	notes := []Note{{ID: id}, {ID: "20261111-120001-abcdef12"}, {ID: "20261111-120002-abcdef12"}}
	text := "First (note:" + id + ") deadbeef, note:deadbeef; abcdef12 and 12345678."
	got := ResolveReferences(text, notes, nil)
	if len(got) != 3 || got[0].NoteID != id || got[0].Plan.Resolution != "unlinked" || got[1].Plan.Resolution != "ambiguous" || got[2].Plan.Resolution != "missing" {
		t.Fatalf("got %+v", got)
	}
	for _, text := range []string{"https://example.test/deadbeef", "feature-deadbeef", "deadbeef01234567", "prefix_deadbeef", "note:../../deadbeef", "ordinary prose", "20261111-120000-plan-deadbeef", "\x00deadbeef"} {
		if got := ResolveReferences(text, notes, nil); len(got) != 0 {
			t.Errorf("%q: %+v", text, got)
		}
	}
	duplicate := ResolveReferences("deadbeef", []Note{{ID: id}, {ID: id}}, nil)
	if len(duplicate) != 1 || duplicate[0].NoteID != "" || duplicate[0].Plan.Resolution != "ambiguous" {
		t.Fatalf("duplicates: %+v", duplicate)
	}
	if got := ResolveReferences(strings.Repeat(" ", int(agentinput.MaxFileBytes))+"deadbeef", notes, nil); len(got) != 0 {
		t.Fatal("oversize input accepted")
	}
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "%08x ", i)
	}
	if got := ResolveReferences(b.String(), nil, nil); len(got) != 128 {
		t.Fatalf("output bound: %d", len(got))
	}
}
