package note

import (
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/taodata"
)

func readyInput() ReadinessInput {
	return ReadinessInput{
		Note:     Note{ID: "20260101-000000-0000aaaa", Status: StatusOpen, Text: "Fix `note.LookupPlan` fallthrough.", Tags: []string{"tier1", "autopilot"}},
		FixSites: []FixSiteCheck{{Name: "note.LookupPlan", Kind: "qualified", Found: true}},
		Health:   taodata.RepoHealth{Status: "ok"},
	}
}

func checkByName(t *testing.T, r Readiness, name string) ReadinessCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("check %q missing from %+v", name, r.Checks)
	return ReadinessCheck{}
}

func TestEvaluateReadinessAllPass(t *testing.T) {
	got := EvaluateReadiness(readyInput())
	if !got.Ready {
		t.Fatalf("expected ready verdict, got %+v", got)
	}
	names := make([]string, 0, len(got.Checks))
	for _, c := range got.Checks {
		names = append(names, c.Name)
		if c.Status != ReadinessPass {
			t.Fatalf("check %s status %q reasons %v", c.Name, c.Status, c.Reasons)
		}
	}
	want := []string{"status", "references", "fix_sites", "tier", "markers", "repository"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("check order %v, want %v", names, want)
	}
	if refs := checkByName(t, got, "references"); !reflect.DeepEqual(refs.Reasons, []string{"no note references"}) {
		t.Fatalf("references reasons %v", refs.Reasons)
	}
}

func TestEvaluateReadinessTolerateNilSlices(t *testing.T) {
	got := EvaluateReadiness(ReadinessInput{})
	if got.Ready {
		t.Fatal("zero input must not be ready")
	}
	if len(got.Checks) != 6 {
		t.Fatalf("expected six checks, got %d", len(got.Checks))
	}
}

func TestEvaluateReadinessStatus(t *testing.T) {
	in := readyInput()
	in.Note.Status = StatusArchived
	in.Note.Archive = &ArchiveMetadata{Plan: &PlanLink{ID: "20260102-000000-plan", Dir: "/elsewhere"}}
	got := EvaluateReadiness(in)
	if got.Ready {
		t.Fatal("archived note must not be ready")
	}
	c := checkByName(t, got, "status")
	if c.Status != ReadinessFail || len(c.Reasons) != 1 {
		t.Fatalf("status check %+v", c)
	}
	for _, want := range []string{"archived", "20260102-000000-plan", "tao note run"} {
		if !strings.Contains(c.Reasons[0], want) {
			t.Fatalf("reason %q missing %q", c.Reasons[0], want)
		}
	}
	if strings.Contains(c.Reasons[0], "/elsewhere") {
		t.Fatalf("reason %q leaked link dir", c.Reasons[0])
	}

	in = readyInput()
	in.Note.Status = StatusPromoted
	in.Note.Promotion = &PromotionLinks{Plan: &PlanLink{ID: "20260103-000000-plan"}}
	c = checkByName(t, EvaluateReadiness(in), "status")
	if c.Status != ReadinessFail || !strings.Contains(c.Reasons[0], "20260103-000000-plan") || !strings.Contains(c.Reasons[0], "promoted") {
		t.Fatalf("promoted status check %+v", c)
	}
}

func TestEvaluateReadinessReferences(t *testing.T) {
	openNote := Note{ID: "20260101-000000-0000bbbb", Status: StatusOpen}
	archivedNote := Note{ID: "20260101-000000-0000cccc", Status: StatusArchived}
	tests := []struct {
		name       string
		ref        ReferenceLookup
		wantStatus string
		wantReason []string
	}{
		{
			name:       "merged pass",
			ref:        ReferenceLookup{Reference: "0000cccc", NoteID: archivedNote.ID, Plan: PlanLookup{Destination: "p1", Status: "completed", PlanMerged: true, Resolution: "resolved"}},
			wantStatus: ReadinessPass,
		},
		{
			name:       "completed not merged fail",
			ref:        ReferenceLookup{Reference: "0000cccc", NoteID: archivedNote.ID, Plan: PlanLookup{Destination: "p1", Status: "completed", Resolution: "resolved"}},
			wantStatus: ReadinessFail,
			wantReason: []string{"p1", "completed", "not merged"},
		},
		{
			name:       "open prerequisite fail",
			ref:        ReferenceLookup{Reference: "0000bbbb", NoteID: openNote.ID, Plan: PlanLookup{Resolution: "unlinked"}},
			wantStatus: ReadinessFail,
			wantReason: []string{"prerequisite note 20260101-000000-0000bbbb is open"},
		},
		{
			name:       "missing unknown",
			ref:        ReferenceLookup{Reference: "deadbeef", Plan: PlanLookup{Resolution: "missing"}},
			wantStatus: ReadinessUnknown,
			wantReason: []string{"deadbeef", "missing"},
		},
		{
			name:       "ambiguous note unknown",
			ref:        ReferenceLookup{Reference: "note:deadbeef", Plan: PlanLookup{Resolution: "ambiguous"}},
			wantStatus: ReadinessUnknown,
			wantReason: []string{"note:deadbeef", "ambiguous"},
		},
		{
			name:       "unavailable unknown",
			ref:        ReferenceLookup{Reference: "0000cccc", NoteID: archivedNote.ID, Plan: PlanLookup{Destination: "p1", Resolution: "unavailable"}},
			wantStatus: ReadinessUnknown,
			wantReason: []string{"unavailable"},
		},
		{
			name:       "unlinked unknown",
			ref:        ReferenceLookup{Reference: "0000cccc", NoteID: archivedNote.ID, Plan: PlanLookup{Resolution: "unlinked"}},
			wantStatus: ReadinessUnknown,
			wantReason: []string{"unlinked"},
		},
		{
			name:       "ambiguous plan unknown",
			ref:        ReferenceLookup{Reference: "0000cccc", NoteID: archivedNote.ID, Plan: PlanLookup{Resolution: "ambiguous"}},
			wantStatus: ReadinessUnknown,
			wantReason: []string{"ambiguous"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := readyInput()
			in.Notes = []Note{openNote, archivedNote}
			in.References = []ReferenceLookup{tt.ref}
			got := EvaluateReadiness(in)
			c := checkByName(t, got, "references")
			if c.Status != tt.wantStatus {
				t.Fatalf("status %q, want %q (%+v)", c.Status, tt.wantStatus, c)
			}
			if tt.wantStatus == ReadinessPass {
				if len(c.Reasons) != 0 || !got.Ready {
					t.Fatalf("pass should carry no reasons and keep ready: %+v ready=%v", c, got.Ready)
				}
				return
			}
			if got.Ready {
				t.Fatalf("verdict must not be ready for %s", tt.wantStatus)
			}
			if len(c.Reasons) != 1 {
				t.Fatalf("expected one reason, got %v", c.Reasons)
			}
			for _, want := range tt.wantReason {
				if !strings.Contains(c.Reasons[0], want) {
					t.Fatalf("reason %q missing %q", c.Reasons[0], want)
				}
			}
		})
	}
}

func TestEvaluateReadinessReferencesFailBeatsUnknown(t *testing.T) {
	in := readyInput()
	in.References = []ReferenceLookup{
		{Reference: "deadbeef", Plan: PlanLookup{Resolution: "missing"}},
		{Reference: "0000cccc", NoteID: "20260101-000000-0000cccc", Plan: PlanLookup{Destination: "p1", Status: "in_progress", Resolution: "resolved"}},
	}
	c := checkByName(t, EvaluateReadiness(in), "references")
	if c.Status != ReadinessFail || len(c.Reasons) != 2 {
		t.Fatalf("mixed references %+v", c)
	}
}

func TestEvaluateReadinessFixSites(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		in := readyInput()
		in.FixSites = nil
		c := checkByName(t, EvaluateReadiness(in), "fix_sites")
		if c.Status != ReadinessFail || !reflect.DeepEqual(c.Reasons, []string{"no concrete fix site named (backticked Go identifier or repository-relative path)"}) {
			t.Fatalf("empty fix sites %+v", c)
		}
	})
	t.Run("missing", func(t *testing.T) {
		in := readyInput()
		in.FixSites = []FixSiteCheck{
			{Name: "internal/gone.go", Kind: "path", Found: false},
			{Name: "note.Keep", Kind: "qualified", Found: true},
			{Name: "Vanished", Kind: "identifier", Found: false, Detail: "no declaration under internal/"},
		}
		c := checkByName(t, EvaluateReadiness(in), "fix_sites")
		want := []string{
			"path internal/gone.go not found in checkout",
			"identifier Vanished not found in checkout: no declaration under internal/",
		}
		if c.Status != ReadinessFail || !reflect.DeepEqual(c.Reasons, want) {
			t.Fatalf("missing fix sites %+v", c)
		}
	})
	t.Run("error", func(t *testing.T) {
		in := readyInput()
		in.FixSiteError = "checkout walk exceeded file budget"
		got := EvaluateReadiness(in)
		c := checkByName(t, got, "fix_sites")
		if c.Status != ReadinessUnknown || !reflect.DeepEqual(c.Reasons, []string{"checkout walk exceeded file budget"}) {
			t.Fatalf("fix site error %+v", c)
		}
		if got.Ready {
			t.Fatal("unknown check must keep verdict not ready")
		}
	})
	t.Run("pass", func(t *testing.T) {
		c := checkByName(t, EvaluateReadiness(readyInput()), "fix_sites")
		if c.Status != ReadinessPass || len(c.Reasons) != 0 {
			t.Fatalf("pass fix sites %+v", c)
		}
	})
}

func TestEvaluateReadinessTier(t *testing.T) {
	tests := []struct {
		name       string
		tags       []string
		wantStatus string
		wantReason string
	}{
		{name: "none", tags: []string{"autopilot"}, wantStatus: ReadinessFail, wantReason: "no tier tag"},
		{name: "one", tags: []string{" Tier2 ", "safety"}, wantStatus: ReadinessPass},
		{name: "conflicting", tags: []string{"tier0", "tier3"}, wantStatus: ReadinessFail, wantReason: "conflicting tier tags: tier0, tier3"},
		{name: "nil tags", tags: nil, wantStatus: ReadinessFail, wantReason: "no tier tag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := readyInput()
			in.Note.Tags = tt.tags
			c := checkByName(t, EvaluateReadiness(in), "tier")
			if c.Status != tt.wantStatus {
				t.Fatalf("status %q want %q (%+v)", c.Status, tt.wantStatus, c)
			}
			if tt.wantReason == "" {
				if len(c.Reasons) != 0 {
					t.Fatalf("unexpected reasons %v", c.Reasons)
				}
				return
			}
			if !reflect.DeepEqual(c.Reasons, []string{tt.wantReason}) {
				t.Fatalf("reasons %v want %q", c.Reasons, tt.wantReason)
			}
		})
	}
}

func TestEvaluateReadinessMarkers(t *testing.T) {
	for _, marker := range ReadinessMarkers {
		t.Run(marker, func(t *testing.T) {
			in := readyInput()
			in.Note.Text = "Fix `note.LookupPlan`; " + strings.ToUpper(marker) + " pending."
			got := EvaluateReadiness(in)
			c := checkByName(t, got, "markers")
			if c.Status != ReadinessFail || len(c.Reasons) != 1 || !strings.Contains(c.Reasons[0], `"`+marker+`"`) {
				t.Fatalf("marker %q check %+v", marker, c)
			}
			if got.Ready {
				t.Fatal("marker hit must keep verdict not ready")
			}
		})
	}
	t.Run("multiple", func(t *testing.T) {
		in := readyInput()
		in.Note.Text = "needs scope and needs owner before an owner decision"
		c := checkByName(t, EvaluateReadiness(in), "markers")
		if c.Status != ReadinessFail || len(c.Reasons) != len(ReadinessMarkers) {
			t.Fatalf("multiple markers %+v", c)
		}
	})
	t.Run("free", func(t *testing.T) {
		c := checkByName(t, EvaluateReadiness(readyInput()), "markers")
		if c.Status != ReadinessPass || len(c.Reasons) != 0 {
			t.Fatalf("marker-free %+v", c)
		}
	})
}

func TestEvaluateReadinessRepository(t *testing.T) {
	in := readyInput()
	in.Health = taodata.RepoHealth{Status: "missing_root", Message: "repo root does not exist", Error: true}
	got := EvaluateReadiness(in)
	c := checkByName(t, got, "repository")
	if c.Status != ReadinessFail || !reflect.DeepEqual(c.Reasons, []string{"repository missing_root: repo root does not exist"}) {
		t.Fatalf("unhealthy repository %+v", c)
	}
	if got.Ready {
		t.Fatal("unhealthy repository must keep verdict not ready")
	}
	in.Health = taodata.RepoHealth{Status: "dirty", Message: "non-fatal", Error: false}
	if c := checkByName(t, EvaluateReadiness(in), "repository"); c.Status != ReadinessPass {
		t.Fatalf("non-error health %+v", c)
	}
}

func TestReadinessTierTags(t *testing.T) {
	if !reflect.DeepEqual(ReadinessTierTags, []string{"tier0", "tier1", "tier2", "tier3"}) {
		t.Fatalf("tier tags %v", ReadinessTierTags)
	}
}
