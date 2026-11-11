package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/taodata"
)

type noteReadyJSON struct {
	ID         string `json:"id"`
	Repository string `json:"repository"`
	Ready      bool   `json:"ready"`
	Checks     []struct {
		Name    string   `json:"name"`
		Status  string   `json:"status"`
		Reasons []string `json:"reasons"`
	} `json:"checks"`
}

var noteReadyCheckOrder = []string{"status", "references", "fix_sites", "tier", "markers", "repository"}

func createReadyTestNote(t *testing.T, app App, out *bytes.Buffer, args ...string) string {
	t.Helper()
	if err := app.Run(context.Background(), append([]string{"note", "create"}, args...)); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
	out.Reset()
	return id
}

func readNoteRecord(t *testing.T, app App, registered taodata.Repo, id string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(app.registry().NotesDir(registered), id+".json")) //nolint:gosec // G304: test reads its own temp note record
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeReadyJSON(t *testing.T, out *bytes.Buffer) noteReadyJSON {
	t.Helper()
	var got noteReadyJSON
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if len(got.Checks) != len(noteReadyCheckOrder) {
		t.Fatalf("checks: %s", out.String())
	}
	for i, name := range noteReadyCheckOrder {
		if got.Checks[i].Name != name {
			t.Fatalf("check %d = %q, want %q", i, got.Checks[i].Name, name)
		}
		if got.Checks[i].Reasons == nil {
			t.Fatalf("check %s reasons must be an array: %s", name, out.String())
		}
	}
	return got
}

func TestNoteReadyDispatchAndFlags(t *testing.T) {
	registered := taodata.Repo{ID: "repo-one", Name: "one", Root: t.TempDir()}
	app, out, _ := noteTestApp(t, nil, registered)
	id := createReadyTestNote(t, app, out, "--tag", "tier1", "Fix `internal/cli/note.go`")
	if err := app.Run(context.Background(), []string{"note", "ready"}); err == nil || !strings.Contains(err.Error(), "usage: tao note ready") {
		t.Fatalf("missing id: %v", err)
	}
	if err := app.Run(context.Background(), []string{"note", "ready", id, "extra"}); err == nil || !strings.Contains(err.Error(), "usage: tao note ready") {
		t.Fatalf("extra positional: %v", err)
	}
	if err := app.Run(context.Background(), []string{"note", "ready", "--tag", "x", id}); err == nil || err.Error() != "--tag is not valid for tao note ready" {
		t.Fatalf("tag: %v", err)
	}
	if err := app.Run(context.Background(), []string{"note", "ready", "--repo", "one", "--json", id}); err != nil {
		t.Fatalf("repo/json: %v", err)
	}
	if got := decodeReadyJSON(t, out); got.ID != id || got.Repository != "repo-one" {
		t.Fatalf("json: %s", out.String())
	}
	if err := app.Run(context.Background(), []string{"note", "ready", "missing"}); err == nil || !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("missing note: %v", err)
	}
}

func TestNoteReadyReadyVerdict(t *testing.T) {
	registered := taodata.Repo{ID: "repo-one", Name: "one", Root: t.TempDir()}
	app, out, errOut := noteTestApp(t, nil, registered)
	if err := os.MkdirAll(filepath.Join(registered.Root, "internal", "cli"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registered.Root, "internal", "cli", "note.go"), []byte("package cli\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	id := createReadyTestNote(t, app, out, "--tag", "tier2", "Tighten `internal/cli/note.go` flag validation")
	before := readNoteRecord(t, app, registered, id)
	if err := app.Run(context.Background(), []string{"note", "ready", id}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"ID: " + id,
		"Repository: repo-one",
		"Verdict: ready",
		"pass status",
		"pass references",
		"- no note references",
		"pass fix_sites",
		"pass tier",
		"pass markers",
		"pass repository",
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("output:\n%s", out.String())
	}
	for i, line := range want {
		if strings.TrimSpace(lines[i]) != line {
			t.Fatalf("line %d = %q, want %q\n%s", i, lines[i], line, out.String())
		}
	}
	if strings.HasPrefix(lines[5], "-") {
		t.Fatalf("reason line must be indented: %q", lines[5])
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "ready", "--json", id}); err != nil {
		t.Fatal(err)
	}
	got := decodeReadyJSON(t, out)
	if !got.Ready || got.ID != id || got.Repository != "repo-one" {
		t.Fatalf("json: %s", out.String())
	}
	for _, check := range got.Checks {
		if check.Status != note.ReadinessPass {
			t.Fatalf("check %s = %s", check.Name, check.Status)
		}
	}
	if got.Checks[2].Reasons == nil || len(got.Checks[2].Reasons) != 0 {
		t.Fatalf("fix_sites reasons: %s", out.String())
	}
	if after := readNoteRecord(t, app, registered, id); !bytes.Equal(before, after) {
		t.Fatalf("note record changed:\n%s\n%s", before, after)
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr: %s", errOut.String())
	}
}

func TestNoteReadyNotReadyVerdict(t *testing.T) {
	registered := taodata.Repo{ID: "repo-one", Name: "one", Root: t.TempDir()}
	app, out, _ := noteTestApp(t, nil, registered)
	app.RepoHealthCheck = func(context.Context, taodata.Repo) taodata.RepoHealth {
		return taodata.RepoHealth{Status: taodata.RepoHealthMissingRoot, Error: true, Message: "root is gone"}
	}
	id := createReadyTestNote(t, app, out, "Rename `missingIdentifier`; needs owner decision on scope")
	before := readNoteRecord(t, app, registered, id)
	if err := app.Run(context.Background(), []string{"note", "ready", id}); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"Verdict: not ready (advisory)",
		"fail fix_sites",
		"- identifier missingIdentifier not found in checkout",
		"fail tier",
		"- no tier tag",
		"fail markers",
		`- note text contains "owner decision"`,
		"fail repository",
		"- repository missing_root: root is gone",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in:\n%s", want, text)
		}
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "ready", id, "--json"}); err != nil {
		t.Fatal(err)
	}
	got := decodeReadyJSON(t, out)
	if got.Ready || got.Checks[3].Status != note.ReadinessFail || got.Checks[3].Reasons[0] != "no tier tag" {
		t.Fatalf("json: %s", out.String())
	}
	if after := readNoteRecord(t, app, registered, id); !bytes.Equal(before, after) {
		t.Fatal("note record changed")
	}
}

func TestNoteReadyReferences(t *testing.T) {
	registered := taodata.Repo{ID: "repo-one", Name: "one", Root: t.TempDir()}
	app, out, _ := noteTestApp(t, nil, registered)
	at := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	rows := []note.Note{
		{ID: "20261101-120000-aaaaaaaa", Status: note.StatusArchived, CreatedAt: at, UpdatedAt: at, Archive: &note.ArchiveMetadata{Plan: &note.PlanLink{ID: "plan-merged", Dir: "private"}}},
		{ID: "20261101-120000-bbbbbbbb", Status: note.StatusOpen, Text: "still open", CreatedAt: at, UpdatedAt: at},
	}
	var wrapped *jsonNoteRepository
	app.NoteRepository = func(dir string, ref note.RepoReference) NoteRepository {
		if wrapped == nil {
			wrapped = &jsonNoteRepository{NoteRepository: note.NewRepository(dir, ref), rows: rows}
		}
		return wrapped
	}
	app.Repository = func(string) Repository {
		return fakeRepository{summaries: []plan.PlanSummary{{ID: "plan-merged", Status: plan.StatusCompleted, PlanMerged: true}}}
	}
	id := createReadyTestNote(t, app, out, "--tag", "tier1", "After note:aaaaaaaa and bbbbbbbb, fix `Makefile`")
	if err := app.Run(context.Background(), []string{"note", "ready", "--json", id}); err != nil {
		t.Fatal(err)
	}
	got := decodeReadyJSON(t, out)
	refs := got.Checks[1]
	if got.Ready || refs.Status != note.ReadinessFail || len(refs.Reasons) != 1 || refs.Reasons[0] != "prerequisite note 20261101-120000-bbbbbbbb is open" {
		t.Fatalf("references: %s", out.String())
	}
	if !wrapped.filter.All {
		t.Fatalf("list filter: %+v", wrapped.filter)
	}

	// A notes or plans load failure degrades every reference plan to unavailable.
	wrapped.err = errors.New("notes unreadable")
	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "ready", "--json", id}); err != nil {
		t.Fatal(err)
	}
	got = decodeReadyJSON(t, out)
	if got.Checks[1].Status != note.ReadinessUnknown || len(got.Checks[1].Reasons) != 2 || !strings.Contains(got.Checks[1].Reasons[0], "is unavailable") {
		t.Fatalf("degraded references: %s", out.String())
	}
}

func TestNoteReadyCancellationPropagates(t *testing.T) {
	registered := taodata.Repo{ID: "repo-one", Name: "one", Root: t.TempDir()}
	app, out, _ := noteTestApp(t, nil, registered)
	id := createReadyTestNote(t, app, out, "--tag", "tier1", "fix `Makefile`")
	app.Repository = func(string) Repository { return fakeRepository{err: context.Canceled} }
	if err := app.Run(context.Background(), []string{"note", "ready", id}); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("output after cancellation: %s", out.String())
	}
}
