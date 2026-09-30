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

// PrepareSubmodules initializes the workspace's pinned recursive submodules using
// Git's configured transport policy and verifies recursive gitlink alignment.
// It never forces checkout or follows remote tips.
func PrepareSubmodules(ctx context.Context, workspaceRoot string, runner CommandRunner, now func() time.Time) (DependencyMetadata, error) {
	if now == nil {
		now = time.Now
	}
	if _, err := os.Lstat(filepath.Join(workspaceRoot, ".gitmodules")); err != nil {
		if os.IsNotExist(err) {
			return DependencyMetadata{Status: "skipped", FailureReason: "no .gitmodules found"}, nil
		}
		started := now().UTC()
		completed := now().UTC()
		failure := fmt.Errorf("inspect workspace .gitmodules: %w", err)
		return DependencyMetadata{Status: "failed", StartedAt: &started, CompletedAt: &completed, FailureReason: failure.Error()}, failure
	}
	metadata, err := runDependencyCommand(ctx, workspaceRoot, "git", []string{"submodule", "update", "--init", "--recursive"}, runner, now)
	if err != nil {
		metadata.FailureReason = metadata.Command + ": " + metadata.FailureReason
		return metadata, fmt.Errorf("prepare workspace submodules: %s: %w", metadata.FailureReason, err)
	}
	var status bytes.Buffer
	checked, err := runDependencyCommandOutput(ctx, workspaceRoot, "git", []string{"submodule", "status", "--recursive"}, runner, now, &status)
	metadata.Command += " && " + checked.Command
	metadata.CompletedAt = checked.CompletedAt
	if err == nil {
		// Git prefixes every submodule with a space only when initialized at
		// its recorded gitlink. '-', '+', and 'U' indicate missing, divergent,
		// and conflicted prerequisites, including in nested submodules. Do not
		// trim leading spaces or interpret paths (which may contain spaces).
		for _, line := range strings.Split(strings.TrimSuffix(status.String(), "\n"), "\n") {
			if line != "" && line[0] != ' ' {
				err = fmt.Errorf("submodule is not initialized at its recorded gitlink: %s; inspect submodule update configuration and local changes, then initialize the pinned checkout and retry", line)
				checked.FailureReason = err.Error()
				break
			}
		}
	}
	if err != nil {
		metadata.Status = "failed"
		metadata.FailureReason = checked.Command + ": " + checked.FailureReason
		return metadata, fmt.Errorf("prepare workspace submodules: %s: %w", metadata.FailureReason, err)
	}
	return metadata, nil
}

// PrepareDependencies initializes submodules before installing workspace-local JS dependencies.
func PrepareDependencies(ctx context.Context, workspaceRoot string, _ Config, runner CommandRunner, now func() time.Time) (DependencyMetadata, error) {
	metadata, _, err := prepareDependencies(ctx, workspaceRoot, runner, now, false)
	return metadata, err
}

// prepareDependencies shares the ordering boundary with ExecutionPreparer. Only
// JS installation can be cached; the bool result identifies a hard prerequisite
// failure that must never inherit the reused-workspace JS warning policy.
func prepareDependencies(ctx context.Context, workspaceRoot string, runner CommandRunner, now func() time.Time, skipJS bool) (DependencyMetadata, bool, error) {
	submodules, err := PrepareSubmodules(ctx, workspaceRoot, runner, now)
	if err != nil {
		return submodules, true, err
	}
	js := DependencyMetadata{Status: "skipped", FailureReason: "lockfile unchanged since last successful install"}
	if !skipJS {
		js, err = prepareJSDependencies(ctx, workspaceRoot, runner, now)
	}
	if submodules.Status == "skipped" {
		return js, false, err
	}
	if js.Status == "skipped" {
		return submodules, false, nil
	}
	// Keep the existing metadata schema while recording only attempted commands,
	// in order, and the complete attempt's time span and failing step.
	if err != nil {
		js.FailureReason = js.Command + ": " + js.FailureReason
	}
	js.Command = submodules.Command + " && " + js.Command
	js.StartedAt = submodules.StartedAt
	return js, false, err
}

func prepareJSDependencies(ctx context.Context, workspaceRoot string, runner CommandRunner, now func() time.Time) (DependencyMetadata, error) {
	command, args, skipReason := dependencyInstallCommand(workspaceRoot)
	if command == "" {
		return DependencyMetadata{Status: "skipped", FailureReason: skipReason}, nil
	}
	metadata, err := runDependencyCommand(ctx, workspaceRoot, command, args, runner, now)
	if err != nil {
		return metadata, fmt.Errorf("prepare workspace dependencies: %w", err)
	}
	return metadata, nil
}

func runDependencyCommand(ctx context.Context, workspaceRoot, command string, args []string, runner CommandRunner, now func() time.Time) (DependencyMetadata, error) {
	return runDependencyCommandOutput(ctx, workspaceRoot, command, args, runner, now, io.Discard)
}

func runDependencyCommandOutput(ctx context.Context, workspaceRoot, command string, args []string, runner CommandRunner, now func() time.Time, stdout io.Writer) (DependencyMetadata, error) {
	if runner == nil {
		runner = defaultCommandRunner
	}
	if now == nil {
		now = time.Now
	}
	started := now().UTC()
	metadata := DependencyMetadata{Status: "running", Command: strings.Join(append([]string{command}, args...), " "), StartedAt: &started}
	var stderr bytes.Buffer
	err := runner(ctx, workspaceRoot, command, args, stdout, &stderr)
	completed := now().UTC()
	metadata.CompletedAt = &completed
	if err != nil {
		metadata.Status = "failed"
		metadata.FailureReason = strings.TrimSpace(stderr.String())
		if metadata.FailureReason == "" {
			metadata.FailureReason = err.Error()
		}
		return metadata, err
	}
	metadata.Status = "ready"
	return metadata, nil
}

func dependencyInstallCommand(workspaceRoot string) (string, []string, string) {
	command, args, _, ok := detectPackageManager(workspaceRoot)
	if !ok {
		return "", nil, "no supported lockfile found"
	}
	return command, args, ""
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
