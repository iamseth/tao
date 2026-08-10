package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/build"
	"github.com/iamseth/tao/internal/commandrunner"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/planning"
	"github.com/iamseth/tao/internal/run"
	"github.com/iamseth/tao/internal/taodata"
	"github.com/iamseth/tao/internal/tui"
	"github.com/iamseth/tao/internal/workspace"
)

type App struct {
	In                       io.Reader
	Out                      io.Writer
	Err                      io.Writer
	CommandRunner            CommandRunner
	ProcessStarter           run.ProcessStarter
	StatusReporter           run.StatusReporter
	Repository               RepositoryFactory
	Registry                 RegistryFactory
	NoteRepository           NoteRepositoryFactory
	PlanningRepository       PlanningRepositoryFactory
	PlanGenerator            PlanGenerator
	RepoHealthCheck          func(context.Context, taodata.Repo) taodata.RepoHealth
	AcquireNotePromotionLock NotePromotionLockAcquirer
	WorkspaceManager         WorkspaceManagerFactory
	PromptFreshnessCheck     PromptFreshnessChecker
	MonitorCollector         MonitorSnapshotCollector
	MonitorTicker            func(time.Duration) MonitorTicker
	MonitorIsTerminal        func(io.Writer) bool
	UITerminal               tui.Terminal
	UICommandLauncher        tui.CommandLauncher
	SelfUpdater              SelfUpdater
	// Now supplies the wall clock for timestamps recorded by commands. Tests
	// inject a fixed clock; when nil it defaults to time.Now.
	Now func() time.Time
}

// now returns the configured clock, defaulting to time.Now when unset.
func (a App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

type CommandRunner = commandrunner.Runner

// Repository is App's broad compatibility facade. Individual command handlers
// narrow this with local interfaces when they only need a plan record or read-only
// repository surface.
type Repository interface {
	plan.Repository
	plan.SliceRunRepository
	plan.PlanRecordResolver
	plan.PlanDeleter
	plan.LogReader
	plan.LogTailReader
	plan.LogFollower
}

type RepositoryFactory func(plansDir string) Repository

// NoteRepository is deliberately separate from the broad plan repository
// facade: note commands do not need access to plan lifecycle state.
type NoteRepository interface {
	Create(context.Context, string, []string) (note.Note, error)
	Get(context.Context, string) (note.Note, error)
	List(context.Context, note.Filter) ([]note.Note, []note.Warning, error)
	Edit(context.Context, string, string, []string) (note.Note, error)
	Archive(context.Context, string, string) (note.Note, error)
	Reopen(context.Context, string) (note.Note, error)
	PromoteToPlanning(context.Context, string, note.PlanningSessionLink) (note.Note, error)
	PromoteToPlan(context.Context, string, note.PlanLink) (note.Note, error)
}

type NoteRepositoryFactory func(dir string, repo note.RepoReference) NoteRepository

type PlanningRecordCreator interface {
	CreateSession(context.Context, planning.CreateRequest) (*planning.Session, error)
}

type PlanningRepositoryFactory func() PlanningRecordCreator

type PlanGenerator interface {
	GeneratePlan(context.Context, planning.GeneratePlanRequest) (*planning.GeneratePlanResult, error)
}

type NotePromotionLockAcquirer func(context.Context, string, string, string) (func() error, error)

type planLister interface {
	ListPlans(ctx context.Context, filter plan.PlanFilter) ([]plan.PlanSummary, error)
}

type WorkspaceManager interface {
	Prepare(ctx context.Context, options workspace.PrepareOptions) (workspace.Metadata, error)
	Status(ctx context.Context, planID string) (workspace.Metadata, error)
	List(ctx context.Context) ([]workspace.Metadata, error)
	PlanClean(ctx context.Context, planID string) (workspace.CleanPlan, error)
	Clean(ctx context.Context, planID string, options workspace.CleanOptions) (workspace.CleanPlan, error)
	PlanManagedCleanup(ctx context.Context) ([]workspace.ManagedCleanup, error)
	CleanManaged(ctx context.Context, item workspace.ManagedCleanup, options workspace.CleanOptions) error
}

type WorkspaceManagerFactory func(repoRoot string) (WorkspaceManager, error)

func (a App) Run(ctx context.Context, args []string) error {
	a = a.withDefaultStatusReporter()
	if len(args) == 0 {
		return a.usage()
	}

	plansDir := ""
	args, err := parseGlobalFlags(args, &plansDir)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return a.usage()
	}

	command := normalizeCommand(args[0])
	if command == "complete" {
		if len(args) == 2 && args[1] == "note-ids" {
			return a.completeNoteIDs(ctx)
		}
		return a.complete(ctx, a.repository(plansDir), args[1:])
	}
	if command == "--version" {
		if err := a.runStartupUpdate(ctx); err != nil {
			return err
		}
		return a.version()
	}
	if command == "help" || command == "-h" || command == "--help" {
		if err := a.runStartupUpdate(ctx); err != nil {
			return err
		}
		return a.usage()
	}
	metadata := commandByName(command)
	if metadata == nil {
		return fmt.Errorf("unknown command %q", args[0])
	}
	if metadata.name != updateCommand.name {
		if err := a.runStartupUpdate(ctx); err != nil {
			return err
		}
	}
	if containsHelpFlag(args[1:]) {
		return renderCommandHelp(a.Out, metadata)
	}
	if metadata.execute == nil {
		return fmt.Errorf("command %q is not executable", metadata.name)
	}
	if metadata.name != "version" && metadata.name != doctorCommand.name && metadata.name != installPromptsCommand.name {
		a.warnIfStalePrompts()
	}
	return metadata.execute(a.newCommandContext(metadata, ctx, plansDir, args[1:]))
}

func containsHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

// commandContext carries the dependencies the dispatcher resolves once per
// invocation and hands to a command's executor, replacing the previous
// positional (App, context.Context, plansDir, args) parameters. Cross-cutting
// concerns — repository resolution today, and global flags, output mode, or
// telemetry later — are wired here in one place instead of being repeated in
// every command handler.
type commandContext struct {
	app      App
	ctx      context.Context
	plansDir string
	args     []string
	// repo is the plan repository declared by commandMetadata.repository, resolved
	// once by the dispatcher. It is nil for commands that declare repositoryNone.
	repo Repository
}

// newCommandContext resolves the per-invocation dependencies for metadata,
// including the plan repository the command declared.
func (a App) newCommandContext(metadata *commandMetadata, ctx context.Context, plansDir string, args []string) commandContext {
	return commandContext{
		app:      a,
		ctx:      ctx,
		plansDir: plansDir,
		args:     args,
		repo:     a.resolveRepository(metadata.repository, plansDir),
	}
}

// resolveRepository builds the plan repository a command declared, or returns nil
// when it declared repositoryNone.
func (a App) resolveRepository(kind repositoryKind, plansDir string) Repository {
	switch kind {
	case repositoryDefault:
		return a.repository(plansDir)
	default:
		return nil
	}
}

var buildVersion = build.Version
var buildCommit = build.Commit
var buildAge = build.BuildAge

func (a App) version() error {
	if err := writeln(a.Out, "tao "+buildVersion()); err != nil {
		return err
	}
	if err := writeln(a.Out, "commit: "+buildCommit()); err != nil {
		return err
	}
	return writeln(a.Out, "build age: "+buildAge())
}

func (a App) input() io.Reader {
	if a.In != nil {
		return a.In
	}
	return os.Stdin
}

func (a App) repository(plansDir string) Repository {
	if a.Repository != nil {
		return a.Repository(plansDir)
	}
	return plan.NewFileRepository(plansDir)
}

func (a App) workspaceManager(repoRoot string) (WorkspaceManager, error) {
	if a.WorkspaceManager != nil {
		return a.WorkspaceManager(repoRoot)
	}
	return workspace.NewManager(workspace.Options{RepoRoot: repoRoot})
}

func parseGlobalFlags(args []string, plansDir *string) ([]string, error) {
	for len(args) > 0 {
		switch args[0] {
		case "--plans-dir":
			if len(args) < 2 {
				return nil, errors.New("--plans-dir requires a value")
			}
			*plansDir = args[1]
			args = append(args[:0], args[2:]...)
		case "--plans-dir=":
			return nil, errors.New("--plans-dir requires a value")
		default:
			if after, ok := strings.CutPrefix(args[0], "--plans-dir="); ok {
				*plansDir = after
				args = args[1:]
				continue
			}
			return args, nil
		}
	}
	return args, nil
}
