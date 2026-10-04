package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/iamseth/tao/internal/agent"
	"github.com/iamseth/tao/internal/herdr"
	"github.com/iamseth/tao/internal/monitor"
	"github.com/iamseth/tao/internal/plan"
	"github.com/iamseth/tao/internal/runtimeconfig"
	"github.com/iamseth/tao/internal/taodata"
)

// Eligibility is a fresh read, not a lifecycle transaction. The foreground
// prompt must independently refuse live runs before preparing any fixes.
type uiPlanFixLauncher struct {
	app    App
	input  io.Reader
	output io.Writer
	run    func(*exec.Cmd) error
}

func newUIPlanFixLauncher(app App, input io.Reader, output io.Writer) *uiPlanFixLauncher {
	return &uiPlanFixLauncher{app: app, input: input, output: output, run: (*exec.Cmd).Run}
}

func (launcher *uiPlanFixLauncher) Launch(ctx context.Context, row monitor.Row) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(row.PlanID) == "" || row.RepositoryID == "" || row.RepositoryRoot == "" {
		return fmt.Errorf("plan ID and repository identity and root are required")
	}
	if filepath.Base(row.PlanID) != row.PlanID || row.PlanID == "." || row.PlanID == ".." {
		return fmt.Errorf("plan identity does not match selection")
	}
	registered, err := (&uiNoteEditor{app: launcher.app}).creationRepository(row.RepositoryID)
	if err != nil {
		return fmt.Errorf("plan repository identity or root changed: %w", err)
	}
	root, err := filepath.EvalSymlinks(registered.Root)
	if err != nil {
		return fmt.Errorf("plan repository identity or root changed: %w", err)
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root != registered.Root || root != row.RepositoryRoot || taodata.RepoID(root) != registered.ID {
		return fmt.Errorf("plan repository identity or root changed")
	}
	if err := launcher.app.requireHealthyNoteRepository(ctx, registered); err != nil {
		return err
	}
	current, err := launcher.app.repository(launcher.app.registry().PlansDir(registered)).ResolvePlan(ctx, row.PlanID)
	if err != nil {
		return fmt.Errorf("load plan for fix: %w", err)
	}
	if current.State.Plan.ID != row.PlanID {
		return fmt.Errorf("plan identity does not match selection")
	}
	if lock, err := plan.ReadRunLock(current.Dir); err == nil && lock.ProcessAlive {
		return fmt.Errorf("plan has a live run")
	}
	app, err := launcher.app.settingsForRepository(ctx, registered)
	if err != nil {
		return err
	}
	defaults, err := app.envDefaultsFor(runtimeconfig.EnvAgent, runtimeconfig.EnvModel)
	if err != nil {
		return err
	}
	descriptor, ok := agent.Lookup(defaults.Agent)
	if !ok {
		return fmt.Errorf("unsupported plan fix agent %q", defaults.Agent)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	args := []string{"/tao-fix-plan " + current.State.Plan.ID}
	if defaults.Base != "" {
		args = append([]string{"--model", defaults.Base}, args...)
	}
	cmd := exec.CommandContext(ctx, descriptor.ToolName, args...) //nolint:gosec // G204: registered executable and validated plan ID, without a shell.
	cmd.Dir = root
	cmd.Stdin, cmd.Stdout, cmd.Stderr = launcher.input, launcher.output, launcher.app.noteErrorOutput()
	cmd.Env = herdr.StripInjectedEnv(os.Environ())
	if err := launcher.run(cmd); err != nil {
		return fmt.Errorf("foreground plan fix: %w", err)
	}
	return nil
}
