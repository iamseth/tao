package run

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/iamseth/tao/internal/plan"
)

// RunDependencies is the dependency struct a run execution reads from. It holds
// only real collaborators; this file performs no defaulting. The order in which
// missing dependencies are resolved lives in run_setup.go so the dependency
// graph stays legible at a single place.
type RunDependencies struct {
	CommandRunner       CommandRunner
	ProcessStarter      ProcessStarter
	SliceExecutor       SliceExecutor
	PlanRecordFactory   PlanRecordFactory
	PullRequestCreator  PullRequestCreator
	ReviewCreator       ReviewCreator
	EventAppender       plan.EventAppender
	LogAppender         plan.LogAppender
	RootResolver        ExecutionRootResolver
	WorkspacePreparer   WorkspacePreparer
	AgentFactory        AgentCapabilitiesFactory
	StatusReporter      StatusReporter
	HeaderReporter      HeaderReporter
	OutputWriter        io.Writer
	SessionLogWriter    io.Writer
	TransportRetryDelay func(context.Context, time.Duration) error
	Now                 func() time.Time
}

// newRunDependencies returns the collaborators composed into Options. It does
// no defaulting: callers run the explicit setup in run_setup.go to resolve
// omitted dependencies.
func newRunDependencies(options Options) RunDependencies {
	return options.RunDependencies
}

// requireResolvedDependencies is the completeness guard for a fully set-up run.
// Once run_setup.go has resolved the dependency graph, every collaborator a run
// actually invokes must be non-nil; this guard turns a missed default into a
// loud, named error instead of a nil dereference deep inside execution. The
// required list lives beside the struct on purpose: a newly added field is
// weighed here at the same moment it is given a defaulting home in run_setup.go.
//
// StatusReporter, HeaderReporter, SessionLogWriter, and Now are intentionally
// excluded: reporting and session logging are optional, and now() falls back to
// time.Now.
func requireResolvedDependencies(dependencies RunDependencies) error {
	required := []struct {
		name    string
		missing bool
	}{
		{"CommandRunner", dependencies.CommandRunner == nil},
		{"ProcessStarter", dependencies.ProcessStarter == nil},
		{"WorkspacePreparer", dependencies.WorkspacePreparer == nil},
		{"EventAppender", dependencies.EventAppender == nil},
		{"LogAppender", dependencies.LogAppender == nil},
		{"PlanRecordFactory", dependencies.PlanRecordFactory == nil},
		{"RootResolver", dependencies.RootResolver == nil},
		{"OutputWriter", dependencies.OutputWriter == nil},
		{"AgentFactory", dependencies.AgentFactory == nil},
		{"SliceExecutor", dependencies.SliceExecutor == nil},
		{"PullRequestCreator", dependencies.PullRequestCreator == nil},
		{"ReviewCreator", dependencies.ReviewCreator == nil},
		{"TransportRetryDelay", dependencies.TransportRetryDelay == nil},
	}
	var missing []string
	for _, dependency := range required {
		if dependency.missing {
			missing = append(missing, dependency.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("run dependencies incompletely resolved after setup: %s still nil", strings.Join(missing, ", "))
	}
	return nil
}

// commandRunnerConfig and clockConfig let helpers read a single collaborator
// from any of the structs that expose it (Options, RunDependencies,
// runExecution, agent option values) without threading the concrete type. The
// accessors below are pure field reads and never default a missing value.
// Options exposes them by embedding RunDependencies.
type commandRunnerConfig interface{ commandRunner() CommandRunner }
type clockConfig interface{ clock() func() time.Time }

func (d RunDependencies) commandRunner() CommandRunner      { return d.CommandRunner }
func (d RunDependencies) clock() func() time.Time           { return d.Now }
func (d RunDependencies) eventAppender() plan.EventAppender { return d.EventAppender }

func (e runExecution) commandRunner() CommandRunner      { return e.Dependencies.CommandRunner }
func (e runExecution) clock() func() time.Time           { return e.Dependencies.Now }
func (e runExecution) eventAppender() plan.EventAppender { return e.Dependencies.EventAppender }
