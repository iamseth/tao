package planning

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/taodata"
)

func TestPlanningSessionRepositoryPersistsRepoScopedDraft(t *testing.T) {
	dataHome := t.TempDir()
	repoMeta := taodata.Repo{Schema: taodata.RepoSchema, ID: "repo-a", Name: "Repo A", Root: "/work/repo-a", Branch: "master"}
	if err := taodata.NewRegistry(dataHome).WriteRepo(repoMeta); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 6, 14, 21, 55, 0, 0, time.UTC)
	store := NewFileRepository(dataHome)
	store.Now = func() time.Time { return now }
	store.Suffix = func() (string, error) { return "abc123", nil }

	session, err := store.CreateSession(context.Background(), CreateRequest{
		RepoID:        repoMeta.ID,
		InitialPrompt: "Plan a web workflow",
		Source: &SourceEnvelope{Note: &SourceNoteSnapshot{
			ID: " note-1 ", Text: "  preserve markdown  ", Tags: []string{" Web ", "web", ""}, CapturedAt: now,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if session.Status != StatusDraft || session.Repo.ID != repoMeta.ID || len(session.Messages) != 1 || session.Messages[0].Role != RoleUser {
		t.Fatalf("unexpected created session: %+v", session)
	}
	if session.Source == nil || session.Source.Type != "note" || session.Source.Note == nil {
		t.Fatalf("expected typed source note, got %+v", session.Source)
	}
	if source := session.Source.Note; source.ID != "note-1" || source.Text != "  preserve markdown  " || len(source.Tags) != 1 || source.Tags[0] != "web" || source.RepoID != repoMeta.ID || source.RepoName != repoMeta.Name {
		t.Fatalf("source was not normalized: %+v", source)
	}

	sessionPath := filepath.Join(dataHome, "repos", repoMeta.ID, "planning-sessions", session.ID, "session.json")
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("expected session file at %s: %v", sessionPath, err)
	}
	if _, err := os.Stat(filepath.Join(dataHome, "repos", repoMeta.ID, "plans", session.ID)); !os.IsNotExist(err) {
		t.Fatalf("planning session should not create executable plan artifacts, stat err=%v", err)
	}

	loaded, err := store.GetSession(context.Background(), QualifyID(repoMeta.ID, session.ID))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != session.ID || loaded.Title != "Plan a web workflow" {
		t.Fatalf("unexpected loaded session: %+v", loaded)
	}

	result, err := store.ListSessions(context.Background(), ListFilter{RepoID: repoMeta.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].RouteID != QualifyID(repoMeta.ID, session.ID) || result.Sessions[0].MessageCount != 1 {
		t.Fatalf("unexpected session list: %+v", result)
	}
}

// TestPlanningSessionRepositoryReadsLegacyNoteProvenance verifies historical
// and unknown source fields remain readable while recognized note provenance
// is retained.
func TestPlanningSessionRepositoryReadsLegacyNoteProvenance(t *testing.T) {
	dataHome := t.TempDir()
	repoID := "repo-a"
	sessionID := "20260101-000000-legacy"
	legacy := `{
		"schema": "tao.planning.session.v1",
		"id": "` + sessionID + `",
		"title": "Legacy planning draft",
		"status": "draft",
		"repo": {"id": "` + repoID + `", "name": "Repo A", "root": "/work/repo-a"},
		"source": {"type": "note", "historical_field": true, "note": {"id": "note-9", "text": "old note", "tags": ["web"], "captured_at": "2026-01-01T00:00:00Z", "unknown": "ok"}},
		"created_at": "2026-01-01T00:00:00Z",
		"updated_at": "2026-01-01T00:00:00Z",
		"last_activity_at": "2026-01-01T00:00:00Z",
		"messages": [{"id": "msg-001", "role": "user", "content": "old note", "created_at": "2026-01-01T00:00:00Z"}]
	}`
	sessionDir := filepath.Join(dataHome, "repos", repoID, "planning-sessions", sessionID)
	if err := os.MkdirAll(sessionDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionDir, "session.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	store := NewFileRepository(dataHome)
	loaded, err := store.GetSession(context.Background(), QualifyID(repoID, sessionID))
	if err != nil {
		t.Fatalf("legacy session with source.note should deserialize, got error: %v", err)
	}
	if loaded.ID != sessionID || loaded.Title != "Legacy planning draft" || len(loaded.Messages) != 1 {
		t.Fatalf("unexpected loaded legacy session: %+v", loaded)
	}
	if loaded.Source == nil || loaded.Source.Note == nil || loaded.Source.Note.ID != "note-9" {
		t.Fatalf("expected historical source note to remain readable: %+v", loaded.Source)
	}
}
