package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/plannerroute"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

type fakeNoteRegistry struct {
	current taodata.Repo
	repos   []taodata.Repo
	dir     string
}

func (r *fakeNoteRegistry) Current(context.Context) (taodata.Repo, error) { return r.current, nil }
func (r *fakeNoteRegistry) ReadRepo(id string) (taodata.Repo, error) {
	for _, repo := range r.repos {
		if repo.ID == id {
			return repo, nil
		}
	}
	return taodata.Repo{}, os.ErrNotExist
}
func (r *fakeNoteRegistry) ListRepos() ([]taodata.Repo, error) {
	return append([]taodata.Repo(nil), r.repos...), nil
}
func (r *fakeNoteRegistry) NotesDir(repo taodata.Repo) string {
	return filepath.Join(r.dir, repo.ID, "notes")
}
func (r *fakeNoteRegistry) PlansDir(repo taodata.Repo) string {
	return filepath.Join(r.dir, repo.ID, "plans")
}

func (r *fakeNoteRegistry) PlannerRoutesDir(repo taodata.Repo) string {
	return filepath.Join(r.dir, repo.ID, "planner-routes")
}

type terminalInput struct{ *strings.Reader }

func (terminalInput) IsTerminal() bool { return true }

func noteTestApp(t *testing.T, in any, repos ...taodata.Repo) (App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	registry := &fakeNoteRegistry{current: repos[0], repos: repos, dir: t.TempDir()}
	app := App{
		Out:      out,
		Err:      errOut,
		Registry: func() NoteRegistry { return registry },
		NoteRepository: func(dir string, ref note.RepoReference) NoteRepository {
			repo := note.NewRepository(dir, ref)
			repo.Now = func() time.Time { return time.Date(2026, 7, 13, 16, 0, 0, 0, time.UTC) }
			repo.IDSuffix = func() string { return "abcd" }
			return repo
		},
		RepoHealthCheck: func(context.Context, taodata.Repo) taodata.RepoHealth {
			return taodata.RepoHealth{Status: taodata.RepoHealthOK}
		},
	}
	if reader, ok := in.(interface{ Read([]byte) (int, error) }); ok {
		app.In = reader
	}
	return app, out, errOut
}

func noteArchiveTestApp(t *testing.T, registered taodata.Repo) (App, *bytes.Buffer, *fakeNoteRegistry) {
	t.Helper()
	out := new(bytes.Buffer)
	registry := &fakeNoteRegistry{current: registered, repos: []taodata.Repo{registered}, dir: t.TempDir()}
	app := App{
		In:       strings.NewReader(""),
		Out:      out,
		Err:      io.Discard,
		Registry: func() NoteRegistry { return registry },
		NoteRepository: func(dir string, ref note.RepoReference) NoteRepository {
			return note.NewRepository(dir, ref)
		},
		Repository: func(dir string) Repository { return plan.NewFileRepository(dir) },
	}
	return app, out, registry
}

func TestNoteCreateListShowEditArchiveAndReopen(t *testing.T) {
	repo := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo"}
	app, out, _ := noteTestApp(t, strings.NewReader(""), repo)
	ctx := context.Background()
	if err := app.Run(ctx, []string{"n", "c", "--tag", "CLI", "fix", "the", "thing"}); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
	out.Reset()
	if err := app.Run(ctx, []string{"note"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID  STATUS  TAGS  DESTINATION  NOTE", id, "open", "cli", "fix the thing"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("list missing %q: %q", want, out.String())
		}
	}

	out.Reset()
	if err := app.Run(ctx, []string{"n", "e", id, "expanded", "detail"}); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := app.Run(ctx, []string{"note", "show", id}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Repository: tao-123", "Tags: cli", "Text:\nexpanded detail"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("show missing %q: %q", want, out.String())
		}
	}

	out.Reset()
	if err := app.Run(ctx, []string{"n", "a", id, "--reason", "later"}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(ctx, []string{"n", "reopen", id}); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(ctx, []string{"n", "reopen", id}); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Fatalf("expected actionable reopen error, got %v", err)
	}
}

func TestNoteArchiveToValidatedPlanIsLockedIdempotentAndRendered(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	registered := taodata.Repo{ID: "tao-123", Name: "tao", Root: repoRoot}
	app, out, registry := noteArchiveTestApp(t, registered)
	planID := "20260819-1200-linked-plan"
	planDir := writeRunPlan(t, registry.PlansDir(registered), planID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)

	if err := app.Run(context.Background(), []string{"note", "create", "plan", "this"}); err != nil {
		t.Fatal(err)
	}
	noteID := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
	out.Reset()
	locked := false
	app.AcquireNotePromotionLock = func(_ context.Context, _, id, owner string) (func() error, error) {
		if id != noteID || owner != "note archive --plan" {
			t.Fatalf("lock request id=%q owner=%q", id, owner)
		}
		locked = true
		return func() error { locked = false; return nil }, nil
	}
	base := app.noteRepository(registered)
	app.NoteRepository = func(string, note.RepoReference) NoteRepository {
		return &lockCheckingArchiveRepository{NoteRepository: base, locked: &locked}
	}

	if err := app.Run(context.Background(), []string{"note", "archive", "--plan", "linked-plan", noteID[:12]}); err != nil {
		t.Fatal(err)
	}
	if locked || !strings.Contains(out.String(), "Archived note "+noteID+" to plan "+planID) {
		t.Fatalf("lock=%v output=%q", locked, out.String())
	}
	stored, err := base.Get(context.Background(), noteID)
	if err != nil || stored.Status != note.StatusArchived || stored.Archive == nil || stored.Archive.Plan == nil || stored.Archive.Plan.ID != planID || stored.Archive.Plan.Dir != planDir {
		t.Fatalf("linked note=%#v err=%v", stored, err)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "archive", "--plan", planID, noteID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already archived to plan "+planID) {
		t.Fatalf("retry output=%q", out.String())
	}
	otherPlanID := "20260819-1201-other-plan"
	writeRunPlan(t, registry.PlansDir(registered), otherPlanID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	if err := app.Run(context.Background(), []string{"note", "archive", "--plan", otherPlanID, noteID}); err == nil || !errors.Is(err, note.ErrImmutable) {
		t.Fatalf("different-plan terminal error=%v", err)
	}
	stored, err = base.Get(context.Background(), noteID)
	if err != nil || stored.Archive.Plan.ID != planID {
		t.Fatalf("terminal destination changed: %#v, %v", stored, err)
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "list", "--all"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "plan:"+planID) {
		t.Fatalf("list output=%q", out.String())
	}
	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "show", noteID}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Plan: " + planID, "Plan directory: " + planDir} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("show missing %q: %q", want, out.String())
		}
	}
}

type lockCheckingArchiveRepository struct {
	NoteRepository
	locked *bool
}

func (r *lockCheckingArchiveRepository) ArchiveToPlan(ctx context.Context, id string, link note.PlanLink) (note.Note, error) {
	if !*r.locked {
		return note.Note{}, errors.New("archive called without lock")
	}
	return r.NoteRepository.ArchiveToPlan(ctx, id, link)
}

func TestNoteArchiveToPlanRejectsInvalidDestinationsBeforeMutation(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	registered := taodata.Repo{ID: "tao-123", Name: "tao", Root: repoRoot}
	app, out, registry := noteArchiveTestApp(t, registered)
	if err := app.Run(context.Background(), []string{"note", "create", "keep", "open"}); err != nil {
		t.Fatal(err)
	}
	noteID := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))

	for _, id := range []string{"20260819-1200-ambiguous-one", "20260819-1201-ambiguous-two"} {
		writeRunPlan(t, registry.PlansDir(registered), id, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	}
	invalidID := "20260819-1202-invalid"
	invalidDir := writeRunPlan(t, registry.PlansDir(registered), invalidID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	invalidSlicesPath := filepath.Join(invalidDir, "slices.json")
	invalidSlices := strings.Replace(readText(t, invalidSlicesPath), `"plan_id":"`+invalidID+`"`, `"plan_id":"different"`, 1)
	if err := os.WriteFile(invalidSlicesPath, []byte(invalidSlices), 0o600); err != nil {
		t.Fatal(err)
	}
	foreignID := "20260819-1203-foreign"
	foreignDir := writeRunPlan(t, registry.PlansDir(registered), foreignID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	statePath := filepath.Join(foreignDir, "state.json")
	state := readText(t, statePath)
	state = strings.Replace(state, fmt.Sprintf("%q", repoRoot), `"/foreign/repository"`, 1)
	if err := os.WriteFile(statePath, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	externalID := "20260819-1204-external"
	externalDir := writeRunPlan(t, t.TempDir(), externalID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	app.AcquireNotePromotionLock = func(context.Context, string, string, string) (func() error, error) {
		t.Fatal("external plan reached note mutation lock")
		return nil, nil
	}

	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "missing", args: []string{"note", "archive", "--plan", "missing", noteID}, want: "not found"},
		{name: "ambiguous", args: []string{"note", "archive", "--plan", "ambiguous", noteID}, want: "ambiguous"},
		{name: "invalid", args: []string{"note", "archive", "--plan", invalidID, noteID}, want: "artifact IDs are missing or inconsistent"},
		{name: "foreign", args: []string{"note", "archive", "--plan", foreignID, noteID}, want: "does not match registered repository"},
		{name: "external path", args: []string{"note", "archive", "--plan", externalDir, noteID}, want: "outside repository plans directory"},
		{name: "blank flag", args: []string{"note", "archive", "--plan=", noteID}, want: "--plan requires"},
		{name: "conflicting flags", args: []string{"note", "archive", "--plan", foreignID, "--reason", "done", noteID}, want: "cannot be used"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := app.Run(context.Background(), test.args)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want %q", err, test.want)
			}
			stored, getErr := app.noteRepository(registered).Get(context.Background(), noteID)
			if getErr != nil || stored.Status != note.StatusOpen {
				t.Fatalf("note mutated after rejection: %#v, %v", stored, getErr)
			}
		})
	}
}

func TestNoteArchiveToPlanAcceptsLegacyPlanningPromotionAndReportsRecovery(t *testing.T) {
	repoRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	registered := taodata.Repo{ID: "tao-123", Name: "tao", Root: repoRoot}
	app, out, registry := noteArchiveTestApp(t, registered)
	planID := "20260819-1200-legacy-plan"
	writeRunPlan(t, registry.PlansDir(registered), planID, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	if err := app.Run(context.Background(), []string{"note", "create", "legacy", "planning"}); err != nil {
		t.Fatal(err)
	}
	noteID := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
	base := app.noteRepository(registered)
	legacy, err := base.Get(context.Background(), noteID)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Status = note.StatusPromoted
	legacy.Promotion = &note.PromotionLinks{PlanningSession: &note.PlanningSessionLink{ID: "repo/session"}}
	content, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registry.NotesDir(registered), noteID+".json"), append(content, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.Run(context.Background(), []string{"note", "archive", "--plan", planID, noteID}); err != nil {
		t.Fatal(err)
	}
	stored, err := base.Get(context.Background(), noteID)
	if err != nil || stored.Archive == nil || stored.Archive.PlanningSession == nil || stored.Archive.PlanningSession.ID != "repo/session" {
		t.Fatalf("legacy provenance=%#v err=%v", stored, err)
	}

	if err := app.Run(context.Background(), []string{"note", "create", "link", "failure"}); err != nil {
		t.Fatal(err)
	}
	failedID := strings.TrimSpace(strings.TrimPrefix(out.String()[strings.LastIndex(out.String(), "Created note "):], "Created note "))
	failure := errors.New("write failed")
	app.NoteRepository = func(string, note.RepoReference) NoteRepository {
		return &failingPlanArchiveRepository{NoteRepository: base, err: failure}
	}
	err = app.Run(context.Background(), []string{"note", "archive", "--plan", planID, failedID[:len(failedID)-1]})
	recovery := "tao note archive --repo " + registered.ID + " --plan " + planID + " " + failedID
	if err == nil || !errors.Is(err, failure) || !strings.Contains(err.Error(), "left untouched") || !strings.Contains(err.Error(), "`"+recovery+"`") {
		t.Fatalf("recovery error=%v", err)
	}
}

type failingPlanArchiveRepository struct {
	NoteRepository
	err error
}

func (r *failingPlanArchiveRepository) ArchiveToPlan(context.Context, string, note.PlanLink) (note.Note, error) {
	return note.Note{}, r.err
}

func TestNoteCreateAndEditPreserveFlagShapedText(t *testing.T) {
	repo := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "create unknown flag first", args: []string{"n", "c", "--hello", "then", "--tag"}, want: "--hello then --tag"},
		{name: "create explicit boundary for known flag first", args: []string{"note", "create", "--", "--tag", "is text"}, want: "--tag is text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, out, _ := noteTestApp(t, strings.NewReader(""), repo)
			if err := app.Run(context.Background(), tt.args); err != nil {
				t.Fatal(err)
			}
			id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
			item, err := app.noteRepository(repo).Get(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if item.Text != tt.want {
				t.Fatalf("note text = %q, want %q", item.Text, tt.want)
			}
		})
	}

	t.Run("edit preserves unknown and known flag words", func(t *testing.T) {
		app, out, _ := noteTestApp(t, strings.NewReader(""), repo)
		if err := app.Run(context.Background(), []string{"note", "create", "original"}); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
		if err := app.Run(context.Background(), []string{"note", "edit", id, "--hello", "then", "--tag"}); err != nil {
			t.Fatal(err)
		}
		item, err := app.noteRepository(repo).Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Text != "--hello then --tag" {
			t.Fatalf("edited note text = %q", item.Text)
		}
	})

	t.Run("edit accepts explicit boundary before known flag", func(t *testing.T) {
		app, out, _ := noteTestApp(t, strings.NewReader(""), repo)
		if err := app.Run(context.Background(), []string{"note", "create", "original"}); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
		if err := app.Run(context.Background(), []string{"note", "edit", id, "--", "--tag", "is text"}); err != nil {
			t.Fatal(err)
		}
		item, err := app.noteRepository(repo).Get(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Text != "--tag is text" {
			t.Fatalf("edited note text = %q", item.Text)
		}
	})
}

func TestNotePipedAndInteractiveInputAndRepoSelection(t *testing.T) {
	one := taodata.Repo{ID: "one-123", Name: "duplicate", Root: "/one"}
	two := taodata.Repo{ID: "two-456", Name: "duplicate", Root: "/two"}
	app, out, _ := noteTestApp(t, strings.NewReader("first\n\nsecond\n"), one, two)
	if err := app.Run(context.Background(), []string{"note", "--repo", "two", "create", "--tag=pipe"}); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "show", "--repo=two-456", id}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "first\n\nsecond\n") || !strings.Contains(out.String(), "Repository: two-456") {
		t.Fatalf("piped multiline text not preserved: %q", out.String())
	}

	app, _, _ = noteTestApp(t, strings.NewReader(""), one, two)
	if err := app.Run(context.Background(), []string{"note", "--repo", "duplicate"}); err == nil || !strings.Contains(err.Error(), "one-123, two-456") {
		t.Fatalf("expected duplicate-name candidates, got %v", err)
	}

	interactive := terminalInput{strings.NewReader("prompted text\n")}
	app, out, errOut := noteTestApp(t, interactive, one)
	if err := app.Run(context.Background(), []string{"n", "c"}); err != nil {
		t.Fatal(err)
	}
	if errOut.String() != "note> " || !strings.Contains(out.String(), "Created note") {
		t.Fatalf("interactive input output = stdout %q stderr %q", out.String(), errOut.String())
	}
}

func TestNoteRunRejectsConcurrentSourceMutations(t *testing.T) {
	clearTaoEnv(t)
	repoMeta := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo", Branch: "main"}
	for _, mutation := range []string{"edit", "archive"} {
		t.Run(mutation, func(t *testing.T) {
			app, out, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
			if err := app.Run(context.Background(), []string{"note", "create", "original source"}); err != nil {
				t.Fatal(err)
			}
			id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
			out.Reset()

			generationStarted := make(chan struct{})
			continueGeneration := make(chan struct{})
			var sourceText string
			fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
			app.Repository = func(string) Repository { return plan.NewFileRepository(filepath.Dir(fixture.dir)) }
			app.PlanGenerator = planGeneratorFunc(func(_ context.Context, request planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
				sourceText = request.Session.Source.Note.Text
				close(generationStarted)
				<-continueGeneration
				return &planning.GeneratePlanResult{Allocation: planning.PlanAllocation{ID: fixture.id, Dir: fixture.dir}, Validation: planning.ValidationResult{OK: true}}, nil
			})
			app.CommandRunner = func(_ context.Context, _ string, name string, args []string, stdout, _ io.Writer) error {
				if name == "git" {
					writeRunGitOutput(stdout, args)
				}
				return nil
			}
			app.ProcessStarter = fakeCLIProcessStarter(t, "done", func(string) {
				fixture.write(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
			})

			promoted := make(chan error, 1)
			go func() {
				promoted <- app.Run(context.Background(), []string{"note", "run", "--execution-mode", "current", "--commit-policy", "none", "--no-review", id})
			}()
			<-generationStarted

			mutationArgs := []string{"note", "edit", id, "edited source"}
			if mutation == "archive" {
				mutationArgs = []string{"note", "archive", id}
			}
			mutationErr := app.Run(context.Background(), mutationArgs)
			if !errors.Is(mutationErr, note.ErrPromotionLocked) {
				t.Fatalf("concurrent %s error = %v", mutation, mutationErr)
			}
			stored, err := app.noteRepository(repoMeta).Get(context.Background(), id)
			if err != nil || stored.Status != note.StatusOpen || stored.Text != "original source" {
				t.Fatalf("note changed during generation: %#v, %v", stored, err)
			}

			close(continueGeneration)
			if err := <-promoted; err != nil {
				t.Fatalf("run note: %v", err)
			}
			stored, err = app.noteRepository(repoMeta).Get(context.Background(), id)
			if err != nil || stored.Status != note.StatusPromoted || sourceText != "original source" {
				t.Fatalf("promotion source=%q note=%#v err=%v", sourceText, stored, err)
			}
		})
	}
}

func TestNoteRequiresRegistrationAndValidatesInput(t *testing.T) {
	current := taodata.Repo{ID: "missing", Name: "missing", Root: "/missing"}
	missingRegistry := &fakeNoteRegistry{current: current, dir: t.TempDir()}
	app := App{In: strings.NewReader("text"), Out: io.Discard, Err: io.Discard, Registry: func() NoteRegistry { return missingRegistry }}
	if err := app.Run(context.Background(), []string{"note", "create"}); err == nil || !strings.Contains(err.Error(), "tao init") {
		t.Fatalf("expected init guidance, got %v", err)
	}

	registered := taodata.Repo{ID: "repo-1", Name: "repo", Root: "/repo"}
	app, _, _ = noteTestApp(t, strings.NewReader(""), registered)
	if err := app.Run(context.Background(), []string{"note", "create"}); err == nil || !strings.Contains(err.Error(), "blank") {
		t.Fatalf("expected blank input error, got %v", err)
	}
	if err := app.Run(context.Background(), []string{"note", "list", "--status", "unknown"}); err == nil || !strings.Contains(err.Error(), "invalid note status") {
		t.Fatalf("expected status error, got %v", err)
	}
	for _, removed := range []string{"plan", "p"} {
		err := app.Run(context.Background(), []string{"note", removed})
		if err == nil || !strings.Contains(err.Error(), "unknown note subcommand") || strings.Contains(err.Error(), "reopen, plan") {
			t.Fatalf("removed subcommand %q guidance = %v", removed, err)
		}
	}
	if err := app.Run(context.Background(), []string{"note", "show"}); err == nil || !errors.Is(err, note.ErrNotFound) && !strings.Contains(err.Error(), "usage") {
		t.Fatalf("expected show usage error, got %v", err)
	}
}

type planGeneratorFunc func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error)

func (f planGeneratorFunc) GeneratePlan(ctx context.Context, request planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
	return f(ctx, request)
}

type failingPlanPromotionRepository struct {
	*note.Repository
	err error
}

func (r *failingPlanPromotionRepository) PromoteToPlan(context.Context, string, note.PlanLink) (note.Note, error) {
	return note.Note{}, r.err
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestNoteRunGeneratesLinksThenUsesNormalRun(t *testing.T) {
	clearTaoEnv(t)
	executeCalls := 0
	oldExecutor := executeSinglePlan
	executeSinglePlan = func(service run.Service, ctx context.Context, request run.Request) error {
		executeCalls++
		if request.RepairVerification || request.Reverify {
			t.Fatalf("note run must use ordinary automatic policy, got recovery request: %+v", request)
		}
		return service.Execute(ctx, request)
	}
	t.Cleanup(func() { executeSinglePlan = oldExecutor })
	t.Setenv("TAO_SESSION_TIMEOUT", "37m")
	repoMeta := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo", Branch: "main"}
	app, out, errOut := noteTestApp(t, strings.NewReader(""), repoMeta)
	fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
	app.RepoHealthCheck = func(context.Context, taodata.Repo) taodata.RepoHealth {
		return taodata.RepoHealth{Status: taodata.RepoHealthOK}
	}
	app.Repository = func(plansDir string) Repository {
		if plansDir != filepath.Dir(fixture.dir) {
			t.Fatalf("plans dir = %q, want %q", plansDir, filepath.Dir(fixture.dir))
		}
		return plan.NewFileRepository(plansDir)
	}
	generated := 0
	app.PlanGenerator = planGeneratorFunc(func(_ context.Context, request planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
		generated++
		if request.Timeout != 37*time.Minute || request.PermissionMode != agent.PermissionModeBypassPermissions || !request.RejectOpenQuestions {
			t.Fatalf("generation policy = timeout %s, permission %q, reject=%v", request.Timeout, request.PermissionMode, request.RejectOpenQuestions)
		}
		if request.Session.Source == nil || request.Session.Source.Note == nil || request.Session.Source.Note.Text != "implement a small fix" || request.Session.Repo.ID != repoMeta.ID {
			t.Fatalf("source-linked session = %#v", request.Session)
		}
		return &planning.GeneratePlanResult{
			Allocation: planning.PlanAllocation{ID: fixture.id, Dir: fixture.dir},
			Validation: planning.ValidationResult{OK: true, Findings: []planning.ValidationFinding{{Severity: "warning", Message: "old plan compatibility"}}},
		}, nil
	})
	app.CommandRunner = func(_ context.Context, _ string, name string, args []string, stdout, _ io.Writer) error {
		if name == "git" {
			writeRunGitOutput(stdout, args)
		}
		return nil
	}
	var id string
	app.ProcessStarter = fakeCLIProcessStarter(t, "done", func(string) {
		stored, err := app.noteRepository(repoMeta).Get(context.Background(), id)
		if err != nil || stored.Status != note.StatusPromoted || stored.Promotion == nil || stored.Promotion.Plan == nil {
			t.Fatalf("agent started before durable promotion: note=%#v err=%v", stored, err)
		}
		fixture.write(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
	})
	if err := app.Run(context.Background(), []string{"note", "create", "implement", "a", "small", "fix"}); err != nil {
		t.Fatal(err)
	}
	id = strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "run", "--execution-mode", "current", "--commit-policy", "none", "--no-review", "--dangerously-skip-permissions", id}); err != nil {
		t.Fatal(err)
	}
	if generated != 1 || !strings.Contains(errOut.String(), "warning: old plan compatibility") || !strings.Contains(out.String(), "Promoted note "+id+" to plan "+fixture.id) {
		t.Fatalf("generated=%d stdout=%q stderr=%q", generated, out.String(), errOut.String())
	}
	stored, err := app.noteRepository(repoMeta).Get(context.Background(), id)
	if err != nil || stored.Promotion.Plan.Mode != "run" || stored.Promotion.Plan.Dir != fixture.dir {
		t.Fatalf("persisted promotion = %#v, err=%v", stored.Promotion, err)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "r", id}); err != nil {
		t.Fatal(err)
	}
	if generated != 1 || !strings.Contains(out.String(), "Run it with: tao run "+fixture.id) {
		t.Fatalf("duplicate invocation generated=%d output=%q", generated, out.String())
	}
	if executeCalls != 1 {
		t.Fatalf("shared run entry calls = %d, want one Service.Execute handoff", executeCalls)
	}
}

func TestNoteRunGenerationTelemetrySurvivesHandoff(t *testing.T) {
	for _, kind := range []runtimeconfig.AgentKind{runtimeconfig.AgentPi, runtimeconfig.AgentClaude} {
		t.Run(string(kind), func(t *testing.T) {
			clearTaoEnv(t)
			dataHome := t.TempDir()
			t.Setenv("TAO_DATA_HOME", dataHome)
			t.Setenv("TAO_AGENT", string(kind))
			repoMeta := taodata.Repo{ID: "tao-123", Name: "tao", Root: t.TempDir(), Branch: "main"}
			registry := taodata.NewRegistry(dataHome)
			if err := registry.WriteRepo(repoMeta); err != nil {
				t.Fatal(err)
			}
			app, out, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
			app.CommandRunner = func(_ context.Context, _ string, name string, args []string, stdout, _ io.Writer) error {
				if name == "git" {
					writeRunGitOutput(stdout, args)
				}
				return nil
			}
			var fixture runPlanFixture
			var noteID string
			calls := 0
			onPrompt := func(prompt string) {
				calls++
				if calls == 1 {
					marker := "preallocated plan directory: `"
					_, rest, ok := strings.Cut(prompt, marker)
					if !ok {
						t.Fatal("generation prompt missing allocation")
					}
					dir, _, ok := strings.Cut(rest, "`")
					if !ok {
						t.Fatal("unterminated allocation")
					}
					fixture = runPlanFixture{root: filepath.Dir(dir), id: filepath.Base(dir), t: t}
					fixture.write(plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
					state, err := plan.ReadState(dir)
					if err != nil {
						t.Fatal(err)
					}
					state.Repo.Root = repoMeta.Root
					state.Plan.ChangeType = plan.ChangeTypeFeat
					state.Plan.Decision = &plan.Decision{
						Problem: "Missing feature", WhyNow: "Ready", ExpectedBenefit: "Feature works",
						Readiness: plan.DecisionReadinessReady, SuccessCriteria: []string{"Tests pass"},
						Disposition: plan.DecisionDispositionReady, DispositionReason: "Bounded work",
						Priority: plan.Priority{Level: plan.PriorityOverallLevelShould, Impact: plan.PriorityLevelMedium, Urgency: plan.PriorityLevelLow, Effort: plan.PriorityEffortSmall, Risk: plan.PriorityLevelLow, Confidence: plan.PriorityLevelHigh, Rationale: "Small useful change"},
					}
					state.Plan.Sequence = &plan.Sequence{Position: 1, Total: 1}
					content, err := json.Marshal(state)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(dir, "state.json"), content, 0o600); err != nil {
						t.Fatal(err)
					}
					return
				}
				if calls != 2 {
					t.Fatalf("unexpected provider invocation %d", calls)
				}
				stored, err := app.noteRepository(repoMeta).Get(context.Background(), noteID)
				if err != nil || stored.Promotion == nil || stored.Promotion.Plan == nil || stored.Promotion.Plan.ID != fixture.id {
					t.Fatalf("handoff before durable promotion: %#v, %v", stored, err)
				}
				detail, err := plan.NewFileRepository("").ResolvePlan(context.Background(), fixture.dir)
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, event := range detail.Events {
					if event.Type == plan.EventTypeAgentMetrics && event.Metrics != nil && event.Metrics.Role == plan.AgentRolePlanning {
						found = true
					}
				}
				if !found {
					t.Fatal("handoff missing persisted planning usage")
				}
				fixture.write(plan.StatusCompleted, nil, []string{"001-a"}, "001-a", plan.StatusCompleted)
			}
			app.ProcessStarter = fakeCLIProcessStarter(t, "generated", onPrompt)
			if kind == runtimeconfig.AgentClaude {
				app.ProcessStarter = func(_ context.Context, _ string, name string, _ []string) (run.Process, error) {
					if name != "claude" {
						t.Fatalf("unexpected provider %q", name)
					}
					proc := newFakeCLIClaudeProcess(t)
					go func() {
						defer proc.finish()
						prompt, err := io.ReadAll(proc.stdinReader)
						if err != nil {
							t.Error(err)
							return
						}
						onPrompt(string(prompt))
						proc.writeEvent(`{"type":"result","result":"generated","usage":{"input_tokens":5,"output_tokens":7},"total_cost_usd":0}`)
					}()
					return proc, nil
				}
			}
			if err := app.Run(context.Background(), []string{"note", "create", "implement a small fix"}); err != nil {
				t.Fatal(err)
			}
			noteID = strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
			if err := app.Run(context.Background(), []string{"note", "run", "--execution-mode", "current", "--commit-policy", "none", "--no-review", noteID}); err != nil {
				t.Fatal(err)
			}
			if calls != 2 {
				t.Fatalf("provider invocations = %d, want generation plus execution", calls)
			}
			detail, err := plan.NewFileRepository("").ResolvePlan(context.Background(), fixture.dir)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range detail.Events {
				if event.Type != plan.EventTypeAgentMetrics || event.Metrics == nil || event.Metrics.Role != plan.AgentRolePlanning {
					continue
				}
				count++
				if event.SliceID != "" || event.Agent != string(kind) {
					t.Fatalf("planning event = %#v", event)
				}
			}
			if count != 1 {
				t.Fatalf("planning metrics events = %d", count)
			}
			if detail.PlanningSession.HasExport || detail.PlanningSession.Stats != nil || detail.PlanningSession.Prompt != "" {
				t.Fatalf("unexpected planning capture: %#v", detail.PlanningSession)
			}
			if _, err := os.Stat(filepath.Join(dataHome, "repos", repoMeta.ID, "planning-sessions")); !os.IsNotExist(err) {
				t.Fatalf("planning sessions unexpectedly persisted: %v", err)
			}
		})
	}
}

func TestNoteRunFailureBoundaries(t *testing.T) {
	clearTaoEnv(t)
	repoMeta := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo"}
	newOpenNote := func(t *testing.T) (App, string, *bytes.Buffer) {
		t.Helper()
		app, out, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
		app.RepoHealthCheck = func(context.Context, taodata.Repo) taodata.RepoHealth {
			return taodata.RepoHealth{Status: taodata.RepoHealthOK}
		}
		if err := app.Run(context.Background(), []string{"note", "create", "uncertain task"}); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
		out.Reset()
		return app, id, out
	}

	t.Run("generation failure leaves note open and recommends supervised planning", func(t *testing.T) {
		app, id, _ := newOpenNote(t)
		app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
			return nil, &planning.GenerationError{Stage: planning.GenerationStageOpenQuestions, Err: errors.New("questions remain")}
		})
		err := app.Run(context.Background(), []string{"note", "run", id})
		if err == nil || !strings.Contains(err.Error(), "supervised clarification") || !strings.Contains(err.Error(), "/tao-plan note:"+id) {
			t.Fatalf("generation error = %v", err)
		}
		stored, _ := app.noteRepository(repoMeta).Get(context.Background(), id)
		if stored.Status != note.StatusOpen {
			t.Fatalf("failed generation promoted note: %#v", stored)
		}
	})

	t.Run("warning output failure retains promotion and prevents regeneration", func(t *testing.T) {
		app, id, out := newOpenNote(t)
		fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
		generated := 0
		app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
			generated++
			return &planning.GeneratePlanResult{
				Allocation: planning.PlanAllocation{ID: fixture.id, Dir: fixture.dir},
				Validation: planning.ValidationResult{OK: true, Findings: []planning.ValidationFinding{{Severity: "warning", Message: "compatibility warning"}}},
			}, nil
		})
		writeErr := errors.New("diagnostic output failed")
		app.Err = failingWriter{err: writeErr}
		err := app.Run(context.Background(), []string{"note", "run", id})
		if !errors.Is(err, writeErr) || generated != 1 {
			t.Fatalf("warning error=%v generated=%d", err, generated)
		}
		stored, getErr := app.noteRepository(repoMeta).Get(context.Background(), id)
		if getErr != nil || stored.Status != note.StatusPromoted || stored.Promotion == nil || stored.Promotion.Plan == nil || stored.Promotion.Plan.ID != fixture.id {
			t.Fatalf("warning failure lost promotion: note=%#v err=%v", stored, getErr)
		}

		app.Err = new(bytes.Buffer)
		if err := app.Run(context.Background(), []string{"note", "run", id}); err != nil {
			t.Fatal(err)
		}
		if generated != 1 || !strings.Contains(out.String(), "Note already promoted to plan "+fixture.id) {
			t.Fatalf("retry generated=%d output=%q", generated, out.String())
		}
	})

	t.Run("link failure reports durable plan and never runs", func(t *testing.T) {
		app, id, _ := newOpenNote(t)
		fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
		baseFactory := app.NoteRepository
		app.NoteRepository = func(dir string, ref note.RepoReference) NoteRepository {
			return &failingPlanPromotionRepository{Repository: baseFactory(dir, ref).(*note.Repository), err: errors.New("link failed")}
		}
		app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
			return &planning.GeneratePlanResult{Allocation: planning.PlanAllocation{ID: fixture.id, Dir: fixture.dir}, Validation: planning.ValidationResult{OK: true}}, nil
		})
		starts := 0
		app.ProcessStarter = func(context.Context, string, string, []string) (run.Process, error) {
			starts++
			return nil, errors.New("must not start")
		}
		err := app.Run(context.Background(), []string{"note", "run", id})
		if err == nil || !strings.Contains(err.Error(), fixture.id) || !strings.Contains(err.Error(), "recover with tao run") || starts != 0 {
			t.Fatalf("link error=%v starts=%d", err, starts)
		}
	})

	t.Run("approval stop retains promotion and normal remediation", func(t *testing.T) {
		app, id, _ := newOpenNote(t)
		fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
		addApprovalGate(t, fixture.dir, "human approval")
		app.Repository = func(string) Repository { return plan.NewFileRepository(filepath.Dir(fixture.dir)) }
		app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
			return &planning.GeneratePlanResult{Allocation: planning.PlanAllocation{ID: fixture.id, Dir: fixture.dir}, Validation: planning.ValidationResult{OK: true}}, nil
		})
		starts := 0
		app.ProcessStarter = func(context.Context, string, string, []string) (run.Process, error) {
			starts++
			return nil, errors.New("must not start")
		}
		err := app.Run(context.Background(), []string{"note", "run", "--execution-mode", "current", "--commit-policy", "none", "--no-review", id})
		if err == nil || !strings.Contains(err.Error(), "tao approve --slice 001-a "+fixture.id) || starts != 0 {
			t.Fatalf("approval error=%v starts=%d", err, starts)
		}
		stored, _ := app.noteRepository(repoMeta).Get(context.Background(), id)
		if stored.Status != note.StatusPromoted || stored.Promotion.Plan.ID != fixture.id {
			t.Fatalf("approval stop lost promotion: %#v", stored)
		}
	})

	t.Run("lock contention prevents generation", func(t *testing.T) {
		app, id, _ := newOpenNote(t)
		app.AcquireNotePromotionLock = func(context.Context, string, string, string) (func() error, error) {
			return nil, note.ErrPromotionLocked
		}
		generated := 0
		app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
			generated++
			return nil, nil
		})
		err := app.Run(context.Background(), []string{"note", "run", id})
		if !errors.Is(err, note.ErrPromotionLocked) || generated != 0 {
			t.Fatalf("lock error=%v generated=%d", err, generated)
		}
	})

	t.Run("signal cancellation reaches generation", func(t *testing.T) {
		app, id, _ := newOpenNote(t)
		var cancel context.CancelFunc
		withCLICommandSignalContext(t, func(parent context.Context) (context.Context, context.CancelFunc) {
			ctx, stop := context.WithCancel(parent)
			cancel = stop
			return ctx, stop
		})
		app.PlanGenerator = planGeneratorFunc(func(ctx context.Context, _ planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		})
		err := app.Run(context.Background(), []string{"note", "run", id})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("signal error = %v", err)
		}
	})

	t.Run("archived and unhealthy notes stop before generation", func(t *testing.T) {
		app, id, _ := newOpenNote(t)
		if err := app.Run(context.Background(), []string{"note", "archive", id}); err != nil {
			t.Fatal(err)
		}
		generated := 0
		app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
			generated++
			return nil, nil
		})
		err := app.Run(context.Background(), []string{"note", "run", id})
		if err == nil || !strings.Contains(err.Error(), "reopen") || generated != 0 {
			t.Fatalf("archived error=%v generated=%d", err, generated)
		}
		app.RepoHealthCheck = func(context.Context, taodata.Repo) taodata.RepoHealth {
			return taodata.RepoHealth{Status: taodata.RepoHealthMissingRoot, Message: "gone", Error: true}
		}
		err = app.Run(context.Background(), []string{"note", "run", id})
		if err == nil || !strings.Contains(err.Error(), "unhealthy") || generated != 0 {
			t.Fatalf("health error=%v generated=%d", err, generated)
		}
	})
}

func TestNoteRunRejectsContinueAndBatchFlags(t *testing.T) {
	clearTaoEnv(t)
	repoMeta := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo"}
	app, _, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
	for _, flag := range []string{"--continue", "--all", "--active"} {
		err := app.Run(context.Background(), []string{"note", "run", flag, "note-id"})
		if err == nil {
			t.Fatalf("%s unexpectedly accepted", flag)
		}
	}
}

// Routing tests use executable markers, never real provider sessions.
func noteRoutingTestApp(t *testing.T) (App, taodata.Repo, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	clearTaoEnv(t)
	bin := t.TempDir()
	for _, name := range []string{"pi", "claude"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil { //nolint:gosec // G306: executable discovery marker needs exec bit
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	t.Setenv("TAO_AGENT", "claude")
	t.Setenv("TAO_PLANNER_ROUTING_ARMS", "pi=0.5,claude=0.5")
	meta := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo", Branch: "main"}
	app, out, errOut := noteTestApp(t, strings.NewReader(""), meta)
	if err := app.Run(context.Background(), []string{"note", "create", "--tag", "testing", "implement a small fix"}); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
	out.Reset()
	return app, meta, id, out, errOut
}

func noteRoutingRecords(t *testing.T, app App, meta taodata.Repo) []plannerroute.Record {
	t.Helper()
	dir := app.registry().(*fakeNoteRegistry).PlannerRoutesDir(meta)
	records, warnings, err := plannerroute.NewStore(dir).List(context.Background())
	if err != nil || len(warnings) != 0 {
		t.Fatalf("routes: %v, %v", err, warnings)
	}
	return records
}

func TestNoteRunPlannerRouting(t *testing.T) {
	for _, tc := range []struct{ mode, override, label string }{
		{"shadow", "", "shadow recommendation"},
		{"randomized", "", "assigned"},
		{"randomized", "pi", "manual override"},
	} {
		t.Run(tc.mode+tc.override, func(t *testing.T) {
			app, meta, id, out, _ := noteRoutingTestApp(t)
			t.Setenv("TAO_PLANNER_ROUTING", tc.mode)
			fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
			app.Repository = func(string) Repository { return plan.NewFileRepository(filepath.Dir(fixture.dir)) }
			var planningKind runtimeconfig.AgentKind
			app.PlanGenerator = planGeneratorFunc(func(_ context.Context, req planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
				planningKind = req.AgentKind
				records := noteRoutingRecords(t, app, meta)
				if len(records) != 1 || len(records[0].Entries) != 1 || records[0].Entries[0].Kind != plannerroute.EntryAssigned {
					t.Fatalf("assignment must precede planning: %#v", records)
				}
				if !strings.Contains(out.String(), "Planner route "+records[0].ID) {
					t.Fatalf("route not announced before planning: %s", out)
				}
				return &planning.GeneratePlanResult{Allocation: planning.PlanAllocation{ID: fixture.id, Dir: fixture.dir},
					Treatment: planning.Treatment{RuntimeLabel: string(req.AgentKind), ProviderID: "provider", ModelID: "model", MetricsAvailability: "reported"}}, nil
			})
			executions := 0
			old := executeSinglePlan
			executeSinglePlan = func(_ run.Service, _ context.Context, req run.Request) error {
				executions++
				if req.Agent != runtimeconfig.AgentClaude {
					t.Fatalf("execution agent changed: %s", req.Agent)
				}
				return nil
			}
			t.Cleanup(func() { executeSinglePlan = old })
			args := []string{"note", "run", "--no-review", id}
			if tc.override != "" {
				args = append(args, "--planner-arm", tc.override)
			}
			if err := app.Run(context.Background(), args); err != nil {
				t.Fatal(err)
			}
			records := noteRoutingRecords(t, app, meta)
			if len(records) != 1 {
				t.Fatalf("routes = %#v", records)
			}
			r := records[0]
			// This test owns the complete assignment/treatment/attempt/link sequence.
			if len(r.Entries) != 4 || r.Entries[0].Kind != plannerroute.EntryAssigned || r.Entries[1].Kind != plannerroute.EntryTreated || r.Entries[2].Kind != plannerroute.EntryAttempt || r.Entries[3].Kind != plannerroute.EntryLinked {
				t.Fatalf("entry sequence = %#v", r.Entries)
			}
			wantKind := r.Assignment.Selected.Runtime
			if tc.mode == "shadow" {
				wantKind = runtimeconfig.AgentClaude
			}
			if planningKind != wantKind || r.Entries[1].Treatment.RuntimeLabel != string(wantKind) || r.Entries[1].Treatment.ProviderID != "provider" || r.Entries[2].Attempt.Outcome != "plan_created" || r.LinkedPlanID() != fixture.id || executions != 1 {
				t.Fatalf("planning=%s executions=%d record=%#v", planningKind, executions, r)
			}
			if r.Assignment.ManualOverride != (tc.override != "") || tc.override != "" && r.Assignment.Selected.Runtime != runtimeconfig.AgentPi {
				t.Fatalf("override = %#v", r.Assignment)
			}
			if !strings.Contains(out.String(), "("+tc.label+")") || r.Context.BaselineRuntime != runtimeconfig.AgentClaude || r.Context.NoteTextBucket != "short" || len(r.Context.NoteTags) != 1 {
				t.Fatalf("context=%#v output=%s", r.Context, out)
			}
			stored, err := app.noteRepository(meta).Get(context.Background(), id)
			if err != nil || stored.Status != note.StatusPromoted {
				t.Fatalf("note=%#v err=%v", stored, err)
			}
		})
	}
}

func TestNoteRunPlannerRoutingFailures(t *testing.T) {
	for _, mode := range []string{"shadow", "randomized"} {
		t.Run(mode+" ledger unavailable", func(t *testing.T) {
			app, meta, id, _, errOut := noteRoutingTestApp(t)
			dir := app.registry().(*fakeNoteRegistry).PlannerRoutesDir(meta)
			if err := os.MkdirAll(dir, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) //nolint:gosec // G302: restore private directory traversal for cleanup
			if os.Geteuid() == 0 {
				t.Skip("root bypasses directory permissions")
			}
			calls := 0
			app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
				calls++
				return nil, errors.New("planner failed")
			})
			err := app.Run(context.Background(), []string{"note", "run", "--planner-routing", mode, id})
			if err == nil {
				t.Fatal("expected failure")
			}
			if mode == "randomized" && (calls != 0 || !strings.Contains(err.Error(), dir)) {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if mode == "shadow" && (calls != 1 || !strings.Contains(errOut.String(), "warning: planner routing ledger unavailable:")) {
				t.Fatalf("calls=%d warnings=%s", calls, errOut)
			}
			if _, err := os.Stat(app.registry().PlansDir(meta)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("plan directory allocated: %v", err)
			}
		})
	}
	for _, interrupted := range []bool{false, true} {
		t.Run(fmt.Sprintf("failed attempts interrupted=%v", interrupted), func(t *testing.T) {
			app, meta, id, _, _ := noteRoutingTestApp(t)
			var cancel context.CancelFunc
			withCLICommandSignalContext(t, func(parent context.Context) (context.Context, context.CancelFunc) {
				ctx, stop := context.WithCancel(parent)
				cancel = stop
				return ctx, stop
			})
			failure := errors.New("planner failed")
			app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
				if interrupted {
					cancel()
					return nil, &planning.GenerationError{Stage: planning.GenerationStageRuntime, Err: context.Canceled}
				}
				return nil, &planning.GenerationError{Stage: planning.GenerationStageRuntime, Err: failure, Treatment: &planning.Treatment{RuntimeLabel: "pi", ProviderID: "observed", ModelID: "model", MetricsAvailability: "partial"}}
			})
			for range 2 {
				err := app.Run(context.Background(), []string{"note", "run", "--planner-routing=randomized", id})
				if interrupted && !errors.Is(err, context.Canceled) || !interrupted && !errors.Is(err, failure) {
					t.Fatalf("error = %v", err)
				}
			}
			records := noteRoutingRecords(t, app, meta)
			if len(records) != 2 || records[0].Assignment.Draw != records[1].Assignment.Draw || records[0].Assignment.Selected != records[1].Assignment.Selected {
				t.Fatalf("unstable assignments: %#v", records)
			}
			for _, r := range records {
				if r.LinkedPlanID() != "" {
					t.Fatal("failed attempt linked")
				}
				var attempt *plannerroute.Attempt
				var treatment *plannerroute.Treatment
				for _, entry := range r.Entries {
					if entry.Kind == plannerroute.EntryAttempt {
						attempt = entry.Attempt
					}
					if entry.Kind == plannerroute.EntryTreated {
						treatment = entry.Treatment
					}
				}
				wantOutcome, wantProvider := "failed", "observed"
				if interrupted {
					wantOutcome, wantProvider = "interrupted", "unknown"
				}
				if attempt == nil || attempt.Outcome != wantOutcome || attempt.Stage != string(planning.GenerationStageRuntime) || treatment == nil || treatment.ProviderID != wantProvider {
					t.Fatalf("attempt=%#v treatment=%#v", attempt, treatment)
				}
			}
		})
	}
}

func TestNotePlannerRoutingFlagsOnlyRun(t *testing.T) {
	app, _, id, _, _ := noteRoutingTestApp(t)
	for _, sub := range []string{"create", "list", "show", "edit", "archive", "reopen"} {
		for _, flag := range []string{"--planner-routing=shadow", "--planner-arm=pi"} {
			err := app.Run(context.Background(), []string{"note", sub, flag, id})
			if err == nil || !strings.Contains(err.Error(), "not valid for tao note "+sub) {
				t.Fatalf("%s %s: %v", sub, flag, err)
			}
		}
	}
}

func TestNoteRunPlannerRoutingPostPlanFailure(t *testing.T) {
	for _, mode := range []string{"randomized", "shadow"} {
		for _, failure := range []string{"append", "link", "held lock"} {
			t.Run(mode+"/"+failure, func(t *testing.T) {
				app, meta, id, _, errOut := noteRoutingTestApp(t)
				fixture := newRunPlanFixture(t, plan.StatusPlanned, []string{"001-a"}, nil, "001-a", plan.StatusPending)
				app.Repository = func(string) Repository { return plan.NewFileRepository(filepath.Dir(fixture.dir)) }
				var routeID string
				app.PlanGenerator = planGeneratorFunc(func(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
					routes := noteRoutingRecords(t, app, meta)
					if len(routes) != 1 {
						t.Fatalf("routes=%#v", routes)
					}
					routeID = routes[0].ID
					dir := app.registry().PlannerRoutesDir(meta)
					switch failure {
					case "append":
						// Removing only the ledger document makes Append and Link fail,
						// independently of host privileges or filesystem permissions.
						if err := os.Remove(filepath.Join(dir, routeID+".json")); err != nil {
							t.Fatal(err)
						}
					case "held lock":
						holdPlannerRouteLock(t, dir)
					default:
						// A corrupt neighbor allows Append but prevents proving link uniqueness.
						if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{"), 0o600); err != nil {
							t.Fatal(err)
						}
					}
					return &planning.GeneratePlanResult{Allocation: planning.PlanAllocation{ID: fixture.id, Dir: fixture.dir}}, nil
				})
				executions := 0
				old := executeSinglePlan
				executeSinglePlan = func(run.Service, context.Context, run.Request) error { executions++; return nil }
				t.Cleanup(func() { executeSinglePlan = old })
				err := app.Run(context.Background(), []string{"note", "run", "--planner-routing", mode, "--no-review", id})
				if mode == "randomized" {
					if err == nil || !strings.Contains(err.Error(), "recover with tao route link "+routeID+" "+fixture.id) || !strings.Contains(err.Error(), fixture.dir) || executions != 0 {
						t.Fatalf("error=%v executions=%d", err, executions)
					}
				} else if err != nil || executions != 1 || !strings.Contains(errOut.String(), "warning: planner routing ledger unavailable:") {
					t.Fatalf("error=%v executions=%d warnings=%s", err, executions, errOut)
				}
				if failure == "held lock" {
					message := errOut.String()
					if err != nil {
						message += err.Error()
					}
					if !strings.Contains(message, filepath.Join(app.registry().PlannerRoutesDir(meta), ".routes.lock")) {
						t.Fatalf("missing lock path: %s", message)
					}
				}
				stored, err := app.noteRepository(meta).Get(context.Background(), id)
				if err != nil || stored.Status != note.StatusPromoted || stored.Promotion.Plan.ID != fixture.id {
					t.Fatalf("promotion changed: %#v, %v", stored, err)
				}
				if _, err := os.Stat(filepath.Join(fixture.dir, "state.json")); err != nil {
					t.Fatalf("plan lost: %v", err)
				}
				if failure == "link" {
					route, err := plannerroute.NewStore(app.registry().PlannerRoutesDir(meta)).Load(context.Background(), routeID)
					if err != nil {
						t.Fatal(err)
					}
					found := false
					for _, entry := range route.Entries {
						if entry.Kind == plannerroute.EntryAttempt && entry.Attempt.Outcome == "plan_created" {
							found = true
						}
					}
					if !found || route.LinkedPlanID() != "" {
						t.Fatalf("link failure record=%#v", route)
					}
				}
			})
		}
	}
}

func TestNoteRunPlannerRoutingOffOverridesEnvironment(t *testing.T) {
	app, meta, id, _, _ := noteRoutingTestApp(t)
	t.Setenv("TAO_PLANNER_ROUTING", "randomized")
	t.Setenv("TAO_PLANNER_ROUTING_ARMS", "invalid")
	calls := 0
	app.PlanGenerator = planGeneratorFunc(func(_ context.Context, req planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error) {
		calls++
		if req.AgentKind != runtimeconfig.AgentClaude {
			t.Fatalf("baseline changed: %s", req.AgentKind)
		}
		return nil, errors.New("planner failed")
	})
	if err := app.Run(context.Background(), []string{"note", "run", "--planner-routing=off", id}); err == nil || !strings.Contains(err.Error(), "planner failed") || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if routes := noteRoutingRecords(t, app, meta); len(routes) != 0 {
		t.Fatalf("off wrote routes: %#v", routes)
	}
}
