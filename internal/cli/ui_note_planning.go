package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/herdr"
	"github.com/iamseth/tao/internal/note"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

// uiNotePlanningLauncher deliberately does not use run sessions or the detached
// dashboard launcher. Eligibility is a fresh read, not a promotion transaction:
// the planning prompt must independently validate any later lifecycle mutation.
type uiNotePlanningLauncher struct {
	app    App
	input  io.Reader
	output io.Writer
	run    func(*exec.Cmd) error
}

func newUINotePlanningLauncher(app App, input io.Reader, output io.Writer) *uiNotePlanningLauncher {
	return &uiNotePlanningLauncher{app: app, input: input, output: output, run: (*exec.Cmd).Run}
}

func (launcher *uiNotePlanningLauncher) Launch(ctx context.Context, item note.CatalogNote) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	registered, err := (&uiNoteEditor{app: launcher.app}).creationRepository(item.RepositoryID)
	if err != nil {
		return err
	}
	root, err := filepath.EvalSymlinks(registered.Root)
	if err != nil {
		return fmt.Errorf("resolve note repository root: %w", err)
	}
	if !filepath.IsAbs(root) || root != registered.Root || filepath.Clean(root) != root ||
		taodata.RepoID(root) != registered.ID || item.RepositoryRoot != root {
		return fmt.Errorf("note repository identity or root changed")
	}
	if err := launcher.app.requireHealthyNoteRepository(ctx, registered); err != nil {
		return err
	}
	current, err := launcher.app.noteRepository(registered).Get(ctx, item.ID)
	if err != nil {
		return fmt.Errorf("load planning note: %w", err)
	}
	// Get also supports prefixes for CLI users. A dashboard selection must instead
	// match the full persisted ID and repository, even when a prefix is unique.
	if current.ID != item.ID || !uiNoteRepositoryID.MatchString(current.ID) ||
		current.Repo.ID != registered.ID || current.Repo.Root != root {
		return fmt.Errorf("planning note identity does not match selection")
	}
	if current.Status != note.StatusOpen || current.Archive != nil ||
		(current.Promotion != nil && current.Promotion.Plan != nil) {
		return fmt.Errorf("planning note is no longer open or is already linked to a plan")
	}
	kind, err := runtimeconfig.ParseAgentKind(os.Getenv(runtimeconfig.EnvAgent))
	if err != nil {
		return err
	}
	descriptor, ok := agent.Lookup(kind)
	if !ok {
		return fmt.Errorf("unsupported planning agent %q", kind)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, descriptor.ToolName, "/tao-plan note:"+current.ID) //nolint:gosec // G204: registered executable and exact validated note ID; no shell or note prose.
	cmd.Dir = root
	cmd.Stdin, cmd.Stdout, cmd.Stderr = launcher.input, launcher.output, launcher.app.noteErrorOutput()
	cmd.Env = herdr.StripInjectedEnv(os.Environ())
	if err := launcher.run(cmd); err != nil {
		return fmt.Errorf("foreground note planning: %w", err)
	}
	return nil
}
