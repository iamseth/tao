package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

const defaultNoteListLimit = 20

var noteCommand = commandMetadata{
	name:                  "note",
	minPrefix:             "n",
	usageLines:            []string{"note (n) [list (l)] [--repo REPO] [--tag TAG] [--status STATUS] [--all] [--limit N]", "note (n) create (c) [--repo REPO] [--tag TAG] [--] [TEXT...]", "note (n) show (s) [--repo REPO] <note-id>", "note (n) edit (e) [--repo REPO] [--tag TAG] <note-id> [--] [TEXT...]", "note (n) archive (a) [--repo REPO] [--reason TEXT | --plan PLAN] <note-id>", "note (n) reopen [--repo REPO] <note-id>", "note (n) run (r) [--repo REPO] [--max-slices N] [--commit-policy slice|none] [--execution-mode isolated|current] [--pull-request] [--no-review] [--dangerously-skip-permissions] <note-id>"},
	completionDescription: "Capture and maintain repository notes",
	long:                  "Capture and maintain a private note backlog for a registered repository. With no subcommand, note lists open notes newest first.",
	examples:              "  tao n c fix flaky queue test\n  printf 'First line\\n\\nMore detail\\n' | tao note create --tag testing\n  tao note list --all\n  tao note archive 20260713-155800-abcd",
	subcommands: []commandSubcommand{
		{name: "create", aliases: []commandSubcommandAlias{"c"}, description: "Create a note from arguments or standard input"},
		{name: "list", aliases: []commandSubcommandAlias{"l"}, description: "List notes (the default subcommand)"},
		{name: "show", aliases: []commandSubcommandAlias{"s"}, description: "Show raw note text and provenance", completion: completionContext{positional: completionPositional{index: 1, label: "note", completer: completeNoteIDs}}},
		{name: "edit", aliases: []commandSubcommandAlias{"e"}, description: "Edit a note's text and optional tags", completion: completionContext{positional: completionPositional{index: 1, label: "note", completer: completeNoteIDs}}},
		{name: "archive", aliases: []commandSubcommandAlias{"a"}, description: "Archive an open note", completion: completionContext{positional: completionPositional{index: 1, label: "note", completer: completeNoteIDs}}},
		{name: "reopen", description: "Reopen an archived note", completion: completionContext{positional: completionPositional{index: 1, label: "note", completer: completeNoteIDs}}},
		{name: "run", aliases: []commandSubcommandAlias{"r"}, description: "Generate and run a plan for a clear note", completion: completionContext{positional: completionPositional{index: 1, label: "note", completer: completeNoteIDs}}},
	},
	registerFlags: registerNoteFlags,
	completion: completionContext{flagValues: map[string]completionFlagValue{
		"commit-policy":  {kind: completionValueEnum, label: "policy", values: []string{"slice", "none"}},
		"execution-mode": {kind: completionValueEnum, label: "mode", values: []string{"isolated", "current"}},
		"limit":          {kind: completionValueCount, label: "count"},
		"max-slices":     {kind: completionValueCount, label: "count"},
		"plan":           {kind: completionValueText, label: "plan"},
		"reason":         {kind: completionValueText, label: "text"},
		"repo":           {kind: completionValueText, label: "repository"},
		"status":         {kind: completionValueEnum, label: "status", values: []string{"open", "promoted", "archived"}},
		"tag":            {kind: completionValueText, label: "tag"},
	}},
	execute: func(c commandContext) error { return c.app.note(c.ctx, c.args) },
}

type NoteRegistry interface {
	Current(context.Context) (taodata.Repo, error)
	ReadRepo(string) (taodata.Repo, error)
	ListRepos() ([]taodata.Repo, error)
	NotesDir(taodata.Repo) string
	PlansDir(taodata.Repo) string
}

type RegistryFactory func() NoteRegistry

type stringListFlag []string

func (v *stringListFlag) String() string { return strings.Join(*v, ",") }
func (v *stringListFlag) Set(value string) error {
	*v = append(*v, value)
	return nil
}

func registerNoteFlags(fs *flag.FlagSet) {
	fs.String("repo", "", "registered repository ID prefix or exact name")
	fs.Var(new(stringListFlag), "tag", "tag to apply or require (repeatable)")
	fs.Var(new(stringListFlag), "status", "status to list: open, promoted, or archived (repeatable)")
	fs.Bool("all", false, "include all note statuses")
	fs.Int("limit", defaultNoteListLimit, "maximum notes to list (0 means unlimited)")
	fs.String("reason", "", "archive reason")
	fs.String("plan", "", "validated normal plan destination")
	registerRunRequestFlags(fs)
}

func (a App) note(ctx context.Context, args []string) error {
	fs, positional, err := a.parseArgs("note", boundNoteTextArgs(args), registerNoteFlags)
	if err != nil {
		return err
	}
	subcommand := "list"
	if len(positional) > 0 {
		subcommand = normalizeNoteSubcommand(positional[0])
		if subcommand == "" {
			return fmt.Errorf("unknown note subcommand %q; use create, list, show, edit, archive, reopen, or run", positional[0])
		}
		positional = positional[1:]
	}
	if err := validateNoteFlags(fs, subcommand); err != nil {
		return err
	}
	registered, err := a.resolveNoteRepo(ctx, flagStringValue(fs, "repo"))
	if err != nil {
		return err
	}
	repo := a.noteRepository(registered)
	switch subcommand {
	case "create":
		return a.noteCreate(ctx, repo, fs, positional)
	case "list":
		return a.noteList(ctx, repo, fs, positional)
	case "show":
		return a.noteShow(ctx, repo, positional)
	case "edit":
		return a.noteEdit(ctx, registered, repo, fs, positional)
	case "archive":
		return a.noteArchive(ctx, registered, repo, fs, positional)
	case "reopen":
		return a.noteReopen(ctx, repo, positional)
	case "run":
		return a.noteRun(ctx, registered, repo, fs, positional)
	default:
		panic("unreachable note subcommand")
	}
}

// boundNoteTextArgs inserts an option boundary at the start of create/edit
// text. This preserves later flag-shaped words as note content. A leading known
// option name remains ambiguous and must follow an explicit -- boundary.
func boundNoteTextArgs(args []string) []string {
	valueFlags := map[string]bool{
		"repo": true, "tag": true, "status": true, "limit": true,
		"reason": true, "plan": true, "max-slices": true,
		"commit-policy": true, "execution-mode": true,
	}
	knownFlags := map[string]bool{
		"all": true, "pull-request": true,
		"dangerously-skip-permissions": true, "no-review": true,
	}
	for name := range valueFlags {
		knownFlags[name] = true
	}

	subcommand := ""
	positionals := 0
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			return args
		}
		if strings.HasPrefix(arg, "-") && arg != "-" {
			name := strings.TrimPrefix(arg, "-")
			name = strings.TrimPrefix(name, "-")
			if eq := strings.IndexByte(name, '='); eq >= 0 {
				name = name[:eq]
			}
			if !knownFlags[name] {
				if subcommand == "create" || subcommand == "edit" && positionals > 0 {
					return insertNoteOptionBoundary(args, i)
				}
				continue
			}
			if valueFlags[name] && !strings.Contains(arg, "=") && i+1 < len(args) {
				i++
			}
			continue
		}
		if subcommand == "" {
			subcommand = normalizeNoteSubcommand(arg)
			if subcommand == "" {
				return args
			}
			continue
		}
		positionals++
		if subcommand == "create" || subcommand == "edit" && positionals > 1 {
			return insertNoteOptionBoundary(args, i)
		}
	}
	return args
}

func insertNoteOptionBoundary(args []string, index int) []string {
	bounded := make([]string, 0, len(args)+1)
	bounded = append(bounded, args[:index]...)
	bounded = append(bounded, "--")
	return append(bounded, args[index:]...)
}

func normalizeNoteSubcommand(value string) string {
	switch value {
	case "create", "c":
		return "create"
	case "list", "l":
		return "list"
	case "show", "s":
		return "show"
	case "edit", "e":
		return "edit"
	case "archive", "a":
		return "archive"
	case "reopen":
		return "reopen"
	case "run", "r":
		return "run"
	default:
		return ""
	}
}

func validateNoteFlags(fs *flag.FlagSet, subcommand string) error {
	allowed := map[string]map[string]bool{
		"create":  {"repo": true, "tag": true},
		"list":    {"repo": true, "tag": true, "status": true, "all": true, "limit": true},
		"show":    {"repo": true},
		"edit":    {"repo": true, "tag": true},
		"archive": {"repo": true, "reason": true, "plan": true},
		"reopen":  {"repo": true},
		"run":     {"repo": true, "max-slices": true, "commit-policy": true, "execution-mode": true, "pull-request": true, "dangerously-skip-permissions": true, "no-review": true},
	}
	var invalid string
	fs.Visit(func(fl *flag.Flag) {
		if !allowed[subcommand][fl.Name] && invalid == "" {
			invalid = "--" + fl.Name
		}
	})
	if invalid != "" {
		return fmt.Errorf("%s is not valid for tao note %s", invalid, subcommand)
	}
	return nil
}

func (a App) registry() NoteRegistry {
	if a.Registry != nil {
		return a.Registry()
	}
	registry := taodata.NewRegistry("")
	return registry
}

func (a App) noteRepository(repo taodata.Repo) NoteRepository {
	ref := note.RepoReference{ID: repo.ID, Root: repo.Root}
	registry := a.registry()
	if a.NoteRepository != nil {
		return a.NoteRepository(registry.NotesDir(repo), ref)
	}
	return note.NewRepository(registry.NotesDir(repo), ref)
}

func (a App) resolveNoteRepo(ctx context.Context, selector string) (taodata.Repo, error) {
	return taodata.ResolveRepo(ctx, a.registry(), selector)
}

func (a App) noteCreate(ctx context.Context, repo NoteRepository, fs *flag.FlagSet, args []string) error {
	text, err := a.readNoteText(args)
	if err != nil {
		return err
	}
	created, err := repo.Create(ctx, text, noteTags(fs))
	if err != nil {
		return fmt.Errorf("create note: %w", err)
	}
	return writef(a.Out, "Created note %s\n", created.ID)
}

func (a App) noteList(ctx context.Context, repo NoteRepository, fs *flag.FlagSet, args []string) error {
	if len(args) != 0 {
		return errors.New("usage: tao note list [--repo REPO] [--tag TAG] [--status STATUS] [--all] [--limit N]")
	}
	limit := flagIntValue(fs, "limit")
	if limit < 0 {
		return errors.New("--limit must be zero or greater")
	}
	statuses, err := noteStatuses(fs)
	if err != nil {
		return err
	}
	notes, warnings, err := repo.List(ctx, note.Filter{Statuses: statuses, Tags: noteTags(fs), All: flagBoolValue(fs, "all")})
	if err != nil {
		return err
	}
	for _, warning := range warnings {
		if err := writef(a.noteErrorOutput(), "warning: %s\n", warning.Error()); err != nil {
			return err
		}
	}
	if limit > 0 && len(notes) > limit {
		notes = notes[:limit]
	}
	if len(notes) == 0 {
		return writeln(a.Out, "No notes found.")
	}
	if err := writeln(a.Out, "ID  STATUS  TAGS  DESTINATION  NOTE"); err != nil {
		return err
	}
	for _, item := range notes {
		tags := "-"
		if len(item.Tags) > 0 {
			tags = strings.Join(item.Tags, ",")
		}
		if err := writef(a.Out, "%s  %s  %s  %s  %s\n", item.ID, item.Status, tags, noteDestination(item), notePreview(item.Text)); err != nil {
			return err
		}
	}
	return nil
}

func (a App) noteShow(ctx context.Context, repo NoteRepository, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: tao note show <note-id> [--repo REPO]")
	}
	item, err := repo.Get(ctx, args[0])
	if err != nil {
		return fmt.Errorf("show note: %w", err)
	}
	lines := []string{
		"ID: " + item.ID,
		"Repository: " + item.Repo.ID,
		"Repository root: " + emptyDash(item.Repo.Root),
		"Status: " + string(item.Status),
		"Tags: " + emptyDash(strings.Join(item.Tags, ", ")),
		"Created: " + item.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
		"Updated: " + item.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
	if item.Archive != nil {
		lines = append(lines, "Archived: "+item.Archive.ArchivedAt.Format("2006-01-02T15:04:05Z07:00"), "Archive reason: "+emptyDash(item.Archive.Reason))
		if item.Archive.PlanningSession != nil {
			lines = append(lines, "Planning session: "+item.Archive.PlanningSession.ID)
		}
		if item.Archive.Plan != nil {
			lines = append(lines, "Plan: "+item.Archive.Plan.ID, "Plan directory: "+emptyDash(item.Archive.Plan.Dir))
		}
	}
	if item.Promotion != nil && item.Promotion.PlanningSession != nil {
		lines = append(lines, "Planning session: "+item.Promotion.PlanningSession.ID)
	}
	if item.Promotion != nil && item.Promotion.Plan != nil {
		lines = append(lines, "Plan: "+item.Promotion.Plan.ID, "Plan directory: "+emptyDash(item.Promotion.Plan.Dir))
	}
	lines = append(lines, "", "Text:")
	if err := writeLines(a.Out, lines...); err != nil {
		return err
	}
	if _, err := fmt.Fprint(a.Out, item.Text); err != nil {
		return err
	}
	if !strings.HasSuffix(item.Text, "\n") {
		return writeln(a.Out, "")
	}
	return nil
}

func (a App) noteEdit(ctx context.Context, registered taodata.Repo, repo NoteRepository, fs *flag.FlagSet, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: tao note edit <note-id> [--tag TAG] [TEXT...]")
	}
	current, err := repo.Get(ctx, args[0])
	if err != nil {
		return fmt.Errorf("edit note: %w", err)
	}
	if current.Status == note.StatusPromoted {
		return fmt.Errorf("note %s was promoted and cannot be edited", current.ID)
	}
	text, err := a.readNoteText(args[1:])
	if err != nil {
		return err
	}
	tags := current.Tags
	if flagWasProvided(fs, "tag") {
		tags = noteTags(fs)
	}
	updated, err := a.mutateOpenNote(ctx, registered, current.ID, "note edit", func() (note.Note, error) {
		return repo.Edit(ctx, current.ID, text, tags)
	})
	if err != nil {
		return fmt.Errorf("edit note: %w", err)
	}
	return writef(a.Out, "Edited note %s\n", updated.ID)
}

func (a App) noteArchive(ctx context.Context, registered taodata.Repo, repo NoteRepository, fs *flag.FlagSet, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: tao note archive <note-id> [--reason TEXT | --plan PLAN]")
	}
	planInput := strings.TrimSpace(flagStringValue(fs, "plan"))
	if flagWasProvided(fs, "plan") {
		if planInput == "" {
			return errors.New("--plan requires a plan ID")
		}
		if flagWasProvided(fs, "reason") {
			return errors.New("--reason cannot be used with --plan")
		}
		return a.noteArchiveToPlan(ctx, registered, repo, args[0], planInput)
	}
	current, err := repo.Get(ctx, args[0])
	if err != nil {
		return fmt.Errorf("archive note: %w", err)
	}
	switch current.Status {
	case note.StatusPromoted:
		return fmt.Errorf("note %s was promoted and cannot be archived", current.ID)
	case note.StatusArchived:
		return fmt.Errorf("note %s is already archived", current.ID)
	}
	updated, err := a.mutateOpenNote(ctx, registered, current.ID, "note archive", func() (note.Note, error) {
		return repo.Archive(ctx, current.ID, flagStringValue(fs, "reason"))
	})
	if err != nil {
		return fmt.Errorf("archive note: %w", err)
	}
	return writef(a.Out, "Archived note %s\n", updated.ID)
}

func (a App) noteArchiveToPlan(ctx context.Context, registered taodata.Repo, repo NoteRepository, noteID, planInput string) error {
	plansDir := a.registry().PlansDir(registered)
	planRepo := a.repository(plansDir)
	detail, err := planRepo.ResolvePlan(ctx, planInput)
	if err != nil {
		return fmt.Errorf("archive note: resolve plan %q for repository %s: %w", planInput, registered.ID, err)
	}
	if err := validateNoteArchivePlan(detail, registered, plansDir); err != nil {
		return fmt.Errorf("archive note: plan %q is not a valid destination: %w", planInput, err)
	}
	current, err := repo.Get(ctx, noteID)
	if err != nil {
		return fmt.Errorf("resolve note for plan archival: %w", err)
	}
	noteID = current.ID
	link := note.PlanLink{ID: detail.State.Plan.ID, Dir: detail.Dir, Mode: "plan"}
	recovery := fmt.Sprintf("tao note archive --repo %s --plan %s %s", registered.ID, link.ID, noteID)

	release, err := a.acquireNotePromotionLock(ctx, a.registry().NotesDir(registered), noteID, "note archive --plan")
	if err != nil {
		return fmt.Errorf("lock note %s for plan archival: %w; rerun exactly: `%s`", noteID, err, recovery)
	}
	locked := true
	defer func() {
		if locked {
			_ = release()
		}
	}()

	current, err = repo.Get(ctx, noteID)
	if err != nil {
		return fmt.Errorf("re-read note for plan archival: %w; rerun exactly: `%s`", err, recovery)
	}
	if current.Status == note.StatusArchived && current.Archive != nil && current.Archive.Plan != nil && current.Archive.Plan.ID == link.ID {
		if err := release(); err != nil {
			return fmt.Errorf("note %s is already linked to plan %s, but its mutation lock could not be released: %w; rerun exactly: `%s`", noteID, link.ID, err, recovery)
		}
		locked = false
		return writef(a.Out, "Note %s is already archived to plan %s\n", noteID, link.ID)
	}
	updated, err := repo.ArchiveToPlan(ctx, noteID, link)
	if err != nil {
		return fmt.Errorf("plan %s was validated and left untouched, but note %s could not be linked: %w; rerun exactly: `%s`", link.ID, noteID, err, recovery)
	}
	if err := release(); err != nil {
		return fmt.Errorf("note %s was archived to plan %s, but its mutation lock could not be released: %w; rerun exactly: `%s`", updated.ID, link.ID, err, recovery)
	}
	locked = false
	return writef(a.Out, "Archived note %s to plan %s\n", updated.ID, link.ID)
}

func validateNoteArchivePlan(detail *plan.PlanDetail, registered taodata.Repo, plansDir string) error {
	if detail == nil {
		return errors.New("plan loader returned no plan")
	}
	if detail.State.Schema != "tao.plan.state.v1" || detail.Slices.Schema != "tao.plan.slices.v1" {
		return errors.New("normal plan state and slices schemas are required")
	}
	id := strings.TrimSpace(detail.State.Plan.ID)
	if id == "" || detail.Slices.PlanID != id {
		return errors.New("plan artifact IDs are missing or inconsistent")
	}
	if detail.Dir == "" || filepath.Base(filepath.Clean(detail.Dir)) != id {
		return errors.New("plan directory does not match its recorded ID")
	}
	plansRoot, err := filepath.EvalSymlinks(plansDir)
	if err != nil {
		return fmt.Errorf("resolve repository plans directory: %w", err)
	}
	planDir, err := filepath.EvalSymlinks(detail.Dir)
	if err != nil {
		return fmt.Errorf("resolve plan directory: %w", err)
	}
	plansRoot, err = filepath.Abs(plansRoot)
	if err != nil {
		return fmt.Errorf("make repository plans directory absolute: %w", err)
	}
	planDir, err = filepath.Abs(planDir)
	if err != nil {
		return fmt.Errorf("make plan directory absolute: %w", err)
	}
	if filepath.Dir(filepath.Clean(planDir)) != filepath.Clean(plansRoot) {
		return fmt.Errorf("plan directory %q is outside repository plans directory %q", detail.Dir, plansDir)
	}
	planRoot := strings.TrimSpace(detail.State.Repo.Root)
	registeredRoot := strings.TrimSpace(registered.Root)
	if planRoot == "" || registeredRoot == "" || filepath.Clean(planRoot) != filepath.Clean(registeredRoot) {
		return fmt.Errorf("recorded repository root %q does not match registered repository %s root %q", planRoot, registered.ID, registeredRoot)
	}
	if result := plan.ValidatePlanVerification(detail); result.HasErrors() {
		messages := make([]string, 0, len(result.Findings))
		for _, finding := range result.Findings {
			if finding.Severity == plan.VerificationFindingError {
				messages = append(messages, finding.Message)
			}
		}
		return fmt.Errorf("plan verification is invalid: %s", strings.Join(messages, "; "))
	}
	return nil
}

// mutateOpenNote prevents a note from changing while a plan is being created
// from its current contents.
func (a App) mutateOpenNote(ctx context.Context, registered taodata.Repo, noteID, owner string, mutate func() (note.Note, error)) (note.Note, error) {
	release, err := a.acquireNotePromotionLock(ctx, a.registry().NotesDir(registered), noteID, owner)
	if err != nil {
		return note.Note{}, err
	}
	updated, mutationErr := mutate()
	return updated, errors.Join(mutationErr, release())
}

func (a App) noteReopen(ctx context.Context, repo NoteRepository, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: tao note reopen <note-id>")
	}
	current, err := repo.Get(ctx, args[0])
	if err != nil {
		return fmt.Errorf("reopen note: %w", err)
	}
	switch current.Status {
	case note.StatusOpen:
		return fmt.Errorf("note %s is already open", current.ID)
	case note.StatusPromoted:
		return fmt.Errorf("note %s was promoted and cannot be reopened", current.ID)
	}
	updated, err := repo.Reopen(ctx, current.ID)
	if err != nil {
		return fmt.Errorf("reopen note: %w", err)
	}
	return writef(a.Out, "Reopened note %s\n", updated.ID)
}

// notePromotion describes the destination-specific pieces of the shared
// locked open-note promotion protocol.
type notePromotion struct {
	// owner labels the promotion lock owner.
	owner string
	// purpose labels lock and re-read errors, such as "planning".
	purpose string
	// alreadyPromoted renders the terminal already-promoted outcome.
	alreadyPromoted func(item note.Note) error
	// notOpen rejects a note that is neither open nor promoted.
	notOpen func(item note.Note) error
	// promote creates the destination and links the still-locked note. The
	// returned destination and recovery strings name the created destination
	// and how to recover it if the lock release fails afterward.
	promote func(ctx context.Context, item note.Note) (promoted note.Note, destination string, recovery string, err error)
}

// promoteOpenNote owns the serialized direct-run promotion protocol: gate on
// status, lock, re-read under the lock, re-gate, promote, release. It returns
// false without error when the note was already promoted and that outcome has
// been rendered.
func (a App) promoteOpenNote(ctx context.Context, registered taodata.Repo, repo NoteRepository, item note.Note, p notePromotion) (note.Note, bool, error) {
	if item.Status == note.StatusPromoted {
		return note.Note{}, false, p.alreadyPromoted(item)
	}
	if item.Status != note.StatusOpen {
		return note.Note{}, false, p.notOpen(item)
	}
	release, err := a.acquireNotePromotionLock(ctx, a.registry().NotesDir(registered), item.ID, p.owner)
	if err != nil {
		return note.Note{}, false, fmt.Errorf("lock note %s for %s: %w", item.ID, p.purpose, err)
	}
	locked := true
	defer func() {
		if locked {
			_ = release()
		}
	}()

	item, err = repo.Get(ctx, item.ID)
	if err != nil {
		return note.Note{}, false, fmt.Errorf("re-read note for %s: %w", p.purpose, err)
	}
	if item.Status == note.StatusPromoted {
		return note.Note{}, false, p.alreadyPromoted(item)
	}
	if item.Status != note.StatusOpen {
		return note.Note{}, false, p.notOpen(item)
	}

	promoted, destination, recovery, err := p.promote(ctx, item)
	if err != nil {
		return note.Note{}, false, err
	}
	if err := release(); err != nil {
		return note.Note{}, false, fmt.Errorf("note %s was linked to %s, but its promotion lock could not be released: %w; recover with %s", promoted.ID, destination, err, recovery)
	}
	locked = false
	return promoted, true, nil
}

func (a App) noteRun(ctx context.Context, registered taodata.Repo, repo NoteRepository, fs *flag.FlagSet, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: tao note run <note-id> [--repo REPO] [--max-slices N] [--commit-policy slice|none] [--execution-mode isolated|current] [--pull-request] [--no-review] [--dangerously-skip-permissions]")
	}
	inputs, err := resolveRunRequestFlags(fs)
	if err != nil {
		return err
	}
	// Resolve every run option before allocating a plan or invoking the planner.
	request, err := inputs.defaults.newRunRequestWithRepository("pending-note-plan", repositoryRunOptions(registered), inputs.overrides)
	if err != nil {
		return err
	}
	skipPermissions := inputs.skipPermissions
	if err := a.requireHealthyNoteRepository(ctx, registered); err != nil {
		return err
	}

	item, err := repo.Get(ctx, args[0])
	if err != nil {
		return fmt.Errorf("run note: %w", err)
	}
	var generated *planning.GeneratePlanResult
	promoted, ok, err := a.promoteOpenNote(ctx, registered, repo, item, notePromotion{
		owner:           "note run",
		purpose:         "direct planning",
		alreadyPromoted: a.printExistingNotePromotion,
		notOpen: func(item note.Note) error {
			return fmt.Errorf("note %s is %s; reopen it before running", item.ID, item.Status)
		},
		promote: func(ctx context.Context, item note.Note) (note.Note, string, string, error) {
			now := a.now().UTC()
			source := &planning.SourceEnvelope{Type: "note", Note: &planning.SourceNoteSnapshot{
				ID: item.ID, Text: item.Text, Tags: item.Tags, RepoID: registered.ID, RepoName: registered.Name, CapturedAt: now,
			}}
			session, err := planning.NewSession("note-"+item.ID, notePlanningTitle(item.Text), item.Text, registered, source, now)
			if err != nil {
				return note.Note{}, "", "", fmt.Errorf("prepare planning session for note %s: %w", item.ID, err)
			}
			mode := agent.PermissionModeAuto
			if skipPermissions {
				mode = agent.PermissionModeBypassPermissions
			}
			generationCtx, stopSignals := newCommandSignalContext(ctx)
			generated, err = a.planGenerator(inputs.defaults.Agent).GeneratePlan(generationCtx, planning.GeneratePlanRequest{
				Session: session, PermissionMode: mode, Timeout: request.SessionTimeout, RejectOpenQuestions: true,
			})
			stopSignals()
			if err != nil {
				a.printGenerationWarnings(err)
				return note.Note{}, "", "", fmt.Errorf("could not generate a runnable plan for note %s: %w\nUse /tao-plan note:%s for supervised clarification", item.ID, err, item.ID)
			}
			if generated == nil {
				return note.Note{}, "", "", fmt.Errorf("could not generate a runnable plan for note %s: generator returned no plan\nUse /tao-plan note:%s for supervised clarification", item.ID, item.ID)
			}
			promoted, err := repo.PromoteToPlan(ctx, item.ID, note.PlanLink{ID: generated.Allocation.ID, Dir: generated.Allocation.Dir, Mode: "run"})
			if err != nil {
				return note.Note{}, "", "", fmt.Errorf("plan %s was created at %s, but note %s could not be linked: %w; recover with tao run %s", generated.Allocation.ID, generated.Allocation.Dir, item.ID, err, generated.Allocation.ID)
			}
			if err := a.printValidationWarnings(generated.Validation); err != nil {
				return note.Note{}, "", "", err
			}
			return promoted, "plan " + generated.Allocation.ID, "tao run " + generated.Allocation.ID, nil
		},
	})
	if !ok || err != nil {
		return err
	}
	if err := writef(a.Out, "Promoted note %s to plan %s\n", promoted.ID, generated.Allocation.ID); err != nil {
		return err
	}
	request.Input = generated.Allocation.ID
	planRepo := a.repository(filepath.Dir(generated.Allocation.Dir))
	return a.executeResolvedRun(ctx, planRepo, generated.Allocation.ID, request, skipPermissions, runtimeconfig.AutoReworkPolicy{}, false, true)
}

func (a App) planGenerator(agentKind runtimeconfig.AgentKind) PlanGenerator {
	if a.PlanGenerator != nil {
		return a.PlanGenerator
	}
	return planning.NewService(planning.NewFileRepository(""), nil, planning.ServiceOptions{
		Agent: agentKind, ProcessStarter: a.ProcessStarter, Log: a.Out,
	})
}

func (a App) printValidationWarnings(validation planning.ValidationResult) error {
	for _, finding := range validation.Findings {
		if finding.Severity != "warning" {
			continue
		}
		if err := writef(a.noteErrorOutput(), "warning: %s\n", finding.Message); err != nil {
			return err
		}
	}
	return nil
}

func (a App) printGenerationWarnings(err error) {
	var generationErr *planning.GenerationError
	if errors.As(err, &generationErr) && generationErr.Validation != nil {
		_ = a.printValidationWarnings(*generationErr.Validation)
	}
}

func (a App) printExistingNotePromotion(item note.Note) error {
	if item.Promotion != nil && item.Promotion.Plan != nil {
		link := item.Promotion.Plan
		return writeLines(a.Out, "Note already promoted to plan "+link.ID, "Plan directory: "+emptyDash(link.Dir), "Run it with: tao run "+link.ID)
	}
	if item.Promotion != nil && item.Promotion.PlanningSession != nil {
		link := item.Promotion.PlanningSession
		return writeLines(a.Out, "Note already promoted to planning session "+link.ID, "Continue planning in a fresh agent session using the note as /tao-plan context.")
	}
	return fmt.Errorf("note %s is promoted but has no destination", item.ID)
}

func (a App) acquireNotePromotionLock(ctx context.Context, dir, noteID, owner string) (func() error, error) {
	if a.AcquireNotePromotionLock != nil {
		return a.AcquireNotePromotionLock(ctx, dir, noteID, owner)
	}
	lock, err := note.NewPromotionLocker(dir).Acquire(ctx, noteID, owner)
	if err != nil {
		return nil, err
	}
	return lock.Release, nil
}

func (a App) requireHealthyNoteRepository(ctx context.Context, registered taodata.Repo) error {
	health := taodata.RepoHealthChecker{}.Check(ctx, registered)
	if a.RepoHealthCheck != nil {
		health = a.RepoHealthCheck(ctx, registered)
	}
	if health.Error {
		return fmt.Errorf("repository %s is unhealthy [%s]: %s", registered.ID, health.Status, health.Message)
	}
	return nil
}

func notePlanningTitle(text string) string {
	title := notePreview(text)
	if title == "" {
		return "Note planning"
	}
	const maxRunes = 60
	runes := []rune(title)
	if len(runes) > maxRunes {
		title = string(runes[:maxRunes-1]) + "…"
	}
	return title
}

func noteDestination(item note.Note) string {
	if item.Archive != nil && item.Archive.Plan != nil {
		return "plan:" + item.Archive.Plan.ID
	}
	if item.Promotion == nil {
		return "-"
	}
	if item.Promotion.PlanningSession != nil {
		return "planning:" + item.Promotion.PlanningSession.ID
	}
	if item.Promotion.Plan != nil {
		return "plan:" + item.Promotion.Plan.ID
	}
	return "-"
}

func (a App) readNoteText(args []string) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	in := a.input()
	if inputIsTerminal(in) {
		if err := writef(a.noteErrorOutput(), "note> "); err != nil {
			return "", err
		}
		text, err := bufio.NewReader(in).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSuffix(text, "\n"), nil
	}
	content, err := io.ReadAll(io.LimitReader(in, note.MaxText+1))
	if err != nil {
		return "", err
	}
	if len(content) > note.MaxText {
		return "", fmt.Errorf("note text exceeds %d bytes", note.MaxText)
	}
	return string(content), nil
}

func (a App) noteErrorOutput() io.Writer {
	if a.Err != nil {
		return a.Err
	}
	return os.Stderr
}

func inputIsTerminal(in io.Reader) bool {
	if terminal, ok := in.(interface{ IsTerminal() bool }); ok {
		return terminal.IsTerminal()
	}
	file, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func noteTags(fs *flag.FlagSet) []string { return noteStringList(fs, "tag") }

func noteStringList(fs *flag.FlagSet, name string) []string {
	fl := fs.Lookup(name)
	if fl == nil {
		return nil
	}
	value, ok := fl.Value.(*stringListFlag)
	if !ok {
		return nil
	}
	return append([]string(nil), (*value)...)
}

func noteStatuses(fs *flag.FlagSet) ([]note.Status, error) {
	values := noteStringList(fs, "status")
	statuses := make([]note.Status, 0, len(values))
	for _, value := range values {
		status := note.Status(strings.ToLower(strings.TrimSpace(value)))
		switch status {
		case note.StatusOpen, note.StatusPromoted, note.StatusArchived:
			statuses = append(statuses, status)
		default:
			return nil, fmt.Errorf("invalid note status %q; use open, promoted, or archived", value)
		}
	}
	return statuses, nil
}

func (a App) completeNoteIDs(ctx context.Context) error {
	registered, err := a.resolveNoteRepo(ctx, "")
	if err != nil {
		return err
	}
	items, _, err := a.noteRepository(registered).List(ctx, note.Filter{All: true})
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := writeln(a.Out, item.ID); err != nil {
			return err
		}
	}
	return nil
}

func notePreview(text string) string {
	line := ""
	for candidate := range strings.SplitSeq(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(candidate) != "" {
			line = strings.TrimSpace(candidate)
			break
		}
	}
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, line)
	const maxRunes = 72
	if utf8.RuneCountInString(line) <= maxRunes {
		return line
	}
	runes := []rune(line)
	return string(runes[:maxRunes-1]) + "…"
}
