package workspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DependencyMetadata records one workspace dependency preparation attempt.
type DependencyMetadata struct {
	Status        string
	Command       string
	StartedAt     *time.Time
	CompletedAt   *time.Time
	FailureReason string
}

// PrepareDependencies installs workspace-local dependencies according to config.
func PrepareDependencies(ctx context.Context, workspaceRoot string, config Config, runner CommandRunner, now func() time.Time) (DependencyMetadata, error) {
	if runner == nil {
		runner = defaultCommandRunner
	}
	if now == nil {
		now = time.Now
	}
	command, args, skipReason, err := dependencyInstallCommand(workspaceRoot, config)
	if err != nil {
		return DependencyMetadata{Status: "failed", FailureReason: err.Error()}, err
	}
	if command == "" {
		return DependencyMetadata{Status: "skipped", FailureReason: skipReason}, nil
	}
	started := now().UTC()
	metadata := DependencyMetadata{Status: "running", Command: strings.Join(append([]string{command}, args...), " "), StartedAt: &started}
	var stderr bytes.Buffer
	err = runner(ctx, workspaceRoot, command, args, io.Discard, &stderr)
	completed := now().UTC()
	metadata.CompletedAt = &completed
	if err != nil {
		metadata.Status = "failed"
		metadata.FailureReason = strings.TrimSpace(stderr.String())
		if metadata.FailureReason == "" {
			metadata.FailureReason = err.Error()
		}
		return metadata, fmt.Errorf("prepare workspace dependencies: %w", err)
	}
	metadata.Status = "ready"
	return metadata, nil
}

func dependencyInstallCommand(workspaceRoot string, config Config) (string, []string, string, error) {
	switch config.DependencyInstallBehavior {
	case DependencyInstallNever:
		return "", nil, "dependency install behavior is never", nil
	case DependencyInstallCommand:
		return splitDependencyCommand(config.DependencyInstallCommand)
	case DependencyInstallAlways:
		command, args, _, ok := detectPackageManager(workspaceRoot)
		if !ok {
			return "", nil, "", fmt.Errorf("dependency install behavior is always but no supported lockfile was found")
		}
		return command, args, "", nil
	case DependencyInstallAuto, DependencyInstallAutoIfLockfilePresent, "":
		command, args, _, ok := detectPackageManager(workspaceRoot)
		if !ok {
			return "", nil, "no supported lockfile found", nil
		}
		return command, args, "", nil
	default:
		return "", nil, "", fmt.Errorf("unsupported dependency install behavior %q", config.DependencyInstallBehavior)
	}
}

func detectPackageManager(root string) (string, []string, string, bool) {
	checks := []struct {
		files []string
		name  string
		args  []string
	}{
		{files: []string{"pnpm-lock.yaml"}, name: "pnpm", args: []string{"install", "--frozen-lockfile"}},
		{files: []string{"yarn.lock"}, name: "yarn", args: []string{"install", "--frozen-lockfile"}},
		{files: []string{"bun.lockb", "bun.lock"}, name: "bun", args: []string{"install"}},
		{files: []string{"package-lock.json", "npm-shrinkwrap.json"}, name: "npm", args: []string{"ci"}},
	}
	for _, check := range checks {
		for _, file := range check.files {
			path := filepath.Join(root, file)
			if _, err := os.Stat(path); err == nil {
				return check.name, check.args, path, true
			}
		}
	}
	return "", nil, "", false
}

func dependencyLockfileFingerprint(root string) (string, error) {
	_, _, path, ok := detectPackageManager(root)
	if !ok {
		return "", nil
	}
	contents, err := os.ReadFile(path) //nolint:gosec // lockfile path is selected from a fixed allowlist
	if err != nil {
		return "", fmt.Errorf("read dependency lockfile %s: %w", path, err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(contents)), nil
}

func splitDependencyCommand(command string) (string, []string, string, error) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "", nil, "", fmt.Errorf("dependency install command is empty")
	}
	return parts[0], parts[1:], "", nil
}
