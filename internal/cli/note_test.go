package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/run"
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
func (r *fakeNoteRegistry) NotesDir(repo taodata.Repo) string { return r.dir + "/" + repo.ID }

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

func TestNotePlanCreatesSourceLinkedSessionAndIsIdempotent(t *testing.T) {
	repoMeta := taodata.Repo{Schema: taodata.RepoSchema, ID: "tao-123", Name: "tao", Root: "/repo"}
	app, out, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
	dataHome := t.TempDir()
	if err := taodata.NewRegistry(dataHome).WriteRepo(repoMeta); err != nil {
		t.Fatal(err)
	}
	store := planning.NewFileRepository(dataHome)
	store.Now = func() time.Time { return time.Date(2026, 7, 13, 16, 5, 0, 0, time.UTC) }
	store.Suffix = func() (string, error) { return "source", nil }
	app.PlanningRepository = func() PlanningRecordCreator { return store }
	locks := 0
	app.AcquireNotePromotionLock = func(_ context.Context, _ string, _, owner string) (func() error, error) {
		locks++
		if owner != "note plan" {
			t.Fatalf("lock owner = %q", owner)
		}
		return func() error { return nil }, nil
	}
	agentStarts := 0
	app.ProcessStarter = func(context.Context, string, string, []string) (run.Process, error) {
		agentStarts++
		return nil, errors.New("agent must not start")
	}

	if err := app.Run(context.Background(), []string{"note", "create", "Add", "safe /slice handling"}); err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
	out.Reset()
	if err := app.Run(context.Background(), []string{"note", "plan", id}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Planning session created: tao-123:", "fresh agent session", "tao note show " + id} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("plan output missing %q: %q", want, out.String())
		}
	}
	result, err := store.ListSessions(context.Background(), planning.ListFilter{RepoID: repoMeta.ID})
	if err != nil || len(result.Sessions) != 1 {
		t.Fatalf("sessions = %+v, err=%v", result, err)
	}
	created, err := store.GetSession(context.Background(), result.Sessions[0].RouteID)
	if err != nil {
		t.Fatal(err)
	}
	if created.Source == nil || created.Source.Note == nil || created.Source.Note.ID != id || created.Source.Note.Text != "Add safe /slice handling" || created.Messages[0].Content != "Add safe /slice handling" {
		t.Fatalf("session source/prompt = %+v / %+v", created.Source, created.Messages)
	}

	out.Reset()
	if err := app.Run(context.Background(), []string{"n", "p", id}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "already promoted") || locks != 1 {
		t.Fatalf("idempotent output=%q locks=%d", out.String(), locks)
	}
	result, _ = store.ListSessions(context.Background(), planning.ListFilter{RepoID: repoMeta.ID})
	if len(result.Sessions) != 1 || agentStarts != 0 {
		t.Fatalf("retry sessions=%+v agent starts=%d", result.Sessions, agentStarts)
	}
}

func TestNotePlanFailureBoundaries(t *testing.T) {
	repoMeta := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo"}
	t.Run("unhealthy repository leaves note open without planning artifacts", func(t *testing.T) {
		app, out, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
		if err := app.Run(context.Background(), []string{"note", "create", "keep this note"}); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
		healthChecks, locks, repositories := 0, 0, 0
		app.RepoHealthCheck = func(_ context.Context, got taodata.Repo) taodata.RepoHealth {
			healthChecks++
			if got.ID != repoMeta.ID {
				t.Fatalf("health checked repository %q, want %q", got.ID, repoMeta.ID)
			}
			return taodata.RepoHealth{Status: taodata.RepoHealthMissingRoot, Message: "gone", Error: true}
		}
		app.AcquireNotePromotionLock = func(context.Context, string, string, string) (func() error, error) {
			locks++
			return func() error { return nil }, nil
		}
		app.PlanningRepository = func() PlanningRecordCreator {
			repositories++
			return planningCreateRepository{}
		}

		err := app.Run(context.Background(), []string{"note", "plan", id})
		if err == nil || !strings.Contains(err.Error(), "repository tao-123 is unhealthy") {
			t.Fatalf("health error = %v", err)
		}
		item, getErr := app.noteRepository(repoMeta).Get(context.Background(), id)
		if getErr != nil || item.Status != note.StatusOpen {
			t.Fatalf("note after health failure = %+v, err=%v", item, getErr)
		}
		if healthChecks != 1 || locks != 0 || repositories != 0 {
			t.Fatalf("health checks=%d locks=%d planning repositories=%d", healthChecks, locks, repositories)
		}
	})

	t.Run("lock failure prevents session creation", func(t *testing.T) {
		app, out, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
		creates := 0
		app.PlanningRepository = func() PlanningRecordCreator {
			creates++
			return planningCreateRepository{}
		}
		if err := app.Run(context.Background(), []string{"note", "create", "locked"}); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
		app.AcquireNotePromotionLock = func(context.Context, string, string, string) (func() error, error) {
			return nil, note.ErrPromotionLocked
		}
		if err := app.Run(context.Background(), []string{"note", "plan", id}); !errors.Is(err, note.ErrPromotionLocked) {
			t.Fatalf("lock error = %v", err)
		}
		if creates != 0 {
			t.Fatalf("created %d sessions before failure boundaries", creates)
		}
	})

	t.Run("creation failure leaves note open", func(t *testing.T) {
		app, out, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
		app.PlanningRepository = func() PlanningRecordCreator { return planningCreateRepository{err: errors.New("create failed")} }
		app.AcquireNotePromotionLock = func(context.Context, string, string, string) (func() error, error) {
			return func() error { return nil }, nil
		}
		if err := app.Run(context.Background(), []string{"note", "create", "keep open"}); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
		if err := app.Run(context.Background(), []string{"note", "plan", id}); err == nil || !strings.Contains(err.Error(), "create failed") {
			t.Fatalf("creation error = %v", err)
		}
		item, err := app.noteRepository(repoMeta).Get(context.Background(), id)
		if err != nil || item.Status != note.StatusOpen {
			t.Fatalf("note after failure = %+v, err=%v", item, err)
		}
	})

	t.Run("link failure reports durable recovery session", func(t *testing.T) {
		app, out, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
		baseFactory := app.NoteRepository
		app.NoteRepository = func(dir string, ref note.RepoReference) NoteRepository {
			return &failingPlanningPromotionRepository{Repository: baseFactory(dir, ref).(*note.Repository), err: errors.New("link failed")}
		}
		app.PlanningRepository = func() PlanningRecordCreator {
			return planningCreateRepository{session: &planning.Session{ID: "session-1", Repo: planning.RepoRef{ID: repoMeta.ID}}}
		}
		app.AcquireNotePromotionLock = func(context.Context, string, string, string) (func() error, error) {
			return func() error { return nil }, nil
		}
		if err := app.Run(context.Background(), []string{"note", "create", "recover me"}); err != nil {
			t.Fatal(err)
		}
		id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
		err := app.Run(context.Background(), []string{"note", "plan", id})
		if err == nil || !strings.Contains(err.Error(), "tao-123:session-1") || !strings.Contains(err.Error(), "link failed") {
			t.Fatalf("recovery error = %v", err)
		}
	})
}

type planningCreateRepository struct {
	session *planning.Session
	err     error
	create  func(context.Context, planning.CreateRequest) (*planning.Session, error)
}

func (r planningCreateRepository) CreateSession(ctx context.Context, request planning.CreateRequest) (*planning.Session, error) {
	if r.create != nil {
		return r.create(ctx, request)
	}
	return r.session, r.err
}

type failingPlanningPromotionRepository struct {
	*note.Repository
	err error
}

func (r *failingPlanningPromotionRepository) PromoteToPlanning(context.Context, string, note.PlanningSessionLink) (note.Note, error) {
	return note.Note{}, r.err
}

func TestNotePlanAndRunRejectConcurrentSourceMutations(t *testing.T) {
	clearTaoEnv(t)
	repoMeta := taodata.Repo{ID: "tao-123", Name: "tao", Root: "/repo", Branch: "main"}
	for _, promotion := range []string{"plan", "run"} {
		for _, mutation := range []string{"edit", "archive"} {
			t.Run(promotion+"_with_"+mutation, func(t *testing.T) {
				app, out, _ := noteTestApp(t, strings.NewReader(""), repoMeta)
				if err := app.Run(context.Background(), []string{"note", "create", "original source"}); err != nil {
					t.Fatal(err)
				}
				id := strings.TrimSpace(strings.TrimPrefix(out.String(), "Created note "))
				out.Reset()

				generationStarted := make(chan struct{})
				continueGeneration := make(chan struct{})
				var sourceText string
				promotionArgs := []string{"note", "plan", id}
				if promotion == "plan" {
					app.PlanningRepository = func() PlanningRecordCreator {
						return planningCreateRepository{create: func(_ context.Context, request planning.CreateRequest) (*planning.Session, error) {
							sourceText = request.InitialPrompt
							close(generationStarted)
							<-continueGeneration
							return &planning.Session{ID: "session-1", Repo: planning.RepoRef{ID: repoMeta.ID}}, nil
						}}
					}
				} else {
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
					promotionArgs = []string{"note", "run", "--execution-mode", "current", "--commit-policy", "none", "--no-review", id}
				}

				promoted := make(chan error, 1)
				go func() { promoted <- app.Run(context.Background(), promotionArgs) }()
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
					t.Fatalf("%s note: %v", promotion, err)
				}
				stored, err = app.noteRepository(repoMeta).Get(context.Background(), id)
				if err != nil || stored.Status != note.StatusPromoted || sourceText != "original source" {
					t.Fatalf("promotion source=%q note=%#v err=%v", sourceText, stored, err)
				}
			})
		}
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
		if err == nil || !strings.Contains(err.Error(), "supervised clarification") || !strings.Contains(err.Error(), "tao note plan "+id) {
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
