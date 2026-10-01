package gitops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/iamseth/tao/internal/commandrunner"
)

const probeOutputLimit = 1 << 20

// NewReadOnlyClient opts into pinned, lock-free local read probes. The runner
// boundary also refuses mutating helpers, including helpers bypassing git().
func NewReadOnlyClient(repoRoot string, runner commandrunner.Runner) Client {
	if runner == nil {
		runner = defaultRunner
	}
	c := NewClient(repoRoot, func(ctx context.Context, _ string, name string, args []string, stdout, stderr io.Writer) error {
		if !filepath.IsAbs(repoRoot) {
			return errors.New("read-only Git root must be absolute")
		}
		if name != "git" || len(args) < 3 || args[0] != "-C" || args[1] != repoRoot {
			return errors.New("read-only Git requires the configured root")
		}
		probe := args[2:]
		allowed := false
		switch probe[0] {
		case "diff", "status", "rev-parse", "merge-base", "symbolic-ref":
			allowed = true
		case "branch":
			// Only the listing forms used by probes; never combine --list with mutations.
			allowed = true
			hasList := false
			for _, arg := range probe[1:] {
				if arg == "--list" {
					hasList = true
					continue
				}
				if arg == "--format=%(refname:short)" {
					continue
				}
				if len(arg) > 0 && arg[0] == '-' {
					allowed = false
				}
			}
			allowed = allowed && hasList
		}
		if !allowed {
			return fmt.Errorf("read-only Git refuses %q", probe[0])
		}
		pinned := []string{"-C", repoRoot, "--no-optional-locks", "-c", "diff.autoRefreshIndex=false", "-c", "core.fsmonitor=false", "-c", "core.quotePath=false", "--literal-pathspecs"}
		return runner(ctx, "", "git", append(pinned, probe...), stdout, stderr)
	})
	c.readOnly = true
	return c
}

func (c Client) readOnlyClient() Client {
	if c.readOnly {
		return c
	}
	return NewReadOnlyClient(c.repoRoot, c.runner)
}

// boundedProbeOutput keeps configured legacy read helpers bounded as well.
func (c Client) boundedProbeOutput(ctx context.Context, args []string) (string, error) {
	stdout := boundedWriter{limit: probeOutputLimit}
	stderr := boundedWriter{limit: probeOutputLimit}
	err := c.git(ctx, args, &stdout, &stderr)
	if err != nil {
		return "", commandError(args, err, probeStderr(&stderr))
	}
	if stdout.truncated {
		return "", errors.New("read-only Git output exceeded limit")
	}
	return stdout.String(), nil
}

func probeStderr(w *boundedWriter) string {
	text := w.String()
	if w.truncated {
		text += "\n[git stderr truncated]"
	}
	return text
}
