package run

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

func TestRunDependencyHelperAccessors(t *testing.T) {
	runner := func(context.Context, string, string, []string, io.Writer, io.Writer) error { return nil }
	now := func() time.Time { return time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC) }
	appender := eventAppenderFunc(func(string, plan.Event) error { return nil })
	options := Options{RunDependencies: RunDependencies{CommandRunner: runner, Now: now, EventAppender: appender}}
	if options.commandRunner() == nil || options.clock() == nil || options.eventAppender() == nil {
		t.Fatalf("options helper accessors returned nil")
	}
	deps := RunDependencies{CommandRunner: runner, Now: now, EventAppender: appender}
	if deps.commandRunner() == nil || deps.clock() == nil || deps.eventAppender() == nil {
		t.Fatalf("dependency helper accessors returned nil")
	}
	execution := newRunExecution(ExecutionConfig{}, deps)
	if execution.commandRunner() == nil || execution.clock() == nil || execution.eventAppender() == nil {
		t.Fatalf("execution helper accessors returned nil")
	}
	input := WorkspaceResolverInput{CommandRunner: runner, Now: now}
	if input.commandRunner() == nil || input.clock() == nil {
		t.Fatalf("workspace input helper accessors returned nil")
	}
	if agentLabel(AgentPi) != "pi" || agentLabel(AgentClaude) != "claude" || agentLabel("") != "pi" {
		t.Fatalf("unexpected agent labels")
	}
	if nextSliceLabel("001-a") != "001-a" || nextSliceLabel("") != "-" {
		t.Fatalf("unexpected next slice labels")
	}
}
