// Package commandrunner provides shared seams for executing local commands.
package commandrunner

import (
	"context"
	"io"
	"os/exec"
)

// Runner runs a local command with optional stdout and stderr writers.
type Runner func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error

// DefaultLocal executes commands on the local machine.
func DefaultLocal(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...) // #nosec G204 -- callers provide explicit command names and arguments.
	cleanup := configureCommandCancellation(cmd)
	defer cleanup()
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
