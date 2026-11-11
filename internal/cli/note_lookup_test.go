package cli

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
)

func TestNoteLookupContext(t *testing.T) {
	registered := taodata.Repo{ID: "repo-one", Name: "one", Root: t.TempDir()}
	app, _, _ := noteTestApp(t, nil, registered)
	item := note.Note{Text: "note:deadbeef note:aaaaaaaa", Promotion: &note.PromotionLinks{Plan: &note.PlanLink{ID: "linked", Dir: "/must/not/read"}}}
	predecessor := note.Note{ID: "20261111-120000-deadbeef", Archive: &note.ArchiveMetadata{Plan: &note.PlanLink{ID: "linked", Dir: "/must/not/read"}}}
	for _, tc := range []struct {
		name, status, resolution string
		merged                   bool
		planErr, noteErr         error
		warnings                 []note.Warning
	}{
		{name: "merged", status: plan.StatusCompleted, resolution: "resolved", merged: true},
		{name: "reopened", status: plan.StatusInProgress, resolution: "resolved"},
		{name: "completed alone", status: plan.StatusCompleted, resolution: "resolved"},
		{name: "missing", resolution: "missing"},
		{name: "plan failure", planErr: errors.New("unreadable"), resolution: "unavailable"},
		{name: "note failure", noteErr: errors.New("unreadable"), resolution: "unavailable"},
		{name: "partial notes", warnings: []note.Warning{{Err: errors.New("bad note")}}, resolution: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			app.Repository = func(dir string) Repository {
				calls++
				if dir != app.registry().PlansDir(registered) {
					t.Fatalf("cross repository lookup: %s", dir)
				}
				var summaries []plan.PlanSummary
				if tc.status != "" {
					summaries = []plan.PlanSummary{{ID: "linked", Status: tc.status, PlanMerged: tc.merged}}
				}
				return fakeRepository{summaries: summaries, err: tc.planErr}
			}
			repo := &jsonNoteRepository{rows: []note.Note{predecessor}, err: tc.noteErr, warnings: tc.warnings}
			got, err := app.noteLookupContext(context.Background(), registered, repo, item)
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !repo.filter.All || len(got.References) != 2 {
				t.Fatalf("lookup: %+v calls=%d", got, calls)
			}
			ref := got.References[0].Plan
			if ref.Resolution != tc.resolution || ref.PlanMerged != tc.merged {
				t.Fatalf("reference: %+v", ref)
			}
			if tc.resolution != "resolved" && (ref.ID != nil || ref.Status != nil) {
				t.Fatalf("fabricated plan: %+v", ref)
			}
			if got.References[1].Plan.PlanMerged {
				t.Fatal("missing reference merged")
			}
		})
	}
	app.Repository = func(string) Repository { return fakeRepository{} }
	duplicate := predecessor
	duplicate.ID = "20261112-120000-deadbeef"
	ambiguous, err := app.noteLookupContext(context.Background(), registered, &jsonNoteRepository{rows: []note.Note{predecessor, duplicate}}, item)
	if err != nil || ambiguous.References[0].Plan.Resolution != "ambiguous" || ambiguous.References[0].Plan.PlanMerged {
		t.Fatalf("ambiguous: %+v, %v", ambiguous, err)
	}
	app.Repository = func(string) Repository { return fakeRepository{err: context.Canceled} }
	if _, err := app.noteLookupContext(context.Background(), registered, &jsonNoteRepository{}, item); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestNoteShowJSONProvenance(t *testing.T) {
	registered := taodata.Repo{ID: "repo-one", Name: "one", Root: t.TempDir()}
	app, out, _ := noteTestApp(t, nil, registered)
	item := note.Note{ID: "20261111-120000-deadbeef", Status: note.StatusArchived, Text: "source", Archive: &note.ArchiveMetadata{Reason: "linked", Plan: &note.PlanLink{ID: "linked"}, PlanningSession: &note.PlanningSessionLink{ID: "legacy"}}, Promotion: &note.PromotionLinks{PlanningSession: &note.PlanningSessionLink{ID: "legacy"}}}
	app.NoteRepository = func(string, note.RepoReference) NoteRepository {
		return lookupShowRepository{jsonNoteRepository: &jsonNoteRepository{rows: []note.Note{item}}, item: item}
	}
	app.Repository = func(string) Repository {
		return fakeRepository{summaries: []plan.PlanSummary{{ID: "linked", Status: plan.StatusCompleted, PlanMerged: true}}}
	}
	if err := app.Run(context.Background(), []string{"note", "show", item.ID, "--json"}); err != nil {
		t.Fatal(err)
	}
	var row struct {
		noteListJSONRow
		Plan      notePlanJSON          `json:"plan"`
		Archive   *note.ArchiveMetadata `json:"archive"`
		Promotion *note.PromotionLinks  `json:"promotion"`
	}
	if err := json.Unmarshal(out.Bytes(), &row); err != nil {
		t.Fatal(err)
	}
	if row.Status != note.StatusArchived || row.Plan.Status == nil || *row.Plan.Status != plan.StatusCompleted || !row.Plan.PlanMerged || row.Destination == nil || *row.Destination != "linked" || row.Archive.PlanningSession.ID != "legacy" || row.Promotion.PlanningSession.ID != "legacy" {
		t.Fatalf("provenance: %s", out.String())
	}
}

type lookupShowRepository struct {
	*jsonNoteRepository
	item note.Note
}

func (r lookupShowRepository) Get(context.Context, string) (note.Note, error) { return r.item, nil }

func TestNoteShowJSONAliases(t *testing.T) {
	registered := taodata.Repo{ID: "repo-one", Name: "one", Root: t.TempDir()}
	app, out, _ := noteTestApp(t, nil, registered)
	repo := app.noteRepository(registered)
	item, err := repo.Create(context.Background(), "depends on note:deadbeef", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"note", "show", item.ID}); err != nil {
		t.Fatal(err)
	}
	human := out.String()
	if !strings.Contains(human, "Text:\n"+item.Text) || strings.Contains(human, "references") {
		t.Fatal(human)
	}
	for _, sub := range []string{"show", "s"} {
		out.Reset()
		if err := app.Run(context.Background(), []string{"note", sub, item.ID, "--json"}); err != nil {
			t.Fatal(err)
		}
		var row struct {
			noteListJSONRow
			Repository string `json:"repository"`
			Plan       struct {
				ID         *string `json:"id"`
				Status     *string `json:"status"`
				Merged     bool    `json:"plan_merged"`
				Resolution string  `json:"resolution"`
			} `json:"plan"`
			References []json.RawMessage `json:"references"`
		}
		if err := json.Unmarshal(out.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(row.noteListJSONRow, projectNoteListJSON(item)) {
			t.Fatalf("list parity: %#v", row.noteListJSONRow)
		}
		if row.ID != item.ID || row.Status != note.StatusOpen || row.Text != item.Text || row.Repository != registered.ID || row.Plan.ID != nil || row.Plan.Status != nil || row.Plan.Merged || row.Plan.Resolution != "unlinked" || len(row.References) != 1 {
			t.Fatalf("output: %s", out.String())
		}
	}
}
