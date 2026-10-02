package promptinstall

import (
	"errors"

	"github.com/iamseth/tao/internal/runtimeconfig"
)

// ResolvedPaths describes installation locations without creating or installing anything.
type ResolvedPaths struct {
	PromptDir       string
	ExtensionSource string
	ExtensionTarget string
}

// ResolvePaths uses the same discovery policy as installation. Partial paths
// remain available when source discovery or home resolution fails.
func ResolvePaths(agent runtimeconfig.AgentKind) (ResolvedPaths, error) {
	var paths ResolvedPaths
	var promptErr, sourceErr, targetErr error
	paths.PromptDir, promptErr = Dir(agent)
	if agent == runtimeconfig.AgentPi {
		paths.ExtensionTarget, targetErr = piExtensionTarget()
		paths.ExtensionSource, sourceErr = piExtensionSource()
	}
	return paths, errors.Join(promptErr, sourceErr, targetErr)
}
