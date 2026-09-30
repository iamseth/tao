// Package configtypes owns shared configuration values without depending on
// runtime configuration, plan persistence, or repository storage.
package configtypes

import "fmt"

// ExecutionMode controls workspace placement and branch behavior: isolated uses
// a feature branch and worktree; current keeps the launch checkout and branch.
type ExecutionMode string

const (
	ExecutionModeIsolated ExecutionMode = "isolated"
	ExecutionModeCurrent  ExecutionMode = "current"
)

func (m ExecutionMode) String() string {
	return string(m)
}

// NormalizeRecordedExecutionMode accepts historical placement vocabulary while
// preserving missing metadata as unset. It is not a CLI/default parser.
func NormalizeRecordedExecutionMode(value string) (ExecutionMode, error) {
	switch value {
	case "":
		return "", nil
	case "worktree", string(ExecutionModeIsolated):
		return ExecutionModeIsolated, nil
	case string(ExecutionModeCurrent):
		return ExecutionModeCurrent, nil
	default:
		return "", fmt.Errorf("unsupported recorded execution mode %q (want isolated or current)", value)
	}
}
