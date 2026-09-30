// Package commandrunner provides shared seams for executing local commands.
package commandrunner

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// SliceCompletionOwnerEnv carries the managed-session token that authorizes
// only the nested `tao slice-complete` handshake. Tao-spawned local commands
// (declared gates, Git) must never inherit it: a leaked token makes nested
// completion code refuse and turns the plan's own tests red inside a session.
const SliceCompletionOwnerEnv = "TAO_SLICE_COMPLETION_OWNER"

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
	environ, err := verificationCacheEnvironment(ctx, withoutSliceCompletionOwner(os.Environ()))
	if err != nil {
		return err
	}
	cmd.Env = environ
	// A shell can exit while descendants retain its output pipes. Bound Wait's
	// pipe drain too: context cancellation alone does not interrupt that wait
	// after the shell exits. ErrWaitDelay reports incomplete output, and deferred
	// process-group cleanup terminates the remaining descendants on Unix.
	cmd.WaitDelay = 100 * time.Millisecond
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func withoutSliceCompletionOwner(environ []string) []string {
	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {
		if key, _, _ := strings.Cut(entry, "="); key == SliceCompletionOwnerEnv {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
