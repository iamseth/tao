package insights

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/testenv"
)

func TestDefectNotesBoundedAndLinked(t *testing.T) {
	dir := t.TempDir()
	save := func(id string, n note.Note) {
		t.Helper()
		b, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, id+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	n := note.Note{Schema: note.Schema, ID: "b", Repo: note.RepoReference{ID: "repo"}, Text: "Fixes-Plan: 20260101-000000-target", Status: note.StatusArchived, Tags: []string{" BUG "}, Archive: &note.ArchiveMetadata{Plan: &note.PlanLink{ID: "current"}}}
	n.CreatedAt = time.Now()
	n.UpdatedAt = n.CreatedAt
	save("b", n)
	n.ID = "a"
	n.Archive = nil
	n.Status = note.StatusPromoted
	n.Promotion = &note.PromotionLinks{Plan: &note.PlanLink{ID: "legacy"}}
	save("a", n)
	got, complete := readDefectNotes(context.Background(), "repo", dir)
	if !complete || len(got) != 2 || got[0].LegacyPromotionPlanID != "legacy" || got[1].ArchivePlanID != "current" || !reflect.DeepEqual(got[0].Tags, []string{"bug"}) {
		t.Fatalf("%+v, %v", got, complete)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "a.json")) // #nosec G304 -- test fixture.
	n.ID = "wrong"
	n.Repo.ID = "other"
	save("wrong", n)
	if err := os.Symlink(filepath.Join(dir, "a.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "large.json"), []byte(strings.Repeat("x", defectNoteFileBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	got, complete = readDefectNotes(context.Background(), "repo", dir)
	if complete || len(got) != 2 {
		t.Fatalf("%+v, %v", got, complete)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "a.json")) // #nosec G304 -- test fixture.
	if string(before) != string(after) {
		t.Fatal("note changed")
	}
	if _, ok := readDefectNotes(context.Background(), "repo", filepath.Join(dir, "missing")); ok {
		t.Fatal("missing notes treated as complete")
	}
}

func TestDefectNotesTotalBoundAndCancellation(t *testing.T) {
	testenv.SkipIfShort(t)
	dir := t.TempDir()
	data := []byte(strings.Repeat(" ", defectNoteFileBytes))
	for i := 0; i < defectNoteTotalBytes/defectNoteFileBytes+1; i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%03d.json", i)), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, complete := readDefectNotes(context.Background(), "repo", dir); complete {
		t.Fatal("total overflow accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if notes, complete := readDefectNotes(ctx, "repo", dir); complete || len(notes) != 0 {
		t.Fatal("cancelled read accepted")
	}
	link := filepath.Join(t.TempDir(), "notes")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, complete := readDefectNotes(context.Background(), "repo", link); complete {
		t.Fatal("symlink directory accepted")
	}
}

func TestDefectNotesEntryOverflow(t *testing.T) {
	testenv.SkipIfShort(t)
	dir := t.TempDir()
	for i := 0; i <= defectNoteEntries; i++ {
		f, err := os.CreateTemp(dir, "entry-")
		if err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
	}
	got, ok := readDefectNotes(context.Background(), "repo", dir)
	if ok || len(got) != 0 {
		t.Fatal("accepted enumeration prefix")
	}
}
