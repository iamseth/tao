package note

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRepositoryCreateResolveListAndFilter(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "repos", "repo-a", "notes")
	times := []time.Time{time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC), time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)}
	calls := 0
	r := &Repository{Dir: dir, Repo: RepoReference{ID: "repo-a", Root: "/repo"}, Now: func() time.Time { v := times[calls]; calls++; return v }, IDSuffix: func() string { return []string{"aaa", "bbb"}[calls-1] }}
	first, err := r.Create(context.Background(), "first", []string{" Bug ", "bug", "CLI"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.Create(context.Background(), "second", []string{"cli"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(first.Tags, ",") != "bug,cli" {
		t.Fatalf("tags = %#v", first.Tags)
	}
	got, err := r.Get(context.Background(), first.ID[:12])
	if err != nil || got.ID != first.ID {
		t.Fatalf("prefix Get = %#v, %v", got, err)
	}
	got, err = r.Get(context.Background(), first.ID)
	if err != nil || got.ID != first.ID {
		t.Fatalf("exact Get = %#v, %v", got, err)
	}
	notes, warnings, err := r.List(context.Background(), Filter{Tags: []string{"CLI"}})
	if err != nil || len(warnings) != 0 || len(notes) != 2 || notes[0].ID != second.ID {
		t.Fatalf("List = %#v, %#v, %v", notes, warnings, err)
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("notes dir mode = %v, %v", info, err)
	}
	if info, err := os.Stat(filepath.Join(dir, first.ID+".json")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("record mode = %v, %v", info, err)
	}
}

func TestRepositoryLifecycleAndDefaultList(t *testing.T) {
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	r := &Repository{Dir: t.TempDir(), Repo: RepoReference{ID: "repo"}, Now: func() time.Time { now = now.Add(time.Minute); return now }, IDSuffix: func() string { return "one" }}
	n, err := r.Create(context.Background(), "idea", nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err = r.Archive(context.Background(), n.ID, "later")
	if err != nil || n.Status != StatusArchived || n.Archive.Reason != "later" {
		t.Fatalf("Archive = %#v, %v", n, err)
	}
	if notes, _, _ := r.List(context.Background(), Filter{}); len(notes) != 0 {
		t.Fatalf("default list included archived: %#v", notes)
	}
	if _, err := r.Archive(context.Background(), n.ID, "again"); err != nil {
		t.Fatalf("idempotent archive: %v", err)
	}
	n, err = r.Reopen(context.Background(), n.ID)
	if err != nil || n.Status != StatusOpen || n.Archive != nil {
		t.Fatalf("Reopen = %#v, %v", n, err)
	}
	if _, err := r.Reopen(context.Background(), n.ID); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("second Reopen error = %v", err)
	}
	n, err = r.PromoteToPlan(context.Background(), n.ID, PlanLink{ID: "plan-a"})
	if err != nil || n.Status != StatusPromoted {
		t.Fatalf("Promote = %#v, %v", n, err)
	}
	if _, err := r.PromoteToPlan(context.Background(), n.ID, PlanLink{ID: "plan-a"}); err != nil {
		t.Fatalf("idempotent Promote: %v", err)
	}
	if _, err := r.Edit(context.Background(), n.ID, "changed", nil); !errors.Is(err, ErrImmutable) {
		t.Fatalf("Edit promoted error = %v", err)
	}
	if _, err := r.Archive(context.Background(), n.ID, ""); !errors.Is(err, ErrImmutable) {
		t.Fatalf("Archive promoted error = %v", err)
	}
}

func TestRepositoryValidationResolutionWarningsAndCancellation(t *testing.T) {
	r := &Repository{Dir: t.TempDir(), Repo: RepoReference{ID: "repo"}, Now: time.Now, IDSuffix: func() string { return "abc" }}
	for _, text := range []string{" ", strings.Repeat("x", MaxText+1)} {
		if _, err := r.Create(context.Background(), text, nil); err == nil {
			t.Fatalf("Create(%d bytes) succeeded", len(text))
		}
	}
	if _, err := r.Get(context.Background(), "../abc"); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("unsafe id error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "alpha-one.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "alpha-two.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Get(context.Background(), "alpha"); !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("ambiguous error = %v", err)
	}
	notes, warnings, err := r.List(context.Background(), Filter{All: true})
	if err != nil || len(notes) != 0 || len(warnings) != 2 {
		t.Fatalf("List malformed = %#v, %#v, %v", notes, warnings, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Create(ctx, "valid", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Create = %v", err)
	}
	missing := &Repository{Dir: filepath.Join(t.TempDir(), "missing"), Repo: RepoReference{ID: "repo"}}
	if got, warns, err := missing.List(context.Background(), Filter{}); err != nil || got != nil || warns != nil {
		t.Fatalf("missing List = %#v %#v %v", got, warns, err)
	}
}

func TestRepositoryConcurrentCreateCollisionPreservesBothNotes(t *testing.T) {
	now := time.Date(2026, 7, 13, 10, 0, 0, 0, time.UTC)
	r := &Repository{Dir: t.TempDir(), Repo: RepoReference{ID: "repo"}, Now: func() time.Time { return now }, IDSuffix: func() string { return "same" }}

	var linkCalls int
	var linkMu sync.Mutex
	bothLinking := make(chan struct{})
	r.Link = func(oldPath, newPath string) error {
		linkMu.Lock()
		linkCalls++
		call := linkCalls
		if linkCalls == 2 {
			close(bothLinking)
		}
		linkMu.Unlock()
		if call <= 2 {
			<-bothLinking
		}
		return os.Link(oldPath, newPath)
	}

	type result struct {
		note Note
		err  error
	}
	results := make(chan result, 2)
	for _, text := range []string{"first", "second"} {
		go func() {
			n, err := r.Create(context.Background(), text, nil)
			results <- result{note: n, err: err}
		}()
	}
	created := make([]Note, 0, 2)
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		created = append(created, result.note)
	}
	if created[0].ID == created[1].ID {
		t.Fatalf("concurrent creates reused ID %q", created[0].ID)
	}
	notes, warnings, err := r.List(context.Background(), Filter{All: true})
	if err != nil || len(warnings) != 0 || len(notes) != 2 {
		t.Fatalf("List = %#v, %#v, %v", notes, warnings, err)
	}
	texts := map[string]bool{notes[0].Text: true, notes[1].Text: true}
	if !texts["first"] || !texts["second"] {
		t.Fatalf("stored texts = %#v", texts)
	}
}

func TestRepositoryAtomicReplacementFailurePreservesRecordAndCleansTemp(t *testing.T) {
	r := &Repository{Dir: t.TempDir(), Repo: RepoReference{ID: "repo"}, Now: time.Now, IDSuffix: func() string { return "abc" }}
	n, err := r.Create(context.Background(), "original", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Rename = func(string, string) error { return errors.New("rename failed") }
	if _, err := r.Edit(context.Background(), n.ID, "replacement", nil); err == nil {
		t.Fatal("Edit succeeded")
	}
	r.Rename = nil
	got, err := r.Get(context.Background(), n.ID)
	if err != nil || got.Text != "original" {
		t.Fatalf("record after failure = %#v, %v", got, err)
	}
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp-") {
			t.Fatalf("temporary file not cleaned: %#v", entries)
		}
	}
}

func TestRepositoryMutationRacesDoNotRevertPromotion(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Repository, string) (Note, error)
	}{
		{name: "edit", mutate: func(r *Repository, id string) (Note, error) {
			return r.Edit(context.Background(), id, "edited", nil)
		}},
		{name: "archive", mutate: func(r *Repository, id string) (Note, error) {
			return r.Archive(context.Background(), id, "later")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			r := &Repository{Dir: t.TempDir(), Repo: RepoReference{ID: "repo"}, Now: time.Now, IDSuffix: func() string { return "abc" }}
			n, err := r.Create(context.Background(), "original", nil)
			if err != nil {
				t.Fatal(err)
			}

			promotionReplacing := make(chan struct{})
			allowPromotion := make(chan struct{})
			var renameMu sync.Mutex
			renameCalls := 0
			r.Rename = func(oldPath, newPath string) error {
				renameMu.Lock()
				renameCalls++
				call := renameCalls
				renameMu.Unlock()
				if call == 1 {
					close(promotionReplacing)
					<-allowPromotion
				}
				return os.Rename(oldPath, newPath)
			}

			promoted := make(chan error, 1)
			go func() {
				_, promoteErr := r.PromoteToPlan(context.Background(), n.ID, PlanLink{ID: "plan-a"})
				promoted <- promoteErr
			}()
			<-promotionReplacing

			mutationStarted := make(chan struct{})
			mutated := make(chan error, 1)
			go func() {
				close(mutationStarted)
				_, mutateErr := test.mutate(r, n.ID)
				mutated <- mutateErr
			}()
			<-mutationStarted
			select {
			case err := <-mutated:
				t.Fatalf("mutation completed while promotion replacement was locked: %v", err)
			case <-time.After(50 * time.Millisecond):
			}

			close(allowPromotion)
			if err := <-promoted; err != nil {
				t.Fatalf("PromoteToPlan: %v", err)
			}
			if err := <-mutated; !errors.Is(err, ErrImmutable) {
				t.Fatalf("racing mutation error = %v", err)
			}
			got, err := r.Get(context.Background(), n.ID)
			if err != nil || got.Status != StatusPromoted || got.Promotion == nil || got.Promotion.Plan == nil || got.Promotion.Plan.ID != "plan-a" {
				t.Fatalf("record after race = %#v, %v", got, err)
			}
		})
	}
}

func TestRepositoryIgnoresLegacyGlobalDirectory(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, "notes")
	if err := os.Mkdir(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "legacy.json"), []byte(`{"schema":"tao.note.v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewRepository(filepath.Join(home, "repos", "repo", "notes"), RepoReference{ID: "repo"})
	notes, warnings, err := r.List(context.Background(), Filter{All: true})
	if err != nil || notes != nil || warnings != nil {
		t.Fatalf("List = %#v, %#v, %v", notes, warnings, err)
	}
}
