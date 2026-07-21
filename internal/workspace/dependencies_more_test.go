package workspace

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestDependencyInstallCommandBehaviors(t *testing.T) {
	config := DefaultConfig()
	config.DependencyInstallBehavior = DependencyInstallNever
	command, args, reason, err := dependencyInstallCommand(t.TempDir(), config)
	if err != nil || command != "" || args != nil || !strings.Contains(reason, "never") {
		t.Fatalf("never behavior = %q %#v %q %v", command, args, reason, err)
	}

	config = DefaultConfig()
	config.DependencyInstallBehavior = DependencyInstallAlways
	_, _, _, err = dependencyInstallCommand(t.TempDir(), config)
	if err == nil || !strings.Contains(err.Error(), "no supported lockfile") {
		t.Fatalf("expected always-without-lockfile error, got %v", err)
	}

	config = DefaultConfig()
	config.DependencyInstallBehavior = "sometimes"
	_, _, _, err = dependencyInstallCommand(t.TempDir(), config)
	if err == nil || !strings.Contains(err.Error(), "unsupported dependency install behavior") {
		t.Fatalf("expected unsupported behavior error, got %v", err)
	}

	config = DefaultConfig()
	config.DependencyInstallBehavior = DependencyInstallCommand
	_, _, _, err = dependencyInstallCommand(t.TempDir(), config)
	if err == nil || !strings.Contains(err.Error(), "command is empty") {
		t.Fatalf("expected empty command error, got %v", err)
	}
}

func TestPrepareDependenciesFallsBackToRunnerErrorWhenStderrEmpty(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root+"/yarn.lock")
	metadata, err := PrepareDependencies(context.Background(), root, DefaultConfig(), func(ctx context.Context, cwd string, name string, args []string, stdout io.Writer, stderr io.Writer) error {
		return context.Canceled
	}, fixedDependencyClock())
	if err == nil || metadata.Status != "failed" || metadata.FailureReason != context.Canceled.Error() {
		t.Fatalf("expected runner error failure metadata, got %#v err=%v", metadata, err)
	}
}
