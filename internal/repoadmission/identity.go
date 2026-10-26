package repoadmission

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// identityGit is separate from operation runners: identity lookup must not alter
// consumer command sequences. Tests may replace it when no lookup is running.
var identityGit = func(ctx context.Context, root string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "--path-format=absolute", "--git-common-dir") // #nosec G204 -- fixed read-only command; root is a separate -C argument, not shell input.
	// Ambient Git selectors must not redirect admission to a different repository.
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "GIT_") {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	return cmd.Output()
}

func identity(ctx context.Context, root string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("repository admission: missing repository root")
	}
	output, err := identityGit(ctx, root)
	if err != nil {
		return "", fmt.Errorf("repository admission: resolve Git common directory: %w", err)
	}
	path := strings.TrimSuffix(string(output), "\n")
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("repository admission: Git common directory is not absolute")
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("repository admission: canonicalize Git common directory: %w", err)
	}
	return filepath.Clean(path), nil
}
